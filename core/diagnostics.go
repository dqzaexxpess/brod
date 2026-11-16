package core

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Диагностика «что видят сайты»: проверка с точки зрения обычного приложения на этом компьютере (запросы идут тем же путём, что у любой программы:
// при включённом VPN — через TUN по правилам режима). Ничего не меняет, ничего не отправляет, кроме обычных запросов к Cloudflare и Google.

// DiagFinding — вывод диагностики.
type DiagFinding struct {
	Level  string // ok | warn | bad | info
	Title  string
	Detail string
}

// DiagResult — итог диагностики.
type DiagResult struct {
	At              time.Time
	VPNOn           bool
	Mode            string
	IP              string // внешний адрес, который видят сайты
	Country         string
	Colo            string
	WarpOn          bool        // Cloudflare считает, что запрос пришёл через WARP
	IPv6            string      // внешний IPv6, если доступен
	Resolver        string      // адрес рекурсивного DNS-резолвера, который видят авторитетные серверы
	ExitIP          string      // адрес выхода активного узла (из сведений о выходе), если известен
	Geo             []GeoSource // ответы независимых баз геолокации о внешнем адресе (geocheck.go)
	ResolverCountry string
	Findings        []DiagFinding
	Checks          int
	TookMs          int64
}

// diagEnv — внешние зависимости диагностики (подменяются в тестах).
type diagEnv struct {
	http4  *http.Client
	http6  *http.Client
	txt    func(ctx context.Context, name string) ([]string, error)
	geo    geoURLs // адреса баз геолокации (nil — боевые)
	traceV string  // адрес trace по IPv4/по имени
	trace6 string
}

func defaultDiagEnv() diagEnv {
	dial := func(network string) *http.Client {
		return &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			},
		}}
	}
	return diagEnv{
		http4: dial("tcp4"),
		http6: dial("tcp6"),
		txt: func(ctx context.Context, name string) ([]string, error) {
			return net.DefaultResolver.LookupTXT(ctx, name)
		},
		traceV: "https://www.cloudflare.com/cdn-cgi/trace",
		trace6: "https://[2606:4700:4700::1111]/cdn-cgi/trace",
	}
}

// parseTrace разбирает ответ cdn-cgi/trace в карту ключ → значение.
func parseTrace(r io.Reader) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(r, 16<<10))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			m[k] = v
		}
	}
	return m
}

func fetchTrace(ctx context.Context, c *http.Client, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseTrace(resp.Body), nil
}

// resolverKind определяет по адресу, чей это резолвер: публичный (Cloudflare, Google, Quad9, OpenDNS) или чей-то ещё (обычно провайдера).
func resolverKind(ip string) string {
	for _, p := range []struct{ prefix, name string }{
		{"1.1.1.", "Cloudflare"}, {"1.0.0.", "Cloudflare"}, {"162.158.", "Cloudflare"}, {"172.68.", "Cloudflare"}, {"172.69.", "Cloudflare"}, {"172.70.", "Cloudflare"}, {"172.71.", "Cloudflare"},
		{"104.28.", "Cloudflare"}, {"104.23.", "Cloudflare"}, {"104.22.", "Cloudflare"}, {"104.21.", "Cloudflare"}, {"104.20.", "Cloudflare"}, {"108.162.", "Cloudflare"}, {"141.101.", "Cloudflare"},
		{"8.8.8.", "Google"}, {"8.8.4.", "Google"}, {"74.125.", "Google"}, {"172.217.", "Google"}, {"142.250.", "Google"}, {"2a00:1450", "Google"}, {"2606:4700", "Cloudflare"},
		{"9.9.9.", "Quad9"}, {"149.112.", "Quad9"}, {"208.67.", "OpenDNS"},
	} {
		if strings.HasPrefix(ip, p.prefix) {
			return p.name
		}
	}
	return ""
}

// Diagnostics выполняет диагностику.
func (c *Core) Diagnostics(ctx context.Context) DiagResult {
	env := c.diagEnv
	if env.http4 == nil {
		env = defaultDiagEnv()
	}
	start := time.Now()
	res := DiagResult{At: c.cfg.Now(), VPNOn: c.VPNEnabled(), Mode: c.cfg.TUN.withDefaults().Mode}
	add := func(level, title, detail string) {
		res.Findings = append(res.Findings, DiagFinding{level, title, detail})
	}
	if tr, err := fetchTrace(ctx, env.http4, env.traceV); err == nil {
		res.IP, res.Country, res.Colo, res.WarpOn = tr["ip"], strings.ToUpper(tr["loc"]), tr["colo"], tr["warp"] == "on" || tr["warp"] == "plus"
	} else {
		add("bad", "Нет доступа к интернету по IPv4", err.Error())
	}
	res.Checks++
	if tr, err := fetchTrace(ctx, env.http6, env.trace6); err == nil {
		res.IPv6 = tr["ip"]
	}
	res.Checks++
	if txt, err := env.txt(ctx, "o-o.myaddr.l.google.com"); err == nil {
		for _, t := range txt {
			if net.ParseIP(t) != nil {
				res.Resolver = t
			}
		}
	}
	res.Checks++
	if id := c.activeNodeID(); id != "" {
		if m, err := c.st.ExitInfos(); err == nil {
			res.ExitIP = m[id].IP
		}
	}
	// независимые базы геолокации: страна, тип адреса, пояс; и страна DNS-резолвера
	gu := env.geo
	if gu == nil {
		gu = defaultGeoURLs()
	}
	var resolverOrg string
	if res.IP != "" {
		res.Geo = queryGeo(ctx, env.http4, gu)
		res.Checks += len(res.Geo)
		if res.Resolver != "" && gu["resolver"] != "" {
			rg := fetchGeo(ctx, env.http4, "resolver", fmt.Sprintf(gu["resolver"], res.Resolver))
			res.Checks++
			if rg.Err == "" {
				res.ResolverCountry, resolverOrg = rg.Country, rg.Org
			}
		}
	}

	switch {
	case res.IP == "":
	case !res.VPNOn:
		add("info", "VPN выключен", fmt.Sprintf("Сайты видят ваш обычный адрес %s (%s).", res.IP, res.Country))
	case res.Mode == ModeBlocked:
		add("info", "Режим «только заблокированное»", fmt.Sprintf("Обычные сайты видят ваш адрес %s (%s); через VPN идёт только заблокированное — так и задумано.", res.IP, res.Country))
	case res.ExitIP != "" && res.ExitIP == res.IP:
		add("ok", "Сайты видят адрес выхода", fmt.Sprintf("%s (%s) — совпадает с адресом активного узла.", res.IP, res.Country))
	case res.WarpOn:
		add("ok", "Выход через Cloudflare WARP", fmt.Sprintf("Сайты видят адрес Cloudflare %s (%s).", res.IP, res.Country))
	case res.ExitIP != "":
		add("warn", "Виден не адрес активного узла", fmt.Sprintf("Сайты видят %s, а активный узел выходит с %s. Возможно, трафик идёт мимо узла или узел с цепочкой через WARP.", res.IP, res.ExitIP))
	default:
		add("ok", "VPN включён", fmt.Sprintf("Сайты видят %s (%s). Адрес выхода активного узла ещё не определён — сравнить не с чем.", res.IP, res.Country))
	}
	if res.IPv6 != "" {
		if res.VPNOn {
			add("warn", "IPv6 доступен", fmt.Sprintf("Внешний IPv6 %s: при включённом VPN он должен быть заблокирован, иначе возможна утечка мимо туннеля.", res.IPv6))
		} else {
			add("info", "IPv6 доступен", res.IPv6)
		}
	} else if res.VPNOn {
		add("ok", "IPv6 не просачивается", "Внешний IPv6 недоступен.")
	}
	if res.Resolver != "" {
		if kind := resolverKind(res.Resolver); kind != "" {
			add("ok", "DNS: публичный резолвер "+kind, fmt.Sprintf("Серверы имён видят запросы от %s.", res.Resolver))
		} else if res.VPNOn && res.Mode != ModeBlocked {
			add("warn", "DNS: похоже на резолвер провайдера", fmt.Sprintf("Серверы имён видят запросы от %s. В режиме «всё через выход» DNS должен идти через туннель — проверьте настройки DNS системы.", res.Resolver))
		} else {
			add("info", "DNS: резолвер "+res.Resolver, "В этом режиме незаблокированные домены резолвятся локально (быстро и без туннеля).")
		}
	}
	if res.IP != "" {
		res.Findings = append(res.Findings, geoFindings(geoInput{Sources: res.Geo, CFCountry: res.Country, VPNOn: res.VPNOn, Mode: res.Mode,
			ResolverCountry: res.ResolverCountry, ResolverOrg: resolverOrg, Now: c.cfg.Now(), Local: time.Local})...)
	}
	res.TookMs = time.Since(start).Milliseconds()
	return res
}
