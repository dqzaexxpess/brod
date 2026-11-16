package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brod/aggregator"
	"brod/engine"
	"brod/prober"
	"brod/store"
)

func cfgCore(t *testing.T, mutate func(*Config)) *Core {
	t.Helper()
	cfg := Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}, UserSwapGrace: -1}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// fakeCheck: узлы из ok проходят L2 (и google-api, если он в gapi), остальные нет.
type fakeCheck struct {
	mu    sync.Mutex
	ok    map[string]bool
	gapi  map[string]bool
	lat   map[string]int64 // задержка L2 по узлам (по умолчанию 120 мс)
	calls map[string]int
	delay time.Duration
	cur   atomic.Int32
	peak  atomic.Int32
}

func newFakeCheck() *fakeCheck {
	return &fakeCheck{ok: map[string]bool{}, gapi: map[string]bool{}, lat: map[string]int64{}, calls: map[string]int{}}
}

func (f *fakeCheck) check(ctx context.Context, n store.NodeRec, _ *engine.Probe) []prober.Result {
	cur := f.cur.Add(1)
	for {
		p := f.peak.Load()
		if cur <= p || f.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	defer f.cur.Add(-1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	f.calls[n.ID]++
	ok, g, lat := f.ok[n.ID], f.gapi[n.ID], f.lat[n.ID]
	f.mu.Unlock()
	if lat == 0 {
		lat = 120
	}
	if ctx.Err() != nil {
		return []prober.Result{{Level: 2, Class: prober.ClassCanceled}}
	}
	if !ok {
		return []prober.Result{{Level: 2, Class: prober.ClassTimeout}}
	}
	res := []prober.Result{{Level: 2, Target: "gstatic", OK: true, LatencyMs: lat}}
	res = append(res, prober.Result{Level: 4, Target: "google-api", OK: g})
	return res
}

func (f *fakeCheck) callsOf(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func TestCheckRecordsResultsAndSkipsFresh(t *testing.T) {
	fc := newFakeCheck()
	c := cfgCore(t, func(cfg *Config) { cfg.Check = fc.check; cfg.NetID = func() string { return "net1" } })
	var ids []string
	for i := 0; i < 6; i++ {
		id := addNode(t, c, fmt.Sprint("n", i))
		ids = append(ids, id)
		fc.ok[id] = i%2 == 0
		fc.gapi[id] = i == 0
	}
	rep, err := c.CheckNow(context.Background(), CheckOptions{}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Picked != 6 || rep.Checked != 6 || rep.OK != 3 || rep.GoogleOK != 1 {
		t.Fatalf("отчёт: %+v", rep)
	}
	h, _ := c.st.Histories("net1", time.Now())
	if h[ids[0]].L2OK.IsZero() || !h[ids[1]].L2OK.IsZero() || h[ids[1]].LastFail.IsZero() {
		t.Fatalf("история: %+v", h)
	}
	if other, _ := c.st.Histories("другая-сеть", time.Now()); len(other) != 0 {
		t.Fatal("результаты привязаны к сети: в чужой сети истории быть не должно")
	}
	// второй круг: свежие успехи (CheckTTL) и недавние провалы (RetryAfter) пропускаются
	rep2, _ := c.CheckNow(context.Background(), CheckOptions{}, "auto")
	if rep2.Picked != 0 {
		t.Fatalf("повторный круг перепроверяет: %+v", rep2)
	}
	for _, id := range ids {
		if fc.callsOf(id) != 1 {
			t.Fatalf("узел проверен %d раз", fc.callsOf(id))
		}
	}
	// принудительно — все
	if rep3, _ := c.CheckNow(context.Background(), CheckOptions{Force: true}, "user"); rep3.Picked != 6 {
		t.Fatalf("force: %+v", rep3)
	}
}

func TestCheckPrioritizesFavoritesAndHistory(t *testing.T) {
	fc := newFakeCheck()
	var order []string
	var mu sync.Mutex
	c := cfgCore(t, func(cfg *Config) {
		cfg.CheckParallel = 1 // порядок = порядок отбора
		cfg.Check = func(ctx context.Context, n store.NodeRec, p *engine.Probe) []prober.Result {
			mu.Lock()
			order = append(order, n.ID)
			mu.Unlock()
			return fc.check(ctx, n, p)
		}
		cfg.NetID = func() string { return "net1" }
	})
	plain, fav, proven := addNode(t, c, "plain"), addNode(t, c, "fav"), addNode(t, c, "proven")
	_ = c.SetFavorite(fav, true, "user")
	// proven проверялся успешно давно (поэтому не свежий), plain без истории
	_ = c.st.Record([]store.Row{{Node: proven, NodeClass: "direct/tcp/none", Net: "net1", Level: 2, OK: true, At: time.Now().Add(-3 * time.Hour)}})
	if _, err := c.CheckNow(context.Background(), CheckOptions{}, "user"); err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != fav || order[1] != proven || order[2] != plain {
		t.Fatalf("порядок проверки: %v (fav=%s proven=%s plain=%s)", order, short(fav), short(proven), short(plain))
	}
}

func TestCheckBatchLimitAndParallelism(t *testing.T) {
	fc := newFakeCheck()
	fc.delay = 80 * time.Millisecond
	c := cfgCore(t, func(cfg *Config) { cfg.Check = fc.check; cfg.CheckParallel = 3; cfg.CheckBatch = 7 })
	for i := 0; i < 20; i++ {
		addNode(t, c, fmt.Sprint("n", i))
	}
	rep, err := c.CheckNow(context.Background(), CheckOptions{}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Picked != 7 || rep.Checked != 7 {
		t.Fatalf("партия: %+v", rep)
	}
	if peak := fc.peak.Load(); peak > 3 {
		t.Fatalf("параллельность превысила предел: %d", peak)
	}
	if rep, _ := c.CheckNow(context.Background(), CheckOptions{Limit: 2, Force: true}, "user"); rep.Picked != 2 {
		t.Fatalf("Limit: %+v", rep)
	}
}

// Активный и скрытые политикой узлы не проверяются планировщиком.
func TestCheckSkipsActiveAndHidden(t *testing.T) {
	fc := newFakeCheck()
	c := cfgCore(t, func(cfg *Config) { cfg.Check = fc.check })
	active, plain, hidden, normal := addNode(t, c, "active"), addNode(t, c, "plain"), addNode(t, c, "hidden"), addNode(t, c, "normal")
	_ = plain
	_ = c.st.UpsertNodes("src", time.Now().UnixNano(), []store.NodeRec{{ID: hidden, Class: "vless/tcp/none", Proto: "vless", NoSecurity: true, Opts: []byte(`{"type":"direct"}`)}}, time.Now())
	_ = c.Connect(active, "user")
	_, _ = c.CheckNow(context.Background(), CheckOptions{Force: true}, "user")
	if fc.callsOf(active) != 0 {
		t.Fatal("активный узел проверяется по живому outbound'у, а не планировщиком")
	}
	if fc.callsOf(hidden) != 0 {
		t.Fatal("узел security=none скрыт политикой P6 и проверяться не должен")
	}
	if fc.callsOf(normal) != 1 {
		t.Fatal("обычный узел не проверен")
	}
}

func TestCheckBusyAndCancel(t *testing.T) {
	fc := newFakeCheck()
	fc.delay = 2 * time.Second
	c := cfgCore(t, func(cfg *Config) { cfg.Check = fc.check; cfg.CheckParallel = 4 })
	for i := 0; i < 4; i++ {
		addNode(t, c, fmt.Sprint("n", i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan CheckReport, 1)
	go func() { r, _ := c.CheckNow(ctx, CheckOptions{}, "auto"); done <- r }()
	time.Sleep(150 * time.Millisecond)
	if _, err := c.CheckNow(context.Background(), CheckOptions{}, "user"); err != ErrBusy {
		t.Fatalf("ожидали ErrBusy: %v", err)
	}
	cancel()
	select {
	case rep := <-done:
		if rep.Canceled != 4 || rep.Checked != 0 {
			t.Fatalf("отмена: %+v", rep)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("круг не прервался по отмене")
	}
	h, _ := c.st.Histories(c.cfg.NetID(), time.Now())
	if len(h) != 0 {
		t.Fatalf("прерванные проверки записались как провалы: %+v", h)
	}
}

// Узел, который не создаётся (неизвестный тип), записывается провалом, а не валит круг.
func TestCheckRecordsBrokenNode(t *testing.T) {
	fc := newFakeCheck()
	c := cfgCore(t, func(cfg *Config) { cfg.Check = fc.check; cfg.NetID = func() string { return "n" } })
	bad := store.NodeID("bad")
	_ = c.st.UpsertNodes("src", 1, []store.NodeRec{{ID: bad, Class: "x/y/z", Opts: []byte(`{"type":"нет-такого"}`)}}, time.Now())
	rep, err := c.CheckNow(context.Background(), CheckOptions{}, "auto")
	if err != nil || rep.Checked != 1 || rep.OK != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if h, _ := c.st.Histories("n", time.Now()); h[bad].LastFail.IsZero() {
		t.Fatal("провал создания узла не записан")
	}
}

// Автономная работа: после старта подписки обновляются сами, затем узлы проверяются; Close гасит задания.
func TestBackgroundLoops(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2))
	fc := newFakeCheck()
	c := cfgCore(t, func(cfg *Config) {
		cfg.Manual = false
		cfg.Fetch = f.fetch
		cfg.Check = fc.check
		cfg.SeedSources = []aggregator.Source{}
		cfg.RefreshInterval, cfg.CheckInterval = time.Hour, time.Hour
	})
	if err := c.AddSource("A", "https://a.invalid/", "", 1, "user"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, present, _ := c.st.CountNodes(); present == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, present, _ := c.st.CountNodes(); present != 2 {
		t.Fatal("подписки не обновились автоматически")
	}
	for time.Now().Before(deadline) {
		if evs, _ := c.st.Events(0, 0); hasKind(evs, EvProbe) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("после обновления не прошёл круг проверок")
}

func hasKind(evs []store.Event, kind string) bool {
	for _, e := range evs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// Явный список узлов проверяется ровно в заданном порядке, мимо политики отбора (нужен для сравнения режимов на одном наборе).
func TestCheckExplicitIDs(t *testing.T) {
	fc := newFakeCheck()
	var order []string
	var mu sync.Mutex
	c := cfgCore(t, func(cfg *Config) {
		cfg.CheckParallel = 1
		cfg.Check = func(ctx context.Context, n store.NodeRec, p *engine.Probe) []prober.Result {
			mu.Lock()
			order = append(order, n.ID)
			mu.Unlock()
			return fc.check(ctx, n, p)
		}
	})
	a, b, d := addNode(t, c, "a"), addNode(t, c, "b"), addNode(t, c, "d")
	_ = c.st.Record([]store.Row{{Node: a, Net: c.netKey(), Level: 2, OK: true, At: time.Now()}}) // свежий успех: обычный отбор его бы пропустил
	rep, err := c.CheckNow(context.Background(), CheckOptions{IDs: []string{d, "нет-такого", a, b}}, "user")
	if err != nil || rep.Picked != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
	if len(order) != 3 || order[0] != d || order[1] != a || order[2] != b {
		t.Fatalf("порядок: %v", order)
	}
}

func TestFailsTextExplainsZeroAlive(t *testing.T) {
	if failsText(nil) != "" || failsText(map[string]int{}) != "" {
		t.Fatal("без отказов текста нет")
	}
	got := failsText(map[string]int{"timeout": 120, "tls/handshake": 30, "": 2, "connect": 30, "other": 1, "x": 1})
	// по убыванию, при равенстве — по имени, не больше четырёх; пустой класс называется «другое»
	if got != "; отказы: timeout 120, connect 30, tls/handshake 30, другое 2" {
		t.Fatalf("%q", got)
	}
}

func TestCheckReportCountsFailuresByClass(t *testing.T) {
	fc := newFakeCheck()
	c := cfgCore(t, func(cfg *Config) { cfg.Check = fc.check; cfg.NetID = func() string { return "net1" } })
	var ids []string
	for i := 0; i < 5; i++ {
		id := addNode(t, c, fmt.Sprint("n", i))
		fc.ok[id] = i < 2 // два живых, три мёртвых
		ids = append(ids, id)
	}
	rep, err := c.CheckNow(context.Background(), CheckOptions{IDs: ids, Force: true}, "user")
	if err != nil || rep.OK != 2 || rep.Checked != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
	total := 0
	for _, n := range rep.Fails {
		total += n
	}
	if total != 3 {
		t.Fatalf("отказов должно быть 3, разбивка: %v", rep.Fails)
	}
}

// Круг проверки сообщает прогресс: сколько отобрано, сколько проверено и сколько из них живых; по окончании активность снимается.
func TestActivityShowsCheckProgress(t *testing.T) {
	fc := newFakeCheck()
	fc.delay = 120 * time.Millisecond
	c := cfgCore(t, func(cfg *Config) {
		cfg.Check = fc.check
		cfg.NetID = func() string { return "net1" }
		cfg.CheckParallel = 2
	})
	var ids []string
	for i := 0; i < 6; i++ {
		id := addNode(t, c, fmt.Sprint("n", i))
		fc.ok[id] = i%2 == 0 // половина живых
		ids = append(ids, id)
	}
	done := make(chan CheckReport, 1)
	go func() {
		rep, _ := c.CheckNow(context.Background(), CheckOptions{IDs: ids, Force: true}, "user")
		done <- rep
	}()
	waitFor(t, "круг идёт и виден прогресс", func() bool {
		a := c.Activity()
		return a.Checking && a.CheckPicked == 6 && a.CheckDone >= 1 && a.CheckDone < 6
	})
	rep := <-done
	a := c.Activity()
	if a.Checking || a.CheckDone != 6 || a.CheckOK != 3 || rep.OK != 3 {
		t.Fatalf("итог активности: %+v, отчёт %+v", a, rep)
	}
}
