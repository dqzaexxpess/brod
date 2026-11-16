//go:build linux

package main

import (
	"brod/killswitch"
	"brod/supervisor"
)

// newKillswitch — киллсвитч платформы: на Linux правила nftables.
func newKillswitch() supervisor.Killswitch { return &killswitch.Nft{} }

// systemDNSByEngine: на Linux DNS системы в туннель направляет демон (resolvectl), поэтому флаг --tun-no-dns решает пользователь.
func systemDNSByEngine() bool { return false }

// newKillswitchTool — правила для разовых команд (--release-killswitch и др.) и подсказка, как снять их вручную.
func newKillswitchTool() (ksTool, string) {
	return &killswitch.Nft{}, "вручную: sudo nft delete table inet " + killswitch.Table
}
