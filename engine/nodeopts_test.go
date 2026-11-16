package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// параметры узла вне белого списка не доходят до движка: файл не запускается, не читается, bridge не создаётся.
func TestNodeOptsOutsideWhitelistNeverReachEngine(t *testing.T) {
	e, err := New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	dir := t.TempDir()
	marker, script := filepath.Join(dir, "ran"), filepath.Join(dir, "x.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nid -u >> "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("СЕКРЕТНОЕ-СОДЕРЖИМОЕ"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"tor":    `{"type":"tor","tag":"x","executable_path":"` + script + `","data_directory":"` + dir + `"}`,
		"bridge": `{"type":"bridge","tag":"x"}`,
		"cert":   `{"type":"vless","tag":"x","server":"127.0.0.1","server_port":1,"uuid":"5b0e5c2e-0000-4000-8000-000000000001","tls":{"enabled":true,"certificate_path":"` + secret + `"}}`,
		"ss":     `{"type":"shadowsocks","tag":"x","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"p","plugin":"v2ray-plugin","plugin_opts":"tls;host=x;cert=` + secret + `"}`,
	}
	for name, raw := range cases {
		p, err := e.NewProbe([]byte(raw))
		if name != "ss" { // у ss ключ cert= выбрасывается, остальное допустимо
			if err == nil {
				p.Close()
				t.Errorf("%s: NewProbe принял параметры вне белого списка", name)
			}
		} else if p != nil {
			p.Close()
		}
		if err != nil && strings.Contains(err.Error(), "СЕКРЕТНОЕ") {
			t.Errorf("%s: содержимое файла в ошибке NewProbe", name)
		}
		if tag, err := e.Add([]byte(raw)); name != "ss" && err == nil {
			_ = e.Remove(tag)
			t.Errorf("%s: Add принял параметры вне белого списка", name)
		} else if err != nil && strings.Contains(err.Error(), "СЕКРЕТНОЕ") {
			t.Errorf("%s: содержимое файла в ошибке Add", name)
		} else if err == nil {
			_ = e.Remove(tag)
		}
	}
	time.Sleep(500 * time.Millisecond)
	if b, _ := os.ReadFile(marker); len(b) > 0 {
		t.Fatalf("файл из executable_path запущен: %q", b)
	}
}
