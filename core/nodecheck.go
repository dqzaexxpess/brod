package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"brod/engine"
	"brod/store"
)

// Сквозная проверка: запрос ПО-НАСТОЯЩЕМУ через узел до внешнего сайта и разбор ответа (выход: IP, страна, ЦОД Cloudflare). Не зависит от правил
// маршрутизации входа приложений: проверка «открывается ли google.com через вход» на Windows давала 200 и для мёртвых узлов, потому что google.com
// шёл мимо узла по правилам. Здесь трафик идёт в сам outbound узла.

// NodeE2E — итог сквозной проверки узла.
type NodeE2E struct {
	Node    string `json:"node"`              // полный ID узла (пусто — активный выход не узел: WARP)
	Proto   string `json:"proto,omitempty"`   // протокол
	Active  bool   `json:"active"`            // проверялся реальный активный выход (по пути, которым идёт трафик), а не отдельная копия узла
	OK      bool   `json:"ok"`                // запрос дошёл до внешнего сайта через узел
	IP      string `json:"ip,omitempty"`      // внешний адрес выхода
	Country string `json:"country,omitempty"` // страна выхода (ISO)
	Colo    string `json:"colo,omitempty"`    // ЦОД Cloudflare
	Google  string `json:"google,omitempty"`  // ok | fail | ? (поиск Google без капчи)
	Site    string `json:"site,omitempty"`    // ok | fail | ? (строгий сайт под Cloudflare)
	Detail  string `json:"detail,omitempty"`  // причина провала
	Ms      int64  `json:"ms"`                // сколько заняла проверка
}

func triText(v *bool) string {
	switch {
	case v == nil:
		return "?"
	case *v:
		return "ok"
	}
	return "fail"
}

// CheckNodeE2E проверяет узел насквозь. ref — ID или однозначный префикс; пусто или «active» — текущий активный выход (проверяется тот самый
// outbound, по которому идёт трафик, вместе с цепочкой WARP, если она включена).
func (c *Core) CheckNodeE2E(ctx context.Context, ref string) (NodeE2E, error) {
	start := time.Now()
	out := NodeE2E{}
	var p *engine.Probe
	var rec store.NodeRec
	if ref == "" || ref == "active" {
		id := c.activeNodeID()
		c.mu.Lock()
		raw := c.selected[store.ScopeActive]
		c.mu.Unlock()
		if raw == "" {
			return out, fmt.Errorf("%w: активного узла нет", ErrNoNode)
		}
		ap, err := c.eng.Existing(OutActive)
		if err != nil {
			return out, err
		}
		p, out.Active, out.Node = ap, true, id
		if id != "" {
			rec, _ = c.st.Node(id)
		} else {
			out.Proto = raw // warp или direct: узла в базе нет
		}
	} else {
		id, err := c.ResolveNode(ref)
		if err != nil {
			return out, err
		}
		full, err := c.st.Node(id)
		if err != nil {
			return out, err
		}
		opts, err := c.probeOpts(full)
		if err != nil {
			return out, err
		}
		np, err := c.eng.NewProbe(opts)
		if err != nil {
			return out, err
		}
		defer np.Close()
		p, rec, out.Node = np, full, id
	}
	if rec.ID != "" {
		out.Proto = rec.Proto
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	exit := c.cfg.Exit
	r := exit(cctx, rec, p)
	out.Ms = time.Since(start).Milliseconds()
	out.IP, out.Country, out.Colo, out.Detail = r.IP, r.Country, r.Colo, r.Detail
	out.Google, out.Site = triText(r.GoogleOK), triText(r.SiteOK)
	out.OK = r.IP != ""
	if !out.OK && out.Detail == "" {
		out.Detail = "запрос через узел не дошёл до внешнего сайта (таймаут)"
	}
	return out, ctx.Err()
}

// ---- самотест туннеля ----

// SelfTestStep — один шаг самотеста.
type SelfTestStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// SelfTest — итог самотеста VPN.
type SelfTest struct {
	OK        bool           `json:"ok"`
	Mode      string         `json:"mode"`
	Interface string         `json:"interface"`
	Steps     []SelfTestStep `json:"steps"`
}

// selfTestEnv — внешний мир самотеста (подменяется в тестах): имя резолвится системным резолвером (после включения TUN его DNS — в туннеле),
// HTTP-запрос идёт обычным сокетом процесса (после включения TUN — через туннель).
type selfTestEnv struct {
	lookup func(ctx context.Context, host string) ([]string, error)
	get    func(ctx context.Context, url string) (status int, body string, err error)
}

func defaultSelfTestEnv() selfTestEnv {
	return selfTestEnv{
		lookup: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		},
		get: func(ctx context.Context, url string) (int, string, error) {
			// свой Transport без прокси из окружения: трафик должен идти системным путём, то есть через туннель
			hc := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return 0, "", err
			}
			resp, err := hc.Do(req)
			if err != nil {
				return 0, "", err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return resp.StatusCode, string(b), nil
		},
	}
}

// Домены самотеста: заведомо заблокированный в РФ (его имя должно разрешиться через DNS туннеля) и адрес трассировки Cloudflare (даёт внешний IP).
const (
	selfTestBlockedHost = "instagram.com"
	selfTestTraceURL    = "https://www.cloudflare.com/cdn-cgi/trace"
)

func traceIPLoc(body string) (ip, loc string) {
	for _, l := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(l, "ip="):
			ip = strings.TrimSpace(strings.TrimPrefix(l, "ip="))
		case strings.HasPrefix(l, "loc="):
			loc = strings.TrimSpace(strings.TrimPrefix(l, "loc="))
		}
	}
	return
}

// VPNSelfTest проверяет работающий туннель целиком: VPN включён; активный узел отвечает насквозь; заблокированное имя резолвится через DNS
// туннеля в настоящий адрес (не заглушку); внешний запрос обычным сокетом идёт через туннель и, в режимах «всё»/«всё, кроме РФ», выходит с тем
// же IP, что и узел; заблокированный сайт отвечает. Ничего не меняет.
func (c *Core) VPNSelfTest(ctx context.Context) SelfTest {
	return c.vpnSelfTest(ctx, defaultSelfTestEnv())
}

func (c *Core) vpnSelfTest(ctx context.Context, env selfTestEnv) (res SelfTest) {
	res = SelfTest{Mode: c.cfg.TUN.Mode, Interface: c.cfg.TUN.Interface}
	if res.Interface == "" {
		res.Interface = DefaultTUNInterface()
	}
	if res.Mode == "" {
		res.Mode = ModeBlocked
	}
	add := func(name string, ok bool, detail string) {
		res.Steps = append(res.Steps, SelfTestStep{name, ok, detail})
	}
	defer func() {
		res.OK = true
		for _, s := range res.Steps {
			res.OK = res.OK && s.OK
		}
	}()
	if !c.cfg.TUN.Enabled {
		add("vpn", false, "VPN выключен: самотест проверяет работающий туннель (brodctl vpn on)")
		return res
	}
	add("vpn", true, fmt.Sprintf("включён, режим %s, интерфейс %s", res.Mode, res.Interface))

	node, nerr := c.CheckNodeE2E(ctx, "active")
	switch {
	case nerr != nil:
		add("узел насквозь", false, nerr.Error())
	case !node.OK:
		add("узел насквозь", false, "активный узел не отвечает: "+node.Detail)
	default:
		add("узел насквозь", true, fmt.Sprintf("выход %s, %s (%s), %d мс", node.IP, node.Country, node.Colo, node.Ms))
	}

	ips, derr := env.lookup(ctx, selfTestBlockedHost)
	switch {
	case derr != nil:
		add("DNS заблокированного", false, fmt.Sprintf("%s не разрешился: %v", selfTestBlockedHost, derr))
	case !realAddress(ips):
		add("DNS заблокированного", false, fmt.Sprintf("%s → %v: адрес пустой, локальный или заглушка", selfTestBlockedHost, ips))
	default:
		add("DNS заблокированного", true, fmt.Sprintf("%s → %s", selfTestBlockedHost, ips[0]))
	}

	status, body, gerr := env.get(ctx, selfTestTraceURL)
	tunIP, tunLoc := traceIPLoc(body)
	switch {
	case gerr != nil:
		add("внешний запрос через туннель", false, gerr.Error())
	case status != http.StatusOK || tunIP == "":
		add("внешний запрос через туннель", false, fmt.Sprintf("HTTP %d, адрес выхода не получен", status))
	case res.Mode == ModeBlocked:
		// в режиме «только заблокированное» обычные сайты идут напрямую: IP может отличаться от IP узла, это норма
		add("внешний запрос через туннель", true, fmt.Sprintf("ответ получен (режим blocked: незаблокированные сайты идут напрямую), IP %s, %s", tunIP, tunLoc))
	case nerr == nil && node.OK && node.IP != "" && tunIP != node.IP:
		add("внешний запрос через туннель", false, fmt.Sprintf("IP %s (%s) не совпал с IP выхода узла %s: трафик идёт мимо узла (петля, обход туннеля или другой маршрут)", tunIP, tunLoc, node.IP))
	default:
		add("внешний запрос через туннель", true, fmt.Sprintf("IP %s, %s: совпал с выходом узла", tunIP, tunLoc))
	}

	bs, _, berr := env.get(ctx, "https://www."+selfTestBlockedHost+"/")
	if berr != nil {
		add("заблокированный сайт", false, berr.Error())
	} else {
		add("заблокированный сайт", true, fmt.Sprintf("%s отвечает (HTTP %d)", selfTestBlockedHost, bs))
	}
	return res
}

// realAddress: среди адресов есть настоящий (не пустой, не локальный, не из частных диапазонов).
func realAddress(ips []string) bool {
	for _, s := range ips {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		if a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() {
			continue
		}
		return true
	}
	return false
}
