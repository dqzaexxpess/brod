package killswitch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMarkerPathNoRuntimeDir(t *testing.T) {
	none := func(string) string { return "" }
	st := "/home/u/.local/share/brod"
	// ожидания через filepath.Join: тест идёт и на Windows, там разделитель «\»
	if got := markerPath("linux", none, st); got != filepath.Join(st, "brod-killswitch.active") {
		t.Fatalf("без XDG_RUNTIME_DIR признак должен лежать в каталоге состояния, а не в /tmp: %s", got)
	}
	xdg := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		return ""
	}
	if got := markerPath("linux", xdg, st); got != filepath.Join("/run/user/1000", "brod-killswitch.active") {
		t.Fatalf("с XDG_RUNTIME_DIR путь прежний: %s", got)
	}
	win := func(k string) string {
		if k == "ProgramData" {
			return `C:\ProgramData`
		}
		return ""
	}
	if got := markerPath("windows", win, st); !strings.HasSuffix(got, "killswitch.active") || !strings.Contains(got, "Brod") {
		t.Fatalf("Windows: %s", got)
	}
}

// Обновление: прежний демон оставил признак в /tmp — новый его видит (Active) и снимает (removeMarker); чужой файл не считается.
func TestLegacyMarkerCompat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("прежнего расположения на Windows не было")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("BROD_STATE", filepath.Join(t.TempDir(), "state"))
	t.Setenv("TMPDIR", t.TempDir())
	if filepath.Dir(Marker()) == os.TempDir() {
		t.Fatal("новый признак не должен лежать в /tmp")
	}
	old := legacyMarker()
	if err := os.WriteFile(old, []byte("interface=tun0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Active() {
		t.Fatal("прежний признак в /tmp не учтён: блокировка осталась бы невидимой")
	}
	writeMarker(Config{Interface: "tun0"})
	if _, err := os.Stat(old); err == nil {
		t.Fatal("прежний признак не снят при записи нового")
	}
	if fi, err := os.Stat(filepath.Dir(Marker())); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("каталог признака должен быть 0700: %v %v", fi, err)
	}
	_ = os.WriteFile(old, []byte("x"), 0o600)
	removeMarker()
	if Active() {
		t.Fatal("после снятия остался признак")
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("прежний признак не снят removeMarker")
	}
	// ссылка (не обычный файл) признаком не считается
	_ = os.Symlink("/etc/hostname", old)
	if Active() {
		t.Fatal("ссылка принята за признак")
	}
}
