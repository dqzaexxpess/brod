package warp

import "testing"

// IPv4Only: ни IPv6-адреса интерфейса, ни ::/0 в allowed_ips (Windows: отправка на IPv6 через udp4 падала в wsasendmsg).
func TestEndpointIPv4Only(t *testing.T) {
	a := &Account{PrivateKey: "k", PeerPublicKey: "p", AddrV4: "172.16.0.2", AddrV6: "fd01::2"}
	full := a.Endpoint(Options{Mask: "none"})
	if got := full["address"].([]string); len(got) != 2 {
		t.Fatalf("обычный режим: %v", got)
	}
	v4 := a.Endpoint(Options{IPv4Only: true})
	if got := v4["address"].([]string); len(got) != 1 || got[0] != "172.16.0.2/32" {
		t.Fatalf("IPv4Only: адреса %v", got)
	}
	peer := v4["peers"].([]any)[0].(map[string]any)
	if got := peer["allowed_ips"].([]string); len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Fatalf("IPv4Only: allowed_ips %v", got)
	}
}
