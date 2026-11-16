package importer

import "strings"

func singTLS(o map[string]any) tlsInfo {
	t := sub(o, "tls")
	if t == nil || !boolean(t, "enabled") {
		return tlsInfo{}
	}
	info := tlsInfo{on: true, sni: str(t, "server_name"), insecure: boolean(t, "insecure"), alpn: strList(t["alpn"]), fp: str(sub(t, "utls"), "fingerprint")}
	if r := sub(t, "reality"); r != nil && boolean(r, "enabled") {
		info.reality, info.pbk, info.sid = true, str(r, "public_key"), str(r, "short_id")
	}
	return info
}

func singTransport(o map[string]any) trInfo {
	t := sub(o, "transport")
	if t == nil {
		return trInfo{}
	}
	tr := trInfo{typ: str(t, "type"), path: str(t, "path"), service: str(t, "service_name"), mode: str(t, "mode")}
	tr.host = firstNonEmpty(headerHost(sub(t, "headers")), firstOf(t["host"]))
	return tr
}

func fromSingBoxOutbound(o map[string]any) []node {
	typ := strings.ToLower(str(o, "type"))
	var n node
	switch typ {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "hysteria", "tuic", "anytls", "socks":
	default:
		return nil // selector, urltest, direct, block, dns, wireguard (endpoint) и прочее
	}
	proto := map[string]string{"shadowsocks": "ss"}[typ]
	if proto == "" {
		proto = typ
	}
	n = newNode(proto)
	n.name, n.host, n.port = str(o, "tag"), str(o, "server"), num(o, "server_port")
	switch typ {
	case "vless":
		n.user = str(o, "uuid")
		n.set("flow", str(o, "flow"))
		n.set("encryption", "none")
		n.applyTLS(singTLS(o))
		n.applyTransport(singTransport(o))
	case "vmess":
		n.user, n.alterID, n.method = str(o, "uuid"), num(o, "alter_id"), str(o, "security")
		n.applyTLS(singTLS(o))
		n.applyTransport(singTransport(o))
	case "trojan":
		n.pass = str(o, "password")
		n.applyTLS(singTLS(o))
		n.applyTransport(singTransport(o))
	case "shadowsocks":
		n.method, n.pass = str(o, "method"), str(o, "password")
		if p := str(o, "plugin"); p != "" {
			n.set("plugin", strings.TrimSuffix(p+";"+str(o, "plugin_opts"), ";"))
		}
	case "hysteria2":
		n.pass = str(o, "password")
		n.applyTLS(singTLS(o))
		if ob := sub(o, "obfs"); ob != nil {
			n.set("obfs", str(ob, "type"))
			n.set("obfs-password", str(ob, "password"))
		}
	case "hysteria":
		n.set("auth", firstNonEmpty(str(o, "auth_str"), str(o, "auth")))
		n.set("upmbps", str(o, "up_mbps"))
		n.set("downmbps", str(o, "down_mbps"))
		n.set("obfsParam", str(o, "obfs"))
		n.applyTLS(singTLS(o))
	case "tuic":
		n.user, n.pass = str(o, "uuid"), str(o, "password")
		n.set("congestion_control", str(o, "congestion_control"))
		n.set("udp_relay_mode", str(o, "udp_relay_mode"))
		n.applyTLS(singTLS(o))
	case "anytls":
		n.pass = str(o, "password")
		n.applyTLS(singTLS(o))
	case "socks":
		n.user, n.pass = str(o, "username"), str(o, "password")
	}
	if !n.valid() {
		return nil
	}
	return []node{n}
}
