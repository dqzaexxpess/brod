package core

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
)

// Параметры встроенного WARP, заданные вручную (окно «Cloudflare WARP»). Пустое значение — умолчание. Применяются при создании ядра:
// после изменения VPN перезапускается так же, как после настроек туннеля.
const (
	SetWarpEndpoint = "warp.endpoint"  // адрес endpoint'а: IP или имя (по умолчанию engage.cloudflareclient.com; на Windows и Android IP)
	SetWarpPort     = "warp.port"      // порт endpoint'а (по умолчанию 2408)
	SetWarpMask     = "warp.mask"      // quic | dns | stun | sip | none
	SetWarpMTU      = "warp.mtu"       // 1000–1500 (по умолчанию 1280)
	SetWarpMaskHost = "warp.mask_host" // домен маскировки
	SetWarpBrowser  = "warp.browser"   // chrome | firefox | curl (только quic)
)

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func warpHostValue(v, what string) error {
	if v == "" {
		return nil
	}
	if _, err := netip.ParseAddr(v); err == nil {
		return nil
	}
	if !hostRe.MatchString(v) {
		return fmt.Errorf("%w: %s: нужен IP-адрес или имя хоста", ErrInvalid, what)
	}
	return nil
}

func warpEndpointSetting(v string) error { return warpHostValue(v, "адрес WARP") }
func warpMaskHostSetting(v string) error { return warpHostValue(v, "домен маскировки") }

func warpPortSetting(v string) error {
	if v == "" {
		return nil
	}
	if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%w: порт WARP: число от 1 до 65535", ErrInvalid)
	}
	return nil
}

func warpMTUSetting(v string) error {
	if v == "" {
		return nil
	}
	if n, err := strconv.Atoi(v); err != nil || n < 1000 || n > 1500 {
		return fmt.Errorf("%w: MTU WARP: число от 1000 до 1500", ErrInvalid)
	}
	return nil
}

func warpMaskSetting(v string) error {
	switch v {
	case "", "quic", "dns", "stun", "sip", "none":
		return nil
	}
	return fmt.Errorf("%w: маскировка WARP: quic | dns | stun | sip | none", ErrInvalid)
}

func warpBrowserSetting(v string) error {
	switch v {
	case "", "chrome", "firefox", "curl":
		return nil
	}
	return fmt.Errorf("%w: браузер маскировки: chrome | firefox | curl", ErrInvalid)
}

// applyWarpSettings накладывает ручные параметры WARP из настроек на конфигурацию ядра.
func applyWarpSettings(cfg *Config, get func(key string) string) {
	if v := get(SetWarpEndpoint); v != "" {
		cfg.WarpEndpoint = v
	}
	if v := get(SetWarpPort); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.WarpPort = uint16(n)
		}
	}
	if v := get(SetWarpMask); v != "" {
		cfg.WarpMask = v
	}
	if v := get(SetWarpMTU); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.WarpMTU = uint32(n)
		}
	}
	if v := get(SetWarpMaskHost); v != "" {
		cfg.WarpMaskHost = v
	}
	if v := get(SetWarpBrowser); v != "" {
		cfg.WarpBrowser = v
	}
}
