package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestParseArgs(t *testing.T) {
	o := parseArgs([]string{"/VERYSILENT", "/SUPPRESSMSGBOXES", `/USER=PC\misav`, `/D=C:\Brod`})
	if !o.silent || o.user != `PC\misav` || o.dir != `C:\Brod` {
		t.Fatalf("%+v", o)
	}
	if o := parseArgs([]string{"/s"}); !o.silent {
		t.Fatal("/s без учёта регистра")
	}
	if o := parseArgs(nil); o.silent || o.user != "" {
		t.Fatalf("без аргументов тихого режима нет: %+v", o)
	}
}

func TestExtractWritesFiles(t *testing.T) {
	dir := t.TempDir()
	err := extract(makeZip(t, map[string]string{"Brod.exe": "x", "data/flutter_assets/a.bin": "y"}), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Brod.exe", filepath.Join("data", "flutter_assets", "a.bin")} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("нет %s: %v", p, err)
		}
	}
}

func TestExtractRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../evil.txt", "a/../../evil.txt", "/abs.txt"} {
		if err := extract(makeZip(t, map[string]string{name: "x"}), dir); err == nil {
			t.Errorf("путь %q принят", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Fatal("файл записан за пределами каталога")
	}
}

func TestUnpackWithoutPayload(t *testing.T) {
	saved := payload
	payload = nil
	defer func() { payload = saved }()
	if _, err := unpack(); err != errNoPayload {
		t.Fatalf("ожидалась ошибка пустого архива, получено %v", err)
	}
}

func TestDefaultUser(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := defaultUser(env(map[string]string{"USERNAME": "misav", "USERDOMAIN": "PC"})); got != `PC\misav` {
		t.Fatal(got)
	}
	if got := defaultUser(env(map[string]string{})); got != "" {
		t.Fatal(got)
	}
}
