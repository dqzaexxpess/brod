package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"brod/engine"
	"brod/rulesets"
)

type cfgMap = map[string]any

func baseCfg(t *testing.T, mutate func(*Config)) cfgMap {
	t.Helper()
	cfg := Config{StateDir: t.TempDir(), LogLevel: "error", AppHost: "127.0.0.1", AppPort: nextPort()}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := prepareTUN(&cfg); err != nil { // устанавливает встроенные списки в каталог состояния
		t.Fatal(err)
	}
	raw, err := engineConfig(&cfg, "u", "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ValidateConfig(raw); err != nil {
		t.Fatalf("конфиг не проходит проверку движка: %v\n%s", err, raw)
	}
	var m cfgMap
	_ = json.Unmarshal(raw, &m)
	return m
}

func tunCfg(t *testing.T, tun TUNConfig) (cfgMap, string) {
	tun.Enabled = true
	m := baseCfg(t, func(c *Config) { c.TUN = tun; c.TUN.LocalDNS = "192.0.2.53" })
	raw, _ := json.Marshal(m)
	return m, string(raw)
}

func routeRules(m cfgMap) []map[string]any {
	var out []map[string]any
	for _, r := range m["route"].(map[string]any)["rules"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func ruleIndex(rules []map[string]any, match func(map[string]any) bool) int {
	for i, r := range rules {
		if match(r) {
			return i
		}
	}
	return -1
}

func hasSet(r map[string]any, name string) bool {
	l, _ := r["rule_set"].([]any)
	for _, x := range l {
		if x == name {
			return true
		}
	}
	return false
}

// Без TUN в конфиге нет ни TUN-входа, ни DNS-перехвата, ни списков: обычный режим.
func TestTUNOffByDefault(t *testing.T) {
	m := baseCfg(t, nil)
	if raw, _ := json.Marshal(m); strings.Contains(string(raw), `"tun"`) || m["dns"] != nil || strings.Contains(string(raw), "rule_set") {
		t.Fatalf("TUN просочился в конфиг по умолчанию: %s", raw)
	}
}

// Сокеты движка помечаются (default_mark), чтобы киллсвитч пропускал только их; без TUN метки нет.
func TestTUNMarksEngineSockets(t *testing.T) {
	m, _ := tunCfg(t, TUNConfig{Mode: ModeAll})
	if runtime.GOOS != "linux" {
		// метка сокетов есть только на Linux (engineMarksSockets): на других системах sing-box не принимает default_mark
		if _, ok := m["route"].(map[string]any)["default_mark"]; ok {
			t.Fatal("default_mark вне Linux")
		}
		return
	}
	if got := m["route"].(map[string]any)["default_mark"]; got != float64(0x62726f64) {
		t.Fatalf("default_mark = %v", got)
	}
	if _, ok := baseCfg(t, nil)["route"].(map[string]any)["default_mark"]; ok {
		t.Fatal("без TUN метки быть не должно")
	}
}

// Режим по умолчанию — «только заблокированное»: банки/Госуслуги напрямую раньше заблокированного, остальное напрямую,
// вход приложений всегда через dispatch, DNS заблокированных доменов — DoH через активный выход, прочего — локально.
func TestTUNBlockedModeIsDefault(t *testing.T) {
	m, s := tunCfg(t, TUNConfig{})
	rules := routeRules(m)
	app := ruleIndex(rules, func(r map[string]any) bool { return r["inbound"] != nil && r["outbound"] == OutApp })
	sniff := ruleIndex(rules, func(r map[string]any) bool { return r["action"] == "sniff" })
	hijack := ruleIndex(rules, func(r map[string]any) bool { return r["action"] == "hijack-dns" })
	private := ruleIndex(rules, func(r map[string]any) bool { return r["ip_is_private"] == true })
	v6 := ruleIndex(rules, func(r map[string]any) bool { return r["ip_version"] == float64(6) && r["action"] == "reject" })
	direct := ruleIndex(rules, func(r map[string]any) bool {
		return r["outbound"] == OutDirect && hasSet(r, "geosite-category-bank-ru")
	})
	proxy := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == OutApp && hasSet(r, "geosite-ru-blocked") })
	if app != 0 || !(sniff < hijack && hijack < private && private < direct && direct < proxy) || v6 < 0 {
		t.Fatalf("порядок правил: app=%d sniff=%d hijack=%d private=%d v6=%d direct=%d proxy=%d", app, sniff, hijack, private, v6, direct, proxy)
	}
	for _, name := range []string{"geosite-category-gov-ru", "geosite-ru-available-only-inside"} {
		if !hasSet(rules[direct], name) {
			t.Errorf("в «всегда напрямую» нет %s", name)
		}
	}
	for _, name := range []string{"geosite-youtube", "geosite-telegram", "geoip-ru-blocked", "geoip-telegram"} {
		if !hasSet(rules[proxy], name) {
			t.Errorf("в «через туннель» нет %s", name)
		}
	}
	if hasSet(rules[proxy], "geoip-ru") || hasSet(rules[proxy], "geosite-category-ru") {
		t.Error("российские списки не должны идти через туннель")
	}
	if m["route"].(map[string]any)["final"] != OutAppTUN { // dispatch по умолчанию direct: цели работают и для «остального»
		t.Fatalf("final = %v, ожидалось %s", m["route"].(map[string]any)["final"], OutAppTUN)
	}
	dns := m["dns"].(cfgMap)
	if dns["final"] != "local" || !strings.Contains(s, `"server":"192.0.2.53"`) {
		t.Fatalf("DNS: final=%v", dns["final"])
	}
	// [0] — служебное правило node-servers → local (имена серверов узлов; см. nodeservers.go)
	dr := dns["rules"].([]any)[1].(map[string]any)
	if dr["server"] != "remote" {
		t.Fatalf("DNS заблокированных доменов должен идти через remote: %v", dr)
	}
	for _, want := range []string{`"strict_route":true`, `"detour":"active"`, `"default_domain_resolver":"local"`, `"10.77.77.1/30"`} {
		if !strings.Contains(s, want) {
			t.Errorf("в конфиге нет %s", want)
		}
	}
	if strings.Contains(s, "172.19.") || strings.Contains(s, "override_destination") {
		t.Fatalf("лишнее в конфиге: %s", s)
	}
}

func TestTUNAllModes(t *testing.T) {
	m, _ := tunCfg(t, TUNConfig{Mode: ModeAll})
	rules := routeRules(m)
	if ruleIndex(rules, func(r map[string]any) bool { return hasSet(r, "geosite-ru-blocked") }) >= 0 {
		t.Fatal("в режиме all списки заблокированного не нужны")
	}
	if ruleIndex(rules, func(r map[string]any) bool {
		return r["outbound"] == OutDirect && hasSet(r, "geosite-category-bank-ru")
	}) < 0 {
		t.Fatal("банки напрямую и в режиме all")
	}
	if m["route"].(map[string]any)["final"] != OutApp || m["dns"].(cfgMap)["final"] != "remote" {
		t.Fatalf("all: final=%v dns=%v", m["route"].(map[string]any)["final"], m["dns"].(cfgMap)["final"])
	}

	m, _ = tunCfg(t, TUNConfig{Mode: ModeAllButRU})
	rules = routeRules(m)
	ru := ruleIndex(rules, func(r map[string]any) bool {
		return r["outbound"] == OutDirect && hasSet(r, "geoip-ru") && hasSet(r, "geosite-category-ru")
	})
	if ru < 0 {
		t.Fatal("all-but-ru: российские адреса и домены должны идти напрямую")
	}
	if m["route"].(map[string]any)["final"] != OutApp {
		t.Fatal("all-but-ru: остальное через туннель")
	}
	if _, err := New(Config{StateDir: t.TempDir(), TUN: TUNConfig{Enabled: true, Mode: "странный"}}); err == nil {
		t.Fatal("неизвестный режим принят")
	}
}

// Свои списки: суффиксы доменов в правилах маршрута и DNS; режим без маршрутов не трогает системную таблицу.
func TestTUNUserListsAndNoRoute(t *testing.T) {
	m, s := tunCfg(t, TUNConfig{UserDirect: []string{"mybank.example"}, UserProxy: []string{"blocked.example"}, NoAutoRoute: true})
	rules := routeRules(m)
	d := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == OutDirect && r["domain_suffix"] != nil })
	p := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == OutApp && r["domain_suffix"] != nil })
	bl := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == OutApp && hasSet(r, "geosite-ru-blocked") })
	if d < 0 || p < 0 || !(d < p && p < bl) {
		t.Fatalf("порядок пользовательских правил: direct=%d proxy=%d blocked=%d", d, p, bl)
	}
	if !strings.Contains(s, `"auto_route":false`) || strings.Contains(s, `"strict_route":true`) {
		t.Fatalf("режим без маршрутов: %s", s)
	}
}

func TestReadDomainListAndUserFiles(t *testing.T) {
	dir := t.TempDir()
	body := "# комментарий\nExample.COM\n.sub.example.org  # хвост\nбез точки\nbad domain.com\nhttps://x.com\nuser@x.com\n\nok.example\n"
	p := filepath.Join(dir, "rules-direct.txt")
	_ = os.WriteFile(p, []byte(body), 0o600)
	got := readDomainList(p)
	if strings.Join(got, ",") != "example.com,sub.example.org,ok.example" {
		t.Fatalf("список: %v", got)
	}
	if readDomainList(filepath.Join(dir, "нет")) != nil {
		t.Fatal("отсутствующий файл")
	}
	// prepareTUN подхватывает файлы из каталога состояния и ставит встроенные списки
	cfg := Config{StateDir: dir, TUN: TUNConfig{Enabled: true}}
	if err := prepareTUN(&cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.TUN.UserDirect) != 3 || cfg.TUN.RuleSetDir != filepath.Join(dir, "rulesets") {
		t.Fatalf("%+v", cfg.TUN)
	}
	if _, err := os.Stat(filepath.Join(dir, "rulesets", "geosite-ru-blocked.srs")); err != nil {
		t.Fatal("встроенные списки не установлены")
	}
}

// Разбор опций ловит ошибки конфига (проверка действительно что-то проверяет).
func TestValidateConfigRejectsGarbage(t *testing.T) {
	if err := engine.ValidateConfig([]byte(`{"inbounds":[{"type":"tun","interface_name":5}]}`)); err == nil {
		t.Fatal("заведомо неверный конфиг принят")
	}
	if err := engine.ValidateConfig([]byte(`{"outbounds":[{"type":"нет-такого","tag":"x"}]}`)); err == nil {
		t.Fatal("неизвестный тип принят")
	}
}

// Обновление списков не портит файлы при сбое сети и публикует событие.
func TestUpdateRuleSetsFailureKeepsFiles(t *testing.T) {
	// сам TUN в тесте не поднимается (нужны права): проверяется только обновление файлов в каталоге списков
	c := cfgCore(t, func(cfg *Config) {
		cfg.RuleSetClient = &http.Client{Transport: failingTransport{}}
		cfg.Manual = true
	})
	dir := filepath.Join(c.cfg.StateDir, "rulesets")
	if _, err := rulesets.Install(dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "geosite-ru-blocked.srs"))
	if err != nil || len(before) == 0 {
		t.Fatalf("встроенные списки не установлены: %v", err)
	}
	res, err := c.UpdateRuleSets(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) == 0 || len(res.Updated) != 0 {
		t.Fatalf("итог: %+v", res)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "geosite-ru-blocked.srs"))
	if string(after) != string(before) {
		t.Fatal("файл изменился при сбое сети")
	}
	if len(eventsOf(c, EvRuleSets)) != 1 {
		t.Fatal("нет события обновления списков")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("сеть недоступна (тест)")
}

func TestAppsSettings(t *testing.T) {
	for _, ok := range []string{"", "com.example.app", "org.telegram.messenger, com.v2ray.ang", "a.b"} {
		if err := appsSetting(ok); err != nil {
			t.Errorf("%q должно приниматься: %v", ok, err)
		}
	}
	for _, bad := range []string{"noDots", "com.example app", "com..x", "../etc", "1abc.def"} {
		if err := appsSetting(bad); err == nil {
			t.Errorf("%q должно отвергаться", bad)
		}
	}
	if got := splitApps(" a.b , c.d,,"); len(got) != 2 || got[0] != "a.b" || got[1] != "c.d" {
		t.Errorf("splitApps: %v", got)
	}
	if appsModeSetting("include") != nil || appsModeSetting("exclude") != nil || appsModeSetting("off") != nil || appsModeSetting("x") == nil {
		t.Error("режим приложений")
	}
}

// Имена серверов узлов резолвятся только «local»: иначе в режимах all цепочка WARP → узел с доменом уходит в петлю через remote.
func TestNodeServersDNSRuleFirst(t *testing.T) {
	for _, mode := range []string{ModeBlocked, ModeAll, ModeAllButRU} {
		dir := t.TempDir()
		cfg := Config{StateDir: dir, TUN: TUNConfig{Enabled: true, Mode: mode}}
		if err := prepareTUN(&cfg); err != nil {
			t.Fatal(err)
		}
		ns := newNodeServers(cfg.TUN.NodeServers)
		raw, err := engineConfig(&cfg, "u", "p", nil)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			DNS struct {
				Rules []map[string]any `json:"rules"`
			} `json:"dns"`
			Route struct {
				RuleSet []map[string]any `json:"rule_set"`
			} `json:"route"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.DNS.Rules) == 0 || got.DNS.Rules[0]["server"] != "local" || fmt.Sprint(got.DNS.Rules[0]["rule_set"]) != "["+nodeServersTag+"]" {
			t.Fatalf("%s: первым правилом DNS должно быть node-servers → local: %v", mode, got.DNS.Rules)
		}
		found := false
		for _, rs := range got.Route.RuleSet {
			if rs["tag"] == nodeServersTag && rs["format"] == "source" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: в route.rule_set нет %s", mode, nodeServersTag)
		}
		// IP пропускаем, домен записываем (файл заменяется, служебное имя остаётся)
		if ns.note("203.0.113.5") || ns.note("") {
			t.Fatal("IP-адрес и пустое имя в набор не попадают")
		}
		if !ns.note("game.example.net") || ns.note("game.example.net") {
			t.Fatal("домен добавляется один раз")
		}
		b, _ := os.ReadFile(cfg.TUN.NodeServers)
		if !strings.Contains(string(b), "game.example.net") || !strings.Contains(string(b), nodeServersDummy) {
			t.Fatalf("файл набора: %s", b)
		}
	}
}

// default_mark движок принимает только на Linux: на остальных ОС конфиг с ним не собирается (box.New падает), туннель не включается.
func TestEngineMarksSocketsLinuxOnly(t *testing.T) {
	for goos, want := range map[string]bool{"linux": true, "windows": false, "darwin": false, "android": false} {
		if got := engineMarksSockets(goos); got != want {
			t.Errorf("engineMarksSockets(%q) = %v, want %v", goos, got, want)
		}
	}
}

// Киллсвитч есть в Linux и Windows; по умолчанию включён только в Linux (на Windows — вручную, пока WFP-реализация новая).
func TestKillswitchImplementedAndDefault(t *testing.T) {
	for goos, want := range map[string][2]bool{"linux": {true, true}, "windows": {true, false}, "android": {false, false}, "darwin": {false, false}} {
		if got := killswitchImplemented(goos); got != want[0] {
			t.Errorf("killswitchImplemented(%q) = %v, want %v", goos, got, want[0])
		}
		if got := killswitchDefaultOn(goos); got != want[1] {
			t.Errorf("killswitchDefaultOn(%q) = %v, want %v", goos, got, want[1])
		}
	}
}

// «Быстрый DNS» (FakeIP): только в режиме «всё», только по настройке, не при правилах, которым нужен настоящий адрес назначения.
func TestFakeIPConfig(t *testing.T) {
	servers := func(m cfgMap) []map[string]any {
		var out []map[string]any
		for _, s := range m["dns"].(map[string]any)["servers"].([]any) {
			out = append(out, s.(map[string]any))
		}
		return out
	}
	hasFake := func(m cfgMap) bool {
		for _, s := range servers(m) {
			if s["type"] == "fakeip" {
				return true
			}
		}
		return false
	}
	// по умолчанию выключено
	m, _ := tunCfg(t, TUNConfig{Mode: ModeAll})
	if hasFake(m) || m["experimental"] != nil {
		t.Fatal("без настройки FakeIP быть не должно")
	}
	// включено в режиме «всё»: сервер, правило для A/AAAA после локальных, файл хранилища
	m, _ = tunCfg(t, TUNConfig{Mode: ModeAll, FakeIP: true, FakeIPCache: filepath.Join(t.TempDir(), "fakeip.db")})
	if !hasFake(m) {
		t.Fatal("FakeIP включён, а сервера нет")
	}
	rules := m["dns"].(map[string]any)["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["server"] != "fakeip" || fmt.Sprint(last["query_type"]) != "[A AAAA]" {
		t.Fatalf("последнее правило DNS: %v", last)
	}
	for _, s := range servers(m) {
		if s["type"] == "fakeip" && s["inet6_range"] != nil {
			t.Fatal("IPv6 заблокирован: диапазона fc00::/18 быть не должно")
		}
	}
	if cf, _ := m["experimental"].(map[string]any)["cache_file"].(map[string]any); cf["store_fakeip"] != true {
		t.Fatalf("хранилище FakeIP не включено: %v", m["experimental"])
	}
	m, _ = tunCfg(t, TUNConfig{Mode: ModeAll, FakeIP: true, AllowIPv6: true})
	for _, s := range servers(m) {
		if s["type"] == "fakeip" && s["inet6_range"] == nil {
			t.Fatal("IPv6 разрешён: нужен диапазон")
		}
	}
	m, _ = tunCfg(t, TUNConfig{Mode: ModeAll, FakeIP: true})
	rules2 := m["dns"].(map[string]any)["rules"].([]any)
	if len(rules2) < 2 {
		t.Fatalf("правила DNS: %v", rules2)
	}
	loc := rules2[len(rules2)-2].(map[string]any)
	if loc["server"] != "local" || !strings.Contains(fmt.Sprint(loc["domain_suffix"]), "sslip.io") {
		t.Fatalf("имена с частными адресами должны идти в local перед FakeIP: %v", loc)
	}
	// другие режимы: правила зависят от настоящего адреса
	for _, mode := range []string{ModeBlocked, ModeAllButRU} {
		if m, _ := tunCfg(t, TUNConfig{Mode: mode, FakeIP: true}); hasFake(m) {
			t.Fatalf("режим %s: FakeIP не должен включаться", mode)
		}
	}
	// правило с подсетью отключает FakeIP; доменное и geosite — нет
	ipRule := RouteRule{ID: "ip", Enabled: true, Action: ActionDirect, Match: RouteMatch{IPCIDR: []string{"203.0.113.0/24"}}}
	if m, _ := tunCfg(t, TUNConfig{Mode: ModeAll, FakeIP: true, UserRules: []RouteRule{ipRule}}); hasFake(m) {
		t.Fatal("правило ip_cidr требует настоящий адрес: FakeIP должен отключиться")
	}
	ipRule.Enabled = false
	if m, _ := tunCfg(t, TUNConfig{Mode: ModeAll, FakeIP: true, UserRules: []RouteRule{ipRule}}); !hasFake(m) {
		t.Fatal("выключенное правило не должно мешать")
	}
	if (RouteMatch{DomainSuffix: []string{"a.example"}}).needsRealIP() || (RouteMatch{RuleSet: []string{"geosite-ru"}}).needsRealIP() {
		t.Fatal("доменные условия настоящий адрес не требуют")
	}
	if !(RouteMatch{RuleSet: []string{"geoip-ru"}}).needsRealIP() || !(RouteMatch{IPIsPrivate: true}).needsRealIP() {
		t.Fatal("адресные условия требуют настоящий адрес")
	}
}
