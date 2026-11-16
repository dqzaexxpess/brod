package aggregator

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// P3: обновление 20 источников при одном зависшем укладывается в 15 с, а 19 остальных приходят сразу.
func TestOneHungSourceOfTwenty(t *testing.T) {
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "vless://11111111-2222-4333-8444-555555555555@192.0.2.1:443?security=tls#x")
	}))
	defer fast.Close()
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	}))
	defer hung.Close()

	var srcs []Source
	for i := 0; i < 19; i++ {
		srcs = append(srcs, Source{Name: fmt.Sprintf("fast-%d", i), URL: fast.URL})
	}
	srcs = append(srcs, Source{Name: "hung", URL: hung.URL})

	start := time.Now()
	var got, failed int
	var lastFast time.Duration
	for r := range Fetch(context.Background(), srcs, 10*time.Second, t.TempDir()) {
		if r.Err != nil {
			failed++
			continue
		}
		got++
		lastFast = time.Since(start)
	}
	total := time.Since(start)
	if got != 19 || failed != 1 {
		t.Fatalf("получено %d, ошибок %d (ожидалось 19/1)", got, failed)
	}
	if lastFast > 2*time.Second {
		t.Fatalf("быстрые источники пришли за %v, должны сразу", lastFast)
	}
	if total > 15*time.Second {
		t.Fatalf("общее время %v > 15 с", total)
	}
	t.Logf("19 быстрых за %v, весь набор за %v", lastFast.Round(time.Millisecond), total.Round(time.Millisecond))
}

// ETag: повторный запрос получает 304 и берёт тело из кэша.
func TestETagUsesCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprintln(w, "trojan://pw@192.0.2.2:443")
	}))
	defer srv.Close()
	dir := t.TempDir()
	s := []Source{{Name: "a", URL: srv.URL}}
	for i := 0; i < 2; i++ {
		r := <-Fetch(context.Background(), s, 5*time.Second, dir)
		if r.Err != nil || len(r.Links) != 1 {
			t.Fatalf("итерация %d: %+v", i, r)
		}
		if i == 1 && !r.NotModified {
			t.Fatal("второй запрос должен получить 304")
		}
	}
}

// Источник недоступен → берётся устаревший кэш (сеть не должна оставлять пользователя без списка).
func TestStaleCacheWhenDown(t *testing.T) {
	ok := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "trojan://pw@192.0.2.2:443")
	}))
	defer srv.Close()
	dir := t.TempDir()
	s := []Source{{Name: "a", URL: srv.URL}}
	<-Fetch(context.Background(), s, 5*time.Second, dir)
	ok = false
	r := <-Fetch(context.Background(), s, 5*time.Second, dir)
	if r.Err != nil || !r.FromCache || len(r.Links) != 1 {
		t.Fatalf("ожидался кэш: %+v", r)
	}
}

func TestExtractLinks(t *testing.T) {
	plain := "# profile-title: x\nvless://a@h:1\n\n  trojan://b@h:2  \nнессылка\n"
	if got := ExtractLinks([]byte(plain)); len(got) != 2 || got[0] != "vless://a@h:1" || got[1] != "trojan://b@h:2" {
		t.Fatalf("список строк: %q", got)
	}
	raw := "vless://a@h:1\ntrojan://b@h:2"
	for name, enc := range map[string]string{
		"std":          base64.StdEncoding.EncodeToString([]byte(raw)),
		"rawstd":       base64.RawStdEncoding.EncodeToString([]byte(raw)),
		"url":          base64.URLEncoding.EncodeToString([]byte(raw)),
		"rawurl":       base64.RawURLEncoding.EncodeToString([]byte(raw)),
		"с_переносами": "  " + base64.StdEncoding.EncodeToString([]byte(raw))[:10] + "\n" + base64.StdEncoding.EncodeToString([]byte(raw))[10:] + "\n",
	} {
		if got := ExtractLinks([]byte(enc)); len(got) != 2 {
			t.Errorf("base64 (%s): %q", name, got)
		}
	}
	for _, junk := range []string{"", "   \n", "<html>404</html>", "!!!не base64!!!"} {
		if got := ExtractLinks([]byte(junk)); len(got) != 0 {
			t.Errorf("мусор %q дал %q", junk, got)
		}
	}
}

func TestCacheFilesArePrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, "vless://a@h:1\n")
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "cache")
	for range Fetch(context.Background(), []Source{{Name: "s", URL: srv.URL}}, 2*time.Second, dir) {
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, CacheBase(dir, "s") + ".txt": 0o600, CacheBase(dir, "s") + ".etag": 0o600} {
		st, err := os.Stat(p)
		if err != nil || (posixPerms && st.Mode().Perm() != want) {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
}

// ETag без файла кэша не должен навсегда застревать на «304 Not Modified».
func TestEtagWithoutCacheFileRefetches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, "vless://a@h:1\n")
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(CacheBase(dir, "s")+".etag", []byte(`"v1"`), 0o600); err != nil { // etag есть, тела нет
		t.Fatal(err)
	}
	var res Result
	for r := range Fetch(context.Background(), []Source{{Name: "s", URL: srv.URL}}, 2*time.Second, dir) {
		res = r
	}
	if res.Err != nil || len(res.Links) != 1 || res.NotModified {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(CacheBase(dir, "s") + ".txt"); err != nil {
		t.Fatal("тело не сохранено в кэш")
	}
}

// Источник может отдавать полный конфиг: из него берутся только узлы; в base64 он тоже не теряется.
func TestExtractLinksFromFullConfigs(t *testing.T) {
	sb := `{"route":{"final":"x"},"inbounds":[{"type":"mixed","listen":"0.0.0.0"}],"outbounds":[{"type":"selector","tag":"x","outbounds":["a"]},
	 {"type":"trojan","tag":"a","server":"t.example","server_port":443,"password":"pw","tls":{"enabled":true,"server_name":"t.example"}}]}`
	links := ExtractLinks([]byte(sb))
	if len(links) != 1 || !strings.HasPrefix(links[0], "trojan://") || strings.Contains(links[0], "0.0.0.0") {
		t.Fatalf("sing-box: %q", links)
	}
	clash := "proxies:\n  - {name: a, type: trojan, server: t.example, port: 443, password: pw}\nrules: [\"MATCH,DIRECT\"]\n"
	if l := ExtractLinks([]byte(clash)); len(l) != 1 {
		t.Fatalf("clash: %q", l)
	}
	if l := ExtractLinks([]byte("vless://a@h:1\n")); len(l) != 1 { // обычные списки не затронуты
		t.Fatalf("список: %q", l)
	}
}

func TestParseMeta(t *testing.T) {
	h := http.Header{}
	h.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("Мой профиль\x00\x1b[31m")))
	h.Set("Profile-Update-Interval", "12")
	h.Set("Subscription-Userinfo", "upload=100; download=200; total=1000; expire=1893456000")
	m := ParseMeta(h, []byte("vless://a@h:1\n"))
	if m.Title != "Мой профиль[31m" || m.UpdateHours != 12 || m.Upload != 100 || m.Download != 200 || m.Total != 1000 || m.Expire != 1893456000 {
		t.Fatalf("заголовки: %+v", m)
	}
	// комментарии-заголовки в теле; заголовки HTTP главнее
	body := "#profile-title: Из тела\n# profile-update-interval: 6\n#subscription-userinfo: total=5\nvless://a@h:1\n"
	m = ParseMeta(nil, []byte(body))
	if m.Title != "Из тела" || m.UpdateHours != 6 || m.Total != 5 {
		t.Fatalf("комментарии: %+v", m)
	}
	m = ParseMeta(h, []byte(body))
	if m.UpdateHours != 12 || m.Total != 1000 {
		t.Fatalf("приоритет заголовков: %+v", m)
	}
	// мусор не ломает и не даёт нелепых значений
	h2 := http.Header{}
	h2.Set("Profile-Update-Interval", "-5")
	h2.Set("Subscription-Userinfo", "total=abc; expire=-1; ;;")
	h2.Set("Profile-Title", strings.Repeat("я", 500))
	m = ParseMeta(h2, nil)
	if m.UpdateHours != 0 || m.Total != 0 || m.Expire != 0 || len([]rune(m.Title)) != 80 {
		t.Fatalf("мусор: %+v", m)
	}
}

// с SkipStaleParse устаревший кэш при сбое сети не читается и не разбирается, но результат всё так же помечен FromCache.
func TestStaleCacheNotParsedWhenSkipped(t *testing.T) {
	ok := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "trojan://pw@192.0.2.2:443")
	}))
	defer srv.Close()
	dir := t.TempDir()
	s := []Source{{Name: "a", URL: srv.URL, SkipStaleParse: true}}
	<-Fetch(context.Background(), s, 5*time.Second, dir)
	ok = false
	r := <-Fetch(context.Background(), s, 5*time.Second, dir)
	if r.Err != nil || !r.FromCache || len(r.Links) != 0 || r.Bytes == 0 {
		t.Fatalf("ожидался кэш без разбора: %+v", r)
	}
	// кэша нет вовсе — это по-прежнему ошибка
	r = <-Fetch(context.Background(), []Source{{Name: "none", URL: srv.URL, SkipStaleParse: true}}, 5*time.Second, t.TempDir())
	if r.Err == nil {
		t.Fatal("без кэша и сети должна быть ошибка")
	}
}
