package main

import (
	"brod/killswitch"
	"brod/supervisor"
)

// newKillswitch — киллсвитч платформы: на Windows фильтры WFP. Включается настройкой killswitch.enabled (по умолчанию выключен).
func newKillswitch() supervisor.Killswitch { return &killswitch.WFP{} }

// newKillswitchTool — правила для разовых команд и подсказка, как снять их вручную. brodd.exe собран как оконное приложение и в консоль
// не пишет, поэтому для консоли есть та же команда в brodctl.
func newKillswitchTool() (ksTool, string) {
	return &killswitch.WFP{}, "вручную (консоль администратора): brodctl killswitch release; в крайнем случае — перезагрузка"
}

// systemDNSByEngine: DNS интерфейса туннеля задаёт сам движок (sing-tun, `auto_route`).
func systemDNSByEngine() bool { return true }
