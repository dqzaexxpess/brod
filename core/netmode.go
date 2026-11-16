package core

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Режим мобильной сети: оператор в РФ при «ограничении» пропускает только разрешённые адреса и SNI. Признак: российские сайты
// открываются, а зарубежные нет. Проверка идёт напрямую, поэтому только при выключенном VPN (через туннель зарубежное откроется всегда), и только в
// мобильной сети; результат привязан к идентификатору сети и устаревает через netModeTTL.

// Режимы сети (Status.NetMode); пусто — не определялся.
const (
	NetModeOpen      = "open"      // зарубежные сайты открываются
	NetModeWhitelist = "whitelist" // открываются только российские: режим белых списков
	NetModeOffline   = "offline"   // не открывается ничего
)

const (
	netModeTTL      = 10 * time.Minute
	netModeTick     = 15 * time.Second
	netModeProbeTTL = 8 * time.Second
)

var (
	netModeAllowed = []string{"https://yandex.ru/", "https://www.gosuslugi.ru/", "https://ya.ru/"}
	netModeForeign = []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://www.gstatic.com/generate_204", "https://1.1.1.1/"}
)

type netModeState struct {
	mu   sync.Mutex
	net  string // идентификатор сети, для которого получен результат
	mode string
	at   time.Time
}

// NetMode возвращает последний результат для текущей сети; пусто — режим не определён (сеть не мобильная, VPN включён, проверка не шла).
func (c *Core) NetMode() string {
	raw := c.cfg.NetID()
	c.nm.mu.Lock()
	defer c.nm.mu.Unlock()
	if c.nm.net != raw || c.cfg.Now().Sub(c.nm.at) > 3*netModeTTL {
		return ""
	}
	return c.nm.mode
}

// probeReachable — адрес отвечает любым HTTP-кодом (нужна достижимость, а не содержимое); подменяется в тестах. Вторым значением — причина отказа (для журнала).
func (c *Core) probeReachable(ctx context.Context, url string) (bool, string) {
	if c.cfg.NetModeProbe != nil {
		return c.cfg.NetModeProbe(ctx, url), ""
	}
	ctx, cancel := context.WithTimeout(ctx, netModeProbeTTL)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return false, err.Error()
	}
	resp.Body.Close()
	return true, ""
}

// DetectNetMode определяет режим текущей сети: хотя бы один зарубежный адрес открылся — open; иначе, если открылся российский, — whitelist; иначе offline.
func (c *Core) DetectNetMode(ctx context.Context) string {
	var notes []string // журнал: что не открылось и почему (без полных адресов, только имя хоста)
	reach := func(urls []string) bool {
		type r struct {
			url string
			ok  bool
			why string
		}
		res := make(chan r, len(urls))
		for _, u := range urls {
			go func() { ok, why := c.probeReachable(ctx, u); res <- r{u, ok, why} }()
		}
		any := false
		for range urls {
			x := <-res
			if x.ok {
				any = true
			} else {
				notes = append(notes, probeHost(x.url)+": "+x.why)
			}
		}
		return any
	}
	var foreign, allowed bool
	foreign = reach(netModeForeign)
	if !foreign {
		allowed = reach(netModeAllowed)
	}
	mode := NetModeOffline
	switch {
	case foreign:
		mode = NetModeOpen
	case allowed:
		mode = NetModeWhitelist
	}
	if len(notes) > 0 && mode != NetModeOpen {
		log.Printf("режим сети %s; не открылось: %s", mode, strings.Join(notes, "; "))
	}
	return mode
}

func probeHost(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// netModeLoop обновляет режим в мобильной сети при VPN, выключенном на момент проверки.
func (c *Core) netModeLoop(ctx context.Context) {
	t := time.NewTicker(netModeTick)
	defer t.Stop()
	for {
		c.netModeOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Core) netModeOnce(ctx context.Context) {
	raw := c.cfg.NetID()
	if c.cfg.TUN.Enabled || !isMobileNet(raw) {
		return
	}
	c.nm.mu.Lock()
	fresh := c.nm.net == raw && c.cfg.Now().Sub(c.nm.at) < netModeTTL
	c.nm.mu.Unlock()
	if fresh {
		return
	}
	mode := c.DetectNetMode(ctx)
	if ctx.Err() != nil {
		return
	}
	c.nm.mu.Lock()
	c.nm.net, c.nm.mode, c.nm.at = raw, mode, c.cfg.Now()
	c.nm.mu.Unlock()
}
