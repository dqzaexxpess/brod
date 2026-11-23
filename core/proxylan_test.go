package core

import (
	"encoding/json"
	"strconv"
	"testing"
)

// Раздача в локальную сеть: по умолчанию app-вход слушает только loopback; при proxy.lan=1 — на всех адресах, а обращения ядра остаются на loopback
// (на Windows к 0.0.0.0 не подключиться).
func TestProxyLANListenAddress(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	if got := c.ProxyShare(); got.Enabled {
		t.Fatalf("раздача включена по умолчанию: %+v", got)
	}
	if err := c.SetSetting(SetProxyLAN, "1", "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting(SetProxyLAN, "может", "test"); err == nil {
		t.Error("недопустимое значение принято")
	}
	c.Close()

	c2 := newCore(t, dir)
	t.Cleanup(func() { c2.Close() })
	share := c2.ProxyShare()
	if !share.Enabled || share.Port != c2.cfg.AppPort || share.User == "" || share.Pass == "" {
		t.Fatalf("после включения: %+v", share)
	}
	if addr, _, _ := c2.AppProxy(); addr != "127.0.0.1:"+itoa(c2.cfg.AppPort) {
		t.Errorf("ядро должно ходить на loopback, а не на %s", addr)
	}
	raw, err := engineConfig(&c2.cfg, "u", "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Tag    string `json:"tag"`
			Listen string `json:"listen"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Inbounds) == 0 {
		t.Fatalf("конфиг: %v", err)
	}
	if cfg.Inbounds[0].Listen != "0.0.0.0" {
		t.Errorf("app-вход слушает %q, ждали 0.0.0.0", cfg.Inbounds[0].Listen)
	}
	for _, a := range share.Addrs {
		if a == "127.0.0.1" || a == "" {
			t.Errorf("в адресах для других устройств loopback: %v", share.Addrs)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
