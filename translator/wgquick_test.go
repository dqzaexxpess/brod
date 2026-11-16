package translator

import (
	"strings"
	"testing"
)

// Ключи в тестах заведомо фиктивные (байты 0..31 и 32..63 в base64); публичный ключ пира — общеизвестный ключ Cloudflare WARP.
const (
	fakePriv1 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	fakePriv2 = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8="
	warpPub   = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
)

const wgConf = `# профиль
[Interface]
Address = 172.16.0.2, 2606:4700:110:86fb:bdae:3e42:1356:8e8d
MTU = 1380
PrivateKey = ` + fakePriv1 + `
PostUp = rm -rf /
DNS = 1.1.1.1
Jc = 4
Jmin = 40
Jmax = 70
H1 = 1
H2 = 2
H3 = 3
H4 = 4
I1 = <b 0xce000000010897a297ec>

[Peer]
Endpoint = 8.34.146.9:878
PublicKey = ` + warpPub + `
PersistentKeepalive = 15
AllowedIPs = 10.0.0.0/8
`

func TestWGQuickLinksRoundTrip(t *testing.T) {
	links := WGQuickLinks(wgConf)
	if len(links) != 1 {
		t.Fatalf("ссылок %d", len(links))
	}
	if strings.Contains(links[0], "PostUp") || strings.Contains(links[0], "rm%20") || strings.Contains(links[0], "1.1.1.1") {
		t.Fatalf("лишние поля попали в ссылку: %s", links[0])
	}
	o, err := Parse(links[0])
	if err != nil {
		t.Fatal(err)
	}
	if !o.Endpoint || o.Proto != "wireguard" {
		t.Fatalf("ожидался endpoint wireguard: %+v", o)
	}
	opts := o.Opts
	peer := opts["peers"].([]any)[0].(map[string]any)
	if peer["address"] != "8.34.146.9" || peer["port"] != 878 || peer["public_key"] != warpPub {
		t.Fatalf("peer: %v", peer)
	}
	if opts["private_key"] != fakePriv1 || opts["mtu"] != 1380 {
		t.Fatalf("interface: %v", opts)
	}
	if opts["jc"] != uint64(4) || opts["jmin"] != uint64(40) || opts["jmax"] != uint64(70) || opts["h4"] != uint64(4) {
		t.Fatalf("параметры AmneziaWG: %v", opts)
	}
	if opts["i1"] != "<b 0xce000000010897a297ec>" {
		t.Fatalf("i1 потерян или искажён: %v", opts["i1"])
	}
}

func TestWGQuickLinksMultipleAndInvalid(t *testing.T) {
	two := wgConf + "\n[Interface]\nAddress = 172.16.0.2\nPrivateKey = " + fakePriv2 + "\n[Peer]\nPublicKey = " + warpPub + "\nEndpoint = [2606:4700:d0::a29f:c001]:2408\n"
	links := WGQuickLinks(two)
	if len(links) != 2 {
		t.Fatalf("ожидалось 2 конфига, получено %d", len(links))
	}
	o, err := Parse(links[1])
	if err != nil || o.Opts["peers"].([]any)[0].(map[string]any)["address"] != "2606:4700:d0::a29f:c001" {
		t.Fatalf("IPv6 endpoint: %v %v", o, err)
	}
	for _, bad := range []string{"", "[Interface]\nPrivateKey = x\n", "[Peer]\nEndpoint = a:1\n", "обычный текст"} {
		if got := WGQuickLinks(bad); len(got) != 0 {
			t.Errorf("%q: неожиданные ссылки %v", bad, got)
		}
	}
}
