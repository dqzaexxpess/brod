package importer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Диплинки Throne (форк Nekoray): throne://add/<base64url JSON outbound sing-box>. Адрес берётся из самого JSON, поэтому ссылка
// превращается в обычные ссылки узлов тем же разбором, что и одиночный outbound в конфиге.
var throneSchemes = []string{"throne://add/", "nekoray://add/", "nekobox://add/"}

// ThroneLinks раскрывает диплинк Throne в ссылки узлов; ok=false — строка не диплинк или не разбирается.
func ThroneLinks(link string) ([]string, bool) {
	link = strings.TrimSpace(link)
	var payload string
	for _, p := range throneSchemes {
		if len(link) > len(p) && strings.EqualFold(link[:len(p)], p) {
			payload = link[len(p):]
			break
		}
	}
	if payload == "" {
		return nil, false
	}
	if i := strings.IndexByte(payload, '#'); i >= 0 {
		payload = payload[:i]
	}
	var raw []byte
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(strings.TrimRight(payload, "=")); err == nil {
			raw = b
			break
		}
		if b, err := enc.DecodeString(payload); err == nil {
			raw = b
			break
		}
	}
	if len(raw) == 0 || len(raw) > maxDocBytes {
		return nil, false
	}
	var doc any
	if json.NewDecoder(bytes.NewReader(raw)).Decode(&doc) != nil {
		return nil, false
	}
	links, ok := fromJSON(doc)
	return links, ok && len(links) > 0
}

// ExpandDeepLinks заменяет диплинки Throne в списке ссылок на ссылки узлов; остальные ссылки остаются как есть.
func ExpandDeepLinks(links []string) []string {
	var out []string
	changed := false
	for _, l := range links {
		if ex, ok := ThroneLinks(l); ok {
			out = append(out, ex...)
			changed = true
			continue
		}
		out = append(out, l)
	}
	if !changed {
		return links
	}
	return out
}
