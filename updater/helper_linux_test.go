package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootOwned(t *testing.T) {
	skipNonLinux(t)
	if !rootOwned("/", true) || rootOwned("/", false) {
		t.Fatal("/ — каталог root")
	}
	if os.Getuid() == 0 {
		t.Skip("под root временные файлы тоже принадлежат root")
	}
	f := filepath.Join(t.TempDir(), "brod-helper")
	if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rootOwned(f, false) || rootOwned(filepath.Dir(f), true) {
		t.Fatal("файл пользователя принят за поставленного помощника")
	}
	if rootOwned(filepath.Join(t.TempDir(), "missing"), false) {
		t.Fatal("несуществующий файл принят")
	}
}
