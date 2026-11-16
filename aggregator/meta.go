package aggregator

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

// Meta — сведения о подписке из заголовков HTTP и комментариев-заголовков (`# profile-title: …`).
type Meta struct {
	Title       string // название профиля
	UpdateHours int    // рекомендуемый период обновления, часов (0 — не указан)
	Upload      int64  // использовано, байт (subscription-userinfo)
	Download    int64
	Total       int64 // квота, байт (0 — не указана)
	Expire      int64 // unix-секунды окончания (0 — не указан)
}

// IsZero сообщает, что сведений нет.
func (m Meta) IsZero() bool { return m == Meta{} }

// ParseMeta собирает сведения из заголовков ответа (hdr может быть nil) и первых строк тела; заголовки главнее комментариев.
func ParseMeta(hdr http.Header, body []byte) Meta {
	var m Meta
	apply := func(key, val string) {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "profile-title":
			if m.Title == "" {
				m.Title = cleanTitle(val)
			}
		case "profile-update-interval":
			if h, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && m.UpdateHours == 0 && h > 0 && h <= 24*30 {
				m.UpdateHours = h
			}
		case "subscription-userinfo":
			if m.Total == 0 && m.Upload == 0 && m.Download == 0 && m.Expire == 0 {
				parseUserinfo(val, &m)
			}
		}
	}
	for _, k := range []string{"profile-title", "profile-update-interval", "subscription-userinfo"} {
		if v := hdr.Get(k); v != "" {
			apply(k, v)
		}
	}
	lines := strings.Split(string(body[:min(len(body), 4096)]), "\n")
	for i, l := range lines {
		if i >= 20 {
			break
		}
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "#") {
			if k, v, ok := strings.Cut(strings.TrimLeft(l, "# "), ":"); ok {
				apply(k, v)
			}
		}
	}
	return m
}

func parseUserinfo(v string, m *Meta) {
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			m.Upload = n
		case "download":
			m.Download = n
		case "total":
			m.Total = n
		case "expire":
			m.Expire = n
		}
	}
}

// cleanTitle: название приходит из чужого источника — без управляющих символов, не длиннее 80 знаков; префикс base64: декодируется.
func cleanTitle(v string) string {
	v = strings.TrimSpace(v)
	if rest, ok := strings.CutPrefix(v, "base64:"); ok {
		if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest)); err == nil {
			v = string(b)
		}
	}
	var b strings.Builder
	for _, r := range v {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if rs := []rune(out); len(rs) > 80 {
		out = string(rs[:80])
	}
	return out
}
