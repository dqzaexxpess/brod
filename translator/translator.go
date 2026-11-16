// Package translator превращает ссылки подписок в outbound'ы sing-box-lx.
//
// Выход — map[string]any только из полей белого списка (P6): inbounds/route/dns и прочее из внешних
// источников сюда не попадает. Ошибки типизированы (Reason), чтобы Ф1 мог строить список причин по частоте.
package translator

import (
	"brod/nodeopts"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Reason — устойчивый короткий код причины отказа (для статистики), Detail — пояснение без секретов.
type Reason string

const (
	ReasonBadURL          Reason = "bad-url"
	ReasonUnknownScheme   Reason = "unknown-scheme"
	ReasonTruncated       Reason = "truncated-link"
	ReasonBadUUID         Reason = "bad-uuid"
	ReasonBadPort         Reason = "bad-port"
	ReasonNoHost          Reason = "no-host"
	ReasonBadTransport    Reason = "unsupported-transport"
	ReasonBadSecurity     Reason = "unsupported-security"
	ReasonBadExtra        Reason = "bad-extra-json"
	ReasonBadVMess        Reason = "bad-vmess-payload"
	ReasonMissingField    Reason = "missing-field"
	ReasonTCPHTTPObfs     Reason = "unsupported-tcp-http-obfs"
	ReasonUnsupportedFlow Reason = "unsupported-flow"
	ReasonSSMethod        Reason = "unsupported-ss-method"
	ReasonBadRange        Reason = "bad-range"
	ReasonOutOfRange      Reason = "value-out-of-range" // число из ссылки выше предела, безопасного для движка
	ReasonCertPin         Reason = "unsupported-cert-pin"
)

// Error — ошибка разбора с кодом причины.
type Error struct {
	Reason Reason
	Detail string
}

func (e *Error) Error() string { return string(e.Reason) + ": " + e.Detail }

func fail(r Reason, f string, a ...any) error { return &Error{r, fmt.Sprintf(f, a...)} }

// Outbound — результат разбора.
type Outbound struct {
	Proto    string         // vless|vmess|trojan|hysteria2
	Endpoint bool           // true: это endpoint sing-box (wireguard), а не outbound
	Key      string         // ключ дедупликации (proto, host, port, id/password, transport, path, sni)
	Opts     map[string]any // готовый outbound sing-box-lx без поля tag
	Notes    []string       // небыточные предупреждения: insecure, security=none, отброшенные ключи extra
	Flags    Flags
}

// Flags — признаки узла для UI/политики (P6).
type Flags struct {
	Insecure      bool // allowInsecure / skip-cert-verify
	NoSecurity    bool // security=none (по умолчанию скрыт)
	DownloadSplit bool // extra.downloadSettings — lx не поддерживает, узел помечается
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// certPinKeys — параметры закрепления сертификата в ссылках (в нижнем регистре): hysteria2 pinSHA256, xray pcs и родственные.
var certPinKeys = map[string]bool{
	"pinsha256": true, "pcs": true, "pinnedpeercertsha256": true,
	"pinnedpeercertificatechainsha256": true, "pinnedpeercertificatepublickeysha256": true,
}

// hasCertPin: в query ссылки есть непустой параметр закрепления сертификата.
func hasCertPin(link string) bool {
	_, query, ok := strings.Cut(strings.ReplaceAll(link, "&amp;", "&"), "?")
	if !ok {
		return false
	}
	query, _, _ = strings.Cut(query, "#")
	q, _ := url.ParseQuery(query)
	for k, v := range q {
		if certPinKeys[strings.ToLower(k)] && len(v) > 0 && v[0] != "" {
			return true
		}
	}
	return false
}

// Parse разбирает одну ссылку. Ссылка «insecure + закрепление сертификата» отклоняется: движок закрепление не
// проверяет (pinSHA256 у hysteria2 — хэш сертификата, в движке только хэш открытого ключа), и узел остался бы вообще
// без проверки сертификата, а владелец считал бы его закреплённым.
func Parse(link string) (*Outbound, error) {
	o, err := parse(link)
	if err == nil && o.Flags.Insecure && hasCertPin(link) {
		return nil, fail(ReasonCertPin, "закрепление сертификата (pinSHA256, pcs) не поддерживается; вместе с insecure узел остался бы без проверки сертификата")
	}
	return o, err
}

func parse(link string) (*Outbound, error) {
	link = strings.TrimSpace(link)
	// часть источников отдаёт ссылки с HTML-экранированием (&amp;pbk=...): без этого параметры теряются
	link = strings.ReplaceAll(link, "&amp;", "&")
	i := strings.Index(link, "://")
	if i <= 0 {
		return nil, fail(ReasonBadURL, "нет схемы")
	}
	if strings.Contains(link, "…") || strings.HasSuffix(link, "...") || strings.Contains(link, "%E2%80%A6") {
		return nil, fail(ReasonTruncated, "ссылка обрезана источником")
	}
	switch strings.ToLower(link[:i]) {
	case "vless":
		return parseVLESS(link)
	case "ss":
		o, err := parseSS(link)
		if err != nil {
			// часть источников выдаёт VLESS/Trojan-ссылки под схемой ss:// (userinfo = UUID, в query type/security/sni…)
			if alt, ok := fixSchemeSS(link); ok {
				if o2, err2 := Parse(alt); err2 == nil {
					o2.Notes = append(o2.Notes, "scheme-fixed:ss")
					return o2, nil
				}
			}
		}
		return o, err
	case "tuic":
		return parseTUIC(link)
	case "hysteria":
		return parseHysteria1(link)
	case "anytls":
		return parseAnyTLS(link)
	case "socks", "socks5", "socks5h", "socks4", "socks4a":
		return parseSocks(link)
	case "wireguard", "wg":
		return parseWireGuard(link)
	case "ssr":
		return parseSSR(link)
	case "vmess":
		return parseVMess(link)
	case "trojan":
		return parseTrojan(link)
	case "hysteria2", "hy2":
		return parseHy2(link)
	}
	return nil, fail(ReasonUnknownScheme, "%s", strings.ToLower(link[:i]))
}

func hostPort(u *url.URL) (string, int, error) {
	h := u.Hostname()
	if h == "" {
		return "", 0, fail(ReasonNoHost, "пустой host")
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p < 1 || p > 65535 {
		return "", 0, fail(ReasonBadPort, "порт %q", u.Port())
	}
	return h, p, nil
}

func qget(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// securityOf нормализует security: "", none, false -> none; tls; reality. Остальное — отказ (в т.ч. обрезанные значения).
func securityOf(s string) (string, error) {
	switch strings.ToLower(s) {
	case "", "none", "false":
		return "none", nil
	case "tls", "xtls":
		return "tls", nil
	case "reality":
		return "reality", nil
	}
	return "", fail(ReasonBadSecurity, "%q", s)
}

func tlsBlock(q url.Values, sec, host, hdrHost string, o *Outbound) map[string]any {
	sni := qget(q, "sni", "peer", "serverName")
	if sni == "" {
		sni = hdrHost
	}
	if sni == "" {
		sni = host
	}
	t := map[string]any{"enabled": true, "server_name": sni}
	if truthy(qget(q, "allowInsecure", "insecure", "skip-cert-verify")) {
		t["insecure"] = true
		o.Flags.Insecure = true
		o.Notes = append(o.Notes, "insecure")
	}
	if a := qget(q, "alpn"); a != "" {
		var al []string
		for _, x := range strings.Split(a, ",") {
			if x = strings.TrimSpace(x); x != "" {
				al = append(al, x)
			}
		}
		if len(al) > 0 {
			t["alpn"] = al
		}
	}
	fp := strings.ToLower(qget(q, "fp", "fingerprint"))
	if !knownFP[fp] {
		if fp != "" && fp != "none" {
			o.Notes = append(o.Notes, "fp:неизвестный отброшен")
		}
		fp = ""
	}
	if fp == "" && sec == "reality" {
		fp = "chrome"
	}
	if fp != "" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if sec == "reality" {
		t["reality"] = map[string]any{"enabled": true, "public_key": qget(q, "pbk", "publicKey"), "short_id": qget(q, "sid", "shortId")}
	}
	return t
}

func parseVLESS(link string) (*Outbound, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	id := u.User.Username()
	if id == "" {
		return nil, fail(ReasonBadUUID, "пустой id")
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	sec, err := securityOf(cutFragment(q.Get("security")))
	if err != nil {
		return nil, err
	}
	o := &Outbound{Proto: "vless"}
	if !uuidRe.MatchString(id) {
		// Xray и sing-vmess (uuid.NewV5(Nil, id)) принимают произвольную строку как id
		o.Notes = append(o.Notes, "custom-id")
	}
	flow := q.Get("flow")
	if flow == "xtls-rprx-vision-udp443" { // вариант с разрешением UDP/443; у sing-box его нет, берём базовый vision
		flow = "xtls-rprx-vision"
		o.Notes = append(o.Notes, "flow:vision-udp443->vision")
	}
	if flow != "" && flow != "xtls-rprx-vision" {
		return nil, fail(ReasonUnsupportedFlow, "%q", flow)
	}
	typ := strings.ToLower(cutFragment(q.Get("type")))
	if typ == "" {
		typ = "tcp"
	}
	tr, hdrHost, err := transport(typ, q, o)
	if err != nil {
		return nil, err
	}
	opts := map[string]any{"type": "vless", "server": host, "server_port": port, "uuid": id}
	if flow != "" {
		if typ == "xhttp" || typ == "splithttp" || (tr != nil && tr["type"] == "raw") {
			flow = "" // XHTTP несовместим с vision
		} else {
			opts["flow"] = flow
		}
	}
	if sec == "reality" {
		if err := checkReality(q); err != nil {
			return nil, err
		}
	}
	if sec != "none" {
		opts["tls"] = tlsBlock(q, sec, host, hdrHost, o)
	} else {
		o.Flags.NoSecurity = true
		o.Notes = append(o.Notes, "security=none")
	}
	if tr != nil {
		opts["transport"] = tr
	}
	o.Opts = opts
	o.Key = key("vless", host, port, id, typ, q.Get("path"), qget(q, "sni"))
	return o, nil
}

var knownFP = map[string]bool{"chrome": true, "firefox": true, "edge": true, "safari": true, "360": true, "qq": true,
	"ios": true, "android": true, "random": true, "randomized": true}

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]*$`)

// checkReality отсеивает Reality-узлы с заведомо негодными ключами: движок всё равно их не примет.
func checkReality(q url.Values) error {
	pbk := qget(q, "pbk", "publicKey")
	if pbk == "" {
		return fail(ReasonMissingField, "reality public key")
	}
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(pbk, "=")); err != nil || len(b) != 32 {
		return fail(ReasonMissingField, "reality public key негоден")
	}
	if sid := qget(q, "sid", "shortId"); len(sid) > 16 || len(sid)%2 != 0 || !hexRe.MatchString(sid) {
		return fail(ReasonMissingField, "reality short id негоден")
	}
	return nil
}

var badPct = regexp.MustCompile(`%([^0-9a-fA-F]|[0-9a-fA-F][^0-9a-fA-F]|[0-9a-fA-F]?$)`)

// fixPath экранирует '%' без двух hex-цифр: sing-box разбирает путь как URL и отвергает «%..».
func fixPath(p string) string {
	for badPct.MatchString(p) {
		p = badPct.ReplaceAllStringFunc(p, func(m string) string { return "%25" + m[1:] })
	}
	return p
}

// cutFragment отрезает мусор после '#' в значениях query (в части источников ремарка попадает внутрь параметра).
func cutFragment(s string) string {
	if i := strings.Index(s, "#"); i >= 0 {
		return s[:i]
	}
	return s
}

func key(parts ...any) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = strings.ToLower(fmt.Sprint(p))
	}
	return strings.Join(s, "|")
}

func trimErr(err error) string {
	// *url.Error содержит саму ссылку (хост, userinfo) — в причину отказа её не пускаем
	if ue, ok := err.(*url.Error); ok {
		err = ue.Err
	}
	s := err.Error()
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// transport строит transport sing-box и возвращает Host заголовка (для SNI по умолчанию).
func transport(typ string, q url.Values, o *Outbound) (map[string]any, string, error) {
	host := q.Get("host")
	path := q.Get("path")
	switch typ {
	case "tcp", "raw":
		// отказываем только на настоящем http-обфускаторе; прочие значения (vmess, auto, aes-128-gcm…) — мусор из чужих полей
		if ht := strings.ToLower(q.Get("headerType")); ht == "http" {
			// HTTP-обфускация заголовка: транспорт raw (плагин brod/ext/rawhttp, патч lx 0002); host и path — списки через запятую
			tr := map[string]any{"type": "raw", "header_type": "http"}
			if h := splitList(host); len(h) > 0 {
				tr["host"] = h
			}
			if p := splitList(path); len(p) > 0 {
				tr["path"] = p
			}
			o.Notes = append(o.Notes, "tcp-http-obfs")
			hdr := ""
			if h := splitList(host); len(h) > 0 {
				hdr = h[0]
			}
			return tr, hdr, nil
		}
		return nil, host, nil
	case "ws":
		path = fixPath(path)
		tr := map[string]any{"type": "ws", "path": path}
		if host != "" {
			tr["headers"] = map[string]any{"Host": host}
		}
		if i := strings.Index(path, "ed="); i >= 0 { // ?ed=2048 -> max_early_data
			if n, err := strconv.Atoi(digits(path[i+3:])); err == nil && n > 0 {
				tr["max_early_data"] = n
				tr["early_data_header_name"] = "Sec-WebSocket-Protocol"
				p := strings.TrimRight(path[:i], "?&")
				tr["path"] = p
			}
		}
		return tr, host, nil
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": qget(q, "serviceName", "path")}, host, nil
	case "httpupgrade":
		tr := map[string]any{"type": "httpupgrade", "path": fixPath(path)}
		if host != "" {
			tr["host"] = host
		}
		return tr, host, nil
	case "http", "h2":
		tr := map[string]any{"type": "http", "path": fixPath(path)}
		if host != "" {
			tr["host"] = []string{host}
		}
		return tr, host, nil
	case "xhttp", "splithttp":
		tr, err := xhttp(q, o)
		return tr, host, err
	}
	return nil, "", fail(ReasonBadTransport, "%s", typ)
}

func digits(s string) string {
	for i, c := range s {
		if c < '0' || c > '9' {
			return s[:i]
		}
	}
	return s
}

func parseTrojan(link string) (*Outbound, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	pw := u.User.Username()
	if pw == "" {
		return nil, fail(ReasonMissingField, "password")
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	sec, err := securityOf(q.Get("security"))
	if err != nil {
		return nil, err
	}
	if q.Get("security") == "" {
		sec = "tls" // у trojan TLS по умолчанию
	}
	if sec == "reality" {
		if err := checkReality(q); err != nil {
			return nil, err
		}
	}
	o := &Outbound{Proto: "trojan"}
	typ := strings.ToLower(q.Get("type"))
	if typ == "" {
		typ = "tcp"
	}
	tr, hdrHost, err := transport(typ, q, o)
	if err != nil {
		return nil, err
	}
	opts := map[string]any{"type": "trojan", "server": host, "server_port": port, "password": pw}
	if sec != "none" {
		opts["tls"] = tlsBlock(q, sec, host, hdrHost, o)
	} else {
		o.Flags.NoSecurity = true
		o.Notes = append(o.Notes, "security=none")
	}
	if tr != nil {
		opts["transport"] = tr
	}
	o.Opts = opts
	o.Key = key("trojan", host, port, pw, typ, q.Get("path"), qget(q, "sni"))
	return o, nil
}

func parseHy2(link string) (*Outbound, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	pw := u.User.Username()
	if p, ok := u.User.Password(); ok {
		pw += ":" + p
	}
	if pw == "" {
		return nil, fail(ReasonMissingField, "auth")
	}
	q := u.Query()
	o := &Outbound{Proto: "hysteria2"}
	t := map[string]any{"enabled": true, "server_name": firstNonEmpty(qget(q, "sni"), host)}
	if truthy(qget(q, "insecure", "allowInsecure")) {
		t["insecure"] = true
		o.Flags.Insecure = true
		o.Notes = append(o.Notes, "insecure")
	}
	opts := map[string]any{"type": "hysteria2", "server": host, "server_port": port, "password": pw, "tls": t}
	if ob := q.Get("obfs"); ob != "" {
		opts["obfs"] = map[string]any{"type": ob, "password": qget(q, "obfs-password")}
	}
	o.Opts = opts
	o.Key = key("hysteria2", host, port, pw, "", "", qget(q, "sni"))
	return o, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

type vmessJSON struct {
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Scy  string          `json:"scy"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  string          `json:"tls"`
	SNI  string          `json:"sni"`
	ALPN string          `json:"alpn"`
	FP   string          `json:"fp"`
}

func parseVMess(link string) (*Outbound, error) {
	payload := strings.TrimPrefix(link, link[:strings.Index(link, "://")+3])
	if i := strings.IndexAny(payload, "#?"); i >= 0 {
		payload = payload[:i]
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(payload); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fail(ReasonBadVMess, "base64")
	}
	var v vmessJSON
	if json.Unmarshal(raw, &v) != nil {
		return nil, fail(ReasonBadVMess, "json")
	}
	port, perr := strconv.Atoi(strings.Trim(string(v.Port), `"`))
	if perr != nil || port < 1 || port > 65535 {
		return nil, fail(ReasonBadPort, "порт")
	}
	if v.Add == "" {
		return nil, fail(ReasonNoHost, "add")
	}
	if v.ID == "" {
		return nil, fail(ReasonBadUUID, "пустой id")
	}
	aid, _ := strconv.Atoi(strings.Trim(string(v.Aid), `"`))
	q := url.Values{}
	set := func(k, val string) {
		if val != "" {
			q.Set(k, val)
		}
	}
	set("type", v.Net)
	set("host", v.Host)
	set("path", v.Path)
	set("sni", v.SNI)
	set("alpn", v.ALPN)
	set("fp", v.FP)
	set("headerType", v.Type)
	if v.Net == "grpc" {
		set("serviceName", v.Path)
	}
	sec := "none"
	if strings.EqualFold(v.TLS, "tls") {
		sec = "tls"
	}
	o := &Outbound{Proto: "vmess"}
	typ := strings.ToLower(v.Net)
	if typ == "" {
		typ = "tcp"
	}
	tr, hdrHost, err := transport(typ, q, o)
	if err != nil {
		return nil, err
	}
	scy := v.Scy
	if scy == "" {
		scy = "auto"
	}
	opts := map[string]any{"type": "vmess", "server": v.Add, "server_port": port, "uuid": v.ID, "security": scy, "alter_id": aid}
	if sec == "tls" {
		opts["tls"] = tlsBlock(q, sec, v.Add, hdrHost, o)
	} else {
		o.Flags.NoSecurity = true
		o.Notes = append(o.Notes, "security=none")
	}
	if tr != nil {
		opts["transport"] = tr
	}
	o.Opts = opts
	o.Key = key("vmess", v.Add, port, v.ID, typ, v.Path, v.SNI)
	return o, nil
}

// ssMethods — шифры, которые принимает sing-box (остальное — отказ с классом unsupported-ss-method).
var ssMethods = map[string]bool{
	"aes-128-gcm": true, "aes-192-gcm": true, "aes-256-gcm": true, "chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true, "2022-blake3-chacha20-poly1305": true,
	"aes-128-ctr": true, "aes-192-ctr": true, "aes-256-ctr": true, "aes-128-cfb": true, "aes-192-cfb": true, "aes-256-cfb": true,
	"chacha20-ietf": true, "xchacha20": true, "none": true, "rc4-md5": true,
}

func decodeB64(s string) (string, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// parseSS разбирает ss:// в обоих распространённых видах: SIP002 (ss://base64(method:pass)@host:port?plugin=...#tag)
// и старый (ss://base64(method:pass@host:port)#tag).
func parseSS(link string) (*Outbound, error) {
	rest := strings.TrimPrefix(link, link[:strings.Index(link, "://")+3])
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i+1:]
	}
	var userinfo, hostport string
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		userinfo, hostport = rest[:i], rest[i+1:]
		if dec, ok := decodeB64(userinfo); ok && strings.Contains(dec, ":") {
			userinfo = dec
		} else if u, err := url.PathUnescape(userinfo); err == nil {
			userinfo = u // method:password без base64
		}
	} else {
		dec, ok := decodeB64(rest)
		if !ok || !strings.Contains(dec, "@") {
			return nil, fail(ReasonBadURL, "ss: не разобрать")
		}
		j := strings.LastIndex(dec, "@")
		userinfo, hostport = dec[:j], dec[j+1:]
	}
	k := strings.Index(userinfo, ":")
	if k <= 0 {
		return nil, fail(ReasonMissingField, "ss: method:password")
	}
	method, pw := strings.ToLower(userinfo[:k]), userinfo[k+1:]
	switch method { // алиасы шифров из старых клиентов
	case "chacha20-poly1305":
		method = "chacha20-ietf-poly1305"
	case "xchacha20-poly1305":
		method = "xchacha20-ietf-poly1305"
	case "aead_aes_128_gcm":
		method = "aes-128-gcm"
	case "aead_aes_256_gcm":
		method = "aes-256-gcm"
	case "aead_chacha20_poly1305":
		method = "chacha20-ietf-poly1305"
	}
	if !ssMethods[method] {
		return nil, fail(ReasonSSMethod, "%s", method)
	}
	u, err := url.Parse("ss://" + hostport)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	o := &Outbound{Proto: "shadowsocks"}
	if method == "none" { // без шифрования: то же, что security=none у остальных протоколов
		o.Flags.NoSecurity = true
		o.Notes = append(o.Notes, "method=none")
	}
	opts := map[string]any{"type": "shadowsocks", "server": host, "server_port": port, "method": method, "password": pw}
	if q, _ := url.ParseQuery(query); q.Get("plugin") != "" {
		plug := q.Get("plugin")
		name, popts := plug, ""
		if i := strings.Index(plug, ";"); i >= 0 {
			name, popts = plug[:i], plug[i+1:]
		}
		// из опций плагина проходят только известные ключи: cert=/key= заставили бы демон читать файлы на диске
		popts, dropped := nodeopts.PluginOpts(popts)
		for _, d := range dropped {
			o.Notes = append(o.Notes, "plugin:"+d+" отброшен")
		}
		switch name {
		case "obfs-local", "simple-obfs":
			opts["plugin"], opts["plugin_opts"] = "obfs-local", popts
		case "v2ray-plugin":
			opts["plugin"], opts["plugin_opts"] = "v2ray-plugin", popts
		default:
			return nil, fail(ReasonBadTransport, "ss plugin %s", name)
		}
	}
	o.Opts = opts
	o.Key = key("shadowsocks", host, port, pw, method, "", "")
	return o, nil
}

// fixSchemeSS пытается определить настоящий протокол у ss://-ссылки с query от Xray-URI: UUID в userinfo → vless, иначе trojan.
func fixSchemeSS(link string) (string, bool) {
	rest := strings.TrimPrefix(link, "ss://")
	at := strings.Index(rest, "@")
	if at <= 0 || !strings.Contains(rest, "?") {
		return "", false
	}
	q := rest[strings.Index(rest, "?")+1:]
	if !strings.Contains(q, "type=") && !strings.Contains(q, "security=") && !strings.Contains(q, "amp;type=") {
		return "", false
	}
	if uuidRe.MatchString(rest[:at]) {
		return "vless://" + rest, true
	}
	return "trojan://" + rest, true
}

// Class — класс узла «протокол/транспорт/безопасность» (по готовому outbound'у): ключ для априорных долей успеха.
func Class(o map[string]any) string {
	typ, _ := o["type"].(string)
	tr := "tcp"
	if t, ok := o["transport"].(map[string]any); ok {
		if v, _ := t["type"].(string); v != "" {
			tr = v
		}
	}
	sec := "none"
	if t, ok := o["tls"].(map[string]any); ok {
		sec = "tls"
		if _, r := t["reality"]; r {
			sec = "reality"
		}
	}
	return typ + "/" + tr + "/" + sec
}
