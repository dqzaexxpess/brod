// Package prober реализует уровни проверки L1–L4 из P2.
//
// L2–L4 ходят через пробный outbound движка (engine.Probe) с тем же конфигом, что и боевой узел.
// TCP-пинг (L1) никогда не делает узел «живым» — это только предфильтр.
package prober

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"time"

	"brod/engine"
)

// Class — класс отказа (P2).
type Class string

const (
	ClassDNS      Class = "dns"
	ClassTCP      Class = "tcp"
	ClassTLS      Class = "tls/handshake"
	ClassAuth     Class = "auth"
	ClassFreeze   Class = "freeze"
	ClassTimeout  Class = "timeout"
	ClassBlocked  Class = "target-blocked"
	ClassBotCheck Class = "bot-check"
	ClassOther    Class = "other"
	// ClassCanceled — проверка прервана нами (остановка прогона); результатом не считается.
	ClassCanceled Class = "canceled"
)

// Result — итог одной проверки.
type Result struct {
	Level     int     `json:"level"`
	Target    string  `json:"target,omitempty"`
	OK        bool    `json:"ok"`
	Class     Class   `json:"class,omitempty"`
	LatencyMs int64   `json:"latency_ms"`
	Bytes     int64   `json:"bytes,omitempty"`
	SpeedKBps float64 `json:"speed_kbps,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

// Коды Winsock для «отказ / сеть или узел недоступны»: текст этих ошибок на Windows свой (и зависит от языка системы), поэтому они
// распознаются по коду. На других системах таких значений errno нет.
var winsockTCP = []syscall.Errno{10061 /* WSAECONNREFUSED */, 10051 /* WSAENETUNREACH */, 10065 /* WSAEHOSTUNREACH */}

// Classify относит ошибку к классу P2. bytes>0 при таймауте/обрыве означает заморозку (данные пошли и встали).
func Classify(err error, bytes int64) Class {
	if err == nil {
		return ""
	}
	for _, code := range winsockTCP {
		if errors.Is(err, code) {
			return ClassTCP
		}
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "context canceled"):
		return ClassCanceled
	case strings.Contains(s, "unexpected http response status"), strings.Contains(s, "unexpected upload status"), strings.Contains(s, "unexpected download status"), strings.Contains(s, "unexpected status"):
		return ClassAuth
	case strings.Contains(s, "unknown version"):
		return ClassTLS
	case strings.Contains(s, "no such host"), strings.Contains(s, "lookup "):
		return ClassDNS
	case strings.Contains(s, "connection refused"), strings.Contains(s, "no route to host"), strings.Contains(s, "network is unreachable"):
		return ClassTCP
	case strings.Contains(s, "x509"), strings.Contains(s, "tls:"), strings.Contains(s, "handshake"), strings.Contains(s, "certificate"), strings.Contains(s, "reality"):
		return ClassTLS
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "i/o timeout"), strings.Contains(s, "timeout"):
		if bytes > 0 {
			return ClassFreeze
		}
		return ClassTimeout
	case strings.Contains(s, "eof"), strings.Contains(s, "connection reset"), strings.Contains(s, "broken pipe"):
		if bytes > 0 {
			return ClassFreeze
		}
		return ClassTLS
	}
	return ClassOther
}

var (
	reURL  = regexp.MustCompile(`https?://[^\s"]+`)
	reIPv4 = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	reIPv6 = regexp.MustCompile(`\[[0-9a-fA-F:]{3,}\](:\d+)?`)
	// движок пишет «lookup failed for имя: …» и «cache-only lookup missed for имя», Go — «lookup имя: …»
	reLookup = regexp.MustCompile(`lookup (?:(?:failed|missed) for )?[^\s:]+`)
	// «open connection to имя:порт using outbound/…»; для «for» нужно имя с точкой, иначе «for 3 attempts» тоже попадёт
	reConnTo = regexp.MustCompile(`connection to [^\s:]+(:\d+)?`)
	reForDom = regexp.MustCompile(`\bfor (?:[A-Za-z0-9_-]+\.)+[A-Za-z][A-Za-z0-9_-]*(:\d+)?`)
	// IPv6 без квадратных скобок: полная запись из 8 групп либо сжатая с «::» (одиночные «12:34:56» не трогаем)
	reIPv6Full = regexp.MustCompile(`(?i)(^|[^0-9a-z:.])(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}`)
	reIPv6Comp = regexp.MustCompile(`(?i)(^|[^0-9a-z:.])(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6}::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6})?|::[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6})`)
	reCert     = regexp.MustCompile(`(valid for|none matched|wanted to match) .*`)
)

// sanitize вычищает из текста сетевой ошибки адреса и имена узлов: Detail попадает в журналы и файлы результатов,
// а хосты чужих узлов и наш локальный адрес там появляться не должны.
func sanitize(s string) string { return Sanitize(s) }

// Sanitize вычищает из текста адреса, URL и имена узлов (для журналов и ответов API).
func Sanitize(s string) string {
	s = reURL.ReplaceAllString(s, "<url>")
	s = reLookup.ReplaceAllString(s, "lookup <dom>")
	s = reConnTo.ReplaceAllString(s, "connection to <dom>")
	s = reForDom.ReplaceAllString(s, "for <dom>")
	s = reCert.ReplaceAllString(s, "$1 <dom>")
	s = reIPv6.ReplaceAllString(s, "<ip6>")
	s = reIPv6Full.ReplaceAllString(s, "${1}<ip6>")
	s = reIPv6Comp.ReplaceAllString(s, "${1}<ip6>")
	return reIPv4.ReplaceAllString(s, "<ip>")
}

func short(err error) string {
	s := sanitize(err.Error())
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func fin(r Result, start time.Time, err error) Result {
	r.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		r.OK = false
		r.Class = Classify(err, r.Bytes)
		r.Detail = short(err)
	}
	return r
}

// L1 — TCP connect к host:port напрямую (с пути процесса). Только предфильтр.
func L1(ctx context.Context, hostport string, timeout time.Duration) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", hostport)
	if err != nil {
		return fin(Result{Level: 1}, start, err)
	}
	_ = c.Close()
	return fin(Result{Level: 1, OK: true}, start, nil)
}

// L2 — полный хендшейк через узел и HTTPS GET generate_204 (DNS резолвится на стороне узла).
// L2Timeout — общий таймаут L2 (хорошие узлы укладываются в 1–3 с).
var L2Timeout = 6 * time.Second

func L2(ctx context.Context, p *engine.Probe) Result { return L2Within(ctx, p, L2Timeout) }

// L2Within — L2 с явным общим таймаутом: проверка WARP на холодном туннеле (первое рукопожатие на мобильной сети занимает больше L2Timeout).
func L2Within(ctx context.Context, p *engine.Probe, timeout time.Duration) Result {
	const target = "https://www.gstatic.com/generate_204"
	start := time.Now()
	r := Result{Level: 2, Target: "gstatic"}
	resp, err := get(ctx, p.HTTPClient(timeout, 0), target)
	if err != nil {
		return fin(r, start, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		r.Class, r.Detail = ClassOther, fmt.Sprintf("HTTP %d вместо 204", resp.StatusCode)
		return fin(r, start, nil).withFail()
	}
	r.OK = true
	return fin(r, start, nil)
}

func (r Result) withFail() Result { r.OK = false; return r }

// L3 — загрузка ≥minBytes с замером скорости. Простой чтения дольше stall = заморозка (класс freeze).
func L3(ctx context.Context, p *engine.Probe, minBytes int64) Result {
	start := time.Now()
	r := Result{Level: 3, Target: "cloudflare-speed"}
	url := fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", minBytes+minBytes/2)
	resp, err := get(ctx, p.HTTPClient(25*time.Second, 6*time.Second), url)
	if err != nil {
		return fin(r, start, err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	r.Bytes = n
	if err == nil && n < minBytes {
		err = fmt.Errorf("получено %d байт из %d: %w", n, minBytes, io.ErrUnexpectedEOF)
	}
	if err != nil {
		return fin(r, start, err)
	}
	if el := time.Since(start).Seconds(); el > 0 {
		r.SpeedKBps = float64(n) / 1024 / el
	}
	r.OK = true
	return fin(r, start, nil)
}

// Фиксированное видео для проверки playabilityStatus (первое видео YouTube, давно стабильно).
const ytVideoID = "jNQXAC9IVRw"

// YouTubeURLs — адреса проверки YouTube (подменяются в тестах).
type YouTubeURLs struct {
	Redirector string // googlevideo generate_204
	Watch      string // страница просмотра
}

// DefaultYouTubeURLs — боевые адреса.
var DefaultYouTubeURLs = YouTubeURLs{
	Redirector: "https://redirector.googlevideo.com/generate_204",
	Watch:      "https://www.youtube.com/watch?v=" + ytVideoID + "&hl=en",
}

var playabilityRe = regexp.MustCompile(`"playabilityStatus":\{"status":"([A-Z_]+)"(?:,"reason":"([^"]*)")?`)

// L4YouTube — googlevideo generate_204 (видеосервера достижимы) и страница просмотра: статус воспроизведения из ytInitialPlayerResponse.
// Раньше статус брался из innertube-запроса клиента WEB, но он без PO-токена даёт UNPLAYABLE «Video unavailable» и на заведомо рабочем выходе
// (проверено: страница тут же отдаёт OK) — тест браковал почти все хорошие узлы. Ответ «подтвердите, что вы не бот» — провал с классом bot-check.
func L4YouTube(ctx context.Context, p *engine.Probe) Result {
	return youtubeVia(ctx, p.HTTPClient(15*time.Second, 0), DefaultYouTubeURLs)
}

func youtubeVia(ctx context.Context, c *http.Client, u YouTubeURLs) Result {
	start := time.Now()
	r := Result{Level: 4, Target: "youtube"}
	resp, err := get(ctx, c, u.Redirector)
	if err != nil {
		return fin(r, start, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		r.Class, r.Detail = ClassBlocked, fmt.Sprintf("googlevideo HTTP %d", resp.StatusCode)
		return fin(r, start, nil).withFail()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.Watch, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cookie", "SOCS=CAI; CONSENT=YES+1") // без страницы согласия на куки, которую показывают европейским адресам
	resp, err = c.Do(req)
	if err != nil {
		return fin(r, start, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		r.Class, r.Detail = ClassBotCheck, "HTTP 429"
		return fin(r, start, nil).withFail()
	}
	if resp.StatusCode != http.StatusOK {
		r.Class, r.Detail = ClassBlocked, fmt.Sprintf("страница просмотра HTTP %d", resp.StatusCode)
		return fin(r, start, nil).withFail()
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	m := playabilityRe.FindSubmatch(data)
	if m == nil {
		r.Class, r.Detail = ClassBlocked, "в странице нет статуса воспроизведения"
		return fin(r, start, nil).withFail()
	}
	st, why := string(m[1]), strings.ToLower(string(m[2]))
	switch {
	case st == "OK":
		r.OK = true
	case strings.Contains(why, "bot"), strings.Contains(why, "sign in"), st == "LOGIN_REQUIRED":
		r.Class, r.Detail = ClassBotCheck, st
	default:
		r.Class, r.Detail = ClassBlocked, st
	}
	return fin(r, start, nil)
}

// L4Telegram — достижимость API Telegram: TLS-хендшейк и любой HTTP-ответ.
func L4Telegram(ctx context.Context, p *engine.Probe) Result {
	start := time.Now()
	r := Result{Level: 4, Target: "telegram"}
	resp, err := get(ctx, p.HTTPClient(10*time.Second, 0), "https://api.telegram.org/")
	if err != nil {
		return fin(r, start, err)
	}
	resp.Body.Close()
	r.OK = true
	return fin(r, start, nil)
}

func get(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	resp, err := c.Do(req)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return resp, err
}

// L4GoogleAPI — цель google-api (P7): GET googleapis.com/discovery/v1/apis должен дать 200 (403 = класс target-blocked),
// плюс достижимость games.googleapis.com (любой HTTP-ответ). Без этого не работают Play Игры, установка PWA и сертификация Play.
func L4GoogleAPI(ctx context.Context, p *engine.Probe) Result {
	start := time.Now()
	r := Result{Level: 4, Target: "google-api"}
	resp, err := get(ctx, p.HTTPClient(10*time.Second, 0), "https://www.googleapis.com/discovery/v1/apis")
	if err != nil {
		return fin(r, start, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.Class, r.Detail = ClassBlocked, fmt.Sprintf("googleapis HTTP %d", resp.StatusCode)
		return fin(r, start, nil).withFail()
	}
	resp, err = get(ctx, p.HTTPClient(10*time.Second, 0), "https://games.googleapis.com/")
	if err != nil {
		return fin(r, start, err)
	}
	resp.Body.Close()
	r.OK = true
	return fin(r, start, nil)
}
