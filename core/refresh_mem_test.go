package core

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"brod/aggregator"
	"brod/tgproxy"
)

// memLink — синтетическая ссылка VLESS Reality (~330 байт), близкая к реальным.
func memLink(n int) string {
	return fmt.Sprintf("vless://5b0e5c2e-0000-4000-8000-%012d@h%d.example:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.microsoft.com&fp=chrome&pbk=%s&sid=6ba85179e30d4fc2&type=tcp&headerType=none#node-%d-%s",
		n, n, strings.Repeat("A", 43), n, strings.Repeat("x", 40))
}

// замер пика памяти обновления источников на данных реального размера: десятки источников, сотни тысяч ссылок (самый большой — более ста тысяч), заметно меньше разных узлов. Загрузка настоящая (aggregator.Fetch через httptest), база на диске. Запуск:
//
//	BROD_MEMTEST=1 go test ./core -run TestRefreshMemoryPeak -v -count=1
func TestRefreshMemoryPeak(t *testing.T) {
	if os.Getenv("BROD_MEMTEST") == "" {
		t.Skip("замер памяти: BROD_MEMTEST=1")
	}
	const nSrc, biggest, total, distinct = 86, 134_939, 477_978, 110_000
	sizes := make([]int, nSrc)
	sizes[0] = biggest
	rest := total - biggest
	sum := 0
	for i := 1; i < nSrc; i++ { // остальные: убывающая последовательность с суммой rest
		sizes[i] = int(float64(rest) / float64(i+2) / 3.25)
		sum += sizes[i]
	}
	sizes[1] += rest - sum
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		bw := bufio.NewWriterSize(w, 64<<10)
		for j := 0; j < sizes[i]; j++ {
			bw.WriteString(memLink((i*1237 + j) % distinct))
			bw.WriteByte('\n')
		}
		bw.Flush()
	}))
	defer srv.Close()
	seed := make([]aggregator.Source, nSrc)
	for i := range seed {
		seed[i] = aggregator.Source{Name: fmt.Sprintf("s%02d", i), URL: fmt.Sprintf("%s/%d", srv.URL, i), Priority: 1}
	}
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", SeedSources: seed, MaxNodes: -1,
		FetchTimeout: 2 * time.Minute,
		TGProbe:      func(context.Context, tgproxy.Proxy) tgproxy.Result { return tgproxy.Result{Class: "connect"} }})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	debug.FreeOSMemory()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak, peakSys atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			if m.HeapSys-m.HeapReleased > peakSys.Load() {
				peakSys.Store(m.HeapSys - m.HeapReleased)
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	t0 := time.Now()
	rep, err := c.Refresh(context.Background(), "user")
	took := time.Since(t0)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	links := 0
	for _, s := range rep.Sources {
		links += s.Links
	}
	t.Logf("ссылок %d, узлов в базе %d, время %v, пик кучи %d МиБ (над базой %d МиБ), пик занятого кучей %d МиБ, выделено за проход %d МиБ",
		links, rep.Total, took.Round(10*time.Millisecond), peak.Load()>>20, (int64(peak.Load())-int64(base.HeapAlloc))>>20, peakSys.Load()>>20,
		(end.TotalAlloc-base.TotalAlloc)>>20)
	if links != total || rep.Total != distinct {
		t.Fatalf("итог не тот: ссылок %d (ждали %d), узлов %d (ждали %d)", links, total, rep.Total, distinct)
	}
}
