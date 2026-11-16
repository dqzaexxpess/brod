package aggregator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// имя файла кэша — хэш имени источника: ни «..\..\x», ни любые другие символы имени в путь не попадают.
func TestCacheFileNameIsHashOfName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprintln(w, "vless://11111111-2222-4333-8444-555555555555@192.0.2.1:443?security=tls#x")
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "cache")
	for _, name := range []string{`..\..\..\x`, `../../x`, `a:b*c`, `igareck/BLACK_VLESS_RUS`} {
		for r := range Fetch(context.Background(), []Source{{Name: name, URL: srv.URL}}, 5*time.Second, dir) {
			if r.Err != nil || len(r.Links) != 1 {
				t.Fatalf("%q: %v %d", name, r.Err, len(r.Links))
			}
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 8 {
		t.Fatalf("каталог кэша: %v, файлов %d (нужно 4 источника × .txt/.etag)", err, len(ents))
	}
	re := regexp.MustCompile(`^[0-9a-f]{32}\.(txt|etag)$`)
	for _, e := range ents {
		if !re.MatchString(e.Name()) {
			t.Errorf("имя файла кэша %q не хэш", e.Name())
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(dir)); len(entries) != 1 {
		t.Errorf("рядом с каталогом кэша появились файлы: %d", len(entries))
	}
	// LoadCached находит тот же файл
	if got := LoadCached([]Source{{Name: `..\..\..\x`}}, dir); len(got) != 1 || len(got[0].Links) != 1 {
		t.Errorf("LoadCached не нашёл кэш: %+v", got)
	}
}

// Кэш прежнего вида («имя.txt», «имя.etag», «/» заменён на «_») переносится под хэш один раз: после обновления программы
// не нужно перекачивать все источники.
func TestLegacyCacheMigrated(t *testing.T) {
	dir := t.TempDir()
	link := "vless://11111111-2222-4333-8444-555555555555@192.0.2.1:443?security=tls#x\n"
	_ = os.WriteFile(filepath.Join(dir, "a_b.txt"), []byte(link), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "a_b.etag"), []byte(`"old"`), 0o600)
	got := LoadCached([]Source{{Name: "a/b"}}, dir)
	if len(got) != 1 || len(got[0].Links) != 1 {
		t.Fatalf("старый кэш не подхвачен: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a_b.txt")); err == nil {
		t.Error("старый файл остался")
	}
	if b, err := os.ReadFile(CacheBase(dir, "a/b") + ".etag"); err != nil || string(b) != `"old"` {
		t.Errorf("etag не перенесён: %v %q", err, b)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"igareck/BLACK_VLESS_RUS", "t.me/mtp4tg", "mine", "моя подписка", "managed/a-b@main"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q отвергнуто: %v", ok, err)
		}
	}
	for _, bad := range []string{"", `..\..\x`, "../x", "a/../b", "a\\b", "a\x00b", "a:b", "a*b", "/abs", "x\ny", strings.Repeat("a", 200)} {
		if ValidName(bad) == nil {
			t.Errorf("%q принято", bad)
		}
	}
}

func TestValidURL(t *testing.T) {
	for _, ok := range []string{"https://a.example/x?token=1", "http://127.0.0.1:8080/x", "http://localhost/x", "http://[::1]/x"} {
		if err := ValidURL(ok); err != nil {
			t.Errorf("%q отвергнуто: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://a.example/x", "ftp://a.example/", "file:///etc/passwd", "https://", "//a.example/x", "javascript:alert(1)", "http://127.0.0.1.evil.example/"} {
		if ValidURL(bad) == nil {
			t.Errorf("%q принято", bad)
		}
	}
}
