package main

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
)

// meteredScript выбирает сеть маршрута по умолчанию в обход туннеля (адаптер Brod — wintun, тип 53, его маршрут исключается)
// и печатает стоимость её профиля из WinRT (NetworkInformation, Windows.Networking.Connectivity):
// «тип роуминг превышение», например «Fixed False False». При VPN профиль самого туннеля «Unrestricted», поэтому
// GetInternetConnectionProfile() не годится: нужна физическая сеть, через которую туннель идёт.
const meteredScript = `$ErrorActionPreference = 'Stop'
$best = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -AddressFamily IPv4 |
  Where-Object { (Get-NetAdapter -InterfaceIndex $_.ifIndex -ErrorAction SilentlyContinue).InterfaceType -ne 53 } |
  Sort-Object { $_.RouteMetric + $_.InterfaceMetric } | Select-Object -First 1
if (-not $best) { exit 0 }
$guid = (Get-NetAdapter -InterfaceIndex $best.ifIndex).InterfaceGuid
[void][Windows.Networking.Connectivity.NetworkInformation, Windows.Networking.Connectivity, ContentType = WindowsRuntime]
foreach ($p in [Windows.Networking.Connectivity.NetworkInformation]::GetConnectionProfiles()) {
  if ($p.NetworkAdapter -and ('{' + $p.NetworkAdapter.NetworkAdapterId.ToString() + '}') -eq $guid) {
    $c = $p.GetConnectionCost()
    "$($c.NetworkCostType) $($c.Roaming) $($c.OverDataLimit)"
    break
  }
}`

// meteredFunc: платная ли сеть (лимитное подключение в настройках Windows, раздача с телефона, мобильный модем) — по стоимости
// профиля сети маршрута по умолчанию в обход туннеля. Ответ кэшируется на 30 секунд; запрос идёт через Windows PowerShell
// (около секунды), поэтому после первого ответа устаревшее значение обновляется в фоне, а вызывающий получает прежнее.
// Ошибка или нет данных — сеть не считается платной.
func meteredFunc() func() bool {
	var (
		mu       sync.Mutex
		at       time.Time
		val      bool
		inflight bool
	)
	refresh := func() bool {
		v := winMetered()
		mu.Lock()
		val, at, inflight = v, time.Now(), false
		mu.Unlock()
		return v
	}
	return func() bool {
		mu.Lock()
		if at.IsZero() {
			mu.Unlock()
			return refresh() // первый ответ — синхронно: от него зависит, спрашивать ли согласие на скачивание
		}
		if time.Since(at) >= 30*time.Second && !inflight {
			inflight = true
			go refresh()
		}
		v := val
		mu.Unlock()
		return v
	}
}

func winMetered() bool {
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(meteredScript))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	return err == nil && parseCost(string(out))
}

// encodePS — аргумент -EncodedCommand: UTF-16LE в base64 (без экранирования кавычек в командной строке).
func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, r := range u {
		b[2*i], b[2*i+1] = byte(r), byte(r>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// parseCost разбирает «тип роуминг превышение». Платная — Fixed или Variable (так Windows понимает лимитное подключение),
// а также роуминг или превышенный лимит. Unrestricted, Unknown и пустой ответ — нет.
func parseCost(out string) bool {
	f := strings.Fields(out)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "Fixed", "Variable":
		return true
	}
	for _, flag := range f[1:] {
		if flag == "True" {
			return true
		}
	}
	return false
}
