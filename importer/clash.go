package importer

import "strings"

func clashTLS(p map[string]any, defaultOn bool) tlsInfo {
	t := tlsInfo{on: boolean(p, "tls") || defaultOn, sni: firstNonEmpty(str(p, "servername"), str(p, "sni")), insecure: boolean(p, "skip-cert-verify"),
		alpn: strList(p["alpn"]), fp: str(p, "client-fingerprint")}
	if r := sub(p, "reality-opts"); r != nil {
		t.on, t.reality, t.pbk, t.sid = true, true, str(r, "public-key"), str(r, "short-id")
	}
	return t
}

func clashTransport(p map[string]any) trInfo {
	tr := trInfo{typ: str(p, "network")}
	switch normTransport(tr.typ) {
	case "ws":
		ws := sub(p, "ws-opts")
		tr.path, tr.host = str(ws, "path"), headerHost(sub(ws, "headers"))
	case "grpc":
		tr.service = str(sub(p, "grpc-opts"), "grpc-service-name")
	case "http":
		hs := firstMap(sub(p, "h2-opts"), sub(p, "http-opts"))
		tr.path, tr.host = firstOf(hs["path"]), firstNonEmpty(firstOf(hs["host"]), headerHost(sub(hs, "headers")))
	}
	return tr
}

func fromClash(doc map[string]any) []node {
	var out []node
	for _, x := range list(doc, "proxies") {
		p, ok := x.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(str(p, "type"))
		proto := map[string]string{"ss": "ss", "vless": "vless", "vmess": "vmess", "trojan": "trojan", "hysteria2": "hysteria2", "hysteria": "hysteria",
			"tuic": "tuic", "socks5": "socks", "anytls": "anytls"}[typ]
		if proto == "" {
			continue // ssr, wireguard, snell, ... — не выражаются ссылками, которые понимает translator
		}
		n := newNode(proto)
		n.name, n.host, n.port = str(p, "name"), str(p, "server"), num(p, "port")
		switch proto {
		case "vless":
			n.user = str(p, "uuid")
			n.set("flow", str(p, "flow"))
			n.set("encryption", "none")
			n.applyTLS(clashTLS(p, false))
			n.applyTransport(clashTransport(p))
		case "vmess":
			n.user, n.alterID, n.method = str(p, "uuid"), num(p, "alterId"), str(p, "cipher")
			n.applyTLS(clashTLS(p, false))
			n.applyTransport(clashTransport(p))
		case "trojan":
			n.pass = str(p, "password")
			n.applyTLS(clashTLS(p, true)) // trojan в Clash всегда с TLS
			n.applyTransport(clashTransport(p))
		case "ss":
			n.method, n.pass = str(p, "cipher"), str(p, "password")
			if str(p, "plugin") == "obfs" { // simple-obfs; прочие плагины ссылкой не выражаются
				po := sub(p, "plugin-opts")
				n.set("plugin", "obfs-local;obfs="+firstNonEmpty(str(po, "mode"), "http")+";obfs-host="+str(po, "host"))
			}
		case "hysteria2":
			n.pass = str(p, "password")
			n.applyTLS(clashTLS(p, true))
			n.set("obfs", str(p, "obfs"))
			n.set("obfs-password", str(p, "obfs-password"))
		case "hysteria":
			n.set("auth", firstNonEmpty(str(p, "auth-str"), str(p, "auth_str"), str(p, "auth")))
			n.set("upmbps", str(p, "up"))
			n.set("downmbps", str(p, "down"))
			n.applyTLS(clashTLS(p, true))
		case "tuic":
			n.user, n.pass = str(p, "uuid"), str(p, "password")
			n.set("congestion_control", str(p, "congestion-controller"))
			n.set("udp_relay_mode", str(p, "udp-relay-mode"))
			n.applyTLS(clashTLS(p, true))
		case "anytls":
			n.pass = str(p, "password")
			n.applyTLS(clashTLS(p, true))
		case "socks":
			n.user, n.pass = str(p, "username"), str(p, "password")
		}
		if !n.valid() {
			continue
		}
		out = append(out, n)
		if len(out) >= maxNodes {
			break
		}
	}
	return out
}
