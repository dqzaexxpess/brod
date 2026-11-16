package core

import "testing"

// Ссылка, которую печатает deploy/vps/install-xray-reality.sh, должна приниматься как свой сервер.
func TestOwnAcceptsInstallScriptLink(t *testing.T) {
	c := startCore(t, t.TempDir())
	link := "vless://5b0e5c2e-0000-4000-8000-000000000001@203.0.113.50:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.microsoft.com&fp=chrome&pbk=0123456789abcdefghijklmnopqrstuvwxyzABCDEFG&sid=0123456789abcdef&type=tcp#brod-vps"
	rep, err := c.AddOwn(link, "user")
	if err != nil || rep.Added != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}
