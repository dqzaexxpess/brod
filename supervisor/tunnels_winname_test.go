package supervisor

import "testing"

func TestIsWinTunName(t *testing.T) {
	for name, want := range map[string]bool{
		"throne-tun": true, "tun0": true, "wintun": true, "WireGuard Tunnel": true, "wg0": true, "OpenVPN TAP-Windows6": true, "Local Area Connection tap": true,
		"Ethernet": false, "Wi-Fi": false, "vEthernet (WSL)": false, "Teredo Tunneling Pseudo-Interface": false, "isatap.lan": false,
		"Loopback Pseudo-Interface 1": false, "Bluetooth Network Connection": false,
	} {
		if got := isWinTunName(name); got != want {
			t.Errorf("isWinTunName(%q) = %v, ожидалось %v", name, got, want)
		}
	}
}
