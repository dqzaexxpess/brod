package core

import (
	"net"
	"strconv"
	"sync"

	"brod/dnsfallback"
)

// localDNSProxy: общий на процесс локальный DNS-сервер с запасным путём через DoH (dnsfallback.Listen). Поднимается при первом обращении и живёт
// до конца процесса: ядро пересоздаётся при каждом переключении VPN, а сервер от этого не зависит (DNS сети он читает при каждом запросе).
// Если сервер остановился сам (сокет не пересоздался), при следующем обращении поднимается новый, на другом порту: адрес в конфиге движка
// строится заново при каждом создании ядра.
var dnsProxy struct {
	sync.Mutex
	host string
	port int
	done <-chan struct{}
}

func localDNSProxy() (host string, port int, ok bool) {
	dnsProxy.Lock()
	defer dnsProxy.Unlock()
	if dnsProxy.done != nil {
		select {
		case <-dnsProxy.done:
			dnsProxy.done = nil // прежний остановился: поднимаем новый
		default:
			return dnsProxy.host, dnsProxy.port, true
		}
	}
	addr, _, done, err := dnsfallback.Listen(func() string {
		if SystemDNS == nil {
			return ""
		}
		return SystemDNS()
	}, dnsfallback.DefaultDoH)
	if err != nil {
		return "", 0, false
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, false
	}
	dnsProxy.host, dnsProxy.port, dnsProxy.done = h, n, done
	return h, n, true
}
