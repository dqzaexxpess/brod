package translator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ErrNoExport — для этого протокола или набора параметров ссылка не собирается (остаётся показать параметры подключения).
type ErrNoExport struct{ Why string }

func (e *ErrNoExport) Error() string { return e.Why }

func sget(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func mget(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func nget(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

// ToLink собирает ссылку для «поделиться» из готового outbound (обратное к Parse): vless, vmess, trojan, shadowsocks, hysteria2.
// label — название узла (фрагмент после #).
func ToLink(opts map[string]any, label string) (string, error) {
	host, port := sget(opts, "server"), nget(opts, "server_port")
	if host == "" || port == 0 {
		return "", &ErrNoExport{"нет адреса сервера"}
	}
	hp := net.JoinHostPort(host, strconv.Itoa(port))
	switch t := sget(opts, "type"); t {
	case "vless", "trojan":
		q := url.Values{}
		if t == "vless" {
			q.Set("encryption", "none")
			if f := sget(opts, "flow"); f != "" {
				q.Set("flow", f)
			}
		}
		exportTLS(q, mget(opts, "tls"))
		if err := exportTransport(q, mget(opts, "transport")); err != nil {
			return "", err
		}
		id := sget(opts, "uuid")
		if t == "trojan" {
			id = sget(opts, "password")
		}
		if id == "" {
			return "", &ErrNoExport{"нет учётных данных"}
		}
		return (&url.URL{Scheme: t, User: url.User(id), Host: hp, RawQuery: q.Encode(), Fragment: label}).String(), nil
	case "vmess":
		tr := mget(opts, "transport")
		q := url.Values{}
		if err := exportTransport(q, tr); err != nil {
			return "", err
		}
		net := q.Get("type")
		switch net {
		case "":
			net = "tcp"
		}
		path := q.Get("path")
		if net == "grpc" {
			path = q.Get("serviceName")
		}
		tlsOn, sni, alpn, fp := "", "", "", ""
		if tl := mget(opts, "tls"); tl != nil && tl["enabled"] == true {
			tlsOn, sni = "tls", sget(tl, "server_name")
			if a, ok := tl["alpn"].([]any); ok {
				var parts []string
				for _, x := range a {
					parts = append(parts, fmt.Sprint(x))
				}
				alpn = strings.Join(parts, ",")
			}
			fp = sget(mget(tl, "utls"), "fingerprint")
		}
		doc := map[string]any{"v": "2", "ps": label, "add": host, "port": strconv.Itoa(port), "id": sget(opts, "uuid"), "aid": strconv.Itoa(nget(opts, "alter_id")),
			"scy": firstNonEmpty(sget(opts, "security"), "auto"), "net": net, "type": "none", "host": q.Get("host"), "path": path, "tls": tlsOn, "sni": sni, "alpn": alpn, "fp": fp}
		b, _ := json.Marshal(doc)
		return "vmess://" + base64.StdEncoding.EncodeToString(b), nil
	case "shadowsocks":
		method, pw := sget(opts, "method"), sget(opts, "password")
		if method == "" || pw == "" {
			return "", &ErrNoExport{"нет метода или пароля"}
		}
		u := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(method+":"+pw)) + "@" + hp
		if p := sget(opts, "plugin"); p != "" {
			plug := p
			if po := sget(opts, "plugin_opts"); po != "" {
				plug += ";" + po
			}
			u += "/?plugin=" + url.QueryEscape(plug)
		}
		if label != "" {
			u += "#" + url.PathEscape(label)
		}
		return u, nil
	case "hysteria2":
		q := url.Values{}
		if tl := mget(opts, "tls"); tl != nil {
			if s := sget(tl, "server_name"); s != "" {
				q.Set("sni", s)
			}
			if tl["insecure"] == true {
				q.Set("insecure", "1")
			}
		}
		if ob := mget(opts, "obfs"); ob != nil {
			q.Set("obfs", sget(ob, "type"))
			q.Set("obfs-password", sget(ob, "password"))
		}
		pw := sget(opts, "password")
		if pw == "" {
			return "", &ErrNoExport{"нет пароля"}
		}
		return (&url.URL{Scheme: "hysteria2", User: url.User(pw), Host: hp, RawQuery: q.Encode(), Fragment: label}).String(), nil
	default:
		return "", &ErrNoExport{"для протокола «" + t + "» ссылка не поддерживается"}
	}
}

func exportTLS(q url.Values, tl map[string]any) {
	if tl == nil || tl["enabled"] != true {
		q.Set("security", "none")
		return
	}
	if re := mget(tl, "reality"); re != nil && re["enabled"] == true {
		q.Set("security", "reality")
		q.Set("pbk", sget(re, "public_key"))
		if s := sget(re, "short_id"); s != "" {
			q.Set("sid", s)
		}
	} else {
		q.Set("security", "tls")
	}
	if s := sget(tl, "server_name"); s != "" {
		q.Set("sni", s)
	}
	if fp := sget(mget(tl, "utls"), "fingerprint"); fp != "" {
		q.Set("fp", fp)
	}
	if tl["insecure"] == true {
		q.Set("allowInsecure", "1")
	}
	if a, ok := tl["alpn"].([]any); ok && len(a) > 0 {
		var parts []string
		for _, x := range a {
			parts = append(parts, fmt.Sprint(x))
		}
		q.Set("alpn", strings.Join(parts, ","))
	} else if a, ok := tl["alpn"].([]string); ok && len(a) > 0 {
		q.Set("alpn", strings.Join(a, ","))
	}
}

func exportTransport(q url.Values, tr map[string]any) error {
	if tr == nil {
		q.Set("type", "tcp")
		return nil
	}
	switch t := sget(tr, "type"); t {
	case "ws":
		q.Set("type", "ws")
		p := sget(tr, "path")
		if ed := nget(tr, "max_early_data"); ed > 0 {
			sep := "?"
			if strings.Contains(p, "?") {
				sep = "&"
			}
			p += sep + "ed=" + strconv.Itoa(ed)
		}
		q.Set("path", p)
		if h := mget(tr, "headers"); h != nil && sget(h, "Host") != "" {
			q.Set("host", sget(h, "Host"))
		}
	case "grpc":
		q.Set("type", "grpc")
		q.Set("serviceName", sget(tr, "service_name"))
	case "httpupgrade":
		q.Set("type", "httpupgrade")
		q.Set("path", sget(tr, "path"))
		if h := sget(tr, "host"); h != "" {
			q.Set("host", h)
		}
	case "http":
		q.Set("type", "http")
		q.Set("path", sget(tr, "path"))
		if h, ok := tr["host"].([]any); ok && len(h) > 0 {
			q.Set("host", fmt.Sprint(h[0]))
		} else if h, ok := tr["host"].([]string); ok && len(h) > 0 {
			q.Set("host", h[0])
		}
	case "xhttp":
		q.Set("type", "xhttp")
		q.Set("mode", sget(tr, "mode"))
		q.Set("path", sget(tr, "path"))
		if h := sget(tr, "host"); h != "" {
			q.Set("host", h)
		}
	case "raw":
		q.Set("type", "tcp")
		q.Set("headerType", "http")
		join := func(k string) string {
			var parts []string
			switch v := tr[k].(type) {
			case []any:
				for _, x := range v {
					parts = append(parts, fmt.Sprint(x))
				}
			case []string:
				parts = v
			}
			return strings.Join(parts, ",")
		}
		if h := join("host"); h != "" {
			q.Set("host", h)
		}
		if p := join("path"); p != "" {
			q.Set("path", p)
		}
	default:
		return &ErrNoExport{"транспорт «" + t + "» ссылкой не передаётся"}
	}
	return nil
}
