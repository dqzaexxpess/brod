package core

import (
	"errors"
	"testing"
)

func TestWarpSettingsValidation(t *testing.T) {
	ok := map[string][]string{
		SetWarpEndpoint: {"", "188.114.96.74", "engage.cloudflareclient.com", "2606:4700:d0::a29f:c001"},
		SetWarpPort:     {"", "2408", "878", "65535"},
		SetWarpMask:     {"", "quic", "dns", "stun", "sip", "none"},
		SetWarpMTU:      {"", "1280", "1000", "1500"},
		SetWarpMaskHost: {"", "www.google.com", "yandex.ru"},
		SetWarpBrowser:  {"", "chrome", "firefox", "curl"},
	}
	bad := map[string][]string{
		SetWarpEndpoint: {"a b", "http://x", "-x.com"},
		SetWarpPort:     {"0", "65536", "abc", "-1"},
		SetWarpMask:     {"wg", "QUIC"},
		SetWarpMTU:      {"999", "1501", "x"},
		SetWarpMaskHost: {"a_b.com", "x y"},
		SetWarpBrowser:  {"safari"},
	}
	for k, vs := range ok {
		for _, v := range vs {
			if err := settingRules[k](v); err != nil {
				t.Errorf("%s=%q отвергнуто: %v", k, v, err)
			}
		}
	}
	for k, vs := range bad {
		for _, v := range vs {
			if err := settingRules[k](v); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s=%q должно быть отвергнуто, получено %v", k, v, err)
			}
		}
	}
}

func TestApplyWarpSettingsOverridesConfig(t *testing.T) {
	cfg := Config{WarpEndpoint: "162.159.192.1", WarpMask: "quic"}
	vals := map[string]string{SetWarpEndpoint: "188.114.96.74", SetWarpPort: "878", SetWarpMask: "none", SetWarpMTU: "1380", SetWarpMaskHost: "yandex.ru", SetWarpBrowser: "firefox"}
	applyWarpSettings(&cfg, func(k string) string { return vals[k] })
	if cfg.WarpEndpoint != "188.114.96.74" || cfg.WarpPort != 878 || cfg.WarpMask != "none" || cfg.WarpMTU != 1380 || cfg.WarpMaskHost != "yandex.ru" || cfg.WarpBrowser != "firefox" {
		t.Fatalf("настройки не применены: %+v", cfg)
	}
	cfg2 := Config{WarpEndpoint: "162.159.192.1", WarpMask: "quic"}
	applyWarpSettings(&cfg2, func(string) string { return "" })
	if cfg2.WarpEndpoint != "162.159.192.1" || cfg2.WarpMask != "quic" || cfg2.WarpPort != 0 {
		t.Fatalf("пустые настройки не должны менять конфигурацию: %+v", cfg2)
	}
}
