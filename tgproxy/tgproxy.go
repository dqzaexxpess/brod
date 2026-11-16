// Package tgproxy — прокси для Telegram (MTProto и SOCKS5): разбор ссылок из источников и честная проверка «жив ли на самом деле».
//
// Форматы ссылок: https://t.me/proxy?server=H&port=P&secret=S, tg://proxy?…, https://t.me/socks?server=&port=&user=&pass=, tg://socks?….
// Секрет MTProto — base64url (16 байт, «простой»), hex `dd…` («secure», padded) или hex `ee…<домен>` (fake-TLS).
package tgproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Виды прокси.
const (
	KindMTProto = "mtproto"
	KindSocks5  = "socks5"
	KindWeb     = "webproxy" // официальный WEB-прокси Telegram (2026): трафик идёт внутри браузерного движка по HTTPS/WebSocket к обычному сайту
)

// Режимы секрета MTProto.
const (
	ModePlain   = "plain"   // 16 байт, транспорт obfuscated2
	ModeSecure  = "secure"  // dd + 16 байт: padded intermediate
	ModeFakeTLS = "faketls" // ee + 16 байт + домен: трафик маскируется под TLS к домену
)

// Proxy — один прокси.
type Proxy struct {
	Kind   string
	Server string
	Port   int
	Secret string // hex в каноническом виде (для MTProto): 32 hex, dd+32 hex или ee+32 hex+hex(домен)
	User   string // SOCKS5
	Pass   string
}

// ID — стабильный идентификатор по виду, адресу, порту и учётным данным.
func (p Proxy) ID() string {
	h := sha256.Sum256([]byte(strings.Join([]string{p.Kind, p.Server, strconv.Itoa(p.Port), p.Secret, p.User, p.Pass}, "|")))
	return hex.EncodeToString(h[:])[:16]
}

// Mode — режим секрета MTProto ("" для SOCKS5).
func (p Proxy) Mode() string {
	if p.Kind != KindMTProto {
		return ""
	}
	switch {
	case strings.HasPrefix(p.Secret, "ee") && len(p.Secret) > 34:
		return ModeFakeTLS
	case strings.HasPrefix(p.Secret, "dd") && len(p.Secret) == 34:
		return ModeSecure
	}
	return ModePlain
}

// keyAndDomain возвращает 16-байтный ключ секрета и домен fake-TLS.
func (p Proxy) keyAndDomain() (key []byte, domain string, err error) {
	raw, err := hex.DecodeString(p.Secret)
	if err != nil {
		return nil, "", err
	}
	switch p.Mode() {
	case ModeFakeTLS:
		return raw[1:17], string(raw[17:]), nil
	case ModeSecure:
		return raw[1:17], "", nil
	}
	if len(raw) != 16 {
		return nil, "", fmt.Errorf("секрет %d байт вместо 16", len(raw))
	}
	return raw, "", nil
}

// Link — ссылка для открытия в Telegram.
func (p Proxy) Link() string {
	q := url.Values{}
	q.Set("server", p.Server)
	if p.Kind == KindWeb {
		q.Set("secret", p.Secret)
		return "tg://webproxy?" + q.Encode()
	}
	q.Set("port", strconv.Itoa(p.Port))
	if p.Kind == KindSocks5 {
		if p.User != "" {
			q.Set("user", p.User)
			q.Set("pass", p.Pass)
		}
		return "tg://socks?" + q.Encode()
	}
	q.Set("secret", p.Secret)
	return "tg://proxy?" + q.Encode()
}

// Parse разбирает ссылку Telegram-прокси.
func Parse(link string) (Proxy, error) {
	link = strings.TrimSpace(strings.ReplaceAll(link, "&amp;", "&"))
	var kind string
	l := strings.ToLower(link)
	switch {
	case strings.HasPrefix(l, "tg://proxy?"), strings.HasPrefix(l, "https://t.me/proxy?"), strings.HasPrefix(l, "http://t.me/proxy?"):
		kind = KindMTProto
	case strings.HasPrefix(l, "tg://socks?"), strings.HasPrefix(l, "https://t.me/socks?"), strings.HasPrefix(l, "http://t.me/socks?"):
		kind = KindSocks5
	case strings.HasPrefix(l, "tg://webproxy?"), strings.HasPrefix(l, "https://t.me/webproxy?"), strings.HasPrefix(l, "http://t.me/webproxy?"):
		kind = KindWeb
	default:
		return Proxy{}, fmt.Errorf("не ссылка Telegram-прокси")
	}
	u, err := url.Parse(link)
	if err != nil {
		return Proxy{}, err
	}
	q := u.Query()
	p := Proxy{Kind: kind, Server: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(q.Get("server")), ".")), User: q.Get("user"), Pass: q.Get("pass")}
	if p.Server == "" || strings.ContainsAny(p.Server, " /\\\"'<>") {
		return Proxy{}, fmt.Errorf("нет адреса сервера")
	}
	port, err := strconv.Atoi(strings.TrimSpace(q.Get("port")))
	if kind != KindWeb && (err != nil || port < 1 || port > 65535) {
		return Proxy{}, fmt.Errorf("неверный порт")
	}
	p.Port = port
	if kind == KindWeb { // у WEB-прокси порта в ссылке нет: всегда HTTPS на 443; секрет — как у MTProxy (16 байт или dd+16), без ee
		p.Port = 443
		if strings.ContainsAny(p.Server, ":@?#") || !strings.Contains(p.Server, ".") {
			return Proxy{}, fmt.Errorf("WEB-прокси: нужно имя хоста (домен)")
		}
		sec, err := normalizeSecret(q.Get("secret"))
		if err != nil {
			return Proxy{}, err
		}
		if strings.HasPrefix(sec, "ee") && len(sec) > 34 {
			return Proxy{}, fmt.Errorf("WEB-прокси: секрет ee не поддерживается")
		}
		p.Secret = sec
		return p, nil
	}
	if kind == KindMTProto {
		sec, err := normalizeSecret(q.Get("secret"))
		if err != nil {
			return Proxy{}, err
		}
		p.Secret = sec
	}
	return p, nil
}

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// normalizeSecret приводит секрет (hex или base64/base64url) к каноническому hex.
func normalizeSecret(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("нет секрета")
	}
	var raw []byte
	if len(s)%2 == 0 && hexRe.MatchString(s) {
		raw, _ = hex.DecodeString(s)
	} else {
		t := strings.TrimRight(strings.NewReplacer("-", "+", "_", "/").Replace(s), "=")
		for len(t)%4 != 0 {
			t += "="
		}
		b, err := base64.StdEncoding.DecodeString(t)
		if err != nil {
			return "", fmt.Errorf("секрет не hex и не base64")
		}
		raw = b
	}
	switch {
	case len(raw) == 16:
	case len(raw) == 17 && raw[0] == 0xdd:
	case len(raw) > 17 && raw[0] == 0xee:
	default:
		return "", fmt.Errorf("секрет неподходящей длины (%d байт)", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

var linkRe = regexp.MustCompile(`(?i)(?:tg://|https?://t\.me/)(?:proxy|socks|webproxy)\?[^\s"'<>\x60\\]+`)

// Блоки вида «Server: x / Port: 443 / Secret: …» (так прокси публикуют каналы Telegram: текстом, иногда с эмодзи и русскими подписями,
// ссылки в таких сообщениях нет). Допускаем разметку HTML между полями; маскированные адреса («host.***», «Unknown») пропускаем.
var (
	tagRe      = regexp.MustCompile(`<[^>]*>`)
	textProxRe = regexp.MustCompile(`(?is)(?:server|сервер)\s*[:：]\s*([A-Za-z0-9][A-Za-z0-9.\-]*[A-Za-z0-9])[^A-Za-z0-9]{0,40}?(?:port|порт)\s*[:：]\s*(\d{2,5})[^A-Za-z0-9]{0,40}?(?:secret|секрет)\s*[:：]\s*([A-Za-z0-9_=\-]{16,})`)
)

func extractTextBlocks(text string) []string {
	text = tagRe.ReplaceAllString(strings.NewReplacer("<br/>", "\n", "<br>", "\n", "&amp;", "&", "&nbsp;", " ").Replace(text), " ")
	var out []string
	for _, m := range textProxRe.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[1], "unknown") {
			continue
		}
		out = append(out, "tg://proxy?server="+m[1]+"&port="+m[2]+"&secret="+m[3])
	}
	return out
}

// hasFold сообщает, есть ли в b подстрока sub без учёта регистра (простая свёртка Unicode, как у (?i)). Один проход по байтам: на порядки
// дешевле регулярки с (?i) по многомегабайтному телу подписки.
func hasFold(b []byte, sub string) bool {
	if sub == "" {
		return true
	}
	var lead [256]bool
	r0, _ := utf8.DecodeRuneInString(sub)
	for r := r0; ; {
		var enc [utf8.UTFMax]byte
		utf8.EncodeRune(enc[:], r)
		lead[enc[0]] = true
		if r = unicode.SimpleFold(r); r == r0 {
			break
		}
	}
	sb := []byte(sub)
	for i := 0; i+len(sb) <= len(b); i++ {
		if lead[b[i]] && bytes.EqualFold(b[i:i+len(sb)], sb) {
			return true
		}
	}
	return false
}

// mayHaveProxies — необходимое условие для обеих регулярок ниже: ссылка требует «tg://» или «t.me/», текстовый блок — «secret»/«секрет».
// Подписки с узлами их почти никогда не содержат, и тяжёлый разбор по ним пропускается.
func mayHaveProxies(body []byte) bool {
	return hasFold(body, "tg://") || hasFold(body, "t.me/") || hasFold(body, "secret") || hasFold(body, "секрет")
}

// ExtractLinks достаёт ссылки Telegram-прокси из тела источника (текст, JSON, HTML), без дублей, в порядке появления.
func ExtractLinks(body []byte) []string {
	if !mayHaveProxies(body) {
		return nil
	}
	return extractLinks(body)
}

func extractLinks(body []byte) []string {
	text := strings.ReplaceAll(string(body), "&amp;", "&")
	seen := map[string]bool{}
	var out []string
	for _, m := range linkRe.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;)")
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, m := range extractTextBlocks(string(body)) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
