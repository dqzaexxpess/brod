package buildinfo

import (
	"os"
	"regexp"
	"testing"
)

// Версия демона и приложения одна: X.Y.Z в buildinfo.Version и в `version: X.Y.Z+N` файла app/pubspec.yaml (N — номер сборки, растёт).
func TestVersionMatchesPubspec(t *testing.T) {
	if !regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,4}$`).MatchString(Version) {
		t.Fatalf("Version %q: нужен вид X.Y.Z (его сравнивает OTA)", Version)
	}
	b, err := os.ReadFile("../app/pubspec.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^version:\s*(\S+?)\+(\d+)\s*$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("в app/pubspec.yaml нет строки `version: X.Y.Z+N`")
	}
	if string(m[1]) != Version {
		t.Fatalf("версия приложения %s (app/pubspec.yaml) не совпадает с buildinfo.Version %s: поправьте одну из них", m[1], Version)
	}
}
