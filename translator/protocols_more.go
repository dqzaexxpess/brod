package translator

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

// Дополнительные схемы: tuic, hysteria (v1), anytls, socks, wireguard/AmneziaWG.

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func tlsSimple(q url.Values, host string, o *Outbound, defALPN ...string) map[string]any {
	t := map[string]any{"enabled": true, "server_name": firstNonEmpty(qget(q, "sni", "peer", "serverName"), host)}
	if truthy(qget(q, "allow_insecure", "allowInsecure", "insecure", "skip-cert-verify")) {
		t["insecure"] = true
		o.Flags.Insecure = true
		o.Notes = append(o.Notes, "insecure")
	}
	if al := splitList(qget(q, "alpn")); len(al) > 0 {
		t["alpn"] = al
	} else if len(defALPN) > 0 {
		t["alpn"] = defALPN
	}
	return t
}

func parseTUIC(link string) (*Outbound, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	id := u.User.Username()
	pw, _ := u.User.Password()
	if !uuidRe.MatchString(id) {
		return nil, fail(ReasonBadUUID, "tuic: не uuid")
	}
	q := u.Query()
	o := &Outbound{Proto: "tuic"}
	opts := map[string]any{"type": "tuic", "server": host, "server_port": port, "uuid": id, "password": pw,
		"tls": tlsSimple(q, host, o, "h3")}
	if cc := strings.ToLower(qget(q, "congestion_control", "congestion-controller")); cc == "bbr" || cc == "cubic" || cc == "new_reno" {
		opts["congestion_control"] = cc
	}
	if m := strings.ToLower(qget(q, "udp_relay_mode", "udp-relay-mode")); m == "native" || m == "quic" {
		opts["udp_relay_mode"] = m
	}
	o.Opts = opts
	o.Key = key("tuic", host, port, id+":"+pw, "", "", qget(q, "sni"))
	return o, nil
}

func parseHysteria1(link string) (*Outbound, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if p := strings.ToLower(q.Get("protocol")); p != "" && p != "udp" {
		return nil, fail(ReasonBadTransport, "hysteria protocol=%s", p)
	}
	o := &Outbound{Proto: "hysteria"}
	opts := map[string]any{"type": "hysteria", "server": host, "server_port": port, "tls": tlsSimple(q, host, o)}
	if a := qget(q, "auth", "auth_str"); a != "" {
		opts["auth_str"] = a
	}
	up, down := 50, 100 // клиентским скоростям нужны ненулевые значения
	for _, sp := range []struct {
		dst  *int
		name string
		keys [2]string
	}{{&up, "upmbps", [2]string{"upmbps", "up"}}, {&down, "downmbps", [2]string{"downmbps", "down"}}} {
		ds := digits(qget(q, sp.keys[0], sp.keys[1]))
		if ds == "" {
			continue
		}
		v, e := strconv.Atoi(ds)
		if e != nil || v > maxHysteriaMbps {
			return nil, fail(ReasonOutOfRange, "hysteria %s: %s больше предела %d", sp.name, ds, maxHysteriaMbps)
		}
		if v > 0 {
			*sp.dst = v
		}
	}
	opts["up_mbps"], opts["down_mbps"] = up, down
	if ob := qget(q, "obfsParam", "obfs-password", "obfs"); ob != "" && !strings.EqualFold(ob, "none") && !strings.EqualFold(ob, "xplus") {
		opts["obfs"] = ob
	}
	o.Opts = opts
	o.Key = key("hysteria", host, port, qget(q, "auth", "auth_str"), "", "", qget(q, "sni", "peer"))
	return o, nil
}

func parseAnyTLS(link string) (*Outbound, error) {
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
		return nil, fail(ReasonMissingField, "anytls: password")
	}
	o := &Outbound{Proto: "anytls"}
	q := u.Query()
	o.Opts = map[string]any{"type": "anytls", "server": host, "server_port": port, "password": pw, "tls": tlsBlock(q, "tls", host, "", o)}
	o.Key = key("anytls", host, port, pw, "", "", qget(q, "sni"))
	return o, nil
}

func parseSocks(link string) (*Outbound, error) {
	i := strings.Index(link, "://")
	scheme := strings.ToLower(link[:i])
	rest := link[i+3:]
	// socks5://base64(user:pass)@host:port — часть источников кодирует учётные данные
	if at := strings.LastIndex(rest, "@"); at > 0 {
		if dec, ok := decodeB64(rest[:at]); ok && strings.Contains(dec, ":") {
			rest = url.QueryEscape(dec) + rest[at:]
			rest = strings.Replace(rest, "%3A", ":", 1)
		}
	}
	u, err := url.Parse("socks://" + strings.TrimPrefix(rest, "//"))
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	ver := "5"
	switch scheme {
	case "socks4":
		ver = "4"
	case "socks4a":
		ver = "4a"
	}
	o := &Outbound{Proto: "socks"}
	opts := map[string]any{"type": "socks", "server": host, "server_port": port, "version": ver}
	if u.User != nil {
		opts["username"] = u.User.Username()
		if p, ok := u.User.Password(); ok {
			opts["password"] = p
		}
	}
	// SOCKS без TLS/шифрования: оператор видит весь трафик, поведение как у security=none (по умолчанию скрыт)
	o.Flags.NoSecurity = true
	o.Notes = append(o.Notes, "security=none")
	o.Opts = opts
	o.Key = key("socks", host, port, u.User.String(), ver, "", "")
	return o, nil
}

// maxHysteriaMbps — предел скорости hysteria: Brutal-контроль слепо шлёт на заданной скорости, а значение идёт в uint64(mbps)*MbpsToBps.
// Реальный максимум в рабочей базе 1000.
const maxHysteriaMbps = 10000

// awgMax — верхние границы числовых параметров AmneziaWG. Движок не ограничивает jc/jmin/jmax: JunkPackets() делает
// make([][]byte, 0, jc) и make([]byte, jmax) на каждое рукопожатие. Реальные значения: jc 3 (по спецификации до 128),
// jmax/s1/s2 до ~1280, s3/s4 до ~64; в рабочей базе максимум 3. h1…h4 — uint32 или диапазон «a-b» из uint32.
var awgMax = map[string]uint64{"jc": 256, "jmin": 4096, "jmax": 4096, "s1": 4096, "s2": 4096, "s3": 4096, "s4": 4096}

var awgKeys = []string{"jc", "jmin", "jmax", "s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4", "i1", "i2", "i3", "i4", "i5"}

// parseWireGuard разбирает wireguard://PRIVATE@host:port?publickey=…&address=…&mtu=…&reserved=1,2,3 (формат v2rayN/Nekobox)
// и wg://. Результат — endpoint, а не outbound: в sing-box WireGuard это endpoint.
// wgKeyOK: ключ WireGuard — ровно 32 байта в base64 (любом из четырёх видов: источники пишут по-разному).
func wgKeyOK(k string) bool {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(k); err == nil {
			return len(b) == 32
		}
	}
	return false
}

// splitAddrs делит список адресов интерфейса: через запятую (v2rayN) или через «-» (Throne: 10.0.0.2/32-fd00::2/128).
func splitAddrs(s string) []string {
	var out []string
	for _, x := range splitList(s) {
		for _, y := range strings.Split(x, "-") {
			if y = strings.TrimSpace(y); y != "" {
				out = append(out, y)
			}
		}
	}
	return out
}

func parseWireGuard(link string) (*Outbound, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fail(ReasonBadURL, "%v", trimErr(err))
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	// ключи base64 содержат «+», который разбор запроса читает как пробел; для ключей берём значения с сохранённым плюсом
	kq, _ := url.ParseQuery(strings.ReplaceAll(u.RawQuery, "+", "%2B"))
	priv := u.User.Username()
	if priv == "" { // Throne и часть клиентов кладут приватный ключ в параметр, а не в userinfo
		priv = qget(kq, "private_key", "privatekey", "privateKey")
	}
	pub := qget(kq, "publickey", "public_key", "peer_public_key", "pub")
	if priv == "" || pub == "" {
		return nil, fail(ReasonMissingField, "wireguard: private/public key")
	}
	if !wgKeyOK(priv) || !wgKeyOK(pub) {
		return nil, fail(ReasonMissingField, "wireguard: ключ не 32 байта base64")
	}
	addrs := splitAddrs(qget(q, "address", "ip", "local_address"))
	if len(addrs) == 0 {
		return nil, fail(ReasonMissingField, "wireguard: address")
	}
	for i, a := range addrs {
		if !strings.Contains(a, "/") {
			if strings.Contains(a, ":") {
				addrs[i] = a + "/128"
			} else {
				addrs[i] = a + "/32"
			}
		}
	}
	peer := map[string]any{"address": host, "port": port, "public_key": pub, "allowed_ips": []string{"0.0.0.0/0", "::/0"}}
	if psk := qget(kq, "presharedkey", "pre_shared_key", "psk"); psk != "" {
		if !wgKeyOK(psk) {
			return nil, fail(ReasonMissingField, "wireguard: psk не 32 байта base64")
		}
		peer["pre_shared_key"] = psk
	}
	if r := splitList(qget(q, "reserved")); len(r) == 3 {
		var rv []uint8
		for _, x := range r {
			n, e := strconv.Atoi(x)
			if e != nil || n < 0 || n > 255 {
				rv = nil
				break
			}
			rv = append(rv, uint8(n))
		}
		if rv != nil {
			peer["reserved"] = rv
		}
	}
	if n, e := strconv.Atoi(qget(q, "persistent_keepalive_interval", "persistent_keepalive", "keepalive")); e == nil && n > 0 && n <= 65535 {
		peer["persistent_keepalive_interval"] = n
	}
	mtu := 1280
	if n, e := strconv.Atoi(qget(q, "mtu")); e == nil && n >= 576 && n <= 1500 {
		mtu = n
	}
	o := &Outbound{Proto: "wireguard", Endpoint: true}
	opts := map[string]any{"type": "wireguard", "address": addrs, "private_key": priv, "mtu": mtu, "peers": []any{peer}}
	for _, k := range awgKeys { // параметры AmneziaWG, если источник их несёт
		if v := qget(q, k, strings.ToUpper(k)); v != "" {
			if err := checkAWG(k, v); err != nil {
				return nil, err
			}
			if n, e := strconv.ParseUint(v, 10, 32); e == nil && !strings.HasPrefix(k, "i") {
				opts[k] = n
			} else {
				opts[k] = v
			}
		}
	}
	o.Opts = opts
	o.Key = key("wireguard", host, port, pub, priv, "", "")
	return o, nil
}

// parseSSR разбирает ssr://base64(host:port:protocol:method:obfs:base64(password)/?obfsparam=…&protoparam=…&remarks=…).
// Результат — outbound типа shadowsocksr (плагин brod/ext/ssr). SSR устарел и слаб: узел помечается заметкой weak-protocol.
func parseSSR(link string) (*Outbound, error) {
	payload := strings.TrimPrefix(link, link[:strings.Index(link, "://")+3])
	if i := strings.Index(payload, "#"); i >= 0 {
		payload = payload[:i]
	}
	dec, ok := decodeB64(payload)
	if !ok {
		return nil, fail(ReasonBadURL, "ssr: base64")
	}
	main, query := dec, ""
	if i := strings.Index(dec, "/?"); i >= 0 {
		main, query = dec[:i], dec[i+2:]
	} else if i := strings.Index(dec, "?"); i >= 0 {
		main, query = dec[:i], dec[i+1:]
	}
	main = strings.TrimSuffix(main, "/")
	parts := strings.Split(main, ":")
	if len(parts) < 6 { // host может быть IPv6 и содержать двоеточия: берём пять полей справа
		return nil, fail(ReasonMissingField, "ssr: поля host:port:protocol:method:obfs:password")
	}
	n := len(parts)
	host := strings.Trim(strings.Join(parts[:n-5], ":"), "[]")
	port, err := strconv.Atoi(parts[n-5])
	if err != nil || port < 1 || port > 65535 {
		return nil, fail(ReasonBadPort, "ssr: порт")
	}
	proto, method, obfsName := strings.ToLower(parts[n-4]), strings.ToLower(parts[n-3]), strings.ToLower(parts[n-2])
	pw, ok := decodeB64(parts[n-1])
	if !ok || host == "" {
		return nil, fail(ReasonMissingField, "ssr: password/host")
	}
	q, _ := url.ParseQuery(query)
	dp := func(k string) string {
		if v := q.Get(k); v != "" {
			if d, ok := decodeB64(v); ok {
				return d
			}
		}
		return ""
	}
	if obfsName == "" {
		obfsName = "plain"
	}
	if proto == "" {
		proto = "origin"
	}
	o := &Outbound{Proto: "shadowsocksr"}
	o.Opts = map[string]any{"type": "shadowsocksr", "server": host, "server_port": port, "method": method, "password": pw,
		"protocol": proto, "obfs": obfsName}
	if v := dp("protoparam"); v != "" {
		o.Opts["protocol_param"] = v
	}
	if v := dp("obfsparam"); v != "" {
		o.Opts["obfs_param"] = v
	}
	o.Notes = append(o.Notes, "weak-protocol:ssr")
	o.Key = key("shadowsocksr", host, port, pw, proto+"/"+obfsName, "", "")
	return o, nil
}

// checkAWG проверяет числовой параметр AmneziaWG: число не выше awgMax; h1…h4 — uint32 или диапазон «a-b» (как разбирает движок).
// Значение, которое движок всё равно отвергнет (не число), тоже отклоняется. i1…i5 (строки CPS) ограничивает сам движок (MaxMessageSize).
func checkAWG(k, v string) error {
	if lim, ok := awgMax[k]; ok {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n > lim {
			return fail(ReasonOutOfRange, "amneziawg %s=%q: нужно целое не больше %d", k, v, lim)
		}
		return nil
	}
	if len(k) == 2 && k[0] == 'h' {
		parts := strings.Split(v, "-")
		if len(parts) > 2 {
			return fail(ReasonOutOfRange, "amneziawg %s=%q: не uint32 и не диапазон", k, v)
		}
		var lo, hi uint64
		for i, p := range parts {
			n, err := strconv.ParseUint(p, 10, 32)
			if err != nil {
				return fail(ReasonOutOfRange, "amneziawg %s=%q: не uint32 и не диапазон", k, v)
			}
			if i == 0 {
				lo, hi = n, n
			} else {
				hi = n
			}
		}
		if hi < lo {
			return fail(ReasonOutOfRange, "amneziawg %s=%q: диапазон наоборот", k, v)
		}
	}
	return nil
}
