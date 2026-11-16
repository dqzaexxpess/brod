package importer

import "strings"

func xrayStream(o map[string]any) (tlsInfo, trInfo) {
	ss := sub(o, "streamSettings")
	if ss == nil {
		return tlsInfo{}, trInfo{}
	}
	var t tlsInfo
	switch strings.ToLower(str(ss, "security")) {
	case "tls":
		ts := sub(ss, "tlsSettings")
		t = tlsInfo{on: true, sni: str(ts, "serverName"), insecure: boolean(ts, "allowInsecure"), alpn: strList(ts["alpn"]), fp: str(ts, "fingerprint")}
	case "reality":
		rs := sub(ss, "realitySettings")
		t = tlsInfo{on: true, reality: true, sni: str(rs, "serverName"), fp: str(rs, "fingerprint"), pbk: str(rs, "publicKey"), sid: str(rs, "shortId"), spx: str(rs, "spiderX")}
	}
	tr := trInfo{typ: str(ss, "network")}
	switch normTransport(tr.typ) {
	case "ws":
		ws := sub(ss, "wsSettings")
		tr.path, tr.host = str(ws, "path"), firstNonEmpty(headerHost(sub(ws, "headers")), str(ws, "host"))
	case "grpc":
		tr.service = str(sub(ss, "grpcSettings"), "serviceName")
	case "httpupgrade":
		hs := sub(ss, "httpupgradeSettings")
		tr.path, tr.host = str(hs, "path"), str(hs, "host")
	case "xhttp":
		xs := firstMap(sub(ss, "xhttpSettings"), sub(ss, "splithttpSettings"))
		tr.path, tr.host, tr.mode = str(xs, "path"), str(xs, "host"), str(xs, "mode")
	case "http":
		hs := sub(ss, "httpSettings")
		tr.path, tr.host = str(hs, "path"), firstOf(hs["host"])
	case "tcp":
		hdr := sub(sub(ss, "tcpSettings"), "header")
		if strings.EqualFold(str(hdr, "type"), "http") {
			req := sub(hdr, "request")
			tr.headerType = "http"
			tr.path, tr.host = firstOf(req["path"]), firstNonEmpty(headerHost(sub(req, "headers")))
		}
	}
	return t, tr
}

func firstMap(ms ...map[string]any) map[string]any {
	for _, m := range ms {
		if m != nil {
			return m
		}
	}
	return nil
}

func firstEl(m map[string]any, key string) map[string]any {
	if l := list(m, key); len(l) > 0 {
		x, _ := l[0].(map[string]any)
		return x
	}
	return nil
}

func fromXrayOutbound(o map[string]any) []node {
	proto := strings.ToLower(str(o, "protocol"))
	settings := sub(o, "settings")
	name := str(o, "tag")
	tlsI, tr := xrayStream(o)
	switch proto {
	case "vless", "vmess":
		var out []node
		for _, v := range list(settings, "vnext") { // у нескольких серверов — несколько узлов
			srv, _ := v.(map[string]any)
			user := firstEl(srv, "users")
			n := newNode(proto)
			n.name, n.host, n.port, n.user = name, str(srv, "address"), num(srv, "port"), str(user, "id")
			if proto == "vless" {
				n.set("flow", str(user, "flow"))
				n.set("encryption", "none")
			} else {
				n.alterID, n.method = num(user, "alterId"), str(user, "security")
			}
			n.applyTLS(tlsI)
			n.applyTransport(tr)
			if !n.valid() {
				continue // пустая запись: не копим, иначе 8 МБ из «{}» раздуваются в гигабайты
			}
			out = append(out, n)
			if len(out) >= maxNodes {
				break
			}
		}
		return out
	case "trojan", "shadowsocks":
		var out []node
		for _, v := range list(settings, "servers") {
			srv, _ := v.(map[string]any)
			n := newNode(map[string]string{"shadowsocks": "ss"}[proto])
			if proto == "trojan" {
				n = newNode("trojan")
			}
			n.name, n.host, n.port, n.pass = name, str(srv, "address"), num(srv, "port"), str(srv, "password")
			if proto == "shadowsocks" {
				n.method = str(srv, "method")
			} else {
				n.applyTLS(tlsI)
				n.applyTransport(tr)
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
	case "socks":
		var out []node
		for _, v := range list(settings, "servers") {
			srv, _ := v.(map[string]any)
			u := firstEl(srv, "users")
			n := newNode("socks")
			n.name, n.host, n.port, n.user, n.pass = name, str(srv, "address"), num(srv, "port"), str(u, "user"), str(u, "pass")
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
	return nil
}
