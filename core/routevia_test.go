package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/store"
)

const nodeA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRouteViaValidation(t *testing.T) {
	mk := func(v *RouteVia, action string) RouteRule {
		return RouteRule{Enabled: true, Action: action, Via: v, Match: RouteMatch{Domain: []string{"a.example"}}}
	}
	ok := mk(&RouteVia{Kind: ViaCountry, Country: " nl "}, ActionProxy)
	if err := ok.validate(); err != nil || ok.Via.Country != "NL" {
		t.Fatalf("страна: %v, %+v", err, ok.Via)
	}
	node := mk(&RouteVia{Kind: ViaNode, Node: strings.ToUpper(nodeA), Country: "DE", Fallback: FallbackDirect}, ActionProxy)
	if err := node.validate(); err != nil || node.Via.Node != nodeA || node.Via.Country != "" {
		t.Fatalf("узел: %v, %+v", err, node.Via)
	}
	if r := mk(&RouteVia{Kind: ViaWarp, Node: "x"}, ActionProxy); r.validate() != nil || r.Via.Node != "" {
		t.Fatalf("warp: %+v", r.Via)
	}
	if r := mk(&RouteVia{}, ActionProxy); r.validate() == nil {
		// пустой Kind — «без собственного выхода»? Нет: Via с пустым видом отклоняется, чтобы не терять намерение пользователя
		t.Fatal("пустой вид выхода должен отклоняться")
	}
	for name, r := range map[string]RouteRule{
		"direct с via":   mk(&RouteVia{Kind: ViaWarp}, ActionDirect),
		"block с via":    mk(&RouteVia{Kind: ViaWarp}, ActionBlock),
		"вид":            mk(&RouteVia{Kind: "city"}, ActionProxy),
		"страна":         mk(&RouteVia{Kind: ViaCountry, Country: "NLD"}, ActionProxy),
		"страна пустая":  mk(&RouteVia{Kind: ViaCountry}, ActionProxy),
		"узел":           mk(&RouteVia{Kind: ViaNode, Node: "abc"}, ActionProxy),
		"запасной выбор": mk(&RouteVia{Kind: ViaWarp, Fallback: "drop"}, ActionProxy),
	} {
		if err := r.validate(); err == nil {
			t.Errorf("%s: правило должно отклоняться", name)
		}
	}
}

func TestRouteViaInTUNConfig(t *testing.T) {
	m, _ := tunCfg(t, TUNConfig{Mode: ModeBlocked, UserRules: []RouteRule{
		{ID: "r1", Enabled: true, Action: ActionProxy, Via: &RouteVia{Kind: ViaCountry, Country: "NL"}, Match: RouteMatch{DomainSuffix: []string{"nl.example"}}},
		{ID: "r2", Enabled: true, Action: ActionProxy, Match: RouteMatch{DomainSuffix: []string{"plain.example"}}},
		{ID: "r3", Enabled: false, Action: ActionProxy, Via: &RouteVia{Kind: ViaWarp}, Match: RouteMatch{DomainSuffix: []string{"off.example"}}},
	}})
	rules := routeRules(m)
	viaI := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == RuleTag("r1") })
	plainI := ruleIndex(rules, func(r map[string]any) bool { return r["outbound"] == OutApp && r["domain_suffix"] != nil })
	if viaI < 0 || plainI < 0 || viaI > plainI {
		t.Fatalf("правила: via=%d plain=%d", viaI, plainI)
	}
	switches := map[string]bool{}
	for _, o := range m["outbounds"].([]any) {
		if ob := o.(map[string]any); ob["type"] == "switch" {
			switches[ob["tag"].(string)] = true
		}
	}
	if !switches[RuleTag("r1")] || switches[RuleTag("r2")] || switches[RuleTag("r3")] {
		t.Fatalf("switch'и правил в конфиге: %v (нужен только rt-r1)", switches)
	}
}

// ---- привязка на живом движке ----

func recordAlive(t *testing.T, c *Core, id string, ok bool, at time.Time) {
	t.Helper()
	if err := c.st.Record([]store.Row{{Node: id, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: ok, LatencyMs: 100, At: at}}); err != nil {
		t.Fatal(err)
	}
}

func putExit(t *testing.T, c *Core, id, country string) {
	t.Helper()
	if err := c.st.PutExitInfo(store.ExitInfoRec{Node: id, Country: country, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func bindRule(t *testing.T, c *Core, v *RouteVia) {
	t.Helper()
	r := RouteRule{ID: "r1", Enabled: true, Action: ActionProxy, Via: v, Match: RouteMatch{Domain: []string{"x.example"}}}
	c.cfg.TUN.UserRules = []RouteRule{r}
	if !c.eng.Has(RuleTag("r1")) {
		if err := c.eng.AddSwitch(RuleTag("r1"), "", true); err != nil {
			t.Fatal(err)
		}
	}
	c.bindRouteVia()
}

func switchNode(t *testing.T, c *Core) string {
	t.Helper()
	cur, err := c.eng.SwitchTarget(RuleTag("r1"))
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func TestRouteViaCountryPicksBestAndFailsOver(t *testing.T) {
	c := startCore(t, t.TempDir())
	a, b, d := addNode(t, c, "a"), addNode(t, c, "b"), addNode(t, c, "d")
	now := time.Now()
	for _, id := range []string{a, b} {
		putExit(t, c, id, "NL")
	}
	putExit(t, c, d, "DE")
	recordAlive(t, c, a, true, now.Add(-time.Minute)) // a свежее и значит выше рангом
	recordAlive(t, c, b, true, now.Add(-20*time.Minute))
	recordAlive(t, c, d, true, now)
	bindRule(t, c, &RouteVia{Kind: ViaCountry, Country: "NL"})
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("выбран %q, ожидался лучший узел NL %q", got, NodeTag(a))
	}
	st := c.RouteViaStates()
	if len(st) != 1 || st[0].Target != "node" || st[0].Node != a || st[0].Fallback || st[0].Candidates != 2 {
		t.Fatalf("состояние: %+v", st)
	}
	// пока a годен, привязка не прыгает, даже если у b появился более свежий успех
	recordAlive(t, c, b, true, now)
	c.bindRouteVia()
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("привязка прыгнула на %q без причины", got)
	}
	// a упал: переезд на b, прежний узел освобождается сборщиком мусора
	recordAlive(t, c, a, false, now.Add(time.Second))
	c.bindRouteVia()
	if got := switchNode(t, c); got != NodeTag(b) {
		t.Fatalf("после падения a выбран %q, ожидался %q", got, NodeTag(b))
	}
	c.mu.Lock()
	_, aReg := c.reg[a]
	c.mu.Unlock()
	if aReg {
		t.Fatal("узел a больше никому не нужен, но остался зарегистрированным")
	}
	if ev := lastEvent(t, c, EvRouteVia); !strings.Contains(ev.Detail, `"target":"node"`) || ev.Node != b {
		t.Fatalf("событие: %+v", ev)
	}
	// упали все узлы NL: запасной вариант по умолчанию блокирует (цель снята), узел DE не подходит
	recordAlive(t, c, b, false, now.Add(2*time.Second))
	c.bindRouteVia()
	if got := switchNode(t, c); got != "" {
		t.Fatalf("без подходящих узлов цель должна быть снята, а она %q", got)
	}
	if st := c.RouteViaStates(); st[0].Target != "none" || !st[0].Fallback || st[0].Candidates != 0 {
		t.Fatalf("состояние без узлов: %+v", st[0])
	}
}

func TestRouteViaFallbacksAndKinds(t *testing.T) {
	c := startCore(t, t.TempDir())
	a := addNode(t, c, "a")
	// страна без узлов: direct и proxy (через активный switch)
	bindRule(t, c, &RouteVia{Kind: ViaCountry, Country: "FR", Fallback: FallbackDirect})
	if got := switchNode(t, c); got != OutDirect {
		t.Fatalf("direct-запас: %q", got)
	}
	bindRule(t, c, &RouteVia{Kind: ViaCountry, Country: "FR", Fallback: FallbackProxy})
	if got := switchNode(t, c); got != OutActive {
		t.Fatalf("proxy-запас: %q", got)
	}
	if st := c.RouteViaStates(); st[0].Target != "active" || !st[0].Fallback {
		t.Fatalf("состояние: %+v", st[0])
	}
	// закреплённый узел: привязывается независимо от истории проверок
	bindRule(t, c, &RouteVia{Kind: ViaNode, Node: a})
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("узел: %q", got)
	}
	// узла нет в базе: запасной вариант (block)
	bindRule(t, c, &RouteVia{Kind: ViaNode, Node: nodeA})
	if got := switchNode(t, c); got != "" {
		t.Fatalf("несуществующий узел: цель %q", got)
	}
	// WARP не настроен: запасной вариант
	bindRule(t, c, &RouteVia{Kind: ViaWarp, Fallback: FallbackDirect})
	if got := switchNode(t, c); got != OutDirect {
		t.Fatalf("warp без аккаунта: %q", got)
	}
}

func TestRouteCountries(t *testing.T) {
	c := startCore(t, t.TempDir())
	a, b, d := addNode(t, c, "a"), addNode(t, c, "b"), addNode(t, c, "d")
	putExit(t, c, a, "NL")
	putExit(t, c, b, "nl")
	putExit(t, c, d, "DE")
	putExit(t, c, addNode(t, c, "e"), "")
	recordAlive(t, c, a, true, time.Now())
	got, err := c.RouteCountries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (CountryInfo{Code: "NL", Alive: 1, Total: 2}) || got[1] != (CountryInfo{Code: "DE", Alive: 0, Total: 1}) {
		t.Fatalf("страны: %+v", got)
	}
}

// Узел правила проверяется через его switch; после HealthFails провалов подряд он исключается и привязка уходит на другой узел страны,
// не дожидаясь планового круга проверок.
func TestRouteViaProbeFailsOverQuickly(t *testing.T) {
	var c *Core
	bad := ""
	c = cfgCore(t, func(cfg *Config) {
		cfg.HealthFails = 2
		cfg.Liveness = func(ctx context.Context, p *engine.Probe) prober.Result {
			cur, _ := c.eng.SwitchTarget(RuleTag("r1"))
			if bad != "" && cur == NodeTag(bad) {
				return prober.Result{Level: 2, Class: prober.ClassTimeout}
			}
			return prober.Result{Level: 2, OK: true, LatencyMs: 40}
		}
	})
	a, b := addNode(t, c, "a"), addNode(t, c, "b")
	now := time.Now()
	for _, id := range []string{a, b} {
		putExit(t, c, id, "NL")
	}
	recordAlive(t, c, a, true, now.Add(-time.Minute)) // a выше рангом
	recordAlive(t, c, b, true, now.Add(-20*time.Minute))
	bindRule(t, c, &RouteVia{Kind: ViaCountry, Country: "NL"})
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("начальный выбор %q", got)
	}
	// успешная проверка не меняет привязку
	c.probeRouteVia(context.Background())
	c.bindRouteVia()
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("после успешной проверки привязка %q", got)
	}
	// узел a умер: первая неудача только считается (одиночное мигание не повод менять узел)
	bad = a
	c.probeRouteVia(context.Background())
	c.bindRouteVia()
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("после одного провала привязка ушла на %q", got)
	}
	// вторая подряд: узел исключён, привязка переезжает
	c.probeRouteVia(context.Background())
	c.bindRouteVia()
	if got := switchNode(t, c); got != NodeTag(b) {
		t.Fatalf("после двух провалов привязка %q, ожидался %q", got, NodeTag(b))
	}
	// проверка идёт уже через новый узел и успешна: счётчик сброшен
	c.probeRouteVia(context.Background())
	c.viaMu.Lock()
	fails := c.viaFails["r1"]
	c.viaMu.Unlock()
	if fails != 0 {
		t.Fatalf("счётчик провалов после успеха: %d", fails)
	}
}

// При равном ранге выигрывает узел с меньшей задержкой.
func TestRouteViaPrefersLowerLatency(t *testing.T) {
	c := startCore(t, t.TempDir())
	slow, fast := addNode(t, c, "slow"), addNode(t, c, "fast")
	now := time.Now()
	for _, id := range []string{slow, fast} {
		putExit(t, c, id, "NL")
	}
	row := func(id string, ms int64) {
		if err := c.st.Record([]store.Row{{Node: id, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, LatencyMs: ms, At: now}}); err != nil {
			t.Fatal(err)
		}
	}
	row(slow, 900)
	row(fast, 120)
	cands, err := c.countryCandidates("NL", now)
	if err != nil || len(cands) != 2 || cands[0].ID != fast {
		t.Fatalf("порядок кандидатов: %v, %v (быстрый должен быть первым)", idsOfRecs(cands), err)
	}
}
