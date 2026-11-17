// Тесты чистой логики androidcore: bridge.go собирается без тега android, поэтому тесты идут обычным go test на Linux.
// Мост Kotlin↔Go, Start/Stop и сам VpnService тестами не охвачены: только телефон.
package androidcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogLevel(t *testing.T) {
	dir := t.TempDir()
	if got := logLevel(dir); got != "warn" {
		t.Fatalf("без файла: %q", got)
	}
	for in, want := range map[string]string{"debug\n": "debug", " trace ": "trace", "error": "error", "verbose": "warn", "": "warn", "DEBUG": "warn"} {
		if err := os.WriteFile(filepath.Join(dir, "loglevel"), []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := logLevel(dir); got != want {
			t.Errorf("loglevel=%q: %q, ожидалось %q", in, got, want)
		}
	}
}

func TestNetworkIDAndDNSDefaults(t *testing.T) {
	t.Cleanup(func() { SetNetworkID(""); SetDNS("") })
	SetNetworkID("")
	SetDNS("")
	if got := currentNetID(); got != "android" {
		t.Fatalf("netID по умолчанию: %q", got)
	}
	if got := currentDNS(); got != "77.88.8.8" {
		t.Fatalf("DNS по умолчанию: %q", got)
	}
	SetNetworkID("wifi:home")
	SetDNS("192.168.1.1")
	if got := currentNetID(); got != "wifi:home" {
		t.Fatalf("netID: %q", got)
	}
	if got := currentDNS(); got != "192.168.1.1" {
		t.Fatalf("DNS: %q", got)
	}
}

func TestSetMetered(t *testing.T) {
	t.Cleanup(func() { SetMetered(false) })
	SetMetered(true)
	if !metered.Load() {
		t.Fatal("SetMetered(true) не сохранён")
	}
	SetMetered(false)
	if metered.Load() {
		t.Fatal("SetMetered(false) не сохранён")
	}
}

// «Поделиться журналом»: только известные имена (произвольный путь не принимается), содержимое очищено, предел размера держится.
func TestSanitizedLog(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"", "../core.log", "/etc/passwd", "core.log/../x", "other.log", "brod.db"} {
		if _, err := SanitizedLog(dir, bad, 1024); err == nil {
			t.Errorf("имя %q принято", bad)
		}
	}
	if _, err := SanitizedLog(dir, "core.log", 1024); err == nil {
		t.Error("нет файла журнала, а ошибки нет")
	}
	secret := "203.0.113.77"
	body := strings.Repeat("начало журнала\n", 200) + "connect to " + secret + ":443 failed\n"
	if err := os.WriteFile(filepath.Join(dir, "engine.log"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := SanitizedLog(dir, "engine.log", 512)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("адрес сервера остался в журнале для «Поделиться»: %q", out)
	}
	if len(out) > 512 {
		t.Fatalf("предел 512 байт нарушен: %d", len(out))
	}
}
