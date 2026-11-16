// Package importer превращает полные конфиги из публичных источников (sing-box JSON, Clash YAML, Xray JSON) в обычные
// ссылки узлов (P6). Ссылку потом разбирает translator — это единственная граница доверия: из чужого конфига попадает ровно то,
// что выражается ссылкой (сервер, порт, учётные данные, TLS/Reality, транспорт). Всё остальное — inbounds, route, dns,
// clash_api/external_controller, detour, bind_interface, routing_mark, группы, скрипты — отбрасывается по построению, а не по
// чёрному списку, и потому не может «протечь» новым полем.
package importer

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxDocBytes = 8 << 20 // больше конфиг не разбирается
	maxNodes    = 20000   // и больше узлов из одного документа не берётся
)

var yamlProxies = regexp.MustCompile(`(?m)^proxies:`)

// Links возвращает ссылки узлов из полного конфига. ok=false — тело не похоже на конфиг (обычный список ссылок, base64 и т. п.).
func Links(body []byte) (links []string, ok bool) {
	if len(body) > maxDocBytes {
		return nil, false
	}
	t := bytes.TrimSpace(body)
	switch {
	case len(t) > 0 && (t[0] == '{' || t[0] == '['):
		var doc any
		dec := json.NewDecoder(bytes.NewReader(t))
		if dec.Decode(&doc) != nil {
			return nil, false
		}
		return fromJSON(doc)
	case yamlProxies.Match(t):
		var doc map[string]any
		if yaml.Unmarshal(t, &doc) != nil {
			return nil, false
		}
		return finish(fromClash(doc)), true
	}
	return nil, false
}

func fromJSON(doc any) ([]string, bool) {
	var nodes []node
	var docs []map[string]any
	switch d := doc.(type) {
	case map[string]any:
		docs = []map[string]any{d}
	case []any: // список конфигов (v2rayN) либо список outbound'ов
		for _, x := range d {
			if m, ok := x.(map[string]any); ok {
				docs = append(docs, m)
			}
		}
	}
	recognised := false
	for _, d := range docs {
		outs, _ := d["outbounds"].([]any)
		if len(outs) == 0 {
			if _, isOut := d["type"]; isOut {
				outs = []any{d} // одиночный outbound sing-box
			} else if _, isOut := d["protocol"]; isOut {
				outs = []any{d} // одиночный outbound Xray
			}
		}
		for _, o := range outs {
			m, ok := o.(map[string]any)
			if !ok {
				continue
			}
			switch {
			case m["protocol"] != nil:
				recognised = true
				nodes = append(nodes, fromXrayOutbound(m)...)
			case m["type"] != nil:
				recognised = true
				nodes = append(nodes, fromSingBoxOutbound(m)...)
			}
			if len(nodes) >= maxNodes {
				break
			}
		}
		if len(nodes) >= maxNodes {
			break
		}
	}
	if !recognised {
		return nil, false
	}
	return finish(nodes), true
}

func finish(nodes []node) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range nodes {
		if len(out) >= maxNodes {
			break
		}
		l, ok := n.link()
		if !ok || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// ---- промежуточное представление ----

type node struct {
	proto string // vless | vmess | trojan | ss | hysteria2 | hysteria | tuic | anytls | socks
	name  string
	host  string
	port  int
	user  string // uuid / логин
	pass  string // пароль / метод:пароль для ss строится отдельно
	q     url.Values
	// vmess / ss
	method  string
	alterID int
}

func newNode(proto string) node { return node{proto: proto, q: url.Values{}} }

func (n *node) set(k, v string) {
	if v != "" {
		n.q.Set(k, v)
	}
}

func cleanText(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (n node) valid() bool {
	n.host = strings.Trim(n.host, "[]") // IPv6 в конфигах бывает и в скобках, и без
	if n.host == "" || n.port < 1 || n.port > 65535 || strings.ContainsAny(n.host, " /?#@\\") {
		return false
	}
	return cleanText(n.host) && cleanText(n.user) && cleanText(n.pass) && cleanText(n.name)
}

// escUserinfo экранирует логин или пароль для части ссылки до «@». url.PathEscape оставляет «:» и «@» как есть, а здесь они
// разделяют логин, пароль и адрес.
func escUserinfo(s string) string {
	return strings.NewReplacer(":", "%3A", "@", "%40").Replace(url.PathEscape(s))
}

func (n node) link() (string, bool) {
	if !n.valid() {
		return "", false
	}
	n.host = strings.Trim(n.host, "[]")
	hp := net.JoinHostPort(n.host, strconv.Itoa(n.port))
	frag := ""
	if n.name != "" {
		frag = "#" + url.PathEscape(n.name)
	}
	query := ""
	if len(n.q) > 0 {
		query = "?" + n.q.Encode()
	}
	switch n.proto {
	case "vless":
		if n.user == "" {
			return "", false
		}
		return "vless://" + escUserinfo(n.user) + "@" + hp + query + frag, true
	case "trojan", "hysteria2", "anytls":
		if n.pass == "" {
			return "", false
		}
		return n.proto + "://" + escUserinfo(n.pass) + "@" + hp + query + frag, true
	case "tuic":
		if n.user == "" || n.pass == "" {
			return "", false
		}
		return "tuic://" + escUserinfo(n.user) + ":" + escUserinfo(n.pass) + "@" + hp + query + frag, true
	case "hysteria":
		return "hysteria://" + hp + query + frag, true
	case "socks":
		auth := ""
		if n.user != "" {
			auth = escUserinfo(n.user) + ":" + escUserinfo(n.pass) + "@"
		}
		return "socks5://" + auth + hp + frag, true
	case "ss":
		if n.method == "" || n.pass == "" {
			return "", false
		}
		cred := base64URL(n.method + ":" + n.pass)
		return "ss://" + cred + "@" + hp + query + frag, true
	case "vmess":
		if n.user == "" {
			return "", false
		}
		get := func(k string) string { return n.q.Get(k) }
		m := map[string]any{"v": "2", "ps": n.name, "add": n.host, "port": strconv.Itoa(n.port), "id": n.user, "aid": strconv.Itoa(n.alterID),
			"scy": firstNonEmpty(n.method, "auto"), "net": firstNonEmpty(get("type"), "tcp"), "type": firstNonEmpty(get("headerType"), "none"),
			"host": get("host"), "path": firstNonEmpty(get("path"), get("serviceName")), "tls": map[bool]string{true: "tls", false: ""}[get("security") == "tls"],
			"sni": get("sni"), "alpn": get("alpn"), "fp": get("fp")}
		b, _ := json.Marshal(m)
		return "vmess://" + base64Std(b), true
	}
	return "", false
}

// ---- общие помощники ----

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case int:
			return strconv.Itoa(v)
		}
	}
	return ""
}

func num(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
	}
	return 0
}

func boolean(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		switch v := m[k].(type) {
		case bool:
			return v
		case string:
			return v == "true" || v == "1"
		}
	}
	return false
}

func sub(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func list(m map[string]any, key string) []any {
	v, _ := m[key].([]any)
	return v
}

func strList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "1"
	}
	return ""
}
