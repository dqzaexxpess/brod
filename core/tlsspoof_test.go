package core

import (
	"strings"
	"testing"
)

func TestTLSSpoofValidationAndRouteObject(t *testing.T) {
	if !TLSSpoofSupported() {
		t.Skip("подмена SNI только на Linux и Windows")
	}
	base := RouteRule{ID: "a", Enabled: true, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"discord.com"}}}

	r := base
	r.TLSSpoof = "  WWW.Example.COM "
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if r.TLSSpoof != "www.example.com" {
		t.Fatalf("имя приводится к нижнему регистру: %q", r.TLSSpoof)
	}
	o := r.routeObject()
	if o["tls_spoof"] != "www.example.com" || o["tls_spoof_method"] != "wrong-sequence" {
		t.Fatalf("объект маршрута: %v", o)
	}

	r = base
	r.TLSSpoof, r.TLSSpoofMethod = "www.example.com", "wrong-checksum"
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if r.routeObject()["tls_spoof_method"] != "wrong-checksum" {
		t.Fatal("способ не передан")
	}

	for name, mut := range map[string]func(*RouteRule){
		"не имя":             func(r *RouteRule) { r.TLSSpoof = "not a host" },
		"без точки":          func(r *RouteRule) { r.TLSSpoof = "localhost" },
		"адрес":              func(r *RouteRule) { r.TLSSpoof = "1.2.3.4" },
		"неизвестный способ": func(r *RouteRule) { r.TLSSpoof, r.TLSSpoofMethod = "www.example.com", "magic" },
		"способ без имени":   func(r *RouteRule) { r.TLSSpoofMethod = "wrong-ack" },
		"block":              func(r *RouteRule) { r.Action, r.TLSSpoof = ActionBlock, "www.example.com" },
	} {
		r := base
		mut(&r)
		if err := r.validate(); err == nil {
			t.Errorf("%s: правило должно отклоняться", name)
		}
	}
	if (RouteRule{}).routeObject()["tls_spoof"] != nil {
		t.Fatal("без подмены ключей быть не должно")
	}
}

// Конфиг с подменой SNI проходит проверку движка (на Linux и Windows).
func TestTLSSpoofConfigPassesEngineCheck(t *testing.T) {
	if !TLSSpoofSupported() {
		t.Skip("подмена SNI только на Linux и Windows")
	}
	r := RouteRule{ID: "s", Enabled: true, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"discord.com"}}, TLSSpoof: "www.cloudflare.com"}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	_, raw := tunCfg(t, TUNConfig{Mode: ModeAll, UserRules: []RouteRule{r}})
	if !strings.Contains(raw, `"tls_spoof":"www.cloudflare.com"`) {
		t.Fatalf("в конфиге нет подмены: %s", raw)
	}
}
