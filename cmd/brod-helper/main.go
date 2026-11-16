//go:build linux

// brod-helper — помощник от root для установки демона Brod на Linux. Запускается только через
// pkexec (действие polkit dev.godbtw.brod.install), ставится один раз `sudo deploy/install-helper.sh` в /usr/local/libexec/brod/brod-helper.
//
//	brod-helper install АРХИВ МАНИФЕСТ ПОДПИСЬ   поставить brodd из подписанного выпуска в /usr/local/lib/brod/brodd
//	brod-helper pubkey                          напечатать вшитый открытый ключ релизов (проверка установки)
//
// АРХИВ — скачанный brod-ВЕРСИЯ-linux-amd64.tar.gz, МАНИФЕСТ и ПОДПИСЬ — releases.json и releases.json.sig того же выпуска (все три в
// домашнем каталоге вызвавшего). Помощник копирует архив в каталог, куда пишет только root, считает SHA-256 по записанным байтам, сверяет его
// с манифестом, подпись которого проверяет вшитым ключом ota.ReleasesPublicKey, и только своей копии выдаёт права. Аргументы недоверенные:
// оболочка не используется, внешний вызов один (/usr/sbin/setcap с чистым окружением). Режима «без подписи» нет: для разработки есть
// deploy/tun-setup.sh.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"brod/ota"
)

// Переопределяются только при сборке (-ldflags -X) тестового помощника в scripts/verify-helper.sh: временный ключ и временный каталог.
// Продуктовая сборка (deploy/install-helper.sh) их не трогает; во время работы их не меняет ничто (ни аргументы, ни окружение).
var (
	releasesKey = ota.ReleasesPublicKey
	libDir      = "/usr/local/lib/brod"
)

const (
	setcapPath = "/usr/sbin/setcap"
	setcapCaps = "cap_net_admin,cap_net_bind_service,cap_net_raw+ep"
)

// Коды выхода: их проверяет scripts/verify-helper.sh; updater.LinuxApply передаёт в итог установки текст сообщения.
const (
	exitOK       = 0
	exitUsage    = 2  // неверные аргументы
	exitEnv      = 3  // не root, нет PKEXEC_UID, каталог установки не настроен
	exitPath     = 4  // путь отвергнут: ссылка, вне домашнего каталога, не обычный файл, чужой, слишком большой
	exitSig      = 5  // манифест не подписан, подпись неверна, манифест не разобрался, нет файла linux-amd64
	exitHash     = 6  // размер или SHA-256 архива не совпали с манифестом
	exitRollback = 7  // выпуск старее установленного
	exitArchive  = 8  // содержимое архива: распаковка, метка версии, обязательные файлы
	exitSetcap   = 9  // setcap не выполнен
	exitIO       = 10 // запись в каталог установки
)

type failure struct {
	code int
	msg  string
}

func (f *failure) Error() string { return f.msg }

func fail(code int, format string, a ...any) error {
	return &failure{code: code, msg: fmt.Sprintf(format, a...)}
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	if len(args) == 1 && args[0] == "pubkey" {
		fmt.Fprintln(out, releasesKey)
		return exitOK
	}
	if len(args) != 4 || args[0] != "install" {
		fmt.Fprintln(errw, "использование: brod-helper install АРХИВ МАНИФЕСТ ПОДПИСЬ (запускается через pkexec)")
		return exitUsage
	}
	ver, err := func() (string, error) {
		if os.Geteuid() != 0 {
			return "", fail(exitEnv, "нужен root: помощник запускается через pkexec")
		}
		uid, home, err := caller(os.Getenv("PKEXEC_UID"))
		if err != nil {
			return "", err
		}
		key, err := parseKey(releasesKey)
		if err != nil {
			return "", err
		}
		syscall.Umask(0o022)
		in := &installer{lib: libDir, key: key, uid: uid, home: home, maxArchive: maxArchive, setcap: runSetcap}
		return in.install(args[1], args[2], args[3])
	}()
	if err != nil {
		var f *failure
		if !errors.As(err, &f) {
			f = &failure{code: exitIO, msg: err.Error()}
		}
		fmt.Fprintln(errw, "brod-helper: "+f.msg)
		return f.code
	}
	fmt.Fprintf(out, "установлено: brodd %s → %s\n", ver, filepath.Join(libDir, "brodd"))
	return exitOK
}

var uidRe = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

// caller — кто вызвал pkexec: uid из PKEXEC_UID (его ставит сам pkexec), домашний каталог из базы пользователей, а не из окружения.
func caller(s string) (int, string, error) {
	if !uidRe.MatchString(s) {
		return 0, "", fail(exitEnv, "нет PKEXEC_UID или он не число (запуск только через pkexec; root не допускается)")
	}
	uid, err := strconv.Atoi(s)
	if err != nil || uid <= 0 {
		return 0, "", fail(exitEnv, "PKEXEC_UID %q недопустим", s)
	}
	u, err := user.LookupId(s)
	if err != nil {
		return 0, "", fail(exitEnv, "пользователь %d не найден: %v", uid, err)
	}
	h := u.HomeDir
	if !filepath.IsAbs(h) || filepath.Clean(h) != h || h == "/" {
		return 0, "", fail(exitEnv, "домашний каталог пользователя %d недопустим: %q", uid, h)
	}
	return uid, h, nil
}

func parseKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fail(exitEnv, "вшитый ключ релизов повреждён")
	}
	return ed25519.PublicKey(b), nil
}

// userRel проверяет присланный путь и возвращает его часть относительно домашнего каталога. Сами ссылки и выход за каталог отсекает
// openat2 (RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS), здесь — только форма строки.
func userRel(p, home string) (string, error) {
	if len(p) == 0 || len(p) > 4096 || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fail(exitUsage, "путь пуст, слишком длинный или содержит управляющие символы")
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", fail(exitUsage, "нужен абсолютный путь без «.», «..» и повторных «/»: %q", p)
	}
	rel, ok := strings.CutPrefix(p, home+"/")
	if !ok || rel == "" {
		return "", fail(exitPath, "%s: файл должен лежать в домашнем каталоге %s", p, home)
	}
	return rel, nil
}

// openUser открывает файл вызвавшего: внутри домашнего каталога, без ссылок на всём пути, обычный файл, принадлежит вызвавшему, размер
// от 1 до max. Дальше работа идёт только с дескриптором: подмена пути после открытия на результат не влияет.
func (in *installer) openUser(p string, max int64) (*os.File, int64, error) {
	rel, err := userRel(p, in.home)
	if err != nil {
		return nil, 0, err
	}
	hfd, err := unix.Open(in.home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, fail(exitPath, "домашний каталог %s: %v", in.home, err)
	}
	defer unix.Close(hfd)
	fd, err := unix.Openat2(hfd, rel, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	switch {
	case errors.Is(err, unix.ELOOP):
		return nil, 0, fail(exitPath, "%s: символьные ссылки в пути не допускаются", p)
	case errors.Is(err, unix.EXDEV):
		return nil, 0, fail(exitPath, "%s: путь выходит за домашний каталог", p)
	case err != nil:
		return nil, 0, fail(exitPath, "%s: %v", p, err)
	}
	f := os.NewFile(uintptr(fd), p)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, 0, fail(exitPath, "%s: %v", p, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		err = fail(exitPath, "%s: не обычный файл", p)
	case int(st.Uid) != in.uid:
		err = fail(exitPath, "%s: файл принадлежит не вызвавшему (uid %d)", p, st.Uid)
	case st.Size <= 0 || st.Size > max:
		err = fail(exitPath, "%s: размер %d вне допустимого (1…%d)", p, st.Size, max)
	}
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size, nil
}

// runSetcap — единственный внешний вызов: абсолютный путь, чистое окружение, без оболочки. Итог проверяется по атрибуту файла.
func runSetcap(file string) error {
	cmd := exec.Command(setcapPath, setcapCaps, file)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v: %s", setcapPath, err, strings.TrimSpace(string(out)))
	}
	if n, err := unix.Getxattr(file, "security.capability", nil); err != nil || n <= 0 {
		return fmt.Errorf("после setcap у файла нет атрибута security.capability (%v)", err)
	}
	return nil
}
