package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"brod/ext/dispatch"
	"brod/ext/exttest"
	switchout "brod/ext/switch"
)

func newRig(t *testing.T) *exttest.Rig {
	return exttest.New(t, dispatch.RegisterOutbound, switchout.RegisterOutbound)
}

func add(t *testing.T, r *exttest.Rig, tag string, o dispatch.Options) *dispatch.Dispatch {
	t.Helper()
	if err := r.Create(tag, dispatch.Type, &o); err != nil {
		t.Fatal(err)
	}
	d, _ := r.Mgr.Outbound(tag)
	return d.(*dispatch.Dispatch)
}

func dial(t *testing.T, o adapter.Outbound, dest string) string {
	t.Helper()
	name, c := exttest.DialName(t, o, dest)
	c.Close()
	return name
}

func TestRoutingByDomain(t *testing.T) {
	r := newRig(t)
	for _, tag := range []string{"active", "vidsite", "other", "thirdsite"} {
		r.Fake(t, tag)
	}
	d := add(t, r, "app", dispatch.Options{Default: "active"})
	err := d.SetRules([]dispatch.Rule{
		{Target: "vidsite", DomainKeyword: []string{"vidsite"}, DomainSuffix: []string{"hdvidsite.ac"}},
		{Target: "other", Domain: []string{"Otherhost.NET."}, DomainSuffix: []string{"otherhost.cc"}},
		{Target: "thirdsite", DomainSuffix: []string{".thirdsite.top"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"vidsite.example:443":        "vidsite",   // ключевое слово
		"HDVidsite.ac:443":           "vidsite",   // регистр, сам домен суффикса
		"a.b.hdvidsite.ac:443":       "vidsite",   // поддомен
		"nothdvidsite.ac:443":        "vidsite",   // суффикс по границе метки не совпал, но ключевое слово «vidsite» совпало
		"otherhost.net:443":          "other",     // точный, ведущий регистр и точка в конце правила
		"cdn.otherhost.net:443":      "active",    // точный домен поддомены не берёт
		"x.otherhost.cc:443":         "other",     // суффикс
		"thirdsite.top:443":          "thirdsite", // суффикс с ведущей точкой, сам домен
		"api.thirdsite.top:443":      "thirdsite",
		"example.org:443":            "active",
		"93.184.216.34:443":          "active", // IP — без доменных правил
		"evilthirdsite.top.com:443":  "active",
		"thirdsite.top.evil.com:443": "active",
	}
	for dest, want := range cases {
		if got := dial(t, mustOut(t, r, "app"), dest); got != want {
			t.Errorf("%s → %s, ожидали %s", dest, got, want)
		}
	}
	if m := d.Match("api.thirdsite.top"); m != "thirdsite" {
		t.Fatalf("Match: %q", m)
	}
	if m := d.Match("example.org"); m != "" {
		t.Fatalf("Match по умолчанию: %q", m)
	}
}

func mustOut(t *testing.T, r *exttest.Rig, tag string) adapter.Outbound {
	t.Helper()
	o, ok := r.Mgr.Outbound(tag)
	if !ok {
		t.Fatal(tag)
	}
	return o
}

// Порядок значим: побеждает первое правило.
func TestFirstRuleWins(t *testing.T) {
	r := newRig(t)
	r.Fake(t, "active")
	r.Fake(t, "a")
	r.Fake(t, "b")
	d := add(t, r, "app", dispatch.Options{Default: "active"})
	if err := d.SetRules([]dispatch.Rule{{Target: "a", DomainSuffix: []string{"x.com"}}, {Target: "b", DomainSuffix: []string{"x.com"}}}); err != nil {
		t.Fatal(err)
	}
	if got := dial(t, mustOut(t, r, "app"), "www.x.com:80"); got != "a" {
		t.Fatalf("первое правило должно побеждать, ушло в %s", got)
	}
}

func TestSetRulesValidationAndAtomicity(t *testing.T) {
	r := newRig(t)
	r.Fake(t, "active")
	r.Fake(t, "a")
	d := add(t, r, "app", dispatch.Options{Default: "active"})
	good := []dispatch.Rule{{Target: "a", Domain: []string{"x.com"}}}
	if err := d.SetRules(good); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]dispatch.Rule{
		"нет цели":      {{Target: "нет", Domain: []string{"y.com"}}},
		"пустая цель":   {{Domain: []string{"y.com"}}},
		"без условий":   {{Target: "a"}},
		"сам на себя":   {{Target: "app", Domain: []string{"y.com"}}},
		"вторая плохая": {{Target: "a", Domain: []string{"y.com"}}, {Target: "нет", Domain: []string{"z.com"}}},
	}
	for name, rules := range bad {
		if err := d.SetRules(rules); err == nil {
			t.Errorf("%s: ожидали ошибку", name)
		}
		if got := d.Rules(); len(got) != 1 || got[0].Target != "a" {
			t.Fatalf("%s: прежние правила изменились: %+v", name, got)
		}
	}
	if err := d.SetRules(nil); err != nil || len(d.Rules()) != 0 {
		t.Fatalf("очистка правил: %v %v", err, d.Rules())
	}
}

// Менеджер не даёт удалить умолчание и цели правил; после снятия правила цель удаляется.
func TestDependenciesProtectTargets(t *testing.T) {
	r := newRig(t)
	r.Fake(t, "active")
	r.Fake(t, "a")
	r.Fake(t, "b")
	d := add(t, r, "app", dispatch.Options{Default: "active"})
	_ = d.SetRules([]dispatch.Rule{{Target: "a", Domain: []string{"x.com"}}})
	for _, tag := range []string{"active", "a"} {
		if err := r.Mgr.Remove(tag); err == nil || !strings.Contains(err.Error(), "used by app") {
			t.Fatalf("Remove(%s) должен отказывать, получили %v", tag, err)
		}
	}
	if err := r.Mgr.Remove("b"); err != nil {
		t.Fatal(err)
	}
	_ = d.SetRules(nil)
	if err := r.Mgr.Remove("a"); err != nil {
		t.Fatalf("после снятия правила цель удаляется: %v", err)
	}
}

func TestOptionsValidationAndStaticRules(t *testing.T) {
	r := newRig(t)
	if err := r.Create("d1", dispatch.Type, &dispatch.Options{}); err == nil {
		t.Fatal("без default")
	}
	if err := r.Create("d2", dispatch.Type, &dispatch.Options{Default: "d2"}); err == nil {
		t.Fatal("default на самого себя")
	}
	// статический конфиг: правила и умолчание описаны раньше своих целей — разрешаются на стадии Start
	d := add(t, r, "app", dispatch.Options{Default: "late-active", Rules: []dispatch.Rule{{Target: "late", Domain: []string{"x.com"}}}})
	if _, err := d.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.org:80")); err == nil {
		t.Fatal("без default дозвон обязан отказывать")
	}
	if err := d.Start(adapter.StartStateStart, nil); err == nil {
		t.Fatal("цели ещё нет — старт должен падать")
	}
	r.Fake(t, "late-active")
	r.Fake(t, "late")
	if err := d.Start(adapter.StartStateStart, nil); err != nil {
		t.Fatal(err)
	}
	if got := dial(t, d, "x.com:80"); got != "late" {
		t.Fatalf("статическое правило: %s", got)
	}
	if got := dial(t, d, "other.com:80"); got != "late-active" {
		t.Fatalf("умолчание: %s", got)
	}
}

// Цель-switch: правила работают поверх переключаемых целей, смена цели switch видна сразу.
func TestOverSwitch(t *testing.T) {
	r := newRig(t)
	r.Fake(t, "n1")
	r.Fake(t, "n2")
	r.Fake(t, "active")
	if err := r.Create("t-vidsite", switchout.Type, &switchout.Options{Target: "n1"}); err != nil {
		t.Fatal(err)
	}
	d := add(t, r, "app", dispatch.Options{Default: "active"})
	if err := d.SetRules([]dispatch.Rule{{Target: "t-vidsite", DomainKeyword: []string{"vidsite"}}}); err != nil {
		t.Fatal(err)
	}
	if got := dial(t, d, "hdvidsite.ac:443"); got != "n1" {
		t.Fatalf("%s", got)
	}
	sw, _ := r.Mgr.Outbound("t-vidsite")
	if err := sw.(*switchout.Switch).SetTarget("n2"); err != nil {
		t.Fatal(err)
	}
	if got := dial(t, d, "hdvidsite.ac:443"); got != "n2" {
		t.Fatalf("после переключения цели: %s", got)
	}
	if err := r.Mgr.Remove("n2"); err == nil {
		t.Fatal("узел n2 — цель switch t-vidsite, на который смотрит app: удаление обязано отказывать")
	}
}

// TUN: назначение — IP, домен известен только из метаданных соединения (сниффинг) — цель всё равно выбирается по домену.
func TestRoutingByDomainFromMetadata(t *testing.T) {
	r := newRig(t)
	for _, tag := range []string{"active", "vidsite"} {
		r.Fake(t, tag)
	}
	d := add(t, r, "app", dispatch.Options{Default: "active"})
	if err := d.SetRules([]dispatch.Rule{{Target: "vidsite", DomainSuffix: []string{"hdvidsite.ac"}}}); err != nil {
		t.Fatal(err)
	}
	dial := func(domain string) string {
		ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Domain: domain})
		c, err := d.DialContext(ctx, "tcp", M.ParseSocksaddr("93.184.216.34:443"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		buf := make([]byte, 32)
		n, _ := c.Read(buf)
		return string(buf[:n])
	}
	if got := dial("www.hdvidsite.ac"); got != "vidsite" {
		t.Fatalf("сниффленный домен не учтён: %s", got)
	}
	if got := dial("example.org"); got != "active" {
		t.Fatalf("чужой домен: %s", got)
	}
	if got := dial(""); got != "active" {
		t.Fatalf("без домена: %s", got)
	}
}
