package prober

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"brod/engine"
)

// ExitInfo — что видят сайты, когда запрос выходит через узел: адрес и страна выхода и поведенческая «чистота».
// Страна берётся из ответа Cloudflare (cdn-cgi/trace) через сам узел — это страна адреса выхода, а не место, где стоит сервер узла.
// Чистота — по фактам: не просит ли Google капчу, не ставит ли строгий сайт под Cloudflare испытание. Поля bool-указатели:
// nil — не проверялось (сеть оборвалась до этой проверки).
type ExitInfo struct {
	IP       string
	Country  string // ISO 3166-1 alpha-2, как отдаёт Cloudflare («XX» — неизвестно)
	Colo     string // ЦОД Cloudflare, принявший запрос (например FRA) — косвенно: где выход
	GoogleOK *bool  // поиск Google без капчи
	SiteOK   *bool  // строгий сайт под Cloudflare открылся без испытания и отказа
	Detail   string // причина провала, если есть
}

// ExitURLs — адреса проверок (подменяются в тестах).
type ExitURLs struct {
	Trace  string
	Google string
	Site   string
}

// DefaultExitURLs — боевые адреса.
var DefaultExitURLs = ExitURLs{
	Trace:  "https://www.cloudflare.com/cdn-cgi/trace",
	Google: "https://www.google.com/search?q=weather&hl=en&gbv=1",
	Site:   "https://chatgpt.com/",
}

// Exit проверяет выход узла p.
func Exit(ctx context.Context, p *engine.Probe) ExitInfo {
	return exitVia(ctx, p.HTTPClient(12*time.Second, 0), DefaultExitURLs)
}

func exitVia(ctx context.Context, c *http.Client, u ExitURLs) ExitInfo {
	// редиректы не идём: ответ Google со ссылкой на /sorry и 302 — сам признак капчи
	nc := *c
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var out ExitInfo

	if resp, err := get(ctx, &nc, u.Trace); err != nil {
		out.Detail = "trace: " + short(err)
		return out
	} else {
		defer resp.Body.Close()
		sc := bufio.NewScanner(io.LimitReader(resp.Body, 16<<10))
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok {
				continue
			}
			switch k {
			case "ip":
				out.IP = v
			case "loc":
				out.Country = strings.ToUpper(v)
			case "colo":
				out.Colo = v
			}
		}
	}

	if resp, err := get(ctx, &nc, u.Google); err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		ok := googleClean(resp)
		out.GoogleOK = &ok
		if !ok {
			out.Detail = join(out.Detail, "Google просит капчу")
		}
	}

	if resp, err := get(ctx, &nc, u.Site); err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		ok := siteClean(resp)
		out.SiteOK = &ok
		if !ok {
			out.Detail = join(out.Detail, "строгий сайт не пускает")
		}
	}
	return out
}

// googleClean: капча — это 429 либо переход на /sorry/. Согласие на куки (consent.google.com) капчей не считается.
func googleClean(r *http.Response) bool {
	if r.StatusCode == http.StatusTooManyRequests {
		return false
	}
	if loc := r.Header.Get("Location"); strings.Contains(loc, "/sorry/") || strings.Contains(loc, "sorry/index") {
		return false
	}
	return r.StatusCode < 500
}

// siteClean: испытание Cloudflare (заголовок cf-mitigated) или 403/429 — «грязный» выход; 200 и редиректы — нормально.
func siteClean(r *http.Response) bool {
	if r.Header.Get("cf-mitigated") != "" {
		return false
	}
	return r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusTooManyRequests && r.StatusCode < 500
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
