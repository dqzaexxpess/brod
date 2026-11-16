package tgproxy

import (
	"context"
	"net"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// Живая проверка на реальном списке (TG_LIVE_FILE=путь): валидирует рукопожатие на настоящих прокси.
func TestLiveList(t *testing.T) {
	f := os.Getenv("TG_LIVE_FILE")
	if f == "" {
		t.Skip("TG_LIVE_FILE не задан")
	}
	body, _ := os.ReadFile(f)
	links := ExtractLinks(body)
	var proxies []Proxy
	for _, l := range links {
		if p, err := Parse(l); err == nil {
			proxies = append(proxies, p)
		}
	}
	if len(proxies) > 150 {
		proxies = proxies[:150]
	}
	t.Logf("ссылок %d, разобрано %d", len(links), len(proxies))
	var mu sync.Mutex
	type row struct {
		mode  string
		res   Result
		proxy Proxy
	}
	var rows []row
	var wg sync.WaitGroup
	sem := make(chan struct{}, 24)
	d := (&net.Dialer{}).DialContext
	for _, p := range proxies {
		wg.Add(1)
		sem <- struct{}{}
		go func(p Proxy) {
			defer wg.Done()
			defer func() { <-sem }()
			r := Probe(context.Background(), p, d, 8*time.Second)
			mu.Lock()
			rows = append(rows, row{p.Mode(), r, p})
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	stat := map[string]map[string]int{}
	for _, r := range rows {
		if stat[r.mode] == nil {
			stat[r.mode] = map[string]int{}
		}
		k := "ok"
		if !r.res.OK {
			k = r.res.Class
		}
		stat[r.mode][k]++
	}
	for m, s := range stat {
		t.Logf("%-8s %v", m, s)
	}
	var oks []int64
	for _, r := range rows {
		if r.res.OK {
			oks = append(oks, r.res.LatencyMs)
		}
	}
	sort.Slice(oks, func(i, j int) bool { return oks[i] < oks[j] })
	t.Logf("живых: %d из %d; задержки (мс): %v", len(oks), len(rows), oks)
	// подробности по первым отказам handshake/tls — для отладки
	n := 0
	for _, r := range rows {
		if !r.res.OK && (r.res.Class == "handshake" || r.res.Class == "tls") && n < 6 {
			t.Logf("отказ %s/%s: %s", r.mode, r.res.Class, r.res.Detail)
			n++
		}
	}
}
