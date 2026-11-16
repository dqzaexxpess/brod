package cli

import (
	"bytes"
	"strings"
	"testing"
)

// предупреждение переходного режима из итога установки видно в `brodctl update status`.
func TestPrintUpdateShowsInstallWarning(t *testing.T) {
	var out bytes.Buffer
	a := &app{env: Env{Out: &out}}
	a.printUpdate(map[string]any{"last": map[string]any{"ok": true, "from": "0.3.0", "to": "0.4.0", "at": "2026-11-15T10:00:00Z",
		"warning": "переходный режим: помощник brod-helper не установлен"}})
	if !strings.Contains(out.String(), "ВНИМАНИЕ: переходный режим") {
		t.Fatalf("нет предупреждения:\n%s", out.String())
	}
	out.Reset()
	a.printUpdate(map[string]any{"last": map[string]any{"ok": true, "from": "0.3.0", "to": "0.4.0", "at": "2026-11-15T10:00:00Z"}})
	if strings.Contains(out.String(), "ВНИМАНИЕ") {
		t.Fatalf("лишнее предупреждение:\n%s", out.String())
	}
}
