//go:build !linux && !windows

package main

import (
	"brod/killswitch"
	"brod/supervisor"
)

// newKillswitch: киллсвитча на этой платформе нет; nil — не пытаться ставить правила (иначе при каждом
// включении VPN в интерфейсе появлялась бы ошибка «киллсвитч не включился»).
func newKillswitch() supervisor.Killswitch { return nil }

// systemDNSByEngine: resolvectl есть только на Linux; на остальных DNS интерфейса туннеля задаёт сам движок (sing-tun, `auto_route`).
func systemDNSByEngine() bool { return true }

func newKillswitchTool() (ksTool, string) { return &killswitch.Nft{}, "" }
