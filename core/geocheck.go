package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // часовые пояса из самой программы: на Windows системной базы зон нет, а проверка пояса без неё молчала бы
)

// Расширенная проверка выхода: страна адреса определяется не одним источником. Сайты берут страну и тип адреса из своих баз (MaxMind,
// IPinfo, IP2Location, собственные), и базы расходятся: Cloudflare может считать выход нидерландским, а база, которой пользуется стриминг или
// банк, — немецким или «дата-центр». Здесь опрашиваются независимые базы тем же путём, каким идут запросы обычных программ.

// GeoSource — ответ одной базы геолокации о внешнем адресе.
type GeoSource struct {
	Name     string
	IP       string
	Country  string // ISO 3166-1 alpha-2, заглавными
	City     string
	Timezone string // IANA, например Europe/Amsterdam
	Org      string // владелец адреса (провайдер, ASN)
	Hosting  *bool  // база считает адрес дата-центром (только ip-api)
	Proxy    *bool  // база считает адрес прокси или VPN (только ip-api)
	Err      string
}

// geoURLs — адреса баз (подменяются в тестах). Ключ — имя источника.
type geoURLs map[string]string

func defaultGeoURLs() geoURLs {
	return geoURLs{
		"ipinfo":      "https://ipinfo.io/json",
		"ipwho.is":    "https://ipwho.is/",
		"ifconfig.co": "https://ifconfig.co/json",
		// бесплатный ip-api отвечает только по http; запрос не содержит ничего, кроме адреса, который и так видит сервер
		"ip-api":   "http://ip-api.com/json/?fields=status,countryCode,city,timezone,isp,org,as,hosting,proxy,query",
		"resolver": "http://ip-api.com/json/%s?fields=status,countryCode,isp,hosting",
	}
}

func orgJoin(parts ...string) string {
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			return p
		}
	}
	return ""
}

// parseGeo разбирает ответ базы name.
func parseGeo(name string, body []byte) (GeoSource, error) {
	g := GeoSource{Name: name}
	switch name {
	case "ipinfo":
		var v struct {
			IP, City, Country, Org, Timezone string
			Bogon                            bool
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return g, err
		}
		g.IP, g.City, g.Country, g.Timezone, g.Org = v.IP, v.City, strings.ToUpper(v.Country), v.Timezone, v.Org
	case "ipwho.is":
		var v struct {
			Success     bool
			Message     string
			IP          string
			CountryCode string `json:"country_code"`
			City        string
			Timezone    struct{ ID string }
			Connection  struct{ Org, ISP, Domain string }
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return g, err
		}
		if !v.Success {
			return g, fmt.Errorf("%s", orgJoin(v.Message, "отказ"))
		}
		g.IP, g.City, g.Country, g.Timezone, g.Org = v.IP, v.City, strings.ToUpper(v.CountryCode), v.Timezone.ID, orgJoin(v.Connection.Org, v.Connection.ISP)
	case "ifconfig.co":
		var v struct {
			IP         string
			CountryISO string `json:"country_iso"`
			City       string
			TimeZone   string `json:"time_zone"`
			ASNOrg     string `json:"asn_org"`
			ASN        string `json:"asn"`
			Hostname   string
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return g, err
		}
		g.IP, g.City, g.Country, g.Timezone = v.IP, v.City, strings.ToUpper(v.CountryISO), v.TimeZone
		g.Org = strings.TrimSpace(v.ASN + " " + v.ASNOrg)
	case "ip-api", "resolver":
		var v struct {
			Status      string
			Message     string
			Query       string
			CountryCode string
			City        string
			Timezone    string
			ISP, Org    string
			AS          string `json:"as"`
			Hosting     *bool
			Proxy       *bool
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return g, err
		}
		if v.Status != "success" {
			return g, fmt.Errorf("%s", orgJoin(v.Message, "отказ"))
		}
		g.IP, g.City, g.Country, g.Timezone = v.Query, v.City, strings.ToUpper(v.CountryCode), v.Timezone
		g.Org = orgJoin(v.AS, v.Org, v.ISP)
		if v.ISP != "" && v.AS != "" {
			g.Org = v.AS
		}
		g.Hosting, g.Proxy = v.Hosting, v.Proxy
	default:
		return g, fmt.Errorf("неизвестная база %q", name)
	}
	if len(g.Country) != 2 {
		return g, fmt.Errorf("в ответе нет страны")
	}
	return g, nil
}

func fetchGeo(ctx context.Context, c *http.Client, name, u string) GeoSource {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return GeoSource{Name: name, Err: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return GeoSource{Name: name, Err: shortErr(err)}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode != http.StatusOK {
		return GeoSource{Name: name, Err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	g, err := parseGeo(name, b)
	if err != nil {
		return GeoSource{Name: name, Err: shortErr(err)}
	}
	return g
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// queryGeo опрашивает базы параллельно; порядок результата стабилен (ipinfo, ipwho.is, ip-api).
func queryGeo(ctx context.Context, c *http.Client, urls geoURLs) []GeoSource {
	names := []string{"ipinfo", "ipwho.is", "ifconfig.co", "ip-api"}
	out := make([]GeoSource, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		u, ok := urls[n]
		if !ok {
			out[i] = GeoSource{Name: n, Err: "адрес не задан"}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			out[i] = fetchGeo(cctx, c, n, u)
		}()
	}
	wg.Wait()
	return out
}

// geoInput — всё, что нужно для выводов (отдельно от сети: проверяется тестами).
type geoInput struct {
	Sources         []GeoSource
	CFCountry       string // страна по Cloudflare (cdn-cgi/trace)
	VPNOn           bool
	Mode            string
	ResolverCountry string
	ResolverOrg     string
	Now             time.Time
	Local           *time.Location
}

// geoFindings делает выводы по ответам баз.
func geoFindings(in geoInput) []DiagFinding {
	var fs []DiagFinding
	add := func(level, title, detail string) { fs = append(fs, DiagFinding{level, title, detail}) }
	// страны по базам, Cloudflare — тоже одна из баз
	type vote struct{ name, cc string }
	var votes []vote
	if in.CFCountry != "" && in.CFCountry != "XX" {
		votes = append(votes, vote{"Cloudflare", in.CFCountry})
	}
	var ok []GeoSource
	var failed []string
	for _, s := range in.Sources {
		if s.Err != "" {
			failed = append(failed, s.Name)
			continue
		}
		ok = append(ok, s)
		votes = append(votes, vote{s.Name, s.Country})
	}
	if len(ok) == 0 {
		add("info", "Базы геолокации не ответили", "Ни ipinfo, ни ipwho.is, ни ip-api не дали ответа (или они недоступны с этого пути): расхождения стран проверить не удалось.")
		return fs
	}
	// «обычные сайты видят выход» только когда трафик идёт через VPN; в режиме «только заблокированное» это обычный адрес
	seen := map[string][]string{}
	for _, v := range votes {
		seen[v.cc] = append(seen[v.cc], v.name)
	}
	var parts []string
	for _, v := range votes {
		parts = append(parts, v.name+": "+v.cc)
	}
	switch {
	case len(seen) > 1:
		add("warn", "Базы геолокации расходятся по стране", strings.Join(parts, " · ")+". Каждый сайт берёт страну из своей базы: те, у кого база говорит иначе, увидят другую страну или заподозрят подмену. Это свойство адреса узла, а не настройки Brod: помогает выбрать узел, на котором базы согласны.")
	case len(votes) < 2:
		add("info", "Страна проверена одной базой", strings.Join(parts, " · ")+".")
	default:
		add("ok", "Базы геолокации согласны по стране", strings.Join(parts, " · ")+".")
	}
	// города
	cities := map[string][]string{}
	for _, s := range ok {
		if s.City != "" {
			cities[strings.ToLower(s.City)] = append(cities[strings.ToLower(s.City)], s.Name+": "+s.City)
		}
	}
	if len(seen) == 1 && len(cities) > 1 {
		var cs []string
		for _, v := range cities {
			cs = append(cs, v...)
		}
		sort.Strings(cs)
		add("info", "Города расходятся", strings.Join(cs, " · ")+". Для баз это обычно: город определяют приблизительно, страна важнее.")
	}
	// тип адреса
	var hosting, proxy []string
	judged := false
	for _, s := range ok {
		if s.Hosting != nil || s.Proxy != nil {
			judged = true
		}
		if s.Hosting != nil && *s.Hosting {
			hosting = append(hosting, s.Name)
		}
		if s.Proxy != nil && *s.Proxy {
			proxy = append(proxy, s.Name)
		}
	}
	switch {
	case len(proxy) > 0:
		add("warn", "Адрес помечен как прокси или VPN", "База "+strings.Join(proxy, ", ")+" считает адрес выхода прокси/VPN: сайты с антифродом (стриминг, банки, соцсети) могут показать «вы используете VPN» или ограничить вход, даже если страна совпадает.")
	case len(hosting) > 0:
		add("warn", "Адрес из дата-центра", "База "+strings.Join(hosting, ", ")+" относит адрес к хостингу, а не к домашнему или мобильному провайдеру. Часть сайтов не доверяет таким адресам (страна при этом может быть верной).")
	case judged:
		add("ok", "Адрес не помечен как дата-центр или прокси", "По базе ip-api это обычный адрес провайдера.")
	}
	for _, s := range ok {
		if s.Org != "" {
			add("info", "Владелец адреса", s.Org+" (по базе "+s.Name+")")
			break
		}
	}
	// часовой пояс: сайты читают его из браузера (JavaScript), и расхождение с поясом страны выхода выдаёт подмену
	if in.VPNOn && in.Mode != ModeBlocked && in.Local != nil {
		for _, s := range ok {
			if s.Timezone == "" {
				continue
			}
			loc, err := time.LoadLocation(s.Timezone)
			if err != nil {
				continue
			}
			_, exitOff := in.Now.In(loc).Zone()
			_, localOff := in.Now.In(in.Local).Zone()
			if exitOff != localOff {
				add("warn", "Часовой пояс устройства не совпадает с поясом выхода", fmt.Sprintf("Устройство: %s (%s), выход: %s (%s). Сайты читают пояс из браузера и сравнивают с адресом — расхождение выдаёт подмену. Часовой пояс устройства Brod не меняет: можно сменить вручную на время работы через этот выход.", in.Local, offString(localOff), s.Timezone, offString(exitOff)))
			} else {
				add("ok", "Часовой пояс устройства совпадает с поясом выхода", fmt.Sprintf("%s (%s).", s.Timezone, offString(exitOff)))
			}
			break
		}
	}
	// DNS
	if in.VPNOn && in.Mode != ModeBlocked && in.ResolverCountry != "" {
		main := ""
		if len(votes) > 0 {
			main = votes[0].cc
		}
		if main != "" && in.ResolverCountry != main {
			add("warn", "DNS-запросы уходят из другой страны", fmt.Sprintf("Резолвер, который видят серверы имён, находится в %s (%s), а выход — в %s. Сайты и сервисы сравнивают их и замечают подмену; проверьте, что DNS идёт через туннель.", in.ResolverCountry, orgJoin(in.ResolverOrg, "владелец неизвестен"), main))
		} else if main != "" {
			add("ok", "DNS в той же стране, что и выход", fmt.Sprintf("%s (%s).", in.ResolverCountry, orgJoin(in.ResolverOrg, "владелец неизвестен")))
		}
	}
	if len(failed) > 0 {
		add("info", "Не ответили базы", strings.Join(failed, ", ")+": выводы сделаны по остальным.")
	}
	add("info", "Чего эта проверка не видит", "WebRTC (браузер может показать настоящий адрес мимо VPN), язык и раскладку браузера, отпечаток браузера, куки и вход в аккаунт с другой страны. Для проверки браузера: browserleaks.com/webrtc.")
	return fs
}

func offString(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	if m := sec % 3600 / 60; m != 0 {
		return fmt.Sprintf("UTC%s%d:%02d", sign, sec/3600, m)
	}
	return fmt.Sprintf("UTC%s%d", sign, sec/3600)
}
