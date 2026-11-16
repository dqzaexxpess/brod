package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"brod/aggregator"
	"brod/engine"
	"brod/prober"
	"brod/store"
)

func boolp(b bool) *bool { return &b }

func TestCleanliness(t *testing.T) {
	for _, tc := range []struct {
		g, s *bool
		yt   bool
		want string
	}{
		{nil, nil, false, CleanUnknown},
		{boolp(true), boolp(true), false, CleanClean},
		{boolp(true), nil, false, CleanClean},
		{nil, nil, true, CleanClean},
		{boolp(true), boolp(false), false, CleanMixed},
		{boolp(false), boolp(false), true, CleanMixed},
		{boolp(false), boolp(false), false, CleanDirty},
		{boolp(false), nil, false, CleanDirty},
	} {
		if got := cleanliness(tc.g, tc.s, tc.yt); got != tc.want {
			t.Errorf("g=%v s=%v yt=%v: %s, ждали %s", tc.g, tc.s, tc.yt, got, tc.want)
		}
	}
}

func TestExitInfoNowStoresAndShowsInNodes(t *testing.T) {
	var calls atomic.Int32 // замыкание вызывается из нескольких горутин ExitInfoNow
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		YouTube: func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: "youtube", OK: true, LatencyMs: 900}
		},
		Exit: func(_ context.Context, n store.NodeRec, _ *engine.Probe) prober.ExitInfo {
			calls.Add(1)
			if n.Server == "bad.example" {
				return prober.ExitInfo{Detail: "trace: сеть"} // не дошло — ничего не сохраняем
			}
			return prober.ExitInfo{IP: "203.0.113.5", Country: "NL", Colo: "AMS", GoogleOK: boolp(true), SiteOK: boolp(false), Detail: "строгий сайт не пускает"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	good, bad := addNode(t, c, "good"), addNode(t, c, "bad")
	done, err := c.ExitInfoNow(context.Background(), []string{good, bad})
	if err != nil || done != 1 || calls.Load() != 2 {
		t.Fatalf("done=%d calls=%d err=%v", done, calls.Load(), err)
	}
	views, err := c.Nodes(NodesQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var gotGood, gotBad *NodeView
	for i := range views {
		switch views[i].ID {
		case good:
			gotGood = &views[i]
		case bad:
			gotBad = &views[i]
		}
	}
	if gotGood == nil || !gotGood.YouTube {
		t.Fatalf("проверка YouTube при сборе выхода должна дать значок YouTube: %+v", gotGood)
	}
	if gotGood == nil || gotGood.Exit == nil || gotGood.Exit.Country != "NL" || gotGood.Exit.Clean != CleanMixed || gotGood.Exit.IP != "203.0.113.5" {
		t.Fatalf("узел с данными: %+v", gotGood)
	}
	if gotBad == nil || gotBad.Exit != nil {
		t.Fatalf("узел без данных не должен иметь Exit: %+v", gotBad)
	}
}

func TestExitRoundPicksAliveWithoutInfo(t *testing.T) {
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}, ExitBatch: 5,
		YouTube: func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: "youtube", OK: true, LatencyMs: 900}
		},
		Exit: func(context.Context, store.NodeRec, *engine.Probe) prober.ExitInfo {
			return prober.ExitInfo{IP: "198.51.100.1", Country: "DE"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	alive, dead := addNode(t, c, "alive"), addNode(t, c, "dead")
	_ = dead
	// «живой» = успех L2 в текущей сети
	if err := c.st.Record([]store.Row{{Node: alive, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	c.exitRound(context.Background())
	m, _ := c.st.ExitInfos()
	if _, ok := m[alive]; !ok {
		t.Fatal("для живого узла сведения должны собраться")
	}
	if _, ok := m[dead]; ok {
		t.Fatal("для непроверенного узла сведения собирать не нужно")
	}
}

func TestSpeedAndLatencyInNodes(t *testing.T) {
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		Speed: func(_ context.Context, n store.NodeRec, _ *engine.Probe) prober.Result {
			if n.Server == "slow.example" {
				return prober.Result{Level: 3, OK: false, Class: prober.ClassTimeout}
			}
			return prober.Result{Level: 3, OK: true, SpeedKBps: 1536}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	good, slow := addNode(t, c, "fast"), addNode(t, c, "slow")
	if err := c.st.Record([]store.Row{{Node: good, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, LatencyMs: 230, At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	n, err := c.SpeedNow(context.Background(), []string{good, slow})
	if err != nil || n != 1 {
		t.Fatalf("замерилось %d, %v", n, err)
	}
	views, _ := c.Nodes(NodesQuery{})
	for _, v := range views {
		switch v.ID {
		case good:
			if v.SpeedKBps != 1536 || v.LatencyMs != 230 || v.SpeedAt.IsZero() {
				t.Fatalf("узел fast: %+v", v)
			}
		case slow:
			if v.SpeedKBps != 0 {
				t.Fatalf("у узла slow скорости быть не должно: %+v", v)
			}
		}
	}
	// фоновый круг берёт живые узлы без замера
	c.cfg.Speed = func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
		return prober.Result{Level: 3, OK: true, SpeedKBps: 99}
	}
	alive := addNode(t, c, "alive2")
	_ = c.st.Record([]store.Row{{Node: alive, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, At: time.Now()}})
	c.speedRound(context.Background())
	if m, _ := c.st.Speeds(); m[alive].KBps != 99 {
		t.Fatalf("круг замера не тронул живой узел: %v", m)
	}
}

func TestMeasureBatch(t *testing.T) {
	checked := 0
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		Check: func(_ context.Context, _ store.NodeRec, _ *engine.Probe) []prober.Result {
			checked++
			return []prober.Result{{Level: 2, OK: true, LatencyMs: 150}}
		},
		YouTube: func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: "youtube", OK: true}
		},
		Exit: func(context.Context, store.NodeRec, *engine.Probe) prober.ExitInfo {
			return prober.ExitInfo{IP: "198.51.100.9", Country: "FI"}
		},
		Speed: func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
			return prober.Result{Level: 3, OK: true, SpeedKBps: 777}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, b := addNode(t, c, "m1"), addNode(t, c, "m2")
	if err := c.StartMeasure(MeasureOptions{}); err == nil {
		t.Fatal("пустой набор узлов должен отклоняться")
	}
	if err := c.StartMeasure(MeasureOptions{IDs: []string{a}}); err == nil {
		t.Fatal("без вида замера должен отклоняться")
	}
	if err := c.StartMeasure(MeasureOptions{IDs: []string{a, b}, Ping: true, Exit: true, Speed: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.StartMeasure(MeasureOptions{IDs: []string{a}, Speed: true}); err != ErrBusy && c.MeasureState().Running {
		t.Fatalf("второй пакет во время первого должен давать ErrBusy: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for c.MeasureState().Running && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	st := c.MeasureState()
	if st.Running || st.Total != 6 || st.Done != 6 || st.Canceled || st.Finished.IsZero() {
		t.Fatalf("итог: %+v", st)
	}
	views, _ := c.Nodes(NodesQuery{})
	for _, v := range views {
		if v.Exit == nil || v.Exit.Country != "FI" || v.SpeedKBps != 777 || v.LatencyMs != 150 {
			t.Fatalf("после пакета у узла %s должны быть пинг, страна и скорость: %+v", v.ID[:6], v)
		}
	}
	if checked != 2 {
		t.Fatalf("проверок пинга: %d", checked)
	}
}

func TestBestScoreOrdering(t *testing.T) {
	yes, no := true, false
	base := NodeView{L2At: time.Now(), LatencyMs: 300}
	clean := base
	clean.Exit = &ExitView{Clean: CleanClean, GoogleOK: &yes}
	clean.YouTube = true
	clean.SpeedKBps = 4096
	dirty := base
	dirty.Exit = &ExitView{Clean: CleanDirty, GoogleOK: &no}
	fast := base
	fast.SpeedKBps = 8192
	own := base
	own.Own = true
	slowPing := clean
	slowPing.LatencyMs = 3000
	order := []struct {
		name string
		v    NodeView
	}{{"свой", own}, {"чистый+YouTube+скорость", clean}, {"быстрый без данных о чистоте", fast}, {"чистый, но пинг 3 с", slowPing}, {"грязный", dirty}}
	for i := 1; i < len(order); i++ {
		if bestScore(order[i-1].v) <= bestScore(order[i].v) {
			t.Fatalf("%s должен быть лучше %s: %.1f против %.1f", order[i-1].name, order[i].name, bestScore(order[i-1].v), bestScore(order[i].v))
		}
	}
}

func TestConnectBest(t *testing.T) {
	c := startCore(t, t.TempDir())
	if _, err := c.ConnectBest("user"); err == nil {
		t.Fatal("без живых узлов должна быть ошибка")
	}
	a, b := addNode(t, c, "slow"), addNode(t, c, "fast")
	_ = c.st.Record([]store.Row{
		{Node: a, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, LatencyMs: 1500, At: time.Now()},
		{Node: b, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, LatencyMs: 100, At: time.Now()},
	})
	_ = c.st.PutSpeed(store.SpeedRec{Node: b, KBps: 4096, At: time.Now()})
	v, err := c.ConnectBest("user")
	if err != nil || v.ID != b {
		t.Fatalf("лучшим должен быть быстрый узел: %v %v", v.ID, err)
	}
	if sel, _ := c.st.Selection(store.ScopeActive); sel.Node != b {
		t.Fatalf("выбор: %+v", sel)
	}
}

func TestActiveBrief(t *testing.T) {
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if p, k := c.ActiveBrief(); p != "" || k != "" {
		t.Fatalf("без активного узла сводка должна быть пустой, а не %q/%q", p, k)
	}
	id := addNode(t, c, "brief")
	c.mu.Lock()
	c.selected[store.ScopeActive] = id
	c.mu.Unlock()
	if p, k := c.ActiveBrief(); p == "" || k != "" {
		t.Fatalf("протокол есть, страна ещё не определена: %q/%q", p, k)
	}
	if err := c.st.PutExitInfo(store.ExitInfoRec{Node: id, IP: "198.51.100.1", Country: "NL", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, k := c.ActiveBrief(); k != "NL" {
		t.Fatalf("страна = %q, нужно NL", k)
	}
}
