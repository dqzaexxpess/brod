package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"brod/rulesets"
	"brod/store"
)

// Правила маршрутизации пользователя (как в Throne): упорядоченный список «условия → действие», первое совпавшее побеждает.
// Работают в режиме VPN (TUN) и стоят выше встроенных списков и правил режима, но ниже защиты от петель (частные адреса напрямую).
// Условия одного правила складываются по «И» (разные виды) и «ИЛИ» (значения внутри вида). Действия: proxy — через выход
// (цели и активный узел), direct — напрямую, block — отбросить. Применяются при запуске движка: после изменения VPN перезапускается
// (supervisor.Reload).

// SetRouteRules — ключ в таблице настроек, где хранится список правил (JSON).
const SetRouteRules = "route.rules"

// Действия правил.
const (
	ActionProxy  = "proxy"
	ActionDirect = "direct"
	ActionBlock  = "block"
)

// RouteMatch — условия правила; пустое поле не проверяется.
type RouteMatch struct {
	Domain        []string `json:"domain,omitempty"`         // точное совпадение
	DomainSuffix  []string `json:"domain_suffix,omitempty"`  // домен и все поддомены
	DomainKeyword []string `json:"domain_keyword,omitempty"` // подстрока
	DomainRegex   []string `json:"domain_regex,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`      // адрес или подсеть
	Port          []int    `json:"port,omitempty"`         // порты назначения
	PortRange     []string `json:"port_range,omitempty"`   // «1000:2000»
	Network       string   `json:"network,omitempty"`      // tcp | udp
	ProcessName   []string `json:"process_name,omitempty"` // имя процесса (определяется по /proc)
	RuleSet       []string `json:"rule_set,omitempty"`     // встроенные списки (rulesets.Sets)

	// Условия, добавленные для паритета с Throne (маршрутизация, этап B).
	IPVersion         int      `json:"ip_version,omitempty"`           // 4 | 6
	Protocol          []string `json:"protocol,omitempty"`             // протокол по sniff: http, tls, quic, dns, bittorrent… (sniff для TUN включён)
	SourceIPCIDR      []string `json:"source_ip_cidr,omitempty"`       // адрес или подсеть источника
	IPIsPrivate       bool     `json:"ip_is_private,omitempty"`        // назначение — частный адрес
	SourceIPIsPrivate bool     `json:"source_ip_is_private,omitempty"` // источник — частный адрес
	SourcePort        []int    `json:"source_port,omitempty"`
	SourcePortRange   []string `json:"source_port_range,omitempty"`
	ProcessPath       []string `json:"process_path,omitempty"` // полный путь исполняемого файла
	ProcessPathRegex  []string `json:"process_path_regex,omitempty"`
	PackageName       []string `json:"package_name,omitempty"` // Android: пакет приложения
	WifiSSID          []string `json:"wifi_ssid,omitempty"`    // имя сети Wi-Fi, к которой подключено устройство
	WifiBSSID         []string `json:"wifi_bssid,omitempty"`   // адрес точки доступа (aa:bb:cc:dd:ee:ff)
	Invert            bool     `json:"invert,omitempty"`       // инвертировать совпадение всего правила (не совпало — применить)
}

// RouteRule — одно правило.
type RouteRule struct {
	ID                string     `json:"id"`
	Title             string     `json:"title,omitempty"`
	Enabled           bool       `json:"enabled"`
	Match             RouteMatch `json:"match"`
	Action            string     `json:"action"`
	BlockMethod       string     `json:"block_method,omitempty"`        // только при block: reject (по умолчанию, отказ клиенту) | drop (молча отбросить)
	TLSFragment       bool       `json:"tls_fragment,omitempty"`        // дробить TLS ClientHello на части (обход DPI по SNI); для proxy и direct
	TLSRecordFragment bool       `json:"tls_record_fragment,omitempty"` // дробить запись TLS на уровне записей; для proxy и direct
	TLSSpoof          string     `json:"tls_spoof,omitempty"`           // подмена SNI (tlsspoof.go): имя сайта, которое увидит DPI в ложном ClientHello; Linux и Windows, для proxy и direct
	TLSSpoofMethod    string     `json:"tls_spoof_method,omitempty"`    // wrong-sequence (по умолчанию) | wrong-checksum | wrong-ack | wrong-md5 | wrong-timestamp
	Via               *RouteVia  `json:"via,omitempty"`                 // выход правила при action=proxy: узел, страна или WARP (routevia.go); пусто — общий диспетчер
}

const (
	maxRouteRules = 200
	maxListItems  = 500
)

var processNameRe = regexp.MustCompile(`^[^/\x00]{1,64}$`)

func (m RouteMatch) empty() bool {
	return len(m.Domain)+len(m.DomainSuffix)+len(m.DomainKeyword)+len(m.DomainRegex)+len(m.IPCIDR)+len(m.Port)+len(m.PortRange)+len(m.ProcessName)+len(m.RuleSet)+
		len(m.Protocol)+len(m.SourceIPCIDR)+len(m.SourcePort)+len(m.SourcePortRange)+len(m.ProcessPath)+len(m.ProcessPathRegex)+len(m.PackageName)+len(m.WifiSSID)+len(m.WifiBSSID) == 0 &&
		m.Network == "" && m.IPVersion == 0 && !m.IPIsPrivate && !m.SourceIPIsPrivate
}

// refsMissingSet: правило ссылается на свой список (ext-…), которого нет среди определённых.
func (r RouteRule) refsMissingSet(defined map[string]string) bool {
	for _, s := range r.Match.RuleSet {
		if userSetRe.MatchString(s) {
			if _, ok := defined[s]; !ok {
				return true
			}
		}
	}
	return false
}

// needsProcess: условия по процессу (движку нужен find_process).
func (m RouteMatch) needsProcess() bool {
	return len(m.ProcessName)+len(m.ProcessPath)+len(m.ProcessPathRegex)+len(m.PackageName) > 0
}

// onlyDomain: правило целиком про домены (для DNS-правил).
func (m RouteMatch) onlyDomain() bool {
	return len(m.Domain)+len(m.DomainSuffix)+len(m.DomainKeyword)+len(m.DomainRegex) > 0 &&
		len(m.IPCIDR)+len(m.Port)+len(m.PortRange)+len(m.ProcessName)+len(m.RuleSet)+len(m.Protocol)+len(m.SourceIPCIDR)+len(m.SourcePort)+len(m.SourcePortRange)+
			len(m.ProcessPath)+len(m.ProcessPathRegex)+len(m.PackageName)+len(m.WifiSSID)+len(m.WifiBSSID) == 0 &&
		m.Network == "" && m.IPVersion == 0 && !m.IPIsPrivate && !m.SourceIPIsPrivate && !m.Invert
}

// onlyDomainOrSets: правило целиком про домены и списки (rule_set), без адресных и прочих условий и без инверсии.
func (m RouteMatch) onlyDomainOrSets() bool {
	return len(m.RuleSet) > 0 &&
		len(m.IPCIDR)+len(m.Port)+len(m.PortRange)+len(m.ProcessName)+len(m.Protocol)+len(m.SourceIPCIDR)+len(m.SourcePort)+len(m.SourcePortRange)+
			len(m.ProcessPath)+len(m.ProcessPathRegex)+len(m.PackageName)+len(m.WifiSSID)+len(m.WifiBSSID) == 0 &&
		m.Network == "" && m.IPVersion == 0 && !m.IPIsPrivate && !m.SourceIPIsPrivate && !m.Invert
}

func cleanList(in []string, f func(string) (string, error)) ([]string, error) {
	if len(in) > maxListItems {
		return nil, fmt.Errorf("слишком много значений (максимум %d)", maxListItems)
	}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		v, err := f(s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func portRangeOf(s string) (string, error) {
	a, b, ok := strings.Cut(s, ":")
	lo, e1 := strconv.Atoi(a)
	hi, e2 := strconv.Atoi(b)
	if !ok || e1 != nil || e2 != nil || lo < 1 || hi > 65535 || lo > hi {
		return "", fmt.Errorf("недопустимый диапазон портов %q (нужно «1000:2000»)", s)
	}
	return s, nil
}

func checkPorts(ports []int, what string) error {
	if len(ports) > maxListItems {
		return fmt.Errorf("слишком много портов")
	}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("%s %d вне диапазона 1–65535", what, p)
		}
	}
	return nil
}

func cidrOf(s string) (string, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked().String(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", fmt.Errorf("недопустимый адрес или подсеть %q", s)
	}
	return netip.PrefixFrom(a, a.BitLen()).String(), nil
}

// sniffProtocols — протоколы, которые движок определяет по sniff (условие protocol).
var sniffProtocols = map[string]bool{"http": true, "tls": true, "quic": true, "stun": true, "dns": true, "bittorrent": true, "dtls": true, "ssh": true, "rdp": true, "ntp": true}

func domainOf(s string) (string, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " /:@\\\"'*") || !strings.Contains(s, ".") && !strings.EqualFold(s, "localhost") {
		return "", fmt.Errorf("недопустимый домен %q", s)
	}
	return s, nil
}

// validate нормализует правило и проверяет его.
func (r *RouteRule) validate() error {
	r.Title = strings.TrimSpace(r.Title)
	if len(r.Title) > 80 {
		return fmt.Errorf("название длиннее 80 символов")
	}
	switch r.Action {
	case ActionProxy, ActionDirect, ActionBlock:
	default:
		return fmt.Errorf("действие %q: нужно proxy, direct или block", r.Action)
	}
	if err := r.Via.validate(r.Action); err != nil {
		return err
	}
	if err := r.validateTLSSpoof(); err != nil {
		return err
	}
	if (r.TLSFragment || r.TLSRecordFragment) && r.Action == ActionBlock {
		return fmt.Errorf("дробление TLS задаётся только для действий proxy и direct")
	}
	switch r.BlockMethod {
	case "", "reject":
		r.BlockMethod = ""
	case "drop":
		if r.Action != ActionBlock {
			return fmt.Errorf("способ блокировки задаётся только для действия block")
		}
	default:
		return fmt.Errorf("способ блокировки %q: нужно reject или drop", r.BlockMethod)
	}
	var err error
	m := &r.Match
	if m.Domain, err = cleanList(m.Domain, domainOf); err != nil {
		return err
	}
	if m.DomainSuffix, err = cleanList(m.DomainSuffix, domainOf); err != nil {
		return err
	}
	if m.DomainKeyword, err = cleanList(m.DomainKeyword, func(s string) (string, error) {
		s = strings.ToLower(s)
		if len(s) > 100 || strings.ContainsAny(s, " /\\") {
			return "", fmt.Errorf("недопустимое ключевое слово %q", s)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.DomainRegex, err = cleanList(m.DomainRegex, func(s string) (string, error) {
		if len(s) > 200 {
			return "", fmt.Errorf("регулярное выражение длиннее 200 символов")
		}
		if _, err := regexp.Compile(s); err != nil {
			return "", fmt.Errorf("регулярное выражение %q: %v", s, err)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.IPCIDR, err = cleanList(m.IPCIDR, cidrOf); err != nil {
		return err
	}
	if err = checkPorts(m.Port, "порт"); err != nil {
		return err
	}
	if m.PortRange, err = cleanList(m.PortRange, portRangeOf); err != nil {
		return err
	}
	switch m.Network = strings.ToLower(strings.TrimSpace(m.Network)); m.Network {
	case "", "tcp", "udp":
	default:
		return fmt.Errorf("сеть %q: нужно tcp или udp", m.Network)
	}
	if m.ProcessName, err = cleanList(m.ProcessName, func(s string) (string, error) {
		if !processNameRe.MatchString(s) {
			return "", fmt.Errorf("недопустимое имя процесса %q", s)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.IPVersion != 0 && m.IPVersion != 4 && m.IPVersion != 6 {
		return fmt.Errorf("версия IP %d: нужно 4 или 6", m.IPVersion)
	}
	if m.Protocol, err = cleanList(m.Protocol, func(s string) (string, error) {
		s = strings.ToLower(s)
		if !sniffProtocols[s] {
			return "", fmt.Errorf("неизвестный протокол %q (http, tls, quic, stun, dns, bittorrent, dtls, ssh, rdp, ntp)", s)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.SourceIPCIDR, err = cleanList(m.SourceIPCIDR, cidrOf); err != nil {
		return err
	}
	if err = checkPorts(m.SourcePort, "порт источника"); err != nil {
		return err
	}
	if m.SourcePortRange, err = cleanList(m.SourcePortRange, portRangeOf); err != nil {
		return err
	}
	if m.ProcessPath, err = cleanList(m.ProcessPath, func(s string) (string, error) {
		if len(s) > 512 || strings.ContainsRune(s, 0) {
			return "", fmt.Errorf("недопустимый путь процесса")
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.ProcessPathRegex, err = cleanList(m.ProcessPathRegex, func(s string) (string, error) {
		if len(s) > 200 {
			return "", fmt.Errorf("регулярное выражение пути длиннее 200 символов")
		}
		if _, err := regexp.Compile(s); err != nil {
			return "", fmt.Errorf("регулярное выражение пути %q: %v", s, err)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.PackageName, err = cleanList(m.PackageName, func(s string) (string, error) {
		if !packageRe.MatchString(s) {
			return "", fmt.Errorf("недопустимое имя пакета %q", s)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.WifiSSID, err = cleanList(m.WifiSSID, func(s string) (string, error) {
		if s == "" || len(s) > 32 || strings.ContainsRune(s, 0) { // SSID — до 32 байт
			return "", fmt.Errorf("недопустимое имя сети Wi-Fi %q", s)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.WifiBSSID, err = cleanList(m.WifiBSSID, func(s string) (string, error) {
		hw, herr := net.ParseMAC(s)
		if herr != nil || len(hw) != 6 {
			return "", fmt.Errorf("недопустимый адрес точки доступа %q (нужен вид aa:bb:cc:dd:ee:ff)", s)
		}
		return strings.ToLower(hw.String()), nil
	}); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, s := range rulesets.Sets {
		known[s.Name] = true
	}
	if m.RuleSet, err = cleanList(m.RuleSet, func(s string) (string, error) {
		if !known[s] && !userSetRe.MatchString(s) { // встроенный или свой (ext-…; существует ли он, проверяет сборка конфига)
			return "", fmt.Errorf("неизвестный список %q", s)
		}
		return s, nil
	}); err != nil {
		return err
	}
	if m.empty() {
		return fmt.Errorf("у правила нет условий")
	}
	return nil
}

// RouteRules возвращает сохранённые правила.
func (c *Core) RouteRules() ([]RouteRule, error) {
	return loadRouteRules(c.st)
}

func loadRouteRules(st *store.Store) ([]RouteRule, error) {
	v, ok, err := st.Setting(SetRouteRules)
	if err != nil || !ok || v == "" {
		return nil, err
	}
	var rs []RouteRule
	if err := json.Unmarshal([]byte(v), &rs); err != nil {
		return nil, fmt.Errorf("сохранённые правила повреждены: %w", err)
	}
	return rs, nil
}

// PutRouteRules проверяет и сохраняет весь список (порядок = приоритет). Применение — перезапуском VPN (supervisor.Reload).
func (c *Core) PutRouteRules(rs []RouteRule, initiator string) error {
	if len(rs) > maxRouteRules {
		return fmt.Errorf("%w: правил больше %d", ErrInvalid, maxRouteRules)
	}
	seen := map[string]bool{}
	for i := range rs {
		if rs[i].ID == "" {
			rs[i].ID = newRuleID()
		}
		if seen[rs[i].ID] {
			return fmt.Errorf("%w: правило %d: повторяющийся id", ErrInvalid, i+1)
		}
		seen[rs[i].ID] = true
		if err := rs[i].validate(); err != nil {
			return fmt.Errorf("%w: правило %d: %v", ErrInvalid, i+1, err)
		}
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		b = []byte("[]")
	}
	if err := c.st.SetSetting(SetRouteRules, string(b)); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: fmt.Sprintf("%s: %d правил", SetRouteRules, len(rs))})
	return nil
}

// RuleSetInfo — встроенный список, доступный в условиях правил.
type RuleSetInfo struct {
	Name string
	Role string
}

// BuiltinRuleSets перечисляет встроенные списки.
func BuiltinRuleSets() []RuleSetInfo {
	out := make([]RuleSetInfo, len(rulesets.Sets))
	for i, s := range rulesets.Sets {
		out[i] = RuleSetInfo{Name: s.Name, Role: s.Role}
	}
	return out
}

func newRuleID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// routeObject превращает правило в правило маршрута движка.
func (r RouteRule) routeObject() map[string]any {
	m := r.Match
	o := map[string]any{}
	set := func(k string, v any, n int) {
		if n > 0 {
			o[k] = v
		}
	}
	set("domain", m.Domain, len(m.Domain))
	set("domain_suffix", m.DomainSuffix, len(m.DomainSuffix))
	set("domain_keyword", m.DomainKeyword, len(m.DomainKeyword))
	set("domain_regex", m.DomainRegex, len(m.DomainRegex))
	set("ip_cidr", m.IPCIDR, len(m.IPCIDR))
	set("port", m.Port, len(m.Port))
	set("port_range", m.PortRange, len(m.PortRange))
	set("process_name", m.ProcessName, len(m.ProcessName))
	set("rule_set", m.RuleSet, len(m.RuleSet))
	if m.Network != "" {
		o["network"] = m.Network
	}
	set("protocol", m.Protocol, len(m.Protocol))
	set("source_ip_cidr", m.SourceIPCIDR, len(m.SourceIPCIDR))
	set("source_port", m.SourcePort, len(m.SourcePort))
	set("source_port_range", m.SourcePortRange, len(m.SourcePortRange))
	set("process_path", m.ProcessPath, len(m.ProcessPath))
	set("process_path_regex", m.ProcessPathRegex, len(m.ProcessPathRegex))
	set("package_name", m.PackageName, len(m.PackageName))
	set("wifi_ssid", m.WifiSSID, len(m.WifiSSID))
	set("wifi_bssid", m.WifiBSSID, len(m.WifiBSSID))
	if m.IPVersion != 0 {
		o["ip_version"] = m.IPVersion
	}
	if m.IPIsPrivate {
		o["ip_is_private"] = true
	}
	if m.SourceIPIsPrivate {
		o["source_ip_is_private"] = true
	}
	if m.Invert {
		o["invert"] = true
	}
	if r.TLSFragment {
		o["tls_fragment"] = true
	}
	if r.TLSRecordFragment {
		o["tls_record_fragment"] = true
	}
	if r.TLSSpoof != "" {
		o["tls_spoof"] = r.TLSSpoof
		o["tls_spoof_method"] = r.tlsSpoofMethod()
	}
	switch r.Action {
	case ActionProxy:
		o["outbound"] = OutApp
		if r.hasVia() {
			o["outbound"] = RuleTag(r.ID)
		}
	case ActionDirect:
		o["outbound"] = OutDirect
	case ActionBlock:
		o["action"] = "reject"
		if r.BlockMethod == "drop" {
			o["method"] = "drop"
		}
	}
	return o
}

// dnsObject — DNS-правило для чисто доменного правила: proxy → резолвить в туннеле (remote), direct → локально.
func (r RouteRule) dnsObject() map[string]any {
	m := r.Match
	if len(m.RuleSet) > 0 {
		// правило из доменных условий и списков с действием proxy: имена из списка резолвим в туннеле, а не у провайдера.
		// Адресные списки (geoip-*) в DNS-правило не годятся; у своих списков (ext-…) содержимое неизвестно, берём как есть.
		if r.Action != ActionProxy || !m.onlyDomainOrSets() {
			return nil
		}
		for _, n := range m.RuleSet {
			if strings.HasPrefix(n, "geoip-") {
				return nil
			}
		}
	} else if !m.onlyDomain() || r.Action == ActionBlock {
		return nil
	}
	o := map[string]any{"server": "local"}
	if r.Action == ActionProxy {
		o["server"] = "remote"
	}
	if len(m.Domain) > 0 {
		o["domain"] = m.Domain
	}
	if len(m.DomainSuffix) > 0 {
		o["domain_suffix"] = m.DomainSuffix
	}
	if len(m.DomainKeyword) > 0 {
		o["domain_keyword"] = m.DomainKeyword
	}
	if len(m.DomainRegex) > 0 {
		o["domain_regex"] = m.DomainRegex
	}
	if len(m.RuleSet) > 0 {
		o["rule_set"] = m.RuleSet
	}
	return o
}
