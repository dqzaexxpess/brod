package rulesets

import (
	"bytes"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"strings"

	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/option"
)

// Проверка состава списка при приёме (вариант «на клиенте»). Источник чужой и без подписи, а список с ролью «напрямую»
// управляет маршрутом: одно правило «совпадает со всем» уводит трафик мимо туннеля. Поэтому по белому списку допускается только то,
// что реально выдают встроенные списки: geosite-* и доменные свои — домены; geoip-* и адресные свои — подсети. Файл с чем-либо
// ещё (логические правила, invert, порты, сеть, процессы, смесь доменов и адресов, слишком широкие подсети) отклоняется целиком:
// молча вырезать правила нельзя, список потерял бы смысл незаметно.

const (
	kindDomain  = "domain"
	kindAddress = "address"

	// Самые широкие допустимые подсети. Реальные списки: IPv4 не шире /13, IPv6 не шире /29.
	minBitsV4 = 8
	minBitsV6 = 16
)

// probeDomains — типичные имена: условие, совпавшее со всеми, — «весь трафик».
var probeDomains = []string{"example.com", "www.google.com", "a.b.c.d.example.net", "xn--probe.invalid", "localhost"}

// ValidateSet — Validate плюс проверка состава встроенного списка по имени: geosite-* только доменные условия, geoip-* только подсети.
func ValidateSet(b []byte, name string) error {
	want := ""
	switch {
	case strings.HasPrefix(name, "geosite-"):
		want = kindDomain
	case strings.HasPrefix(name, "geoip-"):
		want = kindAddress
	default:
		return fmt.Errorf("список %s: неизвестное семейство имени (ожидалось geosite-* или geoip-*)", name)
	}
	if _, err := checkComposition(b, want); err != nil {
		return fmt.Errorf("список %s: %w", name, err)
	}
	return nil
}

// ValidateUser — Validate плюс проверка состава своего списка: только доменные или только адресные условия (вид определяется по содержимому).
func ValidateUser(b []byte) error {
	_, err := checkComposition(b, "")
	if err != nil {
		return fmt.Errorf("список не принят: %w", err)
	}
	return nil
}

// KindOf возвращает вид списка: «domain» или «address» (после тех же проверок состава, что у ValidateUser).
func KindOf(b []byte) (string, error) { return checkComposition(b, "") }

// KindName — вид списка по-русски («доменные»/«адресные») для сообщений.
func KindName(k string) string { return kindName(k) }

// checkComposition разбирает srs (после проверки пределов Validate) и возвращает вид списка. want пустой — вид любой, но один на весь файл.
func checkComposition(b []byte, want string) (string, error) {
	if err := Validate(b); err != nil {
		return "", err
	}
	rs, err := srs.Read(bytes.NewReader(b), false)
	if err != nil {
		return "", fmt.Errorf("не rule-set формата srs: %w", err)
	}
	if len(rs.Options.Rules) == 0 {
		return "", fmt.Errorf("в списке нет правил")
	}
	kind := ""
	for i, r := range rs.Options.Rules {
		k, err := checkRule(r)
		if err != nil {
			return "", fmt.Errorf("правило %d: %w", i+1, err)
		}
		if kind != "" && k != kind {
			return "", fmt.Errorf("в одном файле и доменные, и адресные условия (правило %d); нужен список одного вида", i+1)
		}
		kind = k
	}
	if want != "" && kind != want {
		return "", fmt.Errorf("%s условия там, где нужны %s", kindName(kind), kindName(want))
	}
	return kind, nil
}

func kindName(k string) string {
	if k == kindAddress {
		return "адресные"
	}
	return "доменные"
}

// checkRule допускает только простое правило: домены (domain, domain_suffix, domain_keyword, domain_regex) либо подсети (ip_cidr).
func checkRule(r option.HeadlessRule) (string, error) {
	if r.Type != "" && r.Type != "default" {
		return "", fmt.Errorf("логические правила (and/or) не допускаются")
	}
	d := r.DefaultOptions
	if d.Invert {
		return "", fmt.Errorf("инверсия (invert) не допускается")
	}
	rest := d // всё, кроме разрешённого, должно быть пустым
	rest.DomainMatcher, rest.IPSet = nil, nil
	rest.Domain, rest.DomainSuffix, rest.DomainKeyword, rest.DomainRegex, rest.IPCIDR = nil, nil, nil, nil, nil
	if !reflect.DeepEqual(rest, option.DefaultHeadlessRule{}) {
		return "", fmt.Errorf("посторонние условия (разрешены только домены или подсети ip_cidr)")
	}
	isDomain := d.DomainMatcher != nil || len(d.Domain)+len(d.DomainSuffix)+len(d.DomainKeyword)+len(d.DomainRegex) > 0
	isAddr := d.IPSet != nil || len(d.IPCIDR) > 0
	switch {
	case isDomain && isAddr:
		return "", fmt.Errorf("домены и подсети в одном правиле")
	case isDomain:
		return kindDomain, checkDomains(d)
	case isAddr:
		return kindAddress, checkAddresses(d)
	}
	return "", fmt.Errorf("правило без условий")
}

func checkDomains(d option.DefaultHeadlessRule) error {
	if m := d.DomainMatcher; m != nil {
		dom, suf := m.Dump()
		for _, s := range append(dom, suf...) {
			if strings.Trim(s, ".") == "" {
				return fmt.Errorf("пустое доменное условие (совпало бы со всем)")
			}
		}
		if matchesAll(m.Match) {
			return fmt.Errorf("доменное условие совпадает с любым именем")
		}
	}
	for _, s := range append(append([]string(nil), d.Domain...), d.DomainSuffix...) {
		if strings.Trim(s, ".") == "" {
			return fmt.Errorf("пустое доменное условие (совпало бы со всем)")
		}
	}
	for _, s := range d.DomainKeyword {
		if s == "" {
			return fmt.Errorf("пустое domain_keyword (совпало бы со всем)")
		}
	}
	for _, s := range d.DomainRegex {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("domain_regex не компилируется: %v", err)
		}
		if re.MatchString("") || matchesAll(re.MatchString) {
			return fmt.Errorf("domain_regex %q совпадает с любым именем", s)
		}
	}
	return nil
}

func matchesAll(match func(string) bool) bool {
	for _, p := range probeDomains {
		if !match(p) {
			return false
		}
	}
	return true
}

func checkAddresses(d option.DefaultHeadlessRule) error {
	var prefixes []netip.Prefix
	if d.IPSet != nil {
		prefixes = d.IPSet.Prefixes()
	}
	for _, s := range d.IPCIDR {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("ip_cidr %q: %v", s, err)
		}
		prefixes = append(prefixes, p)
	}
	if len(prefixes) == 0 {
		return fmt.Errorf("пустой набор подсетей")
	}
	for _, p := range prefixes {
		bits, floor := p.Bits(), minBitsV6
		switch {
		case p.Addr().Is4():
			floor = minBitsV4
		case p.Addr().Is4In6(): // ::ffff:a.b.c.d/N — тот же IPv4
			bits, floor = bits-96, minBitsV4
		}
		if bits < floor {
			return fmt.Errorf("подсеть %s шире допустимой (не короче /%d для IPv4, /%d для IPv6)", p, minBitsV4, minBitsV6)
		}
	}
	return nil
}
