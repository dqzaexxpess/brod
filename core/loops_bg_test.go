package core

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/warp"
)

// T-8: фоновые циклы ruleSetLoop, routeViaLoop, warpAutoLoop. Периоды подменяются пакетными переменными (как warpFastEvery);
// loopWait только верхняя граница ожидания на случай поломки, а не часть сценария.

const loopWait = 10 * time.Second

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(loopWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runLoop запускает цикл в горутине; stop отменяет контекст и ждёт выхода цикла.
func runLoop(t *testing.T, f func(context.Context)) (stop func(), done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() { f(ctx); close(d) }()
	var once atomic.Bool
	stop = func() {
		if once.Swap(true) {
			return
		}
		cancel()
		select {
		case <-d:
		case <-time.After(loopWait):
			t.Error("цикл не вышел после отмены контекста")
		}
	}
	t.Cleanup(stop)
	return stop, d
}

func setPeriods(t *testing.T, soon, every time.Duration) {
	t.Helper()
	s, e := ruleSetSoon, ruleSetEvery
	ruleSetSoon, ruleSetEvery = soon, every
	t.Cleanup(func() { ruleSetSoon, ruleSetEvery = s, e })
}

func countEvents(c *Core, kind string) int {
	evs, _ := c.st.LastEvents(500)
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// ruleSetCore — ядро с клиентом без сети: проход обновления заканчивается без ошибки (провалы наборов считаются в итоге).
func ruleSetCore(t *testing.T) *Core {
	return cfgCore(t, func(cfg *Config) { cfg.RuleSetClient = &http.Client{Transport: failingTransport{}} })
}

// Первый проход по расписанию, повтор, ошибка прохода не убивает цикл, остановка по отмене.
func TestRuleSetLoopFirstRepeatErrorAndStop(t *testing.T) {
	setPeriods(t, 5*time.Millisecond, 15*time.Millisecond)
	c := ruleSetCore(t)
	// каталог списков занят обычным файлом: Install падает, проход заканчивается ошибкой
	dir := filepath.Join(t.TempDir(), "rulesets")
	if err := os.WriteFile(dir, []byte("файл вместо каталога"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.cfg.TUN.RuleSetDir = dir
	stop, _ := runLoop(t, c.ruleSetLoop)

	eventually(t, "ошибка первого прохода", func() bool { return countEvents(c, EvError) >= 1 })
	eventually(t, "повторный проход после ошибки", func() bool { return countEvents(c, EvError) >= 2 })
	if n := countEvents(c, EvRuleSets); n != 0 {
		t.Fatalf("при ошибке события об обновлении быть не должно: %d", n)
	}
	if _, ok, _ := c.st.Setting(loopLastKey("rulesets")); ok {
		t.Fatal("неудачный проход записан как выполненный")
	}
	// ошибка исчезла: цикл жив, проходы идут успешно
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	eventually(t, "успешный проход", func() bool { return countEvents(c, EvRuleSets) >= 1 })
	eventually(t, "повторный успешный проход", func() bool { return countEvents(c, EvRuleSets) >= 2 })
	if _, ok, _ := c.st.Setting(loopLastKey("rulesets")); !ok {
		t.Fatal("успешный проход не запомнен")
	}
	stop()
	n := countEvents(c, EvRuleSets) + countEvents(c, EvError)
	time.Sleep(60 * time.Millisecond)
	if m := countEvents(c, EvRuleSets) + countEvents(c, EvError); m != n {
		t.Fatalf("после остановки проходы продолжаются: %d -> %d", n, m)
	}
}

// Расписание учитывает прошлый проход: свежий проход откладывает первый запуск на остаток периода.
func TestRuleSetLoopHonoursLastRun(t *testing.T) {
	setPeriods(t, 5*time.Millisecond, time.Hour)
	c := ruleSetCore(t)
	c.markLoopRun("rulesets")
	_, done := runLoop(t, c.ruleSetLoop)
	select {
	case <-done:
		t.Fatal("цикл завершился сам")
	case <-time.After(150 * time.Millisecond):
	}
	if n := countEvents(c, EvRuleSets) + countEvents(c, EvError); n != 0 {
		t.Fatalf("проход выполнен раньше срока: %d", n)
	}
}

// Отмена во время ожидания первого прохода: цикл выходит и ничего не обновляет.
func TestRuleSetLoopCancelBeforeFirstPass(t *testing.T) {
	setPeriods(t, time.Hour, time.Hour)
	c := ruleSetCore(t)
	stop, _ := runLoop(t, c.ruleSetLoop)
	stop()
	if n := countEvents(c, EvRuleSets) + countEvents(c, EvError); n != 0 {
		t.Fatalf("проходов %d", n)
	}
}

func setRouteViaFirst(t *testing.T, d time.Duration) {
	t.Helper()
	old := routeViaFirst
	routeViaFirst = d
	t.Cleanup(func() { routeViaFirst = old })
}

// routeViaLoop: первый проход, повтор по RouteViaInterval, провалы проверок не останавливают цикл и приводят к переезду привязки,
// отмена останавливает.
func TestRouteViaLoopProbesRepeatsFailsOverAndStops(t *testing.T) {
	setRouteViaFirst(t, time.Millisecond)
	var c *Core
	var bad atomic.Value
	bad.Store("")
	var probes atomic.Int32
	c = cfgCore(t, func(cfg *Config) {
		cfg.HealthFails = 2
		cfg.RouteViaInterval = 10 * time.Millisecond
		cfg.Liveness = func(ctx context.Context, p *engine.Probe) prober.Result {
			probes.Add(1)
			cur, _ := c.eng.SwitchTarget(RuleTag("r1"))
			if b := bad.Load().(string); b != "" && cur == NodeTag(b) {
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
	recordAlive(t, c, a, true, now.Add(-time.Minute))
	recordAlive(t, c, b, true, now.Add(-20*time.Minute))
	bindRule(t, c, &RouteVia{Kind: ViaCountry, Country: "NL"})
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("начальный выбор %q", got)
	}
	stop, _ := runLoop(t, c.routeViaLoop)
	eventually(t, "первый проход", func() bool { return probes.Load() >= 1 })
	eventually(t, "повторный проход", func() bool { return probes.Load() >= 3 })
	if got := switchNode(t, c); got != NodeTag(a) {
		t.Fatalf("здоровый узел сменён: %q", got)
	}
	bad.Store(a)
	eventually(t, "переезд привязки на b", func() bool { return switchNode(t, c) == NodeTag(b) })
	n := probes.Load()
	eventually(t, "проходы продолжаются после провалов", func() bool { return probes.Load() >= n+2 })
	stop()
	m := probes.Load()
	time.Sleep(60 * time.Millisecond)
	if probes.Load() != m {
		t.Fatalf("после остановки проверки идут: %d -> %d", m, probes.Load())
	}
}

// Без правил с выходом цикл ничего не проверяет, но живёт и останавливается.
func TestRouteViaLoopNoRulesIdle(t *testing.T) {
	setRouteViaFirst(t, time.Millisecond)
	var probes atomic.Int32
	c := cfgCore(t, func(cfg *Config) {
		cfg.RouteViaInterval = 5 * time.Millisecond
		cfg.Liveness = func(context.Context, *engine.Probe) prober.Result { probes.Add(1); return prober.Result{OK: true} }
	})
	stop, done := runLoop(t, c.routeViaLoop)
	time.Sleep(60 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("цикл вышел сам")
	default:
	}
	stop()
	if probes.Load() != 0 {
		t.Fatalf("проверок без правил: %d", probes.Load())
	}
}

// ---- warpAutoLoop ----

type warpAutoRig struct {
	c       *Core
	net     atomic.Value // string
	scans   atomic.Int32
	applied atomic.Int32
}

func newWarpAutoRig(t *testing.T) *warpAutoRig {
	t.Helper()
	old := warpAutoTick
	warpAutoTick = 5 * time.Millisecond
	t.Cleanup(func() { warpAutoTick = old })
	g := warpVerifyGap
	warpVerifyGap = 0
	t.Cleanup(func() { warpVerifyGap = g })
	r := &warpAutoRig{}
	r.net.Store("cell:1")
	r.c = newCore(t, t.TempDir())
	t.Cleanup(func() { r.c.Close() })
	r.c.warpOn, r.c.warpAcc = true, &warp.Account{AddrV4: "172.16.0.2"}
	r.c.cfg.NetID = func() string { return r.net.Load().(string) }
	r.c.cfg.WarpScanProbe = func(context.Context, WarpProfile) prober.Result {
		r.scans.Add(1)
		return prober.Result{Class: prober.ClassTimeout}
	}
	r.c.cfg.OnWarpProfile = func() { r.applied.Add(1) }
	return r
}

func (r *warpAutoRig) quiet(t *testing.T, what string) {
	t.Helper()
	time.Sleep(80 * time.Millisecond)
	if r.scans.Load() != 0 || r.applied.Load() != 0 {
		t.Fatalf("%s: подбор=%d, применений=%d", what, r.scans.Load(), r.applied.Load())
	}
}

// Подбор запускается, когда WARP проверен и не отвечает; в пределах warpAutoEvery на сеть повторно не запускается; смена сети даёт
// новый подбор; отмена останавливает.
func TestWarpAutoLoopScansOncePerNetwork(t *testing.T) {
	r := newWarpAutoRig(t)
	c := r.c
	stop, _ := runLoop(t, c.warpAutoLoop)
	r.quiet(t, "WARP ещё не проверялся")
	c.warpChecked.Store(true)
	c.warpLastOK.Store(true)
	r.quiet(t, "WARP отвечает")
	c.warpLastOK.Store(false)
	eventually(t, "подбор для первой сети", func() bool { return r.scans.Load() > 0 })
	eventually(t, "запись о попытке", func() bool { return !c.warpTriedAt("cell:1").IsZero() })
	eventually(t, "конец первого подбора", func() bool {
		c.wsMu.Lock()
		defer c.wsMu.Unlock()
		return !c.wsRunning
	})
	n := r.scans.Load()
	time.Sleep(80 * time.Millisecond)
	if r.scans.Load() != n {
		t.Fatalf("повторный подбор в пределах %s: %d -> %d", warpAutoEvery, n, r.scans.Load())
	}
	r.net.Store("cell:2")
	eventually(t, "подбор для второй сети", func() bool { return r.scans.Load() > n })
	stop()
	m := r.scans.Load()
	r.net.Store("cell:3")
	time.Sleep(60 * time.Millisecond)
	if r.scans.Load() != m {
		t.Fatal("после остановки подбор продолжается")
	}
}

// Ошибка подбора цикл не убивает: после смены сети подбор идёт снова.
func TestWarpAutoLoopSurvivesScanError(t *testing.T) {
	r := newWarpAutoRig(t)
	c := r.c
	c.warpChecked.Store(true)
	c.wsMu.Lock()
	c.wsRunning = true // WarpScan вернёт ErrBusy
	c.wsMu.Unlock()
	runLoop(t, c.warpAutoLoop)
	eventually(t, "попытка с ошибкой", func() bool { return !c.warpTriedAt("cell:1").IsZero() })
	if r.scans.Load() != 0 {
		t.Fatal("проба вызвана при занятом подборе")
	}
	c.wsMu.Lock()
	c.wsRunning = false
	c.wsMu.Unlock()
	r.net.Store("cell:2")
	eventually(t, "подбор после ошибки", func() bool { return r.scans.Load() > 0 })
}

// warp.auto=0 и непригодная сеть: цикл ничего не делает.
func TestWarpAutoLoopDisabledAndUnusableNet(t *testing.T) {
	r := newWarpAutoRig(t)
	c := r.c
	c.warpChecked.Store(true)
	if err := c.st.SetSetting(SetWarpAuto, "0"); err != nil {
		t.Fatal(err)
	}
	runLoop(t, c.warpAutoLoop)
	r.quiet(t, "warp.auto выключен")
	_ = c.st.SetSetting(SetWarpAuto, "1")
	for _, n := range []string{"", "offline", "unknown"} {
		r.net.Store(n)
		r.quiet(t, "нет пригодной сети "+n)
	}
}

// Для сети уже подобран другой профиль: цикл применяет его (владельцу ядра сообщается один раз) и выходит: ядро будет пересоздано.
func TestWarpAutoLoopAppliesRememberedAndExits(t *testing.T) {
	r := newWarpAutoRig(t)
	c := r.c
	want := WarpProfile{Endpoint: "188.114.97.9", Port: 987, Mask: "quic"}
	if err := c.rememberWarpProfile(want); err != nil {
		t.Fatal(err)
	}
	_, done := runLoop(t, c.warpAutoLoop)
	select {
	case <-done:
	case <-time.After(loopWait):
		t.Fatal("цикл не вышел после применения профиля")
	}
	eventually(t, "уведомление владельца ядра", func() bool { return r.applied.Load() == 1 })
	if v, _, _ := c.st.Setting(SetWarpEndpoint); v != want.Endpoint {
		t.Fatalf("профиль не записан: %q", v)
	}
	if r.scans.Load() != 0 {
		t.Fatal("при запомненном профиле подбор не нужен")
	}
}

// Запомненный профиль совпадает с действующим: цикл живёт и ничего не меняет.
func TestWarpAutoLoopRememberedSameKeepsRunning(t *testing.T) {
	r := newWarpAutoRig(t)
	c := r.c
	if err := c.rememberWarpProfile(c.profileOf()); err != nil {
		t.Fatal(err)
	}
	c.warpChecked.Store(true)
	_, done := runLoop(t, c.warpAutoLoop)
	r.quiet(t, "профиль уже действует")
	select {
	case <-done:
		t.Fatal("цикл вышел")
	default:
	}
}
