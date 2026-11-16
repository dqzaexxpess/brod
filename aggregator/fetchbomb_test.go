package aggregator

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// тело подписки читается без предела. Сервер отдаёт gzip (512 МиБ нулей и перевод строки в сжатом виде ≈0,5 МБ).
func TestFetchRejectsOversizedBody(t *testing.T) {
	const total = 512 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		chunk := make([]byte, 1<<20)
		for i := range chunk {
			chunk[i] = 'a'
		}
		for n := 0; n < total; n += len(chunk) {
			if _, err := zw.Write(chunk); err != nil {
				return
			}
		}
		zw.Close()
	}))
	defer srv.Close()
	dir := t.TempDir()
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()
	var res Result
	for r := range Fetch(context.Background(), []Source{{Name: "bomb", URL: srv.URL}}, 15*time.Second, dir) {
		res = r
	}
	runtime.ReadMemStats(&m1)
	st, _ := os.Stat(filepath.Join(dir, "bomb.txt"))
	var size int64
	if st != nil {
		size = st.Size()
	}
	sysGrowth := (m1.Sys - m0.Sys) >> 20
	allocated := (m1.TotalAlloc - m0.TotalAlloc) >> 20
	t.Logf("ответ: err=%v, байт в памяти %d МиБ, ссылок %d, время %v; выделено %d МиБ, взято у системы +%d МиБ; файл кэша %d МиБ",
		res.Err, res.Bytes>>20, len(res.Links), time.Since(start).Round(10*time.Millisecond), allocated, sysGrowth, size>>20)
	if !errors.Is(res.Err, ErrBodyTooLarge) {
		t.Errorf("ожидалась ErrBodyTooLarge, получено %v", res.Err)
	}
	if size != 0 {
		t.Errorf("файл кэша %d байт: тело сверх предела не должно писаться", size)
	}
	if _, err := os.Stat(filepath.Join(dir, "bomb.etag")); err == nil {
		t.Error("etag записан для отвергнутого ответа")
	}
	// Предел 64 МиБ: буфер чтения растёт удвоением. Sys под -race неточен (теневая память), поэтому считаем выделенное;
	// до исправления было 5300 МиБ.
	if allocated > 512 {
		t.Errorf("выделено %d МиБ", allocated)
	}
}

// из одного источника берётся не больше maxLinksPerSource ссылок.
func TestExtractLinksCapsLinkCount(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxLinksPerSource+1000; i++ {
		fmt.Fprintf(&b, "vless://u@h%d.example:443\n", i)
	}
	if got := len(ExtractLinks([]byte(b.String()))); got != maxLinksPerSource {
		t.Fatalf("ссылок %d, ожидалось ровно %d", got, maxLinksPerSource)
	}
}

// ссылка длиннее maxLinkBytes отбрасывается, остальные из того же источника сохраняются; ссылка на 11 КБ (самая длинная в реальной базе) проходит.
func TestExtractLinksDropsOversizedLink(t *testing.T) {
	ok := "vless://u@ok.example:443?x=" + strings.Repeat("a", 11_000) + "#ok"
	huge := "vless://u@huge.example:443?x=" + strings.Repeat("a", maxLinkBytes+1) + "#huge"
	body := "vless://u@a.example:443#a\n" + huge + "\n" + ok + "\n"
	got := ExtractLinks([]byte(body))
	if len(got) != 2 || got[0] != "vless://u@a.example:443#a" || got[1] != ok {
		t.Fatalf("ссылок %d: %.60v", len(got), got)
	}
	// то же через base64-список
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	if got := ExtractLinks([]byte(enc)); len(got) != 2 {
		t.Fatalf("base64: ссылок %d", len(got))
	}
	// и полный конфиг (importer)
	cfg := `{"outbounds":[{"type":"shadowsocks","tag":"a","server":"a.example","server_port":8388,"method":"aes-128-gcm","password":"p"}]}`
	if got := ExtractLinks([]byte(cfg)); len(got) != 1 {
		t.Fatalf("конфиг: ссылок %d", len(got))
	}
}
