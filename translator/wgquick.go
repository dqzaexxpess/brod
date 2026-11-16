package translator

import (
	"net"
	"net/url"
	"sort"
	"strings"
)

// WGQuickLinks превращает конфиги формата wg-quick (WireGuard и AmneziaWG: секции [Interface] и [Peer]) в ссылки wireguard://, которые
// понимает parseWireGuard. Берутся только поля, нужные для подключения: ключи, адреса, MTU, параметры AmneziaWG (Jc, Jmin, Jmax, S1–S4,
// H1–H4, I1–I5) и первый [Peer] (PublicKey, PresharedKey, Endpoint). Всё остальное (PostUp, DNS, AllowedIPs, PersistentKeepalive) игнорируется:
// конфиг из чужих рук ничего не исполняет. В одном тексте может быть несколько конфигов (новый [Interface] начинает следующий).
func WGQuickLinks(text string) []string {
	type conf struct{ iface, peer map[string]string }
	var confs []*conf
	var cur *conf
	section := ""
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if section == "interface" {
				cur = &conf{iface: map[string]string{}, peer: map[string]string{}}
				confs = append(confs, cur)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || cur == nil {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch section {
		case "interface":
			cur.iface[k] = v
		case "peer":
			if _, have := cur.peer[k]; !have { // только первый [Peer]
				cur.peer[k] = v
			}
		}
	}
	var out []string
	for _, c := range confs {
		priv, pub, ep := c.iface["privatekey"], c.peer["publickey"], c.peer["endpoint"]
		if priv == "" || pub == "" || ep == "" || c.iface["address"] == "" {
			continue
		}
		host, port, err := net.SplitHostPort(ep)
		if err != nil || host == "" || port == "" {
			continue
		}
		q := map[string]string{"publickey": pub, "address": strings.ReplaceAll(c.iface["address"], " ", "")}
		if v := c.iface["mtu"]; v != "" {
			q["mtu"] = v
		}
		if v := c.peer["presharedkey"]; v != "" {
			q["presharedkey"] = v
		}
		for _, k := range awgKeys {
			if v := c.iface[k]; v != "" {
				q[k] = v
			}
		}
		hostPart := host
		if strings.Contains(host, ":") {
			hostPart = "[" + host + "]"
		}
		// у значений I1–I5 есть пробелы и угловые скобки: экранируем каждое значение сами
		var qs []string
		for k, v := range q {
			qs = append(qs, k+"="+url.QueryEscape(v))
		}
		sort.Strings(qs)
		out = append(out, "wireguard://"+url.QueryEscape(priv)+"@"+hostPart+":"+port+"?"+strings.Join(qs, "&"))
	}
	return out
}
