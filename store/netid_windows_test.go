package store

import (
	"strings"
	"testing"
)

func TestNetBaseFromPicksLowestEffectiveMetricAndForeignTunnels(t *testing.T) {
	ads := []netAdapter{
		{Index: 26, Name: "Ethernet", Description: "Intel(R) Ethernet Connection (10) I219-V", Up: true, Metric: 25},
		{Index: 12, Name: "Radmin VPN", Description: "Famatech Radmin VPN Ethernet Adapter", Up: true, Metric: 1}, // метрика интерфейса меньше,
		{Index: 15, Name: "throne-tun", Description: "sing-tun Tunnel", Up: true},
		{Index: 37, Name: "Brod", Description: "sing-tun Tunnel", Up: true, Metric: 0},
		{Index: 2, Name: "Teredo Tunneling Pseudo-Interface", Description: "Teredo Tunneling Pseudo-Interface", Up: true},
		{Index: 16, Name: "Ethernet 2", Description: "TAP-Windows Adapter V9", Up: false},
	}
	routes := []defaultRoute{
		{Index: 26, Gateway: "192.0.2.1", Metric: 0},
		{Index: 12, Gateway: "26.0.0.1", Metric: 9255}, // но маршрут с огромной метрикой
		{Index: 37, Gateway: "10.77.77.2", Metric: 0},  // собственный туннель не считается шлюзом сети
	}
	if got, want := netBaseFrom(ads, routes), "Ethernet/192.0.2.1+throne-tun"; got != want {
		t.Fatalf("netBase: %q, ожидалось %q", got, want)
	}
	if got := netBaseFrom(ads[:2], routes); got != "Ethernet/192.0.2.1" {
		t.Fatalf("без туннелей: %q", got)
	}
	if got := netBaseFrom(ads, nil); got != "no-default-route+throne-tun" {
		t.Fatalf("без маршрута по умолчанию: %q", got)
	}
}

func TestNetIDOnThisMachine(t *testing.T) {
	id := NetID()
	if id == "unknown" || id == "" || strings.HasPrefix(id, "no-default-route") {
		t.Fatalf("NetID: %q", id)
	}
	if strings.Contains(id, "+"+ownTUNWindows) || strings.Contains(id, ","+ownTUNWindows) || strings.Contains(strings.ToLower(id), "teredo") {
		t.Fatalf("лишний туннель в NetID: %q", id)
	}
	t.Logf("NetID = %q", id)
}
