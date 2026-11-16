package translator

import "testing"

// Ссылка в том виде, как её экспортирует Throne: приватный ключ в параметре, «+» в публичном ключе, адреса через «-».
func TestWireGuardThroneExport(t *testing.T) {
	priv := "cHJpdmF0ZUtleUJhc2U2NFZhbHVlMDEyMzQ1Njc4OTA="
	pub := "cHVibGljS2V5Qm+zZTY0VmFsdWVBQUFBQUFBQUFBQUE="
	if !wgKeyOK(pub) {
		t.Fatal("тестовый ключ некорректен")
	}
	link := "wg://188.114.96.166:1074?private_key=" + priv + "&local_address=172.16.0.2/32-2606:4700:110::1/128&mtu=1280" +
		"&enable_amnezia=true&jc=4&jmin=40&jmax=70&h1=1&h2=2&h3=3&h4=4&i1=<b 0x0102>&public_key=" + pub + "&persistent_keepalive_interval=25#Portal"
	o := mustParse(t, link)
	if !o.Endpoint || o.Opts["private_key"] != priv {
		t.Fatalf("приватный ключ из параметра не прочитан: %v", o.Opts)
	}
	peer := o.Opts["peers"].([]any)[0].(map[string]any)
	if peer["public_key"] != pub {
		t.Fatalf("плюс в публичном ключе потерян: %v", peer["public_key"])
	}
	if peer["persistent_keepalive_interval"] != 25 {
		t.Fatalf("keepalive не прочитан: %v", peer)
	}
	addrs := o.Opts["address"].([]string)
	if len(addrs) != 2 || addrs[0] != "172.16.0.2/32" || addrs[1] != "2606:4700:110::1/128" {
		t.Fatalf("адреса через «-» не разделены: %v", addrs)
	}
	if o.Opts["jc"] != uint64(4) || o.Opts["i1"] != "<b 0x0102>" {
		t.Fatalf("параметры AmneziaWG потеряны: %v", o.Opts)
	}
}
