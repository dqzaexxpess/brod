//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"brod/ota"
	"brod/updater"
)

// Тесты без root: каталог установки и домашний каталог — временные, владелец — текущий пользователь (помощник требует, чтобы владельцем
// каталога установки был он сам, в продукте это root), setcap подменён.

type fixture struct {
	t       *testing.T
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
	lib     string
	home    string
	setcaps []string
	in      *installer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, pub: pub, priv: priv, lib: t.TempDir(), home: t.TempDir()}
	if err := os.Chmod(f.lib, 0o755); err != nil { // t.TempDir подчиняется umask (при umask 002 — запись для группы)
		t.Fatal(err)
	}
	f.in = &installer{lib: f.lib, key: pub, uid: os.Getuid(), home: f.home, maxArchive: maxArchive,
		setcap: func(file string) error { f.setcaps = append(f.setcaps, file); return nil }}
	return f
}

// archive собирает архив выпуска с меткой mark и содержимым brodd.
func archive(t *testing.T, mark, brodd string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := []struct {
		name, body string
		mode       int64
	}{
		{"BROD-RELEASE", mark + "\n", 0o644},
		{"brod", "app", 0o755},
		{"brodd", brodd, 0o755},
		{"brodctl", "ctl", 0o755},
		{"data/flutter_assets/AssetManifest.bin", "a", 0o644},
		{"lib/libflutter_linux_gtk.so", "so", 0o644},
	}
	for _, e := range files {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// manifest — подписанный манифест для архива arch (сам архив в манифесте описывается SHA-256 и размером).
func (f *fixture) manifest(pubN int, app string, arch []byte, key ed25519.PrivateKey) (doc, sig []byte) {
	f.t.Helper()
	sum := sha256.Sum256(arch)
	d := ota.ReleasesDoc{Version: pubN, Release: ota.Release{App: app, Assets: map[string]ota.Asset{
		platform: {URL: "https://example.invalid/brod-" + app + "-linux-amd64.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(arch))},
	}}}
	doc, err := json.Marshal(d)
	if err != nil {
		f.t.Fatal(err)
	}
	return doc, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, doc)))
}

func (f *fixture) write(name string, b []byte) string {
	f.t.Helper()
	p := filepath.Join(f.home, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// release кладёт в домашний каталог архив, манифест и подпись и возвращает пути.
func (f *fixture) release(pubN int, app, brodd string) (arch, doc, sig string) {
	a := archive(f.t, app+" linux-amd64", brodd)
	d, s := f.manifest(pubN, app, a, f.priv)
	dir := "dl-" + app + "-" + strconv.Itoa(pubN)
	return f.write(dir+"/brod.tar.gz", a), f.write(dir+"/releases.json", d), f.write(dir+"/releases.json.sig", s)
}

func code(err error) int {
	var fl *failure
	if errors.As(err, &fl) {
		return fl.code
	}
	if err == nil {
		return exitOK
	}
	return -1
}

func wantCode(t *testing.T, err error, want int) {
	t.Helper()
	if got := code(err); got != want {
		t.Fatalf("код %d, ожидался %d (%v)", got, want, err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noStaging(t *testing.T, lib string) {
	t.Helper()
	ents, _ := os.ReadDir(lib)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatalf("остался временный каталог %s", e.Name())
		}
	}
}

func TestInstallSignedRelease(t *testing.T) {
	f := newFixture(t)
	a, d, s := f.release(3, "0.4.0", "daemon-0.4.0")
	ver, err := f.in.install(a, d, s)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.4.0" {
		t.Fatalf("версия %q", ver)
	}
	dest := filepath.Join(f.lib, "brodd")
	if got := readFile(t, dest); got != "daemon-0.4.0" {
		t.Fatalf("brodd: %q", got)
	}
	st, _ := os.Stat(dest)
	if st.Mode().Perm() != 0o755 || !st.Mode().IsRegular() {
		t.Fatalf("права brodd %v", st.Mode())
	}
	// права выдаются копии внутри каталога установки, а не файлу пользователя
	if len(f.setcaps) != 1 || !strings.HasPrefix(f.setcaps[0], filepath.Join(f.lib, ".staging-")) || filepath.Base(f.setcaps[0]) != "brodd" {
		t.Fatalf("setcap вызван для %v", f.setcaps)
	}
	if _, err := os.Stat(filepath.Join(f.lib, "brodd.prev")); err == nil {
		t.Fatal("при первой установке brodd.prev не нужен")
	}
	stt, err := f.in.loadState()
	if err != nil || stt.App != "0.4.0" || stt.Publication != 3 {
		t.Fatalf("состояние %+v, %v", stt, err)
	}
	noStaging(t, f.lib)
}

func TestInstallKeepsPrevAndIsAtomic(t *testing.T) {
	f := newFixture(t)
	a, d, s := f.release(3, "0.4.0", "old")
	if _, err := f.in.install(a, d, s); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Stat(filepath.Join(f.lib, "brodd"))
	a, d, s = f.release(4, "0.5.0", "new")
	if _, err := f.in.install(a, d, s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(f.lib, "brodd")); got != "new" {
		t.Fatalf("brodd %q", got)
	}
	prev := filepath.Join(f.lib, "brodd.prev")
	if got := readFile(t, prev); got != "old" {
		t.Fatalf("brodd.prev %q", got)
	}
	// brodd.prev — тот же файл, что был установлен (жёсткая ссылка: права setcap сохраняются)
	ps, _ := os.Stat(prev)
	if !os.SameFile(old, ps) {
		t.Fatal("brodd.prev не тот же файл, что прежний brodd")
	}
	// третья установка: brodd.prev сдвигается, лишних копий нет
	a, d, s = f.release(5, "0.5.1", "newer")
	if _, err := f.in.install(a, d, s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, prev); got != "new" {
		t.Fatalf("brodd.prev после третьей установки %q", got)
	}
	ents, _ := os.ReadDir(f.lib)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "brodd,brodd.prev,installed.json" {
		t.Fatalf("в каталоге установки: %v", names)
	}
}

func TestInstallRejectsSignatureProblems(t *testing.T) {
	f := newFixture(t)
	arch := archive(t, "0.4.0 linux-amd64", "x")
	a := f.write("a.tar.gz", arch)
	good, sig := f.manifest(3, "0.4.0", arch, f.priv)

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	_, foreign := f.manifest(3, "0.4.0", arch, other)

	tampered := bytes.Replace(good, []byte(`"version":3`), []byte(`"version":9`), 1)
	broken := append([]byte{}, sig...)
	broken[5] ^= 1

	noLinux := ota.ReleasesDoc{Version: 3, Release: ota.Release{App: "0.4.0", Assets: map[string]ota.Asset{
		"windows-amd64": {URL: "https://example.invalid/a.zip", SHA256: strings.Repeat("a", 64), Size: 1}}}}
	nl, _ := json.Marshal(noLinux)
	nlSig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, nl)))

	cases := []struct {
		name     string
		doc, sig []byte
	}{
		{"чужой ключ", good, foreign},
		{"повреждённая подпись", good, broken},
		{"изменённый манифест", tampered, sig},
		{"не подпись", good, []byte("not a signature")},
		{"нет файла linux-amd64", nl, nlSig},
		{"не манифест", []byte(`{"version":1}`), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, []byte(`{"version":1}`))))},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := f.write("m"+strconv.Itoa(i)+"/releases.json", c.doc)
			s := f.write("m"+strconv.Itoa(i)+"/releases.json.sig", c.sig)
			_, err := f.in.install(a, d, s)
			wantCode(t, err, exitSig)
		})
	}
	if len(f.setcaps) != 0 {
		t.Fatal("setcap вызван для отвергнутого выпуска")
	}
	if _, err := os.Stat(filepath.Join(f.lib, "brodd")); err == nil {
		t.Fatal("brodd поставлен из отвергнутого выпуска")
	}
}

func TestInstallRejectsHashMismatch(t *testing.T) {
	f := newFixture(t)
	arch := archive(t, "0.4.0 linux-amd64", "genuine")
	d, s := f.manifest(3, "0.4.0", arch, f.priv)
	dp, sp := f.write("releases.json", d), f.write("releases.json.sig", s)

	// тот же размер, другие байты
	evil := archive(t, "0.4.0 linux-amd64", "EVILEVI")
	if len(evil) != len(arch) {
		evil = append(evil[:0:0], arch...)
		evil[len(evil)-1] ^= 0xff
	}
	_, err := f.in.install(f.write("evil.tar.gz", evil), dp, sp)
	wantCode(t, err, exitHash)
	// другой размер
	_, err = f.in.install(f.write("long.tar.gz", append(append([]byte{}, arch...), 0)), dp, sp)
	wantCode(t, err, exitHash)
	if len(f.setcaps) != 0 {
		t.Fatal("setcap вызван для подменённого архива")
	}
	noStaging(t, f.lib)
}

func TestInstallRejectsRollback(t *testing.T) {
	f := newFixture(t)
	a, d, s := f.release(5, "0.5.0", "v5")
	if _, err := f.in.install(a, d, s); err != nil {
		t.Fatal(err)
	}
	// старая версия приложения с большим номером публикации
	a, d, s = f.release(9, "0.4.9", "old-app")
	_, err := f.in.install(a, d, s)
	wantCode(t, err, exitRollback)
	// та же версия приложения, но публикация старее
	a, d, s = f.release(4, "0.5.0", "old-pub")
	_, err = f.in.install(a, d, s)
	wantCode(t, err, exitRollback)
	if got := readFile(t, filepath.Join(f.lib, "brodd")); got != "v5" {
		t.Fatalf("brodd после отказа: %q", got)
	}
	// повторная установка того же выпуска допустима (починка)
	a, d, s = f.release(5, "0.5.0", "v5")
	if _, err := f.in.install(a, d, s); err != nil {
		t.Fatalf("повторная установка: %v", err)
	}
}

func TestInstallRejectsWrongLabel(t *testing.T) {
	f := newFixture(t)
	arch := archive(t, "0.3.0 linux-amd64", "x") // подписан как 0.4.0, внутри метка 0.3.0
	d, s := f.manifest(3, "0.4.0", arch, f.priv)
	_, err := f.in.install(f.write("a.tar.gz", arch), f.write("r.json", d), f.write("r.sig", s))
	wantCode(t, err, exitArchive)
	noStaging(t, f.lib)
}

func TestInstallSetcapFailureKeepsOld(t *testing.T) {
	f := newFixture(t)
	a, d, s := f.release(3, "0.4.0", "old")
	if _, err := f.in.install(a, d, s); err != nil {
		t.Fatal(err)
	}
	f.in.setcap = func(string) error { return errors.New("нет setcap") }
	a, d, s = f.release(4, "0.5.0", "new")
	_, err := f.in.install(a, d, s)
	wantCode(t, err, exitSetcap)
	if got := readFile(t, filepath.Join(f.lib, "brodd")); got != "old" {
		t.Fatalf("brodd после отказа setcap: %q", got)
	}
	noStaging(t, f.lib)
}

func TestOpenUserRejectsBadPaths(t *testing.T) {
	f := newFixture(t)
	good := f.write("ok/file", []byte("data"))
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	// ссылка в середине пути: ~/dirlink → каталог вне дома
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(f.home, "dirlink")); err != nil {
		t.Fatal(err)
	}
	// ссылка в середине пути внутри дома тоже не допускается
	if err := os.Symlink(filepath.Join(f.home, "ok"), filepath.Join(f.home, "oklink")); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(f.home, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(f.home, "big")
	bf, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := bf.Truncate(maxArchive + 1); err != nil { // разреженный файл: место на диске не занимает
		t.Fatal(err)
	}
	bf.Close()
	empty := f.write("empty", nil)

	cases := []struct {
		name, path string
		code       int
	}{
		{"ссылка", link, exitPath},
		{"ссылка на каталог вне дома в середине пути", filepath.Join(f.home, "dirlink", "outside"), exitPath},
		{"ссылка внутри дома в середине пути", filepath.Join(f.home, "oklink", "file"), exitPath},
		{"вне дома", outside, exitPath},
		{"сам домашний каталог", f.home, exitPath},
		{"каталог", filepath.Join(f.home, "ok"), exitPath},
		{"FIFO", fifo, exitPath},
		{"слишком большой", big, exitPath},
		{"пустой", empty, exitPath},
		{"нет файла", filepath.Join(f.home, "missing"), exitPath},
		{"относительный путь", "ok/file", exitUsage},
		{"с ..", f.home + "/ok/../ok/file", exitUsage},
		{"двойной слэш", f.home + "//ok/file", exitUsage},
		{"перевод строки", f.home + "/ok/fi\nle", exitUsage},
		{"пустой путь", "", exitUsage},
		{"длинный путь", f.home + "/" + strings.Repeat("a", 5000), exitUsage},
		{"префикс дома без слэша", f.home + "x/file", exitPath},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fl, _, err := f.in.openUser(c.path, maxArchive)
			if fl != nil {
				fl.Close()
			}
			wantCode(t, err, c.code)
		})
	}
	fl, n, err := f.in.openUser(good, maxArchive)
	if err != nil || n != 4 {
		t.Fatalf("обычный файл: %v (%d)", err, n)
	}
	fl.Close()
	// файл чужого владельца: проверяем через подмену uid вызвавшего
	f.in.uid = os.Getuid() + 1
	_, _, err = f.in.openUser(good, maxArchive)
	wantCode(t, err, exitPath)
}

// Подмена файла по тому же пути ПОСЛЕ открытия дескриптора не влияет на копию: хэш и содержимое — исходного файла.
func TestCopyFromDescriptorIgnoresLaterSwap(t *testing.T) {
	f := newFixture(t)
	orig := []byte(strings.Repeat("genuine-", 4096))
	p := f.write("dl/brod.tar.gz", orig)
	src, size, err := f.in.openUser(p, maxArchive)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// злоумышленник подменяет путь: новый файл переименованием поверх и сразу удаление
	evil := f.write("dl/evil", bytes.Repeat([]byte("E"), len(orig)))
	if err := os.Rename(evil, p); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(f.lib, "copy")
	sum, n, err := copyHashed(src, dst, size+1)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(orig)
	if n != int64(len(orig)) || sum != hex.EncodeToString(want[:]) {
		t.Fatalf("копия: %d байт, хэш %s", n, sum)
	}
	if got := readFile(t, dst); got != string(orig) {
		t.Fatal("в копии не исходные байты")
	}
	st, _ := os.Stat(dst)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("права копии %v", st.Mode())
	}
}

// Хэш считается по записанным байтам: что записано, то и проверено (в том числе если источник оборвался или вырос).
func TestCopyHashedLimit(t *testing.T) {
	dir := t.TempDir()
	sum, n, err := copyHashed(strings.NewReader("0123456789"), filepath.Join(dir, "a"), 4)
	if err != nil || n != 4 {
		t.Fatalf("%d %v", n, err)
	}
	want := sha256.Sum256([]byte("0123"))
	if sum != hex.EncodeToString(want[:]) || readFile(t, filepath.Join(dir, "a")) != "0123" {
		t.Fatal("записано и посчитано не одно и то же")
	}
	if _, _, err := copyHashed(strings.NewReader("x"), filepath.Join(dir, "a"), 4); err == nil {
		t.Fatal("существующий файл перезаписан")
	}
}

func TestLibDirChecks(t *testing.T) {
	f := newFixture(t)
	a, d, s := f.release(3, "0.4.0", "x")
	if err := os.Chmod(f.lib, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := f.in.install(a, d, s)
	wantCode(t, err, exitEnv)
	_ = os.Chmod(f.lib, 0o755)
	f.in.lib = filepath.Join(f.lib, "missing")
	_, err = f.in.install(a, d, s)
	wantCode(t, err, exitEnv)
}

func TestCallerParsing(t *testing.T) {
	for _, s := range []string{"", "abc", "0", "-1", " 1000", "1000 ", "01000", "99999999999", "1e3"} {
		if _, _, err := caller(s); code(err) != exitEnv {
			t.Errorf("PKEXEC_UID %q принят (%v)", s, err)
		}
	}
	if os.Getuid() == 0 {
		return
	}
	uid, home, err := caller(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Skipf("текущего пользователя нет в базе пользователей: %v", err)
	}
	if uid != os.Getuid() || !filepath.IsAbs(home) {
		t.Fatalf("%d %q", uid, home)
	}
}

func TestRunArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if c := run([]string{"pubkey"}, &out, &errb); c != exitOK || strings.TrimSpace(out.String()) != ota.ReleasesPublicKey {
		t.Fatalf("pubkey: %d %q", c, out.String())
	}
	for _, args := range [][]string{nil, {"install"}, {"install", "a", "b"}, {"install", "a", "b", "c", "d"}, {"setcap", "a", "b", "c"}, {"--allow-unsigned"}} {
		if c := run(args, &out, &errb); c != exitUsage {
			t.Errorf("%q: код %d", args, c)
		}
	}
	if os.Geteuid() != 0 {
		if c := run([]string{"install", "/a", "/b", "/c"}, &out, &errb); c != exitEnv {
			t.Errorf("не root: код %d", c)
		}
	}
	if _, err := parseKey(releasesKey); err != nil {
		t.Fatal("вшитый ключ не разбирается")
	}
}

// Метка релиза формируется тем же кодом, что у сборки архива: тест ловит расхождение формата.
func TestArchiveMarkMatchesUpdater(t *testing.T) {
	if strings.TrimSpace(updater.ReleaseMark("0.4.0", platform)) != "0.4.0 linux-amd64" {
		t.Fatal("формат BROD-RELEASE изменился: поправьте archive() в тестах")
	}
}
