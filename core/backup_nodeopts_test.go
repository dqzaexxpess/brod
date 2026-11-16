package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// «свой сервер» из резервной копии вне белого списка не принимается: иначе при проверке запускался бы произвольный файл.
func TestImportBackupRejectsNodeOutsideWhitelist(t *testing.T) {
	c := newCore(t, t.TempDir())
	dir := t.TempDir()
	marker, script := filepath.Join(dir, "ran"), filepath.Join(dir, "x.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho run >> "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := map[string]map[string]any{
		"tor":    {"type": "tor", "executable_path": script, "data_directory": dir},
		"bridge": {"type": "bridge"},
		"cert":   {"type": "vless", "server": "example.org", "server_port": 443, "uuid": "u", "tls": map[string]any{"enabled": true, "certificate_path": "/etc/passwd"}},
		"detour": {"type": "vless", "server": "example.org", "server_port": 443, "uuid": "u", "detour": "direct"},
	}
	var own []BackupNode
	i := 0
	for name, o := range bad {
		i++
		opts, _ := json.Marshal(o)
		own = append(own, BackupNode{ID: "auditx0000000000000000" + string(rune('a'+i)), Class: "vless/tcp/tls", Proto: "vless", Server: name + ".example", Port: 443, Opts: opts})
	}
	good, _ := json.Marshal(map[string]any{"type": "vless", "server": "good.example", "server_port": 443, "uuid": "5b0e5c2e-0000-4000-8000-000000000001", "tls": map[string]any{"enabled": true, "server_name": "good.example"}})
	own = append(own, BackupNode{ID: "auditgood00000000000001", Class: "vless/tcp/tls", Proto: "vless", Server: "good.example", Port: 443, Opts: good})
	rep, err := c.ImportBackup(Backup{Version: 1, Own: own}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Own != 1 {
		t.Fatalf("принято серверов %d, ожидался только обычный (пропущено: %v)", rep.Own, rep.Skipped)
	}
	if len(rep.Skipped) != len(bad) {
		t.Errorf("пропущено %d, ожидалось %d: %v", len(rep.Skipped), len(bad), rep.Skipped)
	}
	if b, _ := os.ReadFile(marker); len(b) > 0 {
		t.Fatal("файл запущен")
	}
}
