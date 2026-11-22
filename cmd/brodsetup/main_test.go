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

// Архив из Compress-Archive (Windows PowerShell 5.1): имена с обратной косой чертой, каталоги записаны отдельными записями.
func TestExtractBackslashNamesAndDirEntries(t *testing.T) {
	dir := t.TempDir()
	data := makeZip(t, map[string]string{
		`data\app.so`:                              "a",
		`data\flutter_assets\packages\`:            "",
		`data\flutter_assets\packages\icons\a.ttf`: "b",
	})
	if err := extract(data, dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "data", "flutter_assets", "packages", "icons", "a.ttf")); err != nil || string(b) != "b" {
		t.Fatalf("файл не распакован: %v %q", err, b)
	}
	if st, err := os.Stat(filepath.Join(dir, "data", "flutter_assets", "packages")); err != nil || !st.IsDir() {
		t.Fatalf("каталог не создан: %v", err)
	}
}

func TestExtractRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../evil.txt", "a/../../evil.txt", "/abs.txt", `..\evil.txt`, `a\..\..\evil.txt`} {
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

func TestUIExitedRecognisesOnlyOwnRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.txt")
	if uiExited(p, "7") {
		t.Fatal("журнала нет: выход не записан")
	}
	logLine(p, "запуск 7")
	if uiExited(p, "7") {
		t.Fatal("запись загрузчика не считается выходом окна")
	}
	if err := os.WriteFile(p, []byte("x UI-EXIT run=70 code=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if uiExited(p, "7") {
		t.Fatal("чужой номер запуска (70) принят за свой (7)")
	}
	if err := os.WriteFile(p, []byte("x UI-EXIT run=7 code=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !uiExited(p, "7") {
		t.Fatal("намеренный выход не распознан")
	}
}
