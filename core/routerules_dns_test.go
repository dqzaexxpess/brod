package core

import "testing"

// правило «через выход», состоящее только из доменных условий и списков, получает DNS-правило rule_set → remote.
func TestDNSObjectForRuleSetProxy(t *testing.T) {
	rs := func(a string, m RouteMatch) RouteRule { return RouteRule{ID: "r", Enabled: true, Action: a, Match: m} }

	o := rs(ActionProxy, RouteMatch{RuleSet: []string{"ext-0123abcd"}}).dnsObject()
	if o == nil || o["server"] != "remote" {
		t.Fatalf("список с proxy должен резолвиться через remote: %v", o)
	}
	if got, _ := o["rule_set"].([]string); len(got) != 1 || got[0] != "ext-0123abcd" {
		t.Fatalf("в DNS-правило не попал список: %v", o)
	}

	// домены вместе со списком: условия складываются (И), как и в правиле маршрута
	o = rs(ActionProxy, RouteMatch{DomainSuffix: []string{"example.org"}, RuleSet: []string{"geosite-youtube"}}).dnsObject()
	if o == nil || o["rule_set"] == nil || o["domain_suffix"] == nil || o["server"] != "remote" {
		t.Fatalf("домен и список вместе: %v", o)
	}

	// не строится: прямое и блокировка (по спискам), адресные условия и адресные списки geoip-*, инверсия
	for name, r := range map[string]RouteRule{
		"direct":        rs(ActionDirect, RouteMatch{RuleSet: []string{"ext-0123abcd"}}),
		"block":         rs(ActionBlock, RouteMatch{RuleSet: []string{"ext-0123abcd"}}),
		"порт":          rs(ActionProxy, RouteMatch{RuleSet: []string{"ext-0123abcd"}, Port: []int{443}}),
		"cidr":          rs(ActionProxy, RouteMatch{RuleSet: []string{"ext-0123abcd"}, IPCIDR: []string{"10.0.0.0/8"}}),
		"geoip":         rs(ActionProxy, RouteMatch{RuleSet: []string{"geoip-telegram"}}),
		"geoip+geosite": rs(ActionProxy, RouteMatch{RuleSet: []string{"geosite-youtube", "geoip-telegram"}}),
		"invert":        rs(ActionProxy, RouteMatch{RuleSet: []string{"ext-0123abcd"}, Invert: true}),
	} {
		if r.dnsObject() != nil {
			t.Errorf("%s: DNS-правило не строится", name)
		}
	}
}
