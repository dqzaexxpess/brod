package winres

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"brod/buildinfo"
)

// Закоммиченные .syso должны совпадать с тем, что строится сейчас: иначе после смены buildinfo.Version (или описаний) в свойствах
// brodd.exe/brodctl.exe осталась бы старая версия. Исправление: go run ./buildinfo/winres/gen
func TestObjectsUpToDate(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, b := range Binaries {
		want, err := Build(root, b)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, b.Dir, ObjectName))
		if err != nil {
			t.Fatalf("%s: %v (go run ./buildinfo/winres/gen)", b.Name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s/%s устарел: go run ./buildinfo/winres/gen", b.Dir, ObjectName)
		}
	}
}

// Build воспроизводим (без отметки времени) и действительно содержит версию.
func TestBuildDeterministicAndVersioned(t *testing.T) {
	root := filepath.Join("..", "..")
	a, err := Build(root, Binaries[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Build(root, Binaries[0])
	if !bytes.Equal(a, b) {
		t.Fatal("два построения дали разные байты")
	}
	if !bytes.Contains(a, utf16(buildinfo.Version)) || !bytes.Contains(a, utf16("brodd.exe")) {
		t.Fatal("в объекте нет строк версии и имени файла")
	}
}

func utf16(s string) []byte {
	var out []byte
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}
