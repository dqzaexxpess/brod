package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// meteredFunc: платная ли сеть (раздача с телефона, мобильный модем), по данным NetworkManager для интерфейса маршрута по умолчанию.
// Ответ кэшируется на 30 секунд (вызывается при каждом опросе состояния обновления). Нет nmcli или NetworkManager — сеть не считается платной.
func meteredFunc() func() bool {
	var (
		mu  sync.Mutex
		at  time.Time
		val bool
	)
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < 30*time.Second {
			return val
		}
		val, at = nmMetered(defaultIface()), time.Now()
		return val
	}
}

// defaultIface — интерфейс маршрута по умолчанию из /proc/net/route (без внешних программ).
func defaultIface() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 3 && f[1] == "00000000" {
			if flags, err := strconv.ParseUint(f[3], 16, 32); err == nil && flags&1 != 0 { // RTF_UP
				return f[0]
			}
		}
	}
	return ""
}

func nmMetered(iface string) bool {
	if iface == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nmcli", "-g", "GENERAL.METERED", "device", "show", iface).Output()
	return err == nil && parseMetered(string(out))
}

// parseMetered разбирает ответ nmcli: «yes», «yes (guessed)» — платная; «no», «no (guessed)», «unknown» — нет.
func parseMetered(out string) bool { return strings.HasPrefix(strings.TrimSpace(out), "yes") }
