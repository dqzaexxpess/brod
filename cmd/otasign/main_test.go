package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"brod/ota"
)

// otasign целиком. main завершает процесс через os.Exit, поэтому тесты запускают собственный тестовый бинарник заново
// (приём TestMain + переменная окружения), без сборки отдельного файла и без сети.

func TestMain(m *testing.M) {
	if os.Getenv("OTASIGN_TEST_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type runResult struct {
	out, errOut string
	code        int
}

func run(t *testing.T, home string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "OTASIGN_TEST_RUN_MAIN=1", "HOME="+home, "USERPROFILE="+home)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("запуск: %v", err)
		}
		code = ee.ExitCode()
	}
	return runResult{so.String(), se.String(), code}
}

func (r runResult) ok(t *testing.T) runResult {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("код %d, stderr: %s", r.code, r.errOut)
	}
	return r
}

func (r runResult) fails(t *testing.T, want string) {
	t.Helper()
	if r.code != 1 || !strings.Contains(r.errOut, want) {
		t.Fatalf("ждали код 1 и %q в stderr: код %d, %q", want, r.code, r.errOut)
	}
}

func pubFrom(out string) string {
	for _, f := range strings.Fields(out) {
		if len(f) == 44 && strings.HasSuffix(f, "=") {
			return f
		}
	}
	return ""
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUsageAndUnknownCommand(t *testing.T) {
	home := t.TempDir()
	if r := run(t, home); r.code != 2 || !strings.Contains(r.errOut, "usage") {
		t.Fatalf("без аргументов: %+v", r)
	}
	run(t, home, "nope").fails(t, "неизвестная команда")
	run(t, home, "sign").fails(t, "нужен один файл")
	run(t, home, "verify").fails(t, "нужен один файл")
	run(t, home, "check").fails(t, "нужен один файл")
}

func TestKeygenPubSignVerifyRoundTrip(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	keyFile := filepath.Join(dir, "k.key")
	out := run(t, home, "keygen", "-out", keyFile).ok(t).out
	pub := pubFrom(out)
	if pub == "" {
		t.Fatalf("открытый ключ не напечатан: %q", out)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("файл ключа: %v %v", st, err)
		}
	}
	// прежний ключ не перезаписывается
	before, _ := os.ReadFile(keyFile)
	run(t, home, "keygen", "-out", keyFile).fails(t, "уже есть")
	if after, _ := os.ReadFile(keyFile); string(after) != string(before) {
		t.Fatal("ключ перезаписан")
	}
	if got := strings.TrimSpace(run(t, home, "pub", "-key", keyFile).ok(t).out); got != pub {
		t.Fatalf("pub: %q, ожидался %q", got, pub)
	}

	doc := filepath.Join(dir, "doc.txt")
	write(t, doc, "содержимое\n")
	run(t, home, "sign", "-key", keyFile, doc).ok(t)
	sig, err := os.ReadFile(doc + ".sig")
	if err != nil || len(strings.TrimSpace(string(sig))) == 0 {
		t.Fatalf("подпись не записана: %v", err)
	}
	if r := run(t, home, "verify", "-pub", pub, doc).ok(t); !strings.Contains(r.out, "подпись верна") {
		t.Fatalf("verify: %q", r.out)
	}
	// испорченный файл, чужой ключ, испорченный ключ
	write(t, doc, "подмена\n")
	run(t, home, "verify", "-pub", pub, doc).fails(t, "")
	write(t, doc, "содержимое\n")
	other := pubFrom(run(t, home, "keygen", "-out", filepath.Join(dir, "o.key")).ok(t).out)
	run(t, home, "verify", "-pub", other, doc).fails(t, "")
	run(t, home, "verify", "-pub", "!!!", doc).fails(t, "неверный открытый ключ")
	// подпись не по умолчанию вшитым ключом: чужая подпись не проходит
	run(t, home, "verify", doc).fails(t, "")
	// нет файла ключа / мусор в ключе
	run(t, home, "sign", "-key", filepath.Join(dir, "нет.key"), doc).fails(t, "")
	bad := filepath.Join(dir, "bad.key")
	write(t, bad, "не base64\n")
	run(t, home, "sign", "-key", bad, doc).fails(t, "неверный файл ключа")
}

// Ключ по умолчанию зависит от имени файла: releases.json подписывается ключом релизов, остальное ключом списка источников.
func TestDefaultKeySelectionByFileName(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	kdir := filepath.Join(home, ".config", "brod")
	srcPub := pubFrom(run(t, home, "keygen", "-out", filepath.Join(kdir, "ota-signing.key")).ok(t).out)
	relPub := pubFrom(run(t, home, "keygen", "-releases").ok(t).out)
	if srcPub == "" || relPub == "" || srcPub == relPub {
		t.Fatalf("ключи: %q %q", srcPub, relPub)
	}
	if _, err := os.Stat(filepath.Join(kdir, "releases-signing.key")); err != nil {
		t.Fatalf("ключ релизов не в каталоге по умолчанию: %v", err)
	}
	if got := strings.TrimSpace(run(t, home, "pub").ok(t).out); got != srcPub {
		t.Fatalf("pub по умолчанию: %q", got)
	}
	if got := strings.TrimSpace(run(t, home, "pub", "-releases").ok(t).out); got != relPub {
		t.Fatalf("pub -releases: %q", got)
	}
	plain, rel := filepath.Join(dir, "sources.json"), filepath.Join(dir, ota.ReleasesFile)
	write(t, plain, "{}")
	write(t, rel, "{}")
	run(t, home, "sign", plain).ok(t)
	run(t, home, "sign", rel).ok(t)
	run(t, home, "verify", "-pub", srcPub, plain).ok(t)
	run(t, home, "verify", "-pub", relPub, rel).ok(t)
	run(t, home, "verify", "-pub", srcPub, rel).fails(t, "") // релизы подписаны не ключом списка
	run(t, home, "verify", "-pub", relPub, plain).fails(t, "")
}

func TestBuildAndCheckSources(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	from := filepath.Join(dir, "sources.txt")
	write(t, from, "# комментарий\nалфа|https://example.org/a.txt|2\nалфа|https://example.org/a2.txt|3\nбета|https://example.org/b.txt\n")
	out := filepath.Join(dir, "sources.json")
	r := run(t, home, "build", "-version", "7", "-from", from, "-retired", " старый , ,другой", "-o", out).ok(t)
	if !strings.Contains(r.out, "версия 7, источников 2, снятых 2") {
		t.Fatalf("итог build: %q", r.out)
	}
	b, _ := os.ReadFile(out)
	d, err := ota.ParseSources(b)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != 7 || len(d.Sources) != 2 || d.Sources[0].Name != "алфа" || d.Sources[0].URL != "https://example.org/a2.txt" || d.Sources[0].Priority != 3 {
		t.Fatalf("позднее определение должно заменять раннее: %+v", d.Sources)
	}
	if strings.Join(d.Retired, ",") != "старый,другой" {
		t.Fatalf("снятые: %v", d.Retired)
	}
	if r := run(t, home, "check", out).ok(t); !strings.Contains(r.out, "список корректен: версия 7") {
		t.Fatalf("check: %q", r.out)
	}
	// встроенные источники по умолчанию тоже собираются в валидный список
	defOut := filepath.Join(dir, "def.json")
	run(t, home, "build", "-version", "1", "-defaults", "-o", defOut).ok(t)
	run(t, home, "check", defOut).ok(t)
	// клиент отвергает версию 0 и http-адрес: сборка тоже
	run(t, home, "build", "-version", "0", "-from", from, "-o", filepath.Join(dir, "x.json")).fails(t, "")
	write(t, from, "плохой|http://example.org/a.txt\n")
	run(t, home, "build", "-version", "2", "-from", from, "-o", filepath.Join(dir, "y.json")).fails(t, "")
	if _, err := os.Stat(filepath.Join(dir, "y.json")); err == nil {
		t.Fatal("при ошибке проверки файл записан")
	}
	run(t, home, "build", "-version", "2", "-from", filepath.Join(dir, "нет.txt")).fails(t, "")
	write(t, out, "не json")
	run(t, home, "check", out).fails(t, "")
}

func TestReleaseManifest(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	apk, deb := filepath.Join(dir, "brod.apk"), filepath.Join(dir, "brod.bin")
	write(t, apk, "apk-bytes")
	write(t, deb, "linux-bytes!")
	notes := filepath.Join(dir, "notes.txt")
	write(t, notes, "  что нового \n")
	out := filepath.Join(dir, ota.ReleasesFile)
	r := run(t, home, "release", "-version", "5", "-app", "1.2.3",
		"-asset", "android-arm64="+apk+"@https://example.org/brod.apk",
		"-asset", "linux-amd64="+deb+"@https://example.org/brod.bin",
		"-mirror", "linux-amd64=https://mirror.example.org/brod.bin",
		"-notes", "@"+notes, "-o", out).ok(t)
	if !strings.Contains(r.out, "платформ 2") {
		t.Fatalf("итог release: %q", r.out)
	}
	b, _ := os.ReadFile(out)
	d, err := ota.ParseReleases(b)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("apk-bytes"))
	a := d.Release.Assets["android-arm64"]
	if a.SHA256 != hex.EncodeToString(sum[:]) || a.Size != int64(len("apk-bytes")) || a.URL != "https://example.org/brod.apk" || a.Mirror != "" {
		t.Fatalf("android: %+v", a)
	}
	if l := d.Release.Assets["linux-amd64"]; l.Mirror != "https://mirror.example.org/brod.bin" || l.Size != int64(len("linux-bytes!")) {
		t.Fatalf("linux: %+v", l)
	}
	if d.Version != 5 || d.Release.App != "1.2.3" || d.Release.Notes != "что нового" {
		t.Fatalf("манифест: %+v", d)
	}
	if r := run(t, home, "check", out).ok(t); !strings.Contains(r.out, "манифест корректен: публикация 5, приложение 1.2.3, платформ 2") {
		t.Fatalf("check: %q", r.out)
	}

	bad := filepath.Join(dir, "bad", ota.ReleasesFile)
	_ = os.MkdirAll(filepath.Dir(bad), 0o755)
	asset := "android-arm64=" + apk + "@https://example.org/brod.apk"
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", "android-arm64", "-o", bad).fails(t, "нужен вид ПЛАТФОРМА=ФАЙЛ@URL")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", "android-arm64="+apk, "-o", bad).fails(t, "нужен вид ПЛАТФОРМА=ФАЙЛ@URL")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", "android-arm64="+filepath.Join(dir, "нет")+"@https://e.org/x", "-o", bad).fails(t, "")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", asset, "-mirror", "linux-amd64=https://m.example.org/x", "-o", bad).fails(t, "-mirror")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", asset, "-mirror", "android-arm64", "-o", bad).fails(t, "-mirror")
	run(t, home, "release", "-version", "5", "-app", "не версия", "-asset", asset, "-o", bad).fails(t, "версия приложения")
	run(t, home, "release", "-version", "0", "-app", "1.2.3", "-asset", asset, "-o", bad).fails(t, "версия публикации")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-o", bad).fails(t, "нет файлов")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", "plan9-mips="+apk+"@https://example.org/x", "-o", bad).fails(t, "неизвестная платформа")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", "android-arm64="+apk+"@http://example.org/x", "-o", bad).fails(t, "https")
	// http на этот же компьютер допускается только с -loopback
	loop := "android-arm64=" + apk + "@http://127.0.0.1:8080/brod.apk"
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", loop, "-o", bad).fails(t, "https")
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", loop, "-loopback", "-o", bad).ok(t)
	run(t, home, "release", "-version", "5", "-app", "1.2.3", "-asset", asset, "-notes", "@"+filepath.Join(dir, "нет"), "-o", bad).fails(t, "")
}
