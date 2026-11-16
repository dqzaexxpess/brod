package core

import (
	"fmt"
	"testing"
)

// узлы с loopback, частными, link-local и неуказанными адресами не принимаются из подписок.
func TestParseLinksDropsLocalAddresses(t *testing.T) {
	uuid := "5b0e5c2e-0000-4000-8000-000000000001"
	bad := []string{
		"127.0.0.1", "127.8.9.10", "10.1.2.3", "172.16.0.5", "192.168.1.1", "169.254.1.1", "0.0.0.0",
		"[::1]", "[::]", "[fe80::1]", "[fd00::1]", "[::ffff:192.168.0.1]", "localhost", "LOCALHOST", "localhost.", "foo.localhost",
	}
	var links []string
	for _, h := range bad {
		links = append(links, fmt.Sprintf("vless://%s@%s:443?security=tls&sni=x.example&type=tcp#bad", uuid, h))
	}
	links = append(links, "socks5://127.0.0.1:1080#local-proxy", "trojan://pw@192.168.0.7:443?sni=x.example#lan")
	if got := parseLinks(links); len(got) != 0 {
		for _, n := range got {
			t.Errorf("принят узел с локальным адресом: %s", n.Server)
		}
	}
	// публичные адреса и имена проходят
	ok := []string{vlessLink(1), fmt.Sprintf("vless://%s@8.8.8.8:443?security=tls&sni=x.example&type=tcp#ok", uuid),
		fmt.Sprintf("vless://%s@[2001:4860:4860::8888]:443?security=tls&sni=x.example&type=tcp#ok6", uuid)}
	if got := parseLinks(ok); len(got) != len(ok) {
		t.Fatalf("публичные узлы отброшены: %d из %d", len(got), len(ok))
	}
}
