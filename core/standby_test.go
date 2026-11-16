package core

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"brod/store"
)

// seedFresh отмечает узлы как недавно проверенные в сети net1 (так выглядит история после планового круга проверок).
func seedFresh(t *testing.T, c *Core, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_ = c.st.Record([]store.Row{{Node: id, NodeClass: "direct/tcp/none", Net: "net1", Level: 2, OK: true, At: time.Now().Add(-2 * time.Minute)}})
	}
}

// Резерв собирается, пока узел здоров: запасные проверены живой проверкой и записаны как запасные активного выбора; активный в них не входит.
func TestStandbyRefreshFillsFallbacks(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	var alts []string
	for i := 0; i < 3; i++ {
		id := addNode(t, c, fmt.Sprint("alt", i))
		fc.ok[id] = true
		alts = append(alts, id)
	}
	dead := addNode(t, c, "dead")
	fc.ok[dead] = false
	seedFresh(t, c, append(alts, dead)...)
	_ = c.Connect(active, "user")

	c.refreshStandby(context.Background())
	sel, _ := c.st.Selection(store.ScopeActive)
	if len(sel.Fallbacks) != 3 {
		t.Fatalf("запасных %d: %v", len(sel.Fallbacks), sel.Fallbacks)
	}
	for _, id := range sel.Fallbacks {
		if id == active || id == dead {
			t.Fatalf("в резерве недопустимый узел: %s", id)
		}
	}
	if got := c.liveStandby(context.Background(), active); len(got) != 3 {
		t.Fatalf("резерв жив, а liveStandby вернул %d", len(got))
	}
	// резерв другого узла, устаревший или собранный при деградации не годится
	if got := c.liveStandby(context.Background(), "другой"); got != nil {
		t.Fatalf("резерв чужого узла использован: %v", got)
	}
	c.cfg.StandbyFresh = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	if got := c.liveStandby(context.Background(), active); got != nil {
		t.Fatalf("устаревший резерв использован: %v", got)
	}
}

// При деградации замена идёт из резерва: полный поиск альтернатив (проверка кандидатов с нуля) не запускается; из мёртвого резерва — тоже,
// затем выполняется обычный поиск.
func TestStandbyUsedOnDegradationWithoutFullSearch(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	var alts []string
	for i := 0; i < 3; i++ {
		id := addNode(t, c, fmt.Sprint("alt", i))
		fc.ok[id] = true
		alts = append(alts, id)
	}
	seedFresh(t, c, alts...)
	_ = c.Connect(active, "user")
	if err := c.SetSetting(SetDegradeAction, "auto", "user"); err != nil {
		t.Fatal(err)
	}
	c.refreshStandby(context.Background())
	fc.mu.Lock()
	fc.calls = map[string]int{} // дальше считаем только проверки, начатые при деградации
	fc.mu.Unlock()

	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "автозамена из резерва", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
	sel, _ := c.st.Selection(store.ScopeActive)
	if sel.Node == active || sel.By != "auto" {
		t.Fatalf("выбор не сменился: %+v", sel)
	}
	fc.mu.Lock()
	total := 0
	for _, n := range fc.calls {
		total += n
	}
	fc.mu.Unlock()
	if total > len(alts) {
		t.Fatalf("быстрый путь должен проверить только резерв (%d), а проверок было %d", len(alts), total)
	}
	ev := eventsOf(c, EvAlternatives)
	if len(ev) != 1 {
		t.Fatalf("событий альтернатив %d", len(ev))
	}
}

// Резерв мёртв (все запасные не отвечают сейчас): быстрый путь ничего не находит, и выполняется обычный поиск с нуля.
func TestStandbyDeadFallsBackToFullSearch(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	old := addNode(t, c, "old")
	fc.ok[old] = true
	seedFresh(t, c, old)
	_ = c.Connect(active, "user")
	_ = c.SetSetting(SetDegradeAction, "auto", "user")
	c.refreshStandby(context.Background())
	fc.ok[old] = false // за время ожидания умер
	fresh := addNode(t, c, "fresh")
	fc.ok[fresh] = true
	seedFresh(t, c, fresh)

	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "автозамена прежним поиском", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
	if sel, _ := c.st.Selection(store.ScopeActive); sel.Node != fresh {
		t.Fatalf("перешли не на живой узел: %s", sel.Node)
	}
}

// Первую минуту после смены узла, пока не было успеха, проверки ускорены; первый успех возвращает обычный ритм.
func TestWarmupProbingUntilFirstSuccess(t *testing.T) {
	lv, fc := &liveness{ok: false}, newFakeCheck()
	var skew atomic.Int64
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load()) * time.Second) }
	})
	active := addNode(t, c, "active")
	_ = c.Connect(active, "user")
	if !c.warming() {
		t.Fatal("сразу после смены узла проверки должны быть ускорены")
	}
	skew.Store(int64(healthWarm/time.Second) + 5)
	if c.warming() {
		t.Fatal("после минуты без успеха ускорение прекращается")
	}
	skew.Store(0)
	lv.set(true)
	_, _ = c.ProbeActive(context.Background())
	if c.warming() {
		t.Fatal("после первого успеха ускорение не нужно")
	}
}

// Выбор пользователя автозамена не перебивает сразу после начала деградации (пауза UserSwapGrace); узел, выбранный автоматикой, — без паузы.
func TestAutoSwapRespectsUserChoiceForGracePeriod(t *testing.T) {
	var skew atomic.Int64
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
		cfg.UserSwapGrace = time.Minute
		cfg.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load()) * time.Second) }
	})
	active := addNode(t, c, "active")
	alt := addNode(t, c, "alt")
	fc.ok[alt] = true
	seedFresh(t, c, alt)
	_ = c.Connect(active, "user")
	_ = c.SetSetting(SetDegradeAction, "auto", "user")
	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "поиск альтернатив", func() bool { return len(eventsOf(c, EvAlternatives)) >= 1 })
	time.Sleep(150 * time.Millisecond)
	if n := len(eventsOf(c, EvAutoSwap)); n != 0 {
		t.Fatalf("выбор пользователя заменён сразу, автопереходов %d", n)
	}
	if sel, _ := c.st.Selection(store.ScopeActive); sel.Node != active || sel.By != "user" {
		t.Fatalf("выбор изменился: %+v", sel)
	}
	// деградация держится дольше паузы: замена разрешена
	skew.Store(int64(altRetry/time.Second) + 70)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "автозамена после паузы", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
}

// После перезапуска ядра резерв в памяти пуст, но запасные активного выбора сохранены в базе: быстрый путь берёт их (с живой проверкой).
func TestLiveStandbyUsesPersistedFallbacksAfterRestart(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	var alts []string
	for i := 0; i < 2; i++ {
		id := addNode(t, c, fmt.Sprint("alt", i))
		fc.ok[id] = true
		alts = append(alts, id)
	}
	dead := addNode(t, c, "dead")
	fc.ok[dead] = false
	seedFresh(t, c, append(alts, dead)...)
	_ = c.Connect(active, "user")
	c.refreshStandby(context.Background())

	c.standby = standbyState{} // как после запуска: память пуста, в базе запасные остались
	got := c.liveStandby(context.Background(), active)
	if len(got) != 2 {
		t.Fatalf("запасных из базы %d, ожидалось 2", len(got))
	}
	fc.ok[alts[0]], fc.ok[alts[1]] = false, false // а мёртвые запасные из базы не проходят
	if got := c.liveStandby(context.Background(), active); len(got) != 0 {
		t.Fatalf("мёртвые запасные использованы: %v", got)
	}
	if got := c.liveStandby(context.Background(), "другой"); got != nil {
		t.Fatalf("запасные чужого выбора использованы: %v", got)
	}
}

// Список живых альтернатив готов раньше, чем кончилась льгота выбора пользователя: замена происходит по окончании льготы, а не ждёт следующего круга поиска.
func TestAutoSwapFiresWhenGraceEnds(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
		cfg.UserSwapGrace = 700 * time.Millisecond
	})
	active := addNode(t, c, "active")
	alt := addNode(t, c, "alt")
	fc.ok[alt] = true
	seedFresh(t, c, alt)
	_ = c.Connect(active, "user")
	_ = c.SetSetting(SetDegradeAction, "auto", "user")
	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "поиск альтернатив", func() bool { return len(eventsOf(c, EvAlternatives)) >= 1 })
	if n := len(eventsOf(c, EvAutoSwap)); n != 0 {
		t.Fatalf("замена до конца льготы: %d", n)
	}
	waitFor(t, "автозамена по таймеру без нового круга", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
}

// Узел, который с момента старта ни разу не ответил (восстановленный мёртвый), заменяется после короткой льготы, а не после полной UserSwapGrace.
func TestAutoSwapShortGraceForNeverAnsweredNode(t *testing.T) {
	old := neverOKGrace
	neverOKGrace = 300 * time.Millisecond
	defer func() { neverOKGrace = old }()
	lv, fc := &liveness{ok: false}, newFakeCheck() // с самого начала молчит
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
		cfg.UserSwapGrace = time.Hour
	})
	active := addNode(t, c, "active")
	alt := addNode(t, c, "alt")
	fc.ok[alt] = true
	seedFresh(t, c, alt)
	_ = c.Connect(active, "user")
	_ = c.SetSetting(SetDegradeAction, "auto", "user")
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "автозамена мёртвого узла после короткой льготы", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
}

// Быстрые кандидаты идут первыми (по возрастанию задержки), медленный (дольше slowCandidateMs) берётся только если быстрых не хватило.
func TestVerifyPrefersFastCandidates(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	slow := addNode(t, c, "slow")
	fastB := addNode(t, c, "fastB")
	fastA := addNode(t, c, "fastA")
	for _, id := range []string{slow, fastB, fastA} {
		fc.ok[id] = true
	}
	fc.lat[slow], fc.lat[fastB], fc.lat[fastA] = 4300, 900, 300
	seedFresh(t, c, slow, fastB, fastA)
	_ = c.Connect(active, "user")
	nodes, _ := c.st.Nodes(store.NodeFilter{IDs: []string{slow, fastB, fastA}})
	order := []store.NodeRec{}
	for _, want := range []string{slow, fastB, fastA} { // очередь: медленный первым
		for _, n := range nodes {
			if n.ID == want {
				order = append(order, n)
			}
		}
	}
	got, _ := c.verify(context.Background(), "net1", order, 2, len(order))
	if len(got) != 2 || got[0].Node.ID != fastA || got[1].Node.ID != fastB {
		t.Fatalf("ожидались fastA, fastB, получено %v", ids(got))
	}
	// нужно три, быстрых только два: медленный добирает
	got, _ = c.verify(context.Background(), "net1", order, 3, len(order))
	if len(got) != 3 || got[2].Node.ID != slow {
		t.Fatalf("медленный должен быть последним: %v", ids(got))
	}
}

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Node.ID[:6]
	}
	return out
}

// Поиск ничего не нашёл (например, сразу после запуска ядра WARP холодный и проверки падают): следующий поиск через altRetryEmpty, а не через altRetry.
func TestEmptyAlternativesRetrySooner(t *testing.T) {
	var skew atomic.Int64
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := cfgCore(t, func(cfg *Config) {
		cfg.Liveness = lv.fn
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.HealthFails = 2
		cfg.UserSwapGrace = -1
		cfg.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load()) * time.Second) }
	})
	active := addNode(t, c, "active")
	alt := addNode(t, c, "alt")
	seedFresh(t, c, alt) // жив будет позже
	_ = c.Connect(active, "user")
	_ = c.SetSetting(SetDegradeAction, "auto", "user")
	lv.set(false)
	ctx := context.Background()
	_, _ = c.ProbeActive(ctx)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "первый (пустой) поиск", func() bool { return len(eventsOf(c, EvAlternatives)) == 1 })
	fc.mu.Lock()
	fc.ok[alt] = true
	fc.mu.Unlock()
	skew.Store(int64(altRetryEmpty/time.Second) + 1)
	_, _ = c.ProbeActive(ctx)
	waitFor(t, "повторный поиск раньше altRetry", func() bool { return len(eventsOf(c, EvAutoSwap)) == 1 })
}

// Поиск, ничего не нашедший (путь через WARP лежит), не стирает сохранённый резерв.
func TestEmptySearchKeepsSavedFallbacks(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	active := addNode(t, c, "active")
	alt := addNode(t, c, "alt")
	fc.ok[alt] = true
	seedFresh(t, c, alt)
	_ = c.Connect(active, "user")
	c.refreshStandby(context.Background())
	if sel, _ := c.st.Selection(store.ScopeActive); len(sel.Fallbacks) != 1 {
		t.Fatalf("резерв не собран: %v", sel.Fallbacks)
	}
	fc.ok[alt] = false
	c.standby = standbyState{}
	_, _ = c.Alternatives(context.Background(), c.cfg.AltCount)
	if sel, _ := c.st.Selection(store.ScopeActive); len(sel.Fallbacks) != 1 {
		t.Fatalf("пустой поиск стёр резерв: %v", sel.Fallbacks)
	}
}
