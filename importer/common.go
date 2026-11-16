package importer

import (
	"encoding/base64"
	"strings"
)

func base64URL(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// tlsInfo — то, что ссылка умеет сказать о TLS/Reality.
type tlsInfo struct {
	on, reality   bool
	sni, fp       string
	insecure      bool
	alpn          []string
	pbk, sid, spx string
}

func (n *node) applyTLS(t tlsInfo) {
	switch {
	case t.reality:
		n.set("security", "reality")
	case t.on:
		n.set("security", "tls")
	default:
		n.set("security", "none")
	}
	if !t.on && !t.reality {
		return
	}
	n.set("sni", t.sni)
	n.set("allowInsecure", yesNo(t.insecure))
	n.set("alpn", strings.Join(t.alpn, ","))
	n.set("fp", t.fp)
	n.set("pbk", t.pbk)
	n.set("sid", t.sid)
	n.set("spx", t.spx)
}

// trInfo — транспорт.
type trInfo struct {
	typ, path, host, service, mode, headerType string
}

func normTransport(t string) string {
	switch strings.ToLower(t) {
	case "", "tcp", "raw":
		return "tcp"
	case "websocket", "ws":
		return "ws"
	case "h2", "http", "h3":
		return "http"
	case "splithttp", "xhttp":
		return "xhttp"
	}
	return strings.ToLower(t)
}

func (n *node) applyTransport(t trInfo) {
	typ := normTransport(t.typ)
	if typ != "tcp" || t.headerType == "http" {
		n.set("type", typ)
	}
	n.set("path", t.path)
	n.set("host", t.host)
	n.set("serviceName", t.service)
	n.set("mode", t.mode)
	n.set("headerType", t.headerType)
}

func firstOf(v any) string {
	if l := strList(v); len(l) > 0 {
		return l[0]
	}
	return ""
}

// headerHost достаёт Host из headers (строка либо список; ключ в любом регистре).
func headerHost(h map[string]any) string {
	for k, v := range h {
		if strings.EqualFold(k, "host") {
			return firstOf(v)
		}
	}
	return ""
}
