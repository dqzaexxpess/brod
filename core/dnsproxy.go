package core

import (
	"net"
	"strconv"
	"sync"

	"brod/dnsfallback"
)

// localDNSProxy: общий на процесс локальный DNS-сервер с запасным путём через DoH (dnsfallback.Serve). Поднимается при первом обращении и живёт
// до конца процесса: ядро пересоздаётся при каждом переключении VPN, а сервер от этого не зависит (DNS сети он читает при каждом запросе).
var dnsProxy struct {
	once sync.Once
	host string
	port int
	ok   bool
}

func localDNSProxy() (host string, port int, ok bool) {
	dnsProxy.once.Do(func() {
		addr, _, err := dnsfallback.Serve(func() string {
			if SystemDNS == nil {
				return ""
			}
			return SystemDNS()
		}, dnsfallback.DefaultDoH)
		if err != nil {
			return
		}
		h, p, err := net.SplitHostPort(addr)
		if err != nil {
			return
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return
		}
		dnsProxy.host, dnsProxy.port, dnsProxy.ok = h, n, true
	})
	return dnsProxy.host, dnsProxy.port, dnsProxy.ok
}
