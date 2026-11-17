package importer

import (
	"encoding/base64"
	"strings"
	"testing"
)

const throneOutbound = `{"type":"vless","tag":"test","server":"203.0.113.7","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","flow":"xtls-rprx-vision","packet_encoding":"xudp","tls":{"enabled":true,"server_name":"example.org","utls":{"enabled":true,"fingerprint":"chrome"}}}`

func TestThroneDeepLink(t *testing.T) {
	link := "throne://add/" + base64.RawURLEncoding.EncodeToString([]byte(throneOutbound))
	links, ok := ThroneLinks(link)
	if !ok || len(links) != 1 || !strings.HasPrefix(links[0], "vless://11111111-2222-3333-4444-555555555555@203.0.113.7:443") {
		t.Fatalf("диплинк не раскрыт: %v %v", ok, links)
	}
	if !strings.Contains(links[0], "flow=xtls-rprx-vision") {
		t.Fatalf("flow потерян: %s", links[0])
	}
}

func TestThroneDeepLinkVariantsAndGarbage(t *testing.T) {
	std := "nekoray://add/" + base64.StdEncoding.EncodeToString([]byte(throneOutbound)) + "#имя"
	if _, ok := ThroneLinks(std); !ok {
		t.Fatal("base64 со стандартным алфавитом и фрагмент не разобраны")
	}
	for _, bad := range []string{"throne://add/", "throne://add/!!!", "throne://add/" + base64.RawURLEncoding.EncodeToString([]byte("не json")), "vless://x@y:1"} {
		if _, ok := ThroneLinks(bad); ok {
			t.Errorf("%q принят", bad)
		}
	}
}

func TestExpandDeepLinksKeepsOthers(t *testing.T) {
	in := []string{"vless://a@b:1", "throne://add/" + base64.RawURLEncoding.EncodeToString([]byte(throneOutbound)), "trojan://c@d:2"}
	out := ExpandDeepLinks(in)
	if len(out) != 3 || out[0] != in[0] || out[2] != in[2] || !strings.HasPrefix(out[1], "vless://") {
		t.Fatalf("%v", out)
	}
	same := []string{"vless://a@b:1"}
	if got := ExpandDeepLinks(same); &got[0] != &same[0] {
		t.Fatal("без диплинков список возвращается как есть")
	}
}
