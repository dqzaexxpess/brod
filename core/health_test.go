package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brod/engine"
	"brod/monitor"
	"brod/prober"
	"brod/store"
)

// liveness — управляемая подмена проверки живого активного узла.
type liveness struct {
	mu sync.Mutex
	ok bool
}

func (l *liveness) set(ok bool) { l.mu.Lock(); l.ok = ok; l.mu.Unlock() }
func (l *liveness) fn(context.Context, *engine.Probe) prober.Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ok {
		return prober.Result{Level: 2, OK: true, LatencyMs: 50}
	}
	return prober.Result{Level: 2, Class: prober.ClassTimeout}
}

func healthCore(t *testing.T, lv *liveness, fc *fakeCheck) *Core {
	return cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
	})
}

func eventsOf(c *Core, kind string) []store.Event {
	evs, _ := c.st.Events(0, 0)
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	waitForD(t, 5*time.Second, what, f)
}

func waitForD(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

// Быстрая проверка «выход поднялся» не влияет на здоровье узла: провалы быстрой проверки не объявляют деградацию, а успех не гасит её.
func TestProbeActiveQuickDoesNotAffectHealth(t *testing.T) {
	lv, fc := &liveness{ok: false}, newFakeCheck()
	var quickOK atomic.Bool
	var quickCalls atomic.Int32
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
		cfg.Quick = func(context.Context, *engine.Probe) prober.Result {
			quickCalls.Add(1)
			return prober.Result{Level: 2, OK: quickOK.Load(), Target: "quick", LatencyMs: 20}
		}
	})
	ctx := context.Background()
	if _, err := c.ProbeActiveQuick(ctx); err == nil {
		t.Fatal("без активного узла быстрая проверка должна отвечать ошибкой")
	}
	active := addNode(t, c, "active")
	_ = c.Connect(active, "user")
	quickOK.Store(false)
	for i := 0; i < 5; i++ { // провалы быстрой проверки подряд
		if r, err := c.ProbeActiveQuick(ctx); err != nil || r.OK {
			t.Fatalf("быстрая проверка: %+v %v", r, err)
		}
	}
	if c.Degraded() || len(eventsOf(c, EvDegraded)) != 0 {
		t.Fatal("провалы быстрой проверки не должны объявлять деградацию")
	}
	quickOK.Store(true)
	if r, err := c.ProbeActiveQuick(ctx); err != nil || !r.OK || quickCalls.Load() != 6 {
		t.Fatalf("успех быстрой проверки: %+v %v вызовов %d", r, err, quickCalls.Load())
	}
	// деградация по полной проверке по-прежнему считается как раньше
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	if !c.Degraded() {
		t.Fatal("два провала полной проверки — деградация")
	}
	_, _ = c.ProbeActiveQuick(ctx) // успешная быстрая проверка деградацию не снимает
	if !c.Degraded() {
		t.Fatal("быстрая проверка не должна снимать деградацию")
	}
}

// P1: «умирающий» активный узел → событие деградации + альтернативы; узел НЕ меняется без пользователя; восстановление — событием.
func TestActiveDegradationReportsButNeverSwitches(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	var alts []string
	for i := 0; i < 5; i++ {
		id := addNode(t, c, fmt.Sprint("alt", i))
		fc.ok[id] = true
		alts = append(alts, id)
	}
	_ = c.Connect(active, "user")
	tag := activeTag(t, c)
	ctx := context.Background()

	if _, err := c.ProbeActive(ctx); err != nil || c.Degraded() {
		t.Fatalf("здоровый узел: %v", err)
	}
	lv.set(false)
	_, _ = c.ProbeActive(ctx)
	if c.Degraded() || len(eventsOf(c, EvDegraded)) != 0 {
		t.Fatal("один провал не должен объявлять деградацию")
	}
	_, _ = c.ProbeActive(ctx)
	if !c.Degraded() {
		t.Fatal("два провала подряд — деградация")
	}
	_, _ = c.ProbeActive(ctx) // повтор в том же эпизоде
	if n := len(eventsOf(c, EvDegraded)); n != 1 {
		t.Fatalf("событий деградации %d, ожидалось 1", n)
	}
	waitFor(t, "альтернативы", func() bool { return len(eventsOf(c, EvAlternatives)) == 1 })
	var d struct {
		Alternatives []string `json:"alternatives"`
	}
	if err := json.Unmarshal([]byte(eventsOf(c, EvAlternatives)[0].Detail), &d); err != nil || len(d.Alternatives) != 3 {
		t.Fatalf("альтернативы: %+v %v", d, err)
	}
	for _, id := range d.Alternatives {
		if id == active {
			t.Fatal("активный узел в альтернативах")
		}
	}
	if sel, _ := c.st.Selection(store.ScopeActive); len(sel.Fallbacks) != 3 || sel.Node != active || sel.By != "user" {
		t.Fatalf("запасные не сохранены или выбор испорчен: %+v", sel)
	}
	// главное правило P1
	if got := activeTag(t, c); got != tag {
		t.Fatalf("активный узел сменился сам: %q", got)
	}
	if n := len(eventsOf(c, EvSwitch)); n != 1 {
		t.Fatalf("смен узла %d (должна быть одна — пользовательская)", n)
	}

	lv.set(true)
	_, _ = c.ProbeActive(ctx)
	if !c.Degraded() {
		t.Fatal("одного успеха мало для восстановления")
	}
	_, _ = c.ProbeActive(ctx)
	if c.Degraded() || len(eventsOf(c, EvRecovered)) != 1 {
		t.Fatalf("восстановление: degraded=%v событий=%d", c.Degraded(), len(eventsOf(c, EvRecovered)))
	}
}

func TestIntermittentFailuresDoNotDegrade(t *testing.T) {
	lv, fc := &liveness{}, newFakeCheck()
	c := healthCore(t, lv, fc)
	_ = c.Connect(addNode(t, c, "a"), "user")
	for i := 0; i < 6; i++ {
		lv.set(i%2 == 1) // провал, успех, провал, успех…
		_, _ = c.ProbeActive(context.Background())
	}
	if c.Degraded() || len(eventsOf(c, EvDegraded)) != 0 {
		t.Fatal("мигание не должно считаться деградацией")
	}
}

// Смена узла сбрасывает отсчёт; запоздавший результат проверки прежнего узла игнорируется.
func TestSwitchResetsHealth(t *testing.T) {
	lv, fc := &liveness{}, newFakeCheck()
	c := healthCore(t, lv, fc)
	a, b := addNode(t, c, "a"), addNode(t, c, "b")
	_ = c.Connect(a, "user")
	_, _ = c.ProbeActive(context.Background())
	_, _ = c.ProbeActive(context.Background())
	if !c.Degraded() {
		t.Fatal("подготовка: деградация")
	}
	_ = c.Connect(b, "user")
	if c.Degraded() {
		t.Fatal("после смены узла деградация должна сброситься")
	}
	c.noteLiveness(a, prober.Result{Class: prober.ClassTimeout}) // запоздавший провал старого узла
	c.noteLiveness(a, prober.Result{Class: prober.ClassTimeout})
	if c.Degraded() {
		t.Fatal("результат прежнего узла повлиял на нового")
	}
}

// Пассивный детектор не объявляет деградацию сам: он запускает подтверждающие проверки. Узел жив — тревога ложная.
func TestPassiveMonitorOnlyTriggersConfirmation(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	_ = c.Connect(addNode(t, c, "a"), "user")
	for i := 0; i < 3; i++ {
		c.onMonitor(monitor.Event{Scope: monitor.ScopeActive, Stalled: 4, Reason: "нет ответа"})
	}
	time.Sleep(300 * time.Millisecond)
	if c.Degraded() || len(eventsOf(c, EvDegraded)) != 0 {
		t.Fatal("ложное срабатывание детектора (узел отвечает) объявило деградацию")
	}
	// узел действительно умер: подтверждение провалилось K раз подряд
	lv.set(false)
	c.onMonitor(monitor.Event{Scope: monitor.ScopeActive, Stalled: 5, Reason: "нет ответа"})
	waitFor(t, "деградация по подтверждённому срабатыванию", func() bool { return len(eventsOf(c, EvDegraded)) == 1 })
	if d := eventsOf(c, EvDegraded)[0].Detail; !json.Valid([]byte(d)) || !strings.Contains(d, "пассивный детектор") {
		t.Fatalf("причина: %s", d)
	}
	c.onMonitor(monitor.Event{Scope: monitor.ScopeActive, Stalled: 6})
	time.Sleep(200 * time.Millisecond)
	if n := len(eventsOf(c, EvDegraded)); n != 1 {
		t.Fatalf("событий деградации %d (в одном эпизоде одно)", n)
	}
}

// Альтернативы: проверяются живьём (кандидат со свежей, но уже устаревшей по факту историей отсеивается), google-api выше.
func TestAlternativesVerifiedLiveAndOrdered(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	now := time.Now()
	ids := map[string]string{}
	for _, k := range []string{"g", "dead", "x", "y", "z"} {
		id := addNode(t, c, k)
		ids[k] = id
		_ = c.st.Record([]store.Row{{Node: id, NodeClass: "direct/tcp/none", Net: "net1", Level: 2, OK: true, At: now.Add(-5 * time.Minute)}})
		fc.ok[id] = k != "dead"
	}
	_ = c.st.Record([]store.Row{{Node: ids["g"], NodeClass: "direct/tcp/none", Net: "net1", Level: 4, Target: "google-api", OK: true, At: now.Add(-5 * time.Minute)}})
	fc.gapi[ids["g"]] = true
	active := addNode(t, c, "active")
	_ = c.Connect(active, "user")

	alts, err := c.Alternatives(context.Background(), 3)
	if err != nil || len(alts) != 3 {
		t.Fatalf("%v %v", alts, err)
	}
	if alts[0].Node.ID != ids["g"] || !alts[0].GoogleAPI {
		t.Fatalf("узел с google-api должен быть первым: %+v", alts[0])
	}
	for _, a := range alts {
		if a.Node.ID == ids["dead"] || a.Node.ID == active {
			t.Fatalf("в альтернативах недопустимый узел: %+v", a.Node)
		}
	}
	// живая проверка записалась: dead теперь с провалом в этой сети
	if h, _ := c.st.Histories("net1", time.Now()); h[ids["dead"]].LastFail.IsZero() {
		t.Fatal("провал живой проверки кандидата не записан")
	}
}

// После смены сети истории нет — альтернативы берутся из непроверенных и проверяются живьём.
func TestAlternativesAfterNetworkChange(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	var netMu sync.Mutex
	net := "net1"
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness, cfg.Check = lv.fn, fc.check
		cfg.NetID = func() string { netMu.Lock(); defer netMu.Unlock(); return net }
	})
	for i := 0; i < 6; i++ {
		id := addNode(t, c, fmt.Sprint("n", i))
		fc.ok[id] = i >= 2
	}
	_ = c.Connect(addNode(t, c, "active"), "user")
	if _, err := c.CheckNow(context.Background(), CheckOptions{Force: true}, "auto"); err != nil {
		t.Fatal(err)
	}
	if c.CheckNetwork() {
		t.Fatal("сеть не менялась")
	}
	netMu.Lock()
	net = "net2"
	netMu.Unlock()
	if !c.CheckNetwork() {
		t.Fatal("смена сети не замечена")
	}
	if c.CheckNetwork() {
		t.Fatal("о смене сообщается один раз")
	}
	ev := eventsOf(c, EvNetChanged)
	if len(ev) != 1 || !json.Valid([]byte(ev[0].Detail)) {
		t.Fatalf("события смены сети: %+v", ev)
	}
	alts, err := c.Alternatives(context.Background(), 3)
	if err != nil || len(alts) != 3 {
		t.Fatalf("в новой сети без истории: %v %v", alts, err)
	}
	if h, _ := c.st.Histories("net2", time.Now()); len(h) == 0 {
		t.Fatal("результаты новой сети должны записываться отдельно")
	}
	if sel, _ := c.st.Selection(store.ScopeActive); sel.By != "user" {
		t.Fatal("смена сети не должна менять выбор")
	}
}

// Включение цепочки через WARP меняет ключ истории, но не сеть: события о смене сети быть не должно, проверки при этом идут заново.
func TestViaWarpToggleIsNotNetworkChange(t *testing.T) {
	tc := newTCheck()
	c := warpCore(t, tc, nil)
	c.CheckNetwork()
	if err := c.SetSetting(SetViaWarp, "1", "user"); err != nil {
		t.Fatal(err)
	}
	if c.CheckNetwork() {
		t.Fatal("переключение цепочки объявлено сменой сети")
	}
	if len(eventsOf(c, EvNetChanged)) != 0 {
		t.Fatal("событие смены сети из-за переключения цепочки")
	}
	// настоящая смена сети при включённой цепочке — событие есть
	cfgNet := "net2"
	c.cfg.NetID = func() string { return cfgNet }
	if !c.CheckNetwork() || len(eventsOf(c, EvNetChanged)) != 1 {
		t.Fatal("реальная смена сети не замечена")
	}
}

// degrade.action=auto: после деградации и подбора альтернатив выбор сам переходит на первую проверенную; смена одна, с событием.
func TestAutoSwapOnDegradation(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	var alts []string
	for i := 0; i < 3; i++ {
		id := addNode(t, c, fmt.Sprint("alt", i))
		fc.ok[id] = true
		alts = append(alts, id)
	}
	_ = c.Connect(active, "user")
	if err := c.SetSetting(SetDegradeAction, "auto", "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting(SetDegradeAction, "nope", "user"); err == nil {
		t.Fatal("неизвестное действие должно отклоняться")
	}
	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "автопереключение", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
	sel, _ := c.st.Selection(store.ScopeActive)
	if sel.Node == active || sel.By != "auto" {
		t.Fatalf("выбор не сменился автоматически: %+v", sel)
	}
	found := false
	for _, id := range alts {
		if id == sel.Node {
			found = true
		}
	}
	if !found {
		t.Fatalf("перешли не на проверенную альтернативу: %s", sel.Node)
	}
	if c.Degraded() {
		t.Fatal("после смены отсчёт здоровья начинается заново")
	}
	if n := len(eventsOf(c, EvAutoSwap)); n != 1 {
		t.Fatalf("автопереходов %d", n)
	}
}

// По умолчанию (notify) активный узел сам не меняется — принцип P1 не нарушен.
func TestNotifyDoesNotAutoSwap(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	id := addNode(t, c, "alt")
	fc.ok[id] = true
	_ = c.Connect(active, "user")
	if c.DegradeAction() != "notify" {
		t.Fatalf("умолчание: %s", c.DegradeAction())
	}
	lv.set(false)
	_, _ = c.ProbeActive(context.Background())
	_, _ = c.ProbeActive(context.Background())
	waitFor(t, "альтернативы", func() bool { return len(eventsOf(c, EvAlternatives)) == 1 })
	time.Sleep(200 * time.Millisecond)
	if sel, _ := c.st.Selection(store.ScopeActive); sel.Node != active || len(eventsOf(c, EvAutoSwap)) != 0 {
		t.Fatalf("в режиме notify узел меняться не должен: %+v", sel)
	}
}

// Деградация, а альтернатив пока нет (проверки шли через тот же сломанный путь): поиск повторяется, и как только живая найдена — автопереход.
// Раньше поиск шёл один раз при входе в деградацию, и пользователь оставался на мёртвом узле без новых попыток.
func TestAlternativesRetriedWhileDegraded(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	var skew atomic.Int64 // секунд сверх реального времени
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
		cfg.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load()) * time.Second) }
	})
	active := addNode(t, c, "active")
	var alts []string
	for i := 0; i < 2; i++ {
		alts = append(alts, addNode(t, c, fmt.Sprint("alt", i))) // пока не живые: fc.ok не заполнен
	}
	_ = c.Connect(active, "user")
	if err := c.SetSetting(SetDegradeAction, "auto", "user"); err != nil {
		t.Fatal(err)
	}
	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "первый поиск альтернатив", func() bool { return len(eventsOf(c, EvAlternatives)) == 1 })
	if n := len(eventsOf(c, EvAutoSwap)); n != 0 {
		t.Fatalf("альтернатив не было, а автопереходов %d", n)
	}
	// живые альтернативы появились; до срока повтора ничего не происходит
	for _, id := range alts {
		fc.ok[id] = true
	}
	_, _ = c.ProbeActive(ctx)
	time.Sleep(200 * time.Millisecond)
	if n := len(eventsOf(c, EvAlternatives)); n != 1 {
		t.Fatalf("повторный поиск раньше срока: %d", n)
	}
	skew.Store(int64(altRetry/time.Second) + 15)
	_, _ = c.ProbeActive(ctx)

	waitFor(t, "автопереход после повторного поиска", func() bool {
		return len(eventsOf(c, EvAutoSwap)) == 1
	})
	sel, _ := c.st.Selection(store.ScopeActive)
	if sel.Node == active || sel.By != "auto" {
		t.Fatalf("выбор не сменился: %+v", sel)
	}
}
