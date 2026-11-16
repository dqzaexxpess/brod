package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	mode int64
	typ  byte
	link string
}

func tgz(t *testing.T, es []entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: typ, Size: int64(len(e.body)), Linkname: e.link}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "r.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func linuxRelease(version string) []entry {
	es := []entry{{name: releaseFile, body: ReleaseMark(version, "linux-amd64"), mode: 0o644}}
	for _, f := range required["linux-amd64"] {
		m := int64(0o644)
		if !strings.Contains(f, "/") {
			m = 0o755
		}
		es = append(es, entry{name: f, body: "x", mode: m})
	}
	return es
}

func TestExtractTarGzAndLayout(t *testing.T) {
	dest := t.TempDir()
	if err := Extract(tgz(t, linuxRelease("0.4.0")), dest); err != nil {
		t.Fatal(err)
	}
	if err := CheckLayout(dest, "linux-amd64", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dest, "brodd")); posixPerms && st.Mode().Perm() != 0o755 {
		t.Fatalf("brodd: %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Join(dest, "lib/libflutter_linux_gtk.so")); posixPerms && st.Mode().Perm() != 0o644 {
		t.Fatalf("lib: %v", st.Mode())
	}
	for _, c := range []struct{ platform, version string }{{"linux-amd64", "0.5.0"}, {"windows-amd64", "0.4.0"}, {"android-arm64", "0.4.0"}} {
		if CheckLayout(dest, c.platform, c.version) == nil {
			t.Errorf("раскладка принята для %+v", c)
		}
	}
	os.Remove(filepath.Join(dest, "brodctl"))
	if err := CheckLayout(dest, "linux-amd64", "0.4.0"); err == nil || !strings.Contains(err.Error(), "brodctl") {
		t.Fatalf("без brodctl: %v", err)
	}
}

func TestExtractRejectsBadArchives(t *testing.T) {
	big := strings.Repeat("a", 1<<10)
	cases := map[string][]entry{
		"выход вверх":     {{name: "../evil", body: "x", mode: 0o644}},
		"выход глубже":    {{name: "a/../../evil", body: "x", mode: 0o644}},
		"абсолютный путь": {{name: "/etc/evil", body: "x", mode: 0o644}},
		"диск Windows":    {{name: "C:/evil", body: "x", mode: 0o644}},
		"обратная косая":  {{name: `..\evil`, body: "x", mode: 0o644}},
		"символьная ссылка": {
			{name: "l", typ: tar.TypeSymlink, link: "/etc"}, {name: "l/passwd", body: "x", mode: 0o644}},
		"жёсткая ссылка": {{name: "h", typ: tar.TypeLink, link: "/etc/passwd"}},
		"устройство":     {{name: "d", typ: tar.TypeChar}},
		"повтор имени":   {{name: "a", body: "1", mode: 0o644}, {name: "A", body: "2", mode: 0o644}},
	}
	for name, es := range cases {
		dest := t.TempDir()
		if err := Extract(tgz(t, es), dest); err == nil {
			t.Errorf("%s: принято", name)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(dest), "evil")); err == nil {
			t.Errorf("%s: файл вне каталога создан", name)
		}
	}
	_ = big
	// не пустой каталог и неизвестное расширение
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "x"), nil, 0o600)
	if Extract(tgz(t, linuxRelease("0.4.0")), d) == nil {
		t.Error("распаковка в непустой каталог")
	}
	if Extract(filepath.Join(t.TempDir(), "r.rar"), t.TempDir()) == nil {
		t.Error("неизвестный формат")
	}
}

func TestExtractZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(n, body string) {
		w, _ := zw.Create(n)
		w.Write([]byte(body))
	}
	put(releaseFile, ReleaseMark("0.4.0", "windows-amd64"))
	for _, f := range required["windows-amd64"] {
		put(f, "x")
	}
	zw.Close()
	p := filepath.Join(t.TempDir(), "r.zip")
	os.WriteFile(p, buf.Bytes(), 0o600)
	dest := t.TempDir()
	if err := Extract(p, dest); err != nil {
		t.Fatal(err)
	}
	if err := CheckLayout(dest, "windows-amd64", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	// zip-slip
	buf.Reset()
	zw = zip.NewWriter(&buf)
	put(`..\..\evil.dll`, "x")
	zw.Close()
	p2 := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(p2, buf.Bytes(), 0o600)
	if Extract(p2, t.TempDir()) == nil {
		t.Fatal("zip-slip принят")
	}
}
