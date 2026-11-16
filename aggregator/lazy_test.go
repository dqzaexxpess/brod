package aggregator

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// с Lazy удачная загрузка не несёт ссылок в памяти — только файл кэша; LoadLinks даёт тот же результат, что обычная загрузка,
// в том числе для списка в base64 и после ответа 304 (источник не Known: тело берётся из кэша).
func TestLazyFetchReturnsCacheFile(t *testing.T) {
	plain := "vless://11111111-2222-4333-8444-555555555555@192.0.2.1:443?security=tls#a\n# комментарий\ntg://proxy?server=1.2.3.4&port=443&secret=dd00000000000000000000000000000000\nvless://11111111-2222-4333-8444-555555555556@192.0.2.2:443?security=tls#b\n"
	for name, body := range map[string]string{"plain": plain, "b64": base64.StdEncoding.EncodeToString([]byte(plain))} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") == `"v1"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Profile-Title", "Название")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			dir := t.TempDir()
			eager := <-Fetch(context.Background(), []Source{{Name: "a", URL: srv.URL}}, 5*time.Second, dir)
			if eager.Err != nil || len(eager.Links) != 3 {
				t.Fatalf("обычная загрузка: %+v", eager)
			}
			for _, label := range []string{"304", "304 повторно"} { // ETag уже сохранён обычной загрузкой
				r := <-Fetch(context.Background(), []Source{{Name: "a", URL: srv.URL, Lazy: true}}, 5*time.Second, dir)
				if r.Err != nil || r.Links != nil || r.TGLinks != nil || r.CacheFile == "" || r.Bytes == 0 || !r.NotModified {
					t.Fatalf("%s: ждали результат без ссылок с файлом кэша: %+v", label, r)
				}
				links, tg, err := r.LoadLinks()
				if err != nil || strings.Join(links, "|") != strings.Join(eager.Links, "|") || strings.Join(tg, "|") != strings.Join(eager.TGLinks, "|") {
					t.Fatalf("%s: LoadLinks: %v %v, %v (ждали %v %v)", label, links, tg, err, eager.Links, eager.TGLinks)
				}
			}
			// свежий ответ 200 (кэш другой): тоже без ссылок, с метаданными
			r := <-Fetch(context.Background(), []Source{{Name: "b", URL: srv.URL, Lazy: true}}, 5*time.Second, dir)
			if r.Err != nil || r.Links != nil || r.CacheFile == "" || r.NotModified || r.Meta.IsZero() {
				t.Fatalf("200: %+v", r)
			}
			if links, _, err := r.LoadLinks(); err != nil || len(links) != 3 {
				t.Fatalf("200: LoadLinks: %v, %v", links, err)
			}
		})
	}
}

// Lazy не мешает сбою и устаревшему кэшу: ошибка и FromCache приходят без файла кэша; без Lazy файла кэша нет.
func TestLazyKeepsErrorsAndEagerMode(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 503) }))
	defer down.Close()
	r := <-Fetch(context.Background(), []Source{{Name: "a", URL: down.URL, Lazy: true}}, 5*time.Second, t.TempDir())
	if r.Err == nil || r.CacheFile != "" {
		t.Fatalf("сбой: %+v", r)
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "trojan://pw@192.0.2.2:443")
	}))
	defer ok.Close()
	dir := t.TempDir()
	r = <-Fetch(context.Background(), []Source{{Name: "a", URL: ok.URL}}, 5*time.Second, dir)
	if r.CacheFile != "" || len(r.Links) != 1 {
		t.Fatalf("без Lazy: %+v", r)
	}
	// кэш есть, сеть упала, SkipStaleParse+Lazy: устаревшая копия, файл кэша не отдаётся
	r = <-Fetch(context.Background(), []Source{{Name: "a", URL: down.URL, Lazy: true, SkipStaleParse: true}}, 5*time.Second, dir)
	if !r.FromCache || r.CacheFile != "" || r.Links != nil {
		t.Fatalf("устаревший кэш: %+v", r)
	}
}
