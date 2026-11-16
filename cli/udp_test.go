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
