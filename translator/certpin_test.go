package translator

import (
	"errors"
	"strings"
	"testing"
)

// закрепление сертификата из ссылки движок не проверяет (pinSHA256 у hysteria2 — хэш сертификата, а в движке —
// хэш открытого ключа), поэтому «insecure + закрепление» дало бы узел без какой-либо проверки сертификата. Такая ссылка отклоняется.
func TestInsecureWithCertPinRejected(t *testing.T) {
	const uuid = "5b0e5c2e-0000-4000-8000-000000000001"
	pin := strings.Repeat("ab", 32)
	bad := []string{
		"hysteria2://pw@h.example:443?insecure=1&pinSHA256=" + pin + "#x",
		"hy2://pw@h.example:443?insecure=1&pinsha256=" + pin + "#x",
		"vless://" + uuid + "@h.example:443?security=tls&type=tcp&allowInsecure=1&pcs=" + pin + "#x",
		"trojan://pw@h.example:443?security=tls&allowInsecure=1&pinnedPeerCertSha256=" + pin + "#x",
		"tuic://" + uuid + ":pw@h.example:443?allow_insecure=1&pcs=" + pin + "#x",
		"anytls://pw@h.example:443?insecure=1&pcs=" + pin + "#x",
		"hysteria2://pw@h.example:443?insecure=1&amp;pinSHA256=" + pin + "#x", // HTML-экранирование, как в источниках
	}
	for _, l := range bad {
		_, err := Parse(l)
		var pe *Error
		if err == nil || !errors.As(err, &pe) || pe.Reason != ReasonCertPin {
			t.Errorf("ссылка должна отклоняться с причиной %q: %s -> %v", ReasonCertPin, l, err)
		}
	}
	// закрепление без insecure не ослабляет проверку по цепочке доверия; insecure без закрепления остаётся прежним (с пометкой)
	ok := []string{
		"hysteria2://pw@h.example:443?pinSHA256=" + pin + "#x",
		"hysteria2://pw@h.example:443?insecure=1#x",
		"vless://" + uuid + "@h.example:443?security=tls&type=tcp&pcs=" + pin + "#x",
	}
	for _, l := range ok {
		if _, err := Parse(l); err != nil {
			t.Errorf("ссылка должна приниматься: %s -> %v", l, err)
		}
	}
}
