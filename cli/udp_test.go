package cli

import (
	"strings"
	"testing"
)

func TestUDPLine(t *testing.T) {
	good := udpLine(map[string]any{"node": "0123456789abcdef", "proto": "vless", "grade": "good", "received": 10, "sent": 10, "loss_pct": 0.0,
		"median_ms": 80, "min_ms": 60, "max_ms": 120, "server": "stun.example:3478"})
	for _, want := range []string{"0123456789", "ХОРОШО", "потери 0%", "задержка 80 мс", "stun.example:3478"} {
		if !strings.Contains(good, want) {
			t.Errorf("в строке нет %q: %s", want, good)
		}
	}
	none := udpLine(map[string]any{"proto": "vless", "grade": "none", "detail": "ни одного ответа", "server": "stun.example:3478"})
	if !strings.Contains(none, "UDP НЕ ПРОХОДИТ") || !strings.Contains(none, "ни одного ответа") {
		t.Errorf("строка для отсутствия UDP: %s", none)
	}
}

func TestShownClassUDPProtocols(t *testing.T) {
	cases := map[string]string{
		"wireguard/tcp/none": "wireguard/udp/none",
		"hysteria2/tcp/tls":  "hysteria2/udp/tls",
		"tuic/tcp/tls":       "tuic/udp/tls",
		"vless/tcp/reality":  "vless/tcp/reality",
		"vless/ws/tls":       "vless/ws/tls",
		"wireguard/udp/none": "wireguard/udp/none",
	}
	for in, want := range cases {
		if got := shownClass(in); got != want {
			t.Errorf("%s: %s, ожидалось %s", in, got, want)
		}
	}
}

func TestCallsLabel(t *testing.T) {
	for in, want := range map[string]string{"good": "да", "unstable": "рвётся", "none": "нет UDP", "": "—"} {
		if got := callsLabel(in); got != want {
			t.Errorf("%q: %q, ожидалось %q", in, got, want)
		}
	}
}

func TestCleanInstallError(t *testing.T) {
	if got := cleanInstallError("pkexec: Not authorized\n\nThis incident has been reported.\n"); got != "pkexec: Not authorized" {
		t.Fatalf("%q", got)
	}
	if got := cleanInstallError("нет места"); got != "нет места" {
		t.Fatalf("%q", got)
	}
}
