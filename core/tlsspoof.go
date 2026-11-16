package core

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

// Подмена SNI (tls_spoof, идея sni-spoofing): перед настоящим ClientHello на соединение уходит ложный, с безобидным именем сайта и испорченными
// параметрами TCP (неверный номер последовательности, контрольная сумма и т. п.): DPI провайдера разбирает его и пропускает соединение по имени,
// а сервер ложный пакет отбрасывает. Реализация в движке (common/tlsspoof). Нужны: Linux — право cap_net_raw на brodd (deploy/tun-setup.sh),
// Windows — драйвер WinDivert (встроен в движок, служба работает от LocalSystem). Android — недоступно без root (сырые сокеты приложению запрещены).
const defaultTLSSpoofMethod = "wrong-sequence"

var tlsSpoofMethods = map[string]bool{"wrong-sequence": true, "wrong-checksum": true, "wrong-ack": true, "wrong-md5": true, "wrong-timestamp": true}

var spoofHostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func (r RouteRule) tlsSpoofMethod() string {
	if r.TLSSpoofMethod == "" {
		return defaultTLSSpoofMethod
	}
	return r.TLSSpoofMethod
}

// TLSSpoofSupported: платформа умеет подмену SNI (Android — нет).
func TLSSpoofSupported() bool { return runtime.GOOS == "linux" || runtime.GOOS == "windows" }

func (r *RouteRule) validateTLSSpoof() error {
	if r.TLSSpoof == "" {
		if r.TLSSpoofMethod != "" {
			return fmt.Errorf("способ подмены SNI без имени сайта")
		}
		return nil
	}
	if r.Action == ActionBlock {
		return fmt.Errorf("подмена SNI задаётся только для действий proxy и direct")
	}
	if !TLSSpoofSupported() {
		return fmt.Errorf("подмена SNI на этой платформе недоступна")
	}
	r.TLSSpoof = strings.ToLower(strings.TrimSpace(r.TLSSpoof))
	if !spoofHostRe.MatchString(r.TLSSpoof) || len(r.TLSSpoof) > 253 {
		return fmt.Errorf("подмена SNI: нужно имя сайта вроде www.example.com")
	}
	if r.TLSSpoofMethod != "" && !tlsSpoofMethods[r.TLSSpoofMethod] {
		return fmt.Errorf("способ подмены SNI %q: нужен wrong-sequence, wrong-checksum, wrong-ack, wrong-md5 или wrong-timestamp", r.TLSSpoofMethod)
	}
	return nil
}
