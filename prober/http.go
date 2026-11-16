package prober

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"brod/engine"
)

// HTTPSpec — описание проверки пользовательской цели (P7), например videosite или videosite2.
type HTTPSpec struct {
	ID          string // имя цели (поле Target результата)
	URL         string // что запрашивать
	ExpectCodes []int  // допустимые коды ответа (пусто = только 200)
	// RangeURL и MinBytes: проверка потока — Range-запрос по пробной ссылке, должно прийти не меньше MinBytes
	// (ссылки CDN подписаны и живут часы; их обновляет владелец цели). Без RangeURL проверяется только URL.
	RangeURL string
	MinBytes int64
	// MinKBps — нижний порог скорости Range-загрузки (КиБ/с, от начала запроса); медленнее — провал freeze. 0 = без порога.
	MinKBps float64
}

// CheckHTTP проверяет цель через узел p. Провалы: сеть/TLS — по классу ошибки, неожиданный код — target-blocked,
// недобор байт, простой чтения дольше 6 с или скорость ниже MinKBps — freeze.
func CheckHTTP(ctx context.Context, p *engine.Probe, s HTTPSpec) Result {
	start := time.Now()
	r := Result{Level: 4, Target: s.ID}
	if s.RangeURL != "" && s.MinBytes > 0 {
		return checkRange(ctx, p, s, r, start)
	}
	resp, err := get(ctx, p.HTTPClient(12*time.Second, 0), s.URL)
	if err != nil {
		return fin(r, start, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if !codeAllowed(resp.StatusCode, s.ExpectCodes) {
		r.Class, r.Detail = ClassBlocked, fmt.Sprintf("HTTP %d", resp.StatusCode)
		return fin(r, start, nil).withFail()
	}
	r.OK = true
	return fin(r, start, nil)
}

func codeAllowed(code int, expect []int) bool {
	if len(expect) == 0 {
		return code == http.StatusOK
	}
	for _, c := range expect {
		if c == code {
			return true
		}
	}
	return false
}

// maxRangeRedirects — сколько переадресаций проходит проверка потока.
const maxRangeRedirects = 5

func checkRange(ctx context.Context, p *engine.Probe, s HTTPSpec, r Result, start time.Time) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.RangeURL, nil)
	if err != nil {
		return fin(r, start, err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", s.MinBytes-1))
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	c := p.HTTPClient(25*time.Second, 6*time.Second)
	// Подписанные ссылки CDN обычно переадресуют на один из его серверов (302 → 206). Остальные проверки редиректы не проходят: там 302
	// часто означает заглушку блокировки; здесь ссылку дал владелец цели, и важен итог — пришли ли байты. Range сохраняется при переходе.
	c.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) >= maxRangeRedirects {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := c.Do(req)
	if err != nil {
		return fin(r, start, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		r.Class, r.Detail = ClassBlocked, fmt.Sprintf("HTTP %d", resp.StatusCode)
		return fin(r, start, nil).withFail()
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, s.MinBytes))
	r.Bytes = n
	if err == nil && n < s.MinBytes {
		err = fmt.Errorf("получено %d байт из %d: %w", n, s.MinBytes, io.ErrUnexpectedEOF)
	}
	if err != nil {
		return fin(r, start, err)
	}
	if el := time.Since(start).Seconds(); el > 0 {
		r.SpeedKBps = float64(n) / 1024 / el
	}
	if s.MinKBps > 0 && r.SpeedKBps > 0 && r.SpeedKBps < s.MinKBps { // 0 — время не измерить (быстрее разрешения таймера)
		r.Class, r.Detail = ClassFreeze, fmt.Sprintf("скорость %.0f КиБ/с ниже порога %.0f", r.SpeedKBps, s.MinKBps)
		return fin(r, start, nil).withFail()
	}
	r.OK = true
	return fin(r, start, nil)
}
