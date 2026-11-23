//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"brod/ota"
	"brod/updater"
)

const (
	platform   = "linux-amd64"
	maxArchive = 200 << 20 // архив релиза Linux ~60 МБ
	maxSig     = 4096
	stateName  = "installed.json" // что поставлено помощником (защита от отката)
)

type installer struct {
	lib        string // каталог установки (/usr/local/lib/brod)
	keys       []ed25519.PublicKey
	uid        int    // вызвавший (PKEXEC_UID)
	home       string // его домашний каталог из базы пользователей
	maxArchive int64
	setcap     func(file string) error
}

// state — последний выпуск, поставленный помощником. Пишет только root (каталог установки root:root 0755).
type state struct {
	App         string    `json:"app"`
	Publication int       `json:"publication"`
	SHA256      string    `json:"sha256"`
	At          time.Time `json:"at"`
}

// install выполняет шаги установки и возвращает поставленную версию.
func (in *installer) install(archive, manifest, sig string) (string, error) {
	unlock, err := in.lockLib()
	if err != nil {
		return "", err
	}
	defer unlock()

	// 1. Подписанный манифест: подпись над точными прочитанными байтами, строгий разбор (код пакета ota, тот же, что у обновления в ядре).
	doc, err := in.readUser(manifest, ota.MaxDocBytes)
	if err != nil {
		return "", err
	}
	sigb, err := in.readUser(sig, maxSig)
	if err != nil {
		return "", err
	}
	if err := ota.VerifyAny(in.keys, doc, sigb); err != nil {
		return "", fail(exitSig, "манифест отвергнут: %v", err)
	}
	d, err := ota.ParseReleases(doc)
	if err != nil {
		return "", fail(exitSig, "манифест отвергнут: %v", err)
	}
	asset, ok := d.Release.Assets[platform]
	if !ok {
		return "", fail(exitSig, "в манифесте нет файла для %s", platform)
	}

	// 2. Откат: публикация и версия приложения не старее поставленных помощником раньше (повторная установка той же версии допустима).
	st, err := in.loadState()
	if err != nil {
		return "", err
	}
	if d.Version < st.Publication {
		return "", fail(exitRollback, "публикация %d старее уже установленной %d (откат)", d.Version, st.Publication)
	}
	if st.App != "" && (ota.Release{App: st.App}).NewerThan(d.Release.App) {
		return "", fail(exitRollback, "версия %s старее установленной %s (откат)", d.Release.App, st.App)
	}

	// 3. Архив: открыть без ссылок, скопировать из дескриптора в каталог root, считая SHA-256 по записанным байтам.
	src, size, err := in.openUser(archive, in.maxArchive)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if size != asset.Size {
		return "", fail(exitHash, "размер архива %d не совпадает с манифестом %d", size, asset.Size)
	}
	stage, err := os.MkdirTemp(in.lib, ".staging-")
	if err != nil {
		return "", fail(exitIO, "временный каталог: %v", err)
	}
	defer os.RemoveAll(stage)
	pkg := filepath.Join(stage, "release.tar.gz")
	sum, n, err := copyHashed(src, pkg, asset.Size+1)
	if err != nil {
		return "", fail(exitIO, "копия архива: %v", err)
	}
	if n != asset.Size || sum != asset.SHA256 {
		return "", fail(exitHash, "SHA-256 или размер копии архива (%d байт) не совпали с подписанным манифестом", n)
	}

	// 4. Распаковка проверенной копии безопасным распаковщиком и проверка раскладки (метка «версия платформа», обязательные файлы).
	tree := filepath.Join(stage, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		return "", fail(exitIO, "%v", err)
	}
	if err := updater.Extract(pkg, tree); err != nil {
		return "", fail(exitArchive, "распаковка: %v", err)
	}
	if err := updater.CheckLayout(tree, platform, d.Release.App); err != nil {
		return "", fail(exitArchive, "раскладка выпуска: %v", err)
	}

	// 5. Права на свою копию, затем атомарная замена; прежний brodd остаётся как brodd.prev (жёсткая ссылка: тот же файл с теми же правами).
	bin := filepath.Join(tree, "brodd")
	if err := syncFile(bin); err != nil {
		return "", fail(exitIO, "%v", err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return "", fail(exitIO, "%v", err)
	}
	if err := in.setcap(bin); err != nil {
		return "", fail(exitSetcap, "права для режима TUN не выданы: %v", err)
	}
	dest, prev := filepath.Join(in.lib, "brodd"), filepath.Join(in.lib, "brodd.prev")
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Remove(prev); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fail(exitIO, "%v", err)
		}
		if err := os.Link(dest, prev); err != nil {
			return "", fail(exitIO, "сохранить прежний brodd: %v", err)
		}
	}
	if err := os.Rename(bin, dest); err != nil {
		return "", fail(exitIO, "поставить brodd: %v", err)
	}
	if err := in.saveState(state{App: d.Release.App, Publication: d.Version, SHA256: asset.SHA256, At: time.Now().UTC()}); err != nil {
		return "", fail(exitIO, "brodd поставлен, но состояние не записано: %v", err)
	}
	_ = syncFile(in.lib)
	return d.Release.App, nil
}

// lockLib проверяет каталог установки (каталог, владелец — сам помощник, то есть root, не доступен на запись группе и остальным) и берёт
// исключительную блокировку, чтобы две установки не шли одновременно.
func (in *installer) lockLib() (func(), error) {
	var st unix.Stat_t
	if err := unix.Lstat(in.lib, &st); err != nil {
		return nil, fail(exitEnv, "каталог установки %s: %v (помощник поставлен? sudo deploy/install-helper.sh)", in.lib, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Geteuid() || st.Mode&0o022 != 0 {
		return nil, fail(exitEnv, "каталог установки %s должен быть каталогом root:root без записи для группы и остальных", in.lib)
	}
	fd, err := unix.Open(in.lib, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fail(exitEnv, "%v", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return nil, fail(exitEnv, "другая установка ещё идёт")
	}
	return func() { unix.Close(fd) }, nil
}

func (in *installer) readUser(p string, max int64) ([]byte, error) {
	f, size, err := in.openUser(p, max)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return nil, fail(exitPath, "%s: %v", p, err)
	}
	if int64(len(b)) > max {
		return nil, fail(exitPath, "%s: файл вырос во время чтения", p)
	}
	return b, nil
}

// copyHashed пишет не больше limit байт из src в новый файл dst (0600) и возвращает SHA-256 именно записанных байтов.
func copyHashed(src io.Reader, dst string, limit int64) (string, int64, error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, limit))
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func syncFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (in *installer) loadState() (state, error) {
	var st state
	b, err := os.ReadFile(filepath.Join(in.lib, stateName))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil // помощник ещё ничего не ставил
	}
	if err != nil {
		return st, fail(exitEnv, "состояние установки: %v", err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fail(exitEnv, "состояние установки %s повреждено: %v", stateName, err)
	}
	return st, nil
}

func (in *installer) saveState(st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := filepath.Join(in.lib, "."+stateName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(in.lib, stateName))
}
