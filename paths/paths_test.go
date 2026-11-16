package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLockIsExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l")
	rel, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p); err == nil {
		t.Fatal("второй захват должен отказывать")
	}
	rel()
	rel2, err := Lock(p)
	if err != nil {
		t.Fatalf("после освобождения захват должен проходить: %v", err)
	}
	rel2()
}

func TestStateAndSocketOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("переменные XDG и пути со слэшем — Linux; Windows проверяет TestWindowsPaths")
	}
	t.Setenv("VPNCLIENT_STATE", "/x/state")
	t.Setenv("VPNCLIENT_SOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	if StateDir() != "/x/state" || Socket() != "/run/user/1/brod.sock" {
		t.Fatalf("%s %s", StateDir(), Socket())
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if Socket() != "/x/state/brodd.sock" {
		t.Fatal(Socket())
	}
	t.Setenv("VPNCLIENT_SOCK", "/s")
	if Socket() != "/s" {
		t.Fatal(Socket())
	}
}

// Windows: состояние в %LOCALAPPDATA%\\vpnclient, сокет рядом с состоянием (XDG на Windows не бывает).
func TestWindowsPaths(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := func() (string, error) { return `C:\Users\u`, nil }
	e := env(map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`, "XDG_RUNTIME_DIR": "/should/be/ignored"})
	if got, want := stateDir("windows", e, home), filepath.Join(`C:\Users\u\AppData\Local`, "vpnclient"); got != want {
		t.Fatalf("состояние %q, ожидали %q", got, want)
	}
	if got, want := socketPath("windows", e, home), filepath.Join(stateDir("windows", e, home), "run", "brodd.sock"); got != want {
		t.Fatalf("сокет %q, ожидали %q", got, want)
	}
	// без LOCALAPPDATA — по домашнему каталогу
	if got, want := stateDir("windows", env(nil), home), filepath.Join(`C:\Users\u`, "AppData", "Local", "vpnclient"); got != want {
		t.Fatalf("запасной путь %q, ожидали %q", got, want)
	}
	// явные переменные сильнее
	if stateDir("windows", env(map[string]string{"VPNCLIENT_STATE": `D:\s`}), home) != `D:\s` || socketPath("windows", env(map[string]string{"VPNCLIENT_SOCK": `D:\x.sock`}), home) != `D:\x.sock` {
		t.Fatal("VPNCLIENT_STATE и VPNCLIENT_SOCK должны перекрывать умолчания")
	}
	// Linux не изменился: XDG_RUNTIME_DIR используется
	l := env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1"})
	if filepath.ToSlash(socketPath("linux", l, home)) != "/run/user/1/brod.sock" {
		t.Fatal("на Linux сокет в XDG_RUNTIME_DIR")
	}
}

// Файл от прежнего запуска (выход по ошибке, отключение питания) не должен мешать: занятость определяет блокировка ОС, а не наличие файла.
func TestLockIgnoresLeftoverFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "brodd.lock")
	if err := os.WriteFile(p, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := Lock(p)
	if err != nil {
		t.Fatalf("оставшийся файл не должен считаться блокировкой: %v", err)
	}
	if _, err := Lock(p); err == nil || !strings.Contains(err.Error(), "PID") {
		t.Fatalf("второй захват должен отказывать и называть PID: %v", err)
	}
	rel()
}

// Служба хранит состояние в %ProgramData%\Brod, но только если этот каталог создан установщиком: иначе прежнее расположение.
func TestWindowsServiceStateDir(t *testing.T) {
	env := func(k string) string {
		return map[string]string{"ProgramData": `C:\ProgramData`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}[k]
	}
	home := func() (string, error) { return `C:\Users\u`, nil }
	old := dirExists
	defer func() { dirExists = old }()

	dirExists = func(string) bool { return false }
	if got, want := stateDir("windows", env, home), filepath.Join(`C:\Users\u\AppData\Local`, "vpnclient"); got != want {
		t.Fatalf("без каталога службы: %q, ожидали %q", got, want)
	}
	dirExists = func(p string) bool { return p == filepath.Join(`C:\ProgramData`, "Brod") }
	want := filepath.Join(`C:\ProgramData`, "Brod")
	if got := stateDir("windows", env, home); got != want {
		t.Fatalf("с каталогом службы: %q, ожидали %q", got, want)
	}
	if got := socketPath("windows", env, home); got != filepath.Join(want, "run", "brodd.sock") {
		t.Fatalf("сокет службы: %q", got)
	}
}

// BROD_* сильнее прежних VPNCLIENT_*; без переменных на Linux — каталог brod и сокет brod.sock.
func TestBrodNamesOnLinux(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := func() (string, error) { return "/home/u", nil }
	if got := filepath.ToSlash(stateDir("linux", env(nil), home)); got != "/home/u/.local/share/brod" {
		t.Fatalf("состояние: %q", got)
	}
	if got := filepath.ToSlash(stateDir("linux", env(map[string]string{"XDG_DATA_HOME": "/x"}), home)); got != "/x/brod" {
		t.Fatalf("состояние с XDG: %q", got)
	}
	e := env(map[string]string{"BROD_STATE": "/new", "VPNCLIENT_STATE": "/old", "BROD_SOCK": "/new.sock", "VPNCLIENT_SOCK": "/old.sock"})
	if filepath.ToSlash(stateDir("linux", e, home)) != "/new" || filepath.ToSlash(socketPath("linux", e, home)) != "/new.sock" {
		t.Fatal("BROD_STATE и BROD_SOCK должны быть сильнее прежних имён")
	}
	e = env(map[string]string{"VPNCLIENT_STATE": "/old", "VPNCLIENT_SOCK": "/old.sock"})
	if filepath.ToSlash(stateDir("linux", e, home)) != "/old" || filepath.ToSlash(socketPath("linux", e, home)) != "/old.sock" {
		t.Fatal("прежние VPNCLIENT_* остаются рабочими")
	}
}

// Прежний каталог состояния переименовывается в новый один раз и только когда новый ещё не существует.
func TestMigrateLegacyState(t *testing.T) {
	home := t.TempDir()
	env := func(string) string { return "" }
	h := func() (string, error) { return home, nil }
	old := filepath.Join(home, ".local", "share", "vpnclient")
	cur := filepath.Join(home, ".local", "share", "brod")
	if msg, err := migrateLegacyState("linux", env, h); msg != "" || err != nil {
		t.Fatalf("нечего переносить: %q %v", msg, err)
	}
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "state.db"), []byte("база"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg, err := migrateLegacyState("linux", env, h)
	if err != nil || msg == "" {
		t.Fatalf("перенос: %q %v", msg, err)
	}
	if b, err := os.ReadFile(filepath.Join(cur, "state.db")); err != nil || string(b) != "база" {
		t.Fatalf("после переноса: %q %v", b, err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("прежний каталог должен исчезнуть (переименован)")
	}
	// новый уже есть — прежний (если вдруг появился снова) не трогаем
	_ = os.MkdirAll(old, 0o700)
	if msg, err := migrateLegacyState("linux", env, h); msg != "" || err != nil {
		t.Fatalf("при существующем новом: %q %v", msg, err)
	}
	// явные переменные и Windows — не переносим
	if msg, _ := migrateLegacyState("linux", func(k string) string { return map[string]string{"BROD_STATE": "/x"}[k] }, h); msg != "" {
		t.Fatal("при BROD_STATE перенос не нужен")
	}
	if msg, _ := migrateLegacyState("windows", env, h); msg != "" {
		t.Fatal("Windows переносит установщик")
	}
}
