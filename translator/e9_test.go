package translator

import "testing"

// trojan с security=reality проходит ту же проверку ключа, что и vless.
func TestTrojanRealityKeyChecked(t *testing.T) {
	base := "trojan://pw@192.0.2.1:443?security=reality&sni=example.com&fp=chrome&type=tcp"
	if r := reasonOf(t, base); r != ReasonMissingField {
		t.Fatalf("без pbk: %v", r)
	}
	if r := reasonOf(t, base+"&pbk=коротко"); r != ReasonMissingField {
		t.Fatalf("негодный pbk: %v", r)
	}
	if r := reasonOf(t, base+"&pbk="+pbk+"&sid=abc"); r != ReasonMissingField {
		t.Fatalf("негодный sid: %v", r)
	}
	mustParse(t, base+"&pbk="+pbk+"&sid=ab12")
}

// shadowsocks с шифром none — без защиты, как security=none у остальных протоколов (по умолчанию скрыт).
func TestShadowsocksNoneIsNoSecurity(t *testing.T) {
	o := mustParse(t, "ss://none:pw@192.0.2.1:8388#n")
	if !o.Flags.NoSecurity {
		t.Fatalf("ss с шифром none не помечен как без защиты: %+v", o.Flags)
	}
	if o := mustParse(t, "ss://aes-256-gcm:pw@192.0.2.1:8388#n"); o.Flags.NoSecurity {
		t.Fatal("обычный шифр помечен как без защиты")
	}
}

// ключи WireGuard должны быть 32 байтами base64; остальное движок всё равно не примет.
func TestWireGuardKeyLength(t *testing.T) {
	good := "cHJpdmF0ZUtleUJhc2U2NFZhbHVlMDEyMzQ1Njc4OTA%3D" // 32 байта
	pub := "cHVibGljS2V5QmFzZTY0VmFsdWVBQUFBQUFBQUFBQUE%3D"  // 32 байта
	l := "@192.0.2.12:2408?address=172.16.0.2/32&publickey="
	mustParse(t, "wireguard://"+good+l+pub)
	if r := reasonOf(t, "wireguard://"+good+l+"cHVibGljS2V5"); r != ReasonMissingField {
		t.Fatalf("короткий публичный ключ принят: %v", r)
	}
	if r := reasonOf(t, "wireguard://c2hvcnQ%3D"+l+pub); r != ReasonMissingField {
		t.Fatalf("короткий приватный ключ принят: %v", r)
	}
	if r := reasonOf(t, "wireguard://"+good+l+pub+"&psk=AAAA"); r != ReasonMissingField {
		t.Fatalf("короткий psk принят: %v", r)
	}
}
