package core

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"brod/killswitch"
	"brod/rulesets"
)

// Режим TUN: весь трафик системы заходит через TUN-интерфейс. Проверен на живой системе (утечек DNS/IPv4 нет, откат чистый).
//
// Режимы маршрутизации (PLAN P6; списки — встроенные rule-set'ы, пакет rulesets):
//   - "blocked" (по умолчанию): через туннель только заблокированное в РФ (домены и IP) плюс YouTube и Telegram; остальное напрямую.
//     DNS заблокированных доменов — DoH через активный выход, остального — локальным резолвером (быстро, и работает без туннеля).
//   - "all": всё через туннель.
//   - "all-but-ru": всё через туннель, российские домены и адреса напрямую.
//
// Во всех режимах банки, Госуслуги и сервисы «только внутри РФ» идут напрямую, частные адреса — напрямую, DNS перехватывается,
// IPv6 по умолчанию блокируется. Вход для приложений (mixed) не зависит от режима: он всегда идёт через dispatch → цели → active.
// Домен, определённый сниффингом, берут из метаданных соединения dispatch и монитор (override_destination в этой сборке не работает),
// поэтому цели работают по доменам и в TUN.
//
// Своё: файлы `rules-direct.txt` и `rules-proxy.txt` в каталоге состояния (суффиксы доменов по одному в строке, «#» — комментарий);
// читаются при старте демона.
type TUNConfig struct {
	Enabled     bool
	Mode        string // blocked (по умолчанию) | all | all-but-ru
	Interface   string // имя интерфейса (по умолчанию brod0, на Windows Brod; то же имя в deploy/NetworkManager-brod.conf)
	Address     string // адрес интерфейса (по умолчанию 10.77.77.1/30; не должен пересекаться с другими VPN: у Throne 172.19.0.0/24, у Docker 172.17.0.0/16)
	MTU         uint32 // по умолчанию 1400
	Stack       string // system | gvisor | mixed (по умолчанию mixed)
	AllowIPv6   bool   // по умолчанию IPv6 блокируется
	NoStrict    bool   // по умолчанию strict_route (отсекает утечки мимо туннеля)
	NoSystemDNS bool   // не направлять DNS системы в TUN (resolvectl); по умолчанию демон делает это сам, иначе DNS уходит на роутер мимо туннеля
	NoAutoRoute bool   // не трогать системные маршруты: интерфейс создаётся, но трафик в него идёт только по явной привязке (curl --interface). Безопасная проверка конвейера рядом с другим VPN
	DNSUpstream string // DoH-сервер, достижимый через активный узел (по умолчанию 1.1.1.1)
	LocalDNS    string // «локальный» резолвер для имён серверов и незаблокированных доменов (по умолчанию — вышестоящий DNS системы, иначе 77.88.8.8)
	RuleSetDir  string // каталог списков (по умолчанию StateDir/rulesets)
	UserDirect  []string
	UserProxy   []string
	UserSets    map[string]string // свои списки по URL: тег → путь файла srs (usersets.go); nil — загрузить при создании ядра
	UserRules   []RouteRule       // правила маршрутизации пользователя (приложение): выше встроенных списков и правил режима
	// Android: VPN только для перечисленных пакетов либо для всех, кроме них (настройки tun.apps_mode / tun.apps).
	IncludePackage []string
	ExcludePackage []string
	// NodeServers — файл набора правил с доменами серверов узлов (см. nodeservers.go); пусто — без правила.
	NodeServers string
	// FakeIP — «Быстрый DNS» (настройка dns.fakeip, эксперимент): в режиме «всё» домены, идущие через туннель, получают синтетические адреса
	// 198.18.0.0/15, настоящий запрос DoH для них не нужен (fakeip.go). FakeIPCache — файл, где адреса хранятся между пересозданиями ядра.
	FakeIP      bool
	FakeIPCache string
}

// Режимы.
const (
	ModeBlocked  = "blocked"
	ModeAll      = "all"
	ModeAllButRU = "all-but-ru"
)

func (t TUNConfig) withDefaults() TUNConfig {
	if t.Mode == "" {
		t.Mode = ModeBlocked
	}
	if t.Interface == "" {
		t.Interface = DefaultTUNInterface()
	}
	if t.Address == "" {
		t.Address = "10.77.77.1/30"
	}
	if t.MTU == 0 {
		t.MTU = 1400
	}
	if t.Stack == "" {
		t.Stack = "mixed"
	}
	if t.DNSUpstream == "" {
		t.DNSUpstream = "1.1.1.1"
	}
	if t.LocalDNS == "" {
		t.LocalDNS = "77.88.8.8"
	}
	return t
}

// DefaultTUNInterface — имя TUN-интерфейса по умолчанию: на Windows «Brod» (так он называется в «Сетевых подключениях»), на Linux brod0
// (то же имя в deploy/NetworkManager-brod.conf).
func DefaultTUNInterface() string {
	if runtime.GOOS == "windows" {
		return "Brod"
	}
	return "brod0"
}

// InboundTUN — тег TUN-входа.
const InboundTUN = "tun-in"

// ValidMode сообщает, известен ли режим.
func ValidMode(m string) bool {
	return m == "" || m == ModeBlocked || m == ModeAll || m == ModeAllButRU
}

// prepareTUN дополняет настройки TUN из окружения: каталог и установка списков, пользовательские списки, вышестоящий DNS системы.
func prepareTUN(c *Config) error {
	if !c.TUN.Enabled {
		return nil
	}
	if c.TUN.RuleSetDir == "" {
		c.TUN.RuleSetDir = filepath.Join(c.StateDir, "rulesets")
	}
	if _, err := rulesets.Install(c.TUN.RuleSetDir); err != nil {
		return err
	}
	if c.TUN.UserDirect == nil {
		c.TUN.UserDirect = readDomainList(filepath.Join(c.StateDir, "rules-direct.txt"))
	}
	if c.TUN.UserProxy == nil {
		c.TUN.UserProxy = readDomainList(filepath.Join(c.StateDir, "rules-proxy.txt"))
	}
	if c.TUN.LocalDNS == "" {
		c.TUN.LocalDNS = SystemDNS()
	}
	if c.TUN.NodeServers == "" {
		c.TUN.NodeServers = nodeServersPath(c.StateDir)
	}
	newNodeServers(c.TUN.NodeServers) // файл набора должен существовать к запуску движка (и к проверке конфига)
	if c.TUN.FakeIP && c.TUN.FakeIPCache == "" {
		c.TUN.FakeIPCache = filepath.Join(c.StateDir, "fakeip.db")
	}
	return nil
}

// readDomainList читает суффиксы доменов (по одному в строке); некорректные строки пропускаются.
func readDomainList(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.ToLower(strings.TrimSpace(sc.Text()))
		if i := strings.Index(l, "#"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		l = strings.TrimPrefix(l, ".")
		if l != "" && !strings.ContainsAny(l, " /:@\\\"'") && strings.Contains(l, ".") {
			out = append(out, l)
		}
	}
	return out
}

// SystemDNS возвращает вышестоящий DNS системы; платформа (Android: на нём нет /etc/resolv.conf) подменяет функцию.
var SystemDNS = systemUpstreamDNS

// systemUpstreamDNS: первый не локальный DNS из вышестоящих настроек systemd-resolved (то, чем система пользовалась до туннеля).
// Заглушка 127.0.0.53 не годится: её upstream мы сами направляем в TUN, получилась бы петля.
func systemUpstreamDNS() string {
	for _, p := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) == 2 && fs[0] == "nameserver" && !strings.HasPrefix(fs[1], "127.") && !strings.Contains(fs[1], ":") {
				f.Close()
				return fs[1]
			}
		}
		f.Close()
	}
	return ""
}

// engineMarksSockets: метка сокетов (SO_MARK, route.default_mark) существует только в Linux; на Windows и macOS sing-box отказывается
// собирать конфиг с этим полем (box.New: `default_mark` is only supported on linux), а сокеты к узлам там обходят TUN через auto_detect_interface.
func engineMarksSockets(goos string) bool { return goos == "linux" }

// applyTUN добавляет в базовый конфиг TUN-вход, перехват DNS, списки и правила маршрута.
func applyTUN(cfg map[string]any, t TUNConfig) {
	t = t.withDefaults()
	cfg["inbounds"] = append(cfg["inbounds"].([]any), map[string]any{
		"type": "tun", "tag": InboundTUN, "interface_name": t.Interface, "address": []string{t.Address}, "mtu": t.MTU,
		"auto_route": !t.NoAutoRoute, "strict_route": !t.NoStrict && !t.NoAutoRoute, "stack": t.Stack,
	})
	if runtime.GOOS == "android" { // include_package / exclude_package движок принимает только на Android
		in := cfg["inbounds"].([]any)[len(cfg["inbounds"].([]any))-1].(map[string]any)
		if len(t.IncludePackage) > 0 {
			in["include_package"] = t.IncludePackage
		} else if len(t.ExcludePackage) > 0 {
			in["exclude_package"] = t.ExcludePackage
		}
	}

	// ---- списки ----
	route := cfg["route"].(map[string]any)
	var ruleSets []any
	if t.RuleSetDir != "" {
		for _, s := range rulesets.Sets {
			ruleSets = append(ruleSets, map[string]any{"type": "local", "tag": s.Name, "format": "binary", "path": rulesets.Path(t.RuleSetDir, s.Name)})
		}
		route["rule_set"] = ruleSets
	}
	for _, tag := range sortedKeys(t.UserSets) { // свои списки: локальные файлы, которые обновляет ядро (usersets.go)
		ruleSets = append(ruleSets, map[string]any{"type": "local", "tag": tag, "format": "binary", "path": t.UserSets[tag]})
		route["rule_set"] = ruleSets
	}
	if t.NodeServers != "" { // имена серверов узлов — только «local» (иначе петля через remote, см. nodeservers.go)
		ruleSets = append(ruleSets, map[string]any{"type": "local", "tag": nodeServersTag, "format": "source", "path": t.NodeServers})
		route["rule_set"] = ruleSets
	}
	direct := rulesets.Names(rulesets.RoleDirect)
	proxy := rulesets.Names(rulesets.RoleProxy)
	ru := rulesets.Names(rulesets.RoleRU)
	if t.RuleSetDir == "" { // без списков (проверка конфига в тестах) остаются только пользовательские правила
		direct, proxy, ru = nil, nil, nil
	}
	only := func(names []string, prefix string) []string { // только доменные списки (для DNS-правил)
		var out []string
		for _, n := range names {
			if strings.HasPrefix(n, prefix) {
				out = append(out, n)
			}
		}
		return out
	}

	// ---- DNS ----
	// «local» — вышестоящий DNS системы по физическому интерфейсу (мимо туннеля): имена серверов узлов/WARP и всё, что не нужно прятать.
	// «remote» — DoH через активный выход. Адреса серверов нельзя резолвить через сам туннель: его без них ещё нет.
	servers := []any{
		map[string]any{"type": "udp", "tag": "local", "server": t.LocalDNS}, // сокеты привязаны к физическому интерфейсу (auto_detect_interface), в TUN не попадают
		map[string]any{"type": "https", "tag": "remote", "server": t.DNSUpstream, "detour": OutActive, "domain_resolver": "local"},
	}
	var dnsRules []any
	if t.NodeServers != "" {
		dnsRules = append(dnsRules, map[string]any{"rule_set": []string{nodeServersTag}, "server": "local"})
	}
	for _, r := range t.UserRules {
		if r.Enabled {
			if o := r.dnsObject(); o != nil {
				dnsRules = append(dnsRules, o)
			}
		}
	}
	final := "remote"
	switch t.Mode {
	case ModeBlocked:
		// заблокированные домены — DoH в туннеле (ответ без подмены провайдера), остальное локально
		final = "local"
		if len(t.UserProxy) > 0 {
			dnsRules = append(dnsRules, map[string]any{"domain_suffix": t.UserProxy, "server": "remote"})
		}
		if names := only(proxy, "geosite-"); len(names) > 0 {
			dnsRules = append(dnsRules, map[string]any{"rule_set": names, "server": "remote"})
		}
	default:
		if len(t.UserDirect) > 0 {
			dnsRules = append(dnsRules, map[string]any{"domain_suffix": t.UserDirect, "server": "local"})
		}
		localSets := only(direct, "geosite-")
		if t.Mode == ModeAllButRU {
			localSets = append(localSets, only(ru, "geosite-")...)
		}
		if len(localSets) > 0 {
			dnsRules = append(dnsRules, map[string]any{"rule_set": localSets, "server": "local"})
		}
	}
	if fakeIPActive(t) {
		// синтетические адреса получают только запросы A и AAAA, дошедшие до «remote» (локальные правила выше уже отобрали свои домены); остальные типы
		// записей идут в DoH. IPv6-диапазона нет, пока IPv6 заблокирован: AAAA получает пустой ответ
		fk := map[string]any{"type": "fakeip", "tag": "fakeip", "inet4_range": "198.18.0.0/15"}
		if t.AllowIPv6 {
			fk["inet6_range"] = "fc00::/18"
		}
		servers = append(servers, fk)
		// имена, которые по построению указывают на частные и локальные адреса: с синтетическим адресом правило ip_is_private (напрямую) уже не сработает
		dnsRules = append(dnsRules, map[string]any{"domain_suffix": fakeIPLocalSuffixes, "server": "local"})
		dnsRules = append(dnsRules, map[string]any{"query_type": []string{"A", "AAAA"}, "server": "fakeip"})
		if t.FakeIPCache != "" {
			cfg["experimental"] = map[string]any{"cache_file": map[string]any{"enabled": true, "path": t.FakeIPCache, "store_fakeip": true}}
		}
	}
	dns := map[string]any{"servers": servers, "final": final}
	if len(dnsRules) > 0 {
		dns["rules"] = dnsRules
	}
	cfg["dns"] = dns

	// ---- маршрут ----
	rules := []any{
		map[string]any{"inbound": []string{InboundApp}, "outbound": OutApp}, // вход приложений: всегда dispatch → цели / active
		map[string]any{"inbound": []string{InboundTUN}, "action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		map[string]any{"ip_is_private": true, "outbound": OutDirect},
	}
	if !t.AllowIPv6 {
		rules = append(rules, map[string]any{"inbound": []string{InboundTUN}, "ip_version": 6, "action": "reject"})
	}
	// правила пользователя: первое совпавшее побеждает; выше встроенных «всегда напрямую» (банки), чтобы пользователь мог переопределить
	needProcess := false
	for _, r := range t.UserRules {
		if !r.Enabled {
			continue
		}
		o := r.routeObject()
		if len(r.Match.RuleSet) > 0 && t.RuleSetDir == "" {
			continue // без списков (проверка конфига в тестах) правила на них не собираем
		}
		// правило на свой список, которого нет (не скачан или удалён), пропускается целиком: без условия оно совпало бы со всем трафиком
		if r.refsMissingSet(t.UserSets) {
			continue
		}
		if r.Match.needsProcess() {
			needProcess = true
		}
		if r.hasVia() {
			cfg["outbounds"] = append(cfg["outbounds"].([]any), map[string]any{"type": "switch", "tag": RuleTag(r.ID), "interrupt_existing": true})
		}
		rules = append(rules, o)
	}
	if needProcess {
		route["find_process"] = true
	}
	if len(direct) > 0 {
		rules = append(rules, map[string]any{"rule_set": direct, "outbound": OutDirect}) // банки, Госуслуги, «только внутри РФ»
	}
	if len(t.UserDirect) > 0 {
		rules = append(rules, map[string]any{"domain_suffix": t.UserDirect, "outbound": OutDirect})
	}
	if len(t.UserProxy) > 0 {
		rules = append(rules, map[string]any{"domain_suffix": t.UserProxy, "outbound": OutApp})
	}
	switch t.Mode {
	case ModeBlocked:
		if len(proxy) > 0 {
			rules = append(rules, map[string]any{"rule_set": proxy, "outbound": OutApp})
		}
		// остальное — напрямую, но через свой dispatch: пользовательская цель для любого домена работает и в этом режиме
		cfg["outbounds"] = append(cfg["outbounds"].([]any), map[string]any{"type": "dispatch", "tag": OutAppTUN, "default": OutDirect})
		route["final"] = OutAppTUN
	case ModeAllButRU:
		if len(ru) > 0 {
			rules = append(rules, map[string]any{"rule_set": ru, "outbound": OutDirect})
		}
	}
	route["rules"] = rules
	route["auto_detect_interface"] = true
	// метка всех сокетов движка: по ней киллсвитч (killswitch/) отличает трафик движка к узлам от утечки приложений мимо туннеля;
	// на маршрутизацию Linux-TUN не влияет (sing-tun использует метки только для auto_redirect и Android)
	// на Android метка невозможна: SO_MARK требует CAP_NET_ADMIN, у приложения его нет (каждый connect падает с EPERM); сокеты защищает
	// VpnService.protect, киллсвитчем служит системная «Постоянный VPN»
	if engineMarksSockets(runtime.GOOS) {
		route["default_mark"] = killswitch.DefaultMark
	}
	route["default_domain_resolver"] = "local" // имена серверов узлов и WARP — локально, не через туннель
}
