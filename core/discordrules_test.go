package core

import "testing"

// Правила шаблона «Звонки Discord» (приложение): голос приходит без домена, поэтому ловится процессом и UDP-портами;
// в конфиг они должны попасть в режиме «Только заблокированное» выше встроенных списков и направить трафик через выход.
func TestDiscordCallRulesReachTUNConfig(t *testing.T) {
	rules := []RouteRule{
		{ID: "d1", Enabled: true, Action: ActionProxy, Match: RouteMatch{ProcessName: []string{"Discord.exe", "Discord", "discord"}}},
		{ID: "d2", Enabled: true, Action: ActionProxy, Match: RouteMatch{Network: "udp", PortRange: []string{"19294:19344", "50000:65535"}}},
	}
	for i := range rules {
		if err := rules[i].validate(); err != nil {
			t.Fatalf("правило %s отвергнуто: %v", rules[i].ID, err)
		}
	}
	m, _ := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: rules})
	got := routeRules(m)
	udpI := ruleIndex(got, func(r map[string]any) bool { return r["network"] == "udp" && r["port_range"] != nil && r["outbound"] == OutApp })
	procI := ruleIndex(got, func(r map[string]any) bool { return r["process_name"] != nil && r["outbound"] == OutApp })
	if udpI < 0 || procI < 0 {
		t.Fatalf("правила Discord не попали в маршрут через выход: udp=%d proc=%d", udpI, procI)
	}
	if m["route"].(map[string]any)["find_process"] != true {
		t.Fatal("для правила по процессу нужен find_process")
	}
}
