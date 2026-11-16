package core

import (
	"strings"
	"testing"

	"brod/aggregator"
	"brod/engine"
)

func TestRouteRuleValidation(t *testing.T) {
	ok := RouteRule{Enabled: true, Action: ActionProxy, Match: RouteMatch{
		DomainSuffix: []string{" .Example.COM ", ""}, IPCIDR: []string{"10.1.2.3", "192.168.5.7/16"}, Port: []int{443}, PortRange: []string{"1000:2000"},
		Network: "TCP", ProcessName: []string{"firefox"}, RuleSet: []string{"geosite-youtube"}}}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	if ok.Match.DomainSuffix[0] != "example.com" || len(ok.Match.DomainSuffix) != 1 {
		t.Fatalf("домен не нормализован: %v", ok.Match.DomainSuffix)
	}
	if ok.Match.IPCIDR[0] != "10.1.2.3/32" || ok.Match.IPCIDR[1] != "192.168.0.0/16" || ok.Match.Network != "tcp" {
		t.Fatalf("нормализация: %+v", ok.Match)
	}
	bad := map[string]RouteRule{
		"нет условий":     {Action: ActionDirect},
		"действие":        {Action: "route", Match: RouteMatch{Domain: []string{"a.com"}}},
		"домен":           {Action: ActionDirect, Match: RouteMatch{Domain: []string{"bad domain.com"}}},
		"cidr":            {Action: ActionDirect, Match: RouteMatch{IPCIDR: []string{"999.1.1.1"}}},
		"порт":            {Action: ActionDirect, Match: RouteMatch{Port: []int{70000}}},
		"диапазон":        {Action: ActionDirect, Match: RouteMatch{PortRange: []string{"5:1"}}},
		"сеть":            {Action: ActionDirect, Match: RouteMatch{Domain: []string{"a.com"}, Network: "icmp"}},
		"regex":           {Action: ActionDirect, Match: RouteMatch{DomainRegex: []string{"("}}},
		"неизвестный set": {Action: ActionDirect, Match: RouteMatch{RuleSet: []string{"nope"}}},
		"процесс":         {Action: ActionDirect, Match: RouteMatch{ProcessName: []string{"a/b"}}},
	}
	for name, r := range bad {
		if err := r.validate(); err == nil {
			t.Errorf("%s: правило должно отклоняться", name)
		}
	}
}

func TestPutRouteRulesRoundTrip(t *testing.T) {
	c := startCore(t, t.TempDir())
	rs := []RouteRule{
		{Title: "Пример", Enabled: true, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"example.com"}}},
		{Enabled: false, Action: ActionBlock, Match: RouteMatch{Domain: []string{"ads.example.net"}}},
	}
	if err := c.PutRouteRules(rs, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := c.RouteRules()
	if err != nil || len(got) != 2 || got[0].ID == "" || got[1].ID == "" || got[0].ID == got[1].ID || got[0].Title != "Пример" || got[1].Enabled {
		t.Fatalf("%+v %v", got, err)
	}
	if err := c.PutRouteRules([]RouteRule{{Action: ActionDirect}}, "user"); err == nil {
		t.Fatal("правило без условий должно отклоняться")
	}
	if got2, _ := c.RouteRules(); len(got2) != 2 {
		t.Fatal("отклонённое сохранение не должно менять список")
	}
	if err := c.PutRouteRules(nil, "user"); err != nil {
		t.Fatal(err)
	}
	if got3, _ := c.RouteRules(); len(got3) != 0 {
		t.Fatalf("список должен очиститься: %+v", got3)
	}
}

// Правила пользователя стоят выше встроенных «банки напрямую», порядок сохраняется, DNS-правила согласованы с действием.
func TestUserRulesInTUNConfig(t *testing.T) {
	m, raw := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: []RouteRule{
		{ID: "1", Enabled: true, Action: ActionProxy, Match: RouteMatch{DomainSuffix: []string{"my-proxy.example"}}},
		{ID: "2", Enabled: true, Action: ActionBlock, Match: RouteMatch{Domain: []string{"ads.example.net"}}},
		{ID: "3", Enabled: false, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"disabled.example"}}},
		{ID: "4", Enabled: true, Action: ActionDirect, Match: RouteMatch{ProcessName: []string{"curl"}, Port: []int{443}}},
	}})
	rules := routeRules(m)
	proxyI := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == OutApp && r["domain_suffix"] != nil })
	blockI := ruleIndex(rules, func(r map[string]any) bool { return r["action"] == "reject" && r["domain"] != nil })
	procI := ruleIndex(rules, func(r map[string]any) bool { return r["process_name"] != nil })
	bankI := ruleIndex(rules, func(r map[string]any) bool {
		return r["outbound"] == OutDirect && hasSet(r, "geosite-category-bank-ru")
	})
	if proxyI < 0 || blockI < 0 || procI < 0 || bankI < 0 || !(proxyI < blockI && blockI < procI && procI < bankI) {
		t.Fatalf("порядок: proxy=%d block=%d proc=%d bank=%d", proxyI, blockI, procI, bankI)
	}
	if strings.Contains(raw, "disabled.example") {
		t.Fatal("выключенное правило не должно попасть в конфиг")
	}
	if m["route"].(map[string]any)["find_process"] != true {
		t.Fatal("для правил по процессу нужен find_process")
	}
	// DNS: proxy-правило по домену резолвится в туннеле; block и правила с процессом в DNS не попадают
	dnsRules := m["dns"].(map[string]any)["rules"].([]any)
	first := dnsRules[1].(map[string]any)
	if first["server"] != "remote" || first["domain_suffix"] == nil {
		t.Fatalf("первое DNS-правило: %v", first)
	}
	if strings.Count(raw, "ads.example.net") != 1 { // только в route
		t.Fatalf("block-правило не должно быть в DNS: %s", raw)
	}
}

func TestCoreLoadsSavedRulesWhenTUN(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	if err := c.PutRouteRules([]RouteRule{{Enabled: true, Action: ActionProxy, Match: RouteMatch{DomainSuffix: []string{"saved.example"}}}}, "user"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	// новый экземпляр без TUN правил не загружает (они нужны только движку TUN); проверяем сборку конфига с загруженными правилами
	c2, err := New(Config{StateDir: dir, AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	rs, err := c2.RouteRules()
	if err != nil || len(rs) != 1 || rs[0].Match.DomainSuffix[0] != "saved.example" {
		t.Fatalf("%+v %v", rs, err)
	}
}

// Все условия и действия, включая новые, принимает настоящий разбор конфига движка (а не только наша карта).
func TestAllRuleFieldsAcceptedByEngine(t *testing.T) {
	rules := []RouteRule{
		{ID: "a", Enabled: true, Action: ActionDirect, Match: RouteMatch{IPVersion: 6, Protocol: []string{"TLS", "quic"}, SourceIPCIDR: []string{"192.168.1.0/24"},
			IPIsPrivate: true, SourceIPIsPrivate: true, SourcePort: []int{5000}, SourcePortRange: []string{"6000:6100"}, Network: "udp", Invert: true}},
		{ID: "b", Enabled: true, Action: ActionBlock, BlockMethod: "drop", Match: RouteMatch{ProcessPath: []string{"/usr/bin/curl"}, ProcessPathRegex: []string{`^/opt/.*/tool$`}, PackageName: []string{"com.example.app"}}},
		{ID: "c", Enabled: true, Action: ActionProxy, Via: &RouteVia{Kind: ViaCountry, Country: "NL"}, Match: RouteMatch{DomainSuffix: []string{"nl.example"}, Protocol: []string{"http"}}},
	}
	for i := range rules {
		if err := rules[i].validate(); err != nil {
			t.Fatalf("правило %s: %v", rules[i].ID, err)
		}
	}
	_, raw := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: rules})
	if err := engine.ValidateConfig([]byte(raw)); err != nil {
		t.Fatalf("движок не принял конфиг: %v\n%s", err, raw)
	}
	m, _ := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: rules})
	if m["route"].(map[string]any)["find_process"] != true {
		t.Fatal("для условий по пути процесса нужен find_process")
	}
}

func TestNewConditionsValidation(t *testing.T) {
	base := func(m RouteMatch) RouteRule { return RouteRule{Enabled: true, Action: ActionDirect, Match: m} }
	ok := base(RouteMatch{Protocol: []string{" TLS "}, SourceIPCIDR: []string{"10.1.1.1"}, IPVersion: 4})
	if err := ok.validate(); err != nil || ok.Match.Protocol[0] != "tls" || ok.Match.SourceIPCIDR[0] != "10.1.1.1/32" {
		t.Fatalf("нормализация: %v, %+v", err, ok.Match)
	}
	for name, r := range map[string]RouteRule{
		"версия IP":         base(RouteMatch{IPVersion: 5, Domain: []string{"a.example"}}),
		"протокол":          base(RouteMatch{Protocol: []string{"gopher"}}),
		"cidr источника":    base(RouteMatch{SourceIPCIDR: []string{"x"}}),
		"порт источника":    base(RouteMatch{SourcePort: []int{0}}),
		"диапазон":          base(RouteMatch{SourcePortRange: []string{"9:1"}}),
		"regex пути":        base(RouteMatch{ProcessPathRegex: []string{"("}}),
		"пакет":             base(RouteMatch{PackageName: []string{"no_dots"}}),
		"drop без block":    {Enabled: true, Action: ActionDirect, BlockMethod: "drop", Match: RouteMatch{Domain: []string{"a.example"}}},
		"способ блокировки": {Enabled: true, Action: ActionBlock, BlockMethod: "tarpit", Match: RouteMatch{Domain: []string{"a.example"}}},
	} {
		if err := r.validate(); err == nil {
			t.Errorf("%s: правило должно отклоняться", name)
		}
	}
	// только inverted ip_is_private без других условий — это условие, а не пустое правило
	only := base(RouteMatch{IPIsPrivate: true})
	if err := only.validate(); err != nil {
		t.Fatalf("ip_is_private как единственное условие: %v", err)
	}
	// invert не делает правило доменным для DNS
	inv := RouteRule{Enabled: true, Action: ActionProxy, Match: RouteMatch{Domain: []string{"a.example"}, Invert: true}}
	if inv.dnsObject() != nil {
		t.Fatal("DNS-правило для инвертированного правила не строится")
	}
}

// Правило по приложению Android (package_name) работает только при find_process: без него движок не узнаёт приложение, и правило молча не срабатывает.
func TestPackageNameNeedsFindProcess(t *testing.T) {
	rule := RouteRule{ID: "p", Enabled: true, Action: ActionDirect, Match: RouteMatch{PackageName: []string{"com.android.chrome"}}}
	if err := rule.validate(); err != nil {
		t.Fatal(err)
	}
	m, _ := tunCfg(t, TUNConfig{Mode: ModeAll, UserRules: []RouteRule{rule}})
	if m["route"].(map[string]any)["find_process"] != true {
		t.Fatal("для package_name нужен find_process")
	}
	m, _ = tunCfg(t, TUNConfig{Mode: ModeAll})
	if m["route"].(map[string]any)["find_process"] == true {
		t.Fatal("без правил по приложениям find_process не нужен (лишняя нагрузка)")
	}
}

func TestWifiConditions(t *testing.T) {
	r := RouteRule{ID: "w", Enabled: true, Action: ActionDirect, Match: RouteMatch{WifiSSID: []string{" Home "}, WifiBSSID: []string{"AA-BB-CC-DD-EE-FF"}}}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if r.Match.WifiBSSID[0] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("адрес точки не приведён к виду aa:bb:…: %q", r.Match.WifiBSSID[0])
	}
	for _, bad := range []RouteMatch{{WifiSSID: []string{strings.Repeat("x", 33)}}, {WifiBSSID: []string{"not-a-mac"}}, {WifiBSSID: []string{"aa:bb:cc:dd:ee:ff:00:11"}}} {
		x := RouteRule{Enabled: true, Action: ActionDirect, Match: bad}
		if x.validate() == nil {
			t.Errorf("принято %+v", bad)
		}
	}
	_, raw := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: []RouteRule{r}})
	if err := engine.ValidateConfig([]byte(raw)); err != nil {
		t.Fatalf("движок не принял условия Wi-Fi: %v", err)
	}
	if !strings.Contains(raw, "wifi_ssid") || !strings.Contains(raw, "wifi_bssid") {
		t.Fatal("условия Wi-Fi не попали в конфиг")
	}
}

func TestTLSFragmentOptions(t *testing.T) {
	r := RouteRule{ID: "f", Enabled: true, Action: ActionDirect, TLSFragment: true, TLSRecordFragment: true, Match: RouteMatch{DomainSuffix: []string{"example.org"}}}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	b := RouteRule{ID: "b", Enabled: true, Action: ActionBlock, TLSFragment: true, Match: RouteMatch{DomainSuffix: []string{"example.org"}}}
	if b.validate() == nil {
		t.Fatal("дробление принято для block")
	}
	_, raw := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: []RouteRule{r}})
	if err := engine.ValidateConfig([]byte(raw)); err != nil {
		t.Fatalf("движок не принял дробление TLS: %v\n%s", err, raw)
	}
	if !strings.Contains(raw, "tls_fragment") || !strings.Contains(raw, "tls_record_fragment") {
		t.Fatal("параметры дробления не попали в конфиг")
	}
}
