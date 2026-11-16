package notify_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"brod/aggregator"
	"brod/core"
	"brod/engine"
	"brod/internal/testport"
	"brod/notify"
	"brod/prober"
	"brod/store"
)

type fakeSender struct {
	mu     sync.Mutex
	sent   []notify.Notification
	closed []uint32
	next   uint32
	cb     func(uint32, string)
}

func (f *fakeSender) Send(n notify.Notification) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	if n.ReplaceID != 0 {
		return n.ReplaceID, nil
	}
	f.next++
	return f.next, nil
}
func (f *fakeSender) Close(id uint32)                  { f.mu.Lock(); f.closed = append(f.closed, id); f.mu.Unlock() }
func (f *fakeSender) OnAction(cb func(uint32, string)) { f.cb = cb }
func (f *fakeSender) last() notify.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}
func (f *fakeSender) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func setup(t *testing.T) (*core.Core, *fakeSender, *notify.Presenter) {
	t.Helper()
	c, err := core.New(core.Config{StateDir: t.TempDir(), AppPort: testport.Next(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		NetID: func() string { return "net1" },
		Check: func(context.Context, store.NodeRec, *engine.Probe) []prober.Result {
			return []prober.Result{{Level: 2, OK: true}}
		},
		TargetCheck: func(_ context.Context, tg store.Target, _, _ string, _ *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: tg.ID, OK: true}
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	// прежние сценарии проверяют уровень «all» (все уведомления, критичные и «липкие»); новый уровень — в TestImportantLevel
	if err := c.SetSetting(core.SetNotifyLevel, "all", "user"); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{}
	return c, fs, notify.New(c, fs)
}

func node(t *testing.T, c *core.Core, key string) string {
	id := store.NodeID(key)
	if err := c.Store().UpsertNodes("s", time.Now().UnixNano(), []store.NodeRec{{ID: id, Class: "direct/tcp/none", Proto: "direct", Server: key + ".example", Port: 443,
		Opts: []byte(`{"type":"direct"}`)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return id
}

func detail(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }

func TestDegradedThenAlternativesWithActions(t *testing.T) {
	c, fs, p := setup(t)
	active, a1, a2 := node(t, c, "active"), node(t, c, "alt1"), node(t, c, "alt2")
	_ = c.Connect(active, "user")

	p.Handle(store.Event{Kind: core.EvDegraded, Node: active, Detail: detail(map[string]any{"reason": "активная проверка: timeout ×2"})})
	if n := fs.last(); n.Urgency != notify.Critical || !strings.Contains(n.Body, "active.example") || n.Timeout >= 0 {
		t.Fatalf("уведомление о деградации: %+v", n)
	}
	p.Handle(store.Event{Kind: core.EvAlternatives, Node: active, Detail: detail(map[string]any{"alternatives": []string{a1, a2}})})
	n := fs.last()
	if len(n.Actions) != 2 || n.ReplaceID == 0 || !strings.Contains(n.Actions[0].Label, "alt1.example") {
		t.Fatalf("уведомление с альтернативами: %+v", n)
	}
	// узел сам не менялся
	if st, _ := c.Status(); st.Active.ID != active {
		t.Fatal("активный узел сменился без нажатия кнопки")
	}
	fs.cb(n.ReplaceID, n.Actions[1].Key) // пользователь нажал «Переключить на alt2»
	st, _ := c.Status()
	if st.Active.ID != a2 || st.ActiveBy != "user" {
		t.Fatalf("после нажатия: %+v by %s", st.Active, st.ActiveBy)
	}
	if len(fs.closed) != 1 {
		t.Fatal("уведомление не закрыто после действия")
	}
	fs.cb(n.ReplaceID, n.Actions[0].Key) // повторное нажатие на закрытое уведомление ничего не делает
	if st, _ := c.Status(); st.Active.ID != a2 {
		t.Fatal("повторное нажатие переключило узел")
	}
	// неизвестная кнопка безвредна
	fs.cb(999, "connect:0")
}

func TestNoAlternativesRecoveredAndRestoreFailed(t *testing.T) {
	c, fs, p := setup(t)
	active := node(t, c, "active")
	_ = c.Connect(active, "user")
	p.Handle(store.Event{Kind: core.EvDegraded, Node: active, Detail: detail(map[string]any{"reason": "x"})})
	p.Handle(store.Event{Kind: core.EvAlternatives, Node: active, Detail: detail(map[string]any{"alternatives": []string{}})})
	if n := fs.last(); len(n.Actions) != 0 || !strings.Contains(n.Body, "нет") {
		t.Fatalf("без альтернатив: %+v", n)
	}
	p.Handle(store.Event{Kind: core.EvRecovered, Node: active})
	if n := fs.last(); n.Urgency != notify.Low || n.ReplaceID == 0 || !strings.Contains(n.Summary, "снова") {
		t.Fatalf("восстановление: %+v", n)
	}
	p.Handle(store.Event{Kind: core.EvRestoreFailed, Node: active})
	if n := fs.last(); n.Urgency != notify.Critical || !strings.Contains(n.Body, "Автоматически") {
		t.Fatalf("restore-failed: %+v", n)
	}
}

func TestTargetNotifications(t *testing.T) {
	c, fs, p := setup(t)
	cur, alt := node(t, c, "cur"), node(t, c, "alt")
	_ = c.PutTarget(store.Target{ID: "videosite", Owner: "api:myapp", Rules: store.TargetRules{DomainKeyword: []string{"videosite"}}, Check: store.TargetCheck{Kind: "http", URL: "https://r.example/"}}, "user")
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")

	deg := store.Event{Kind: core.EvTargetDegrade, Target: "videosite", Node: cur, Detail: detail(map[string]any{"reason": "403 ×3"})}
	p.Handle(deg)
	p.Handle(deg) // повтор в течение минуты не дублируется
	if fs.count() != 1 {
		t.Fatalf("уведомлений %d, ожидалось 1 (антиспам)", fs.count())
	}
	p.Handle(store.Event{Kind: core.EvAlternatives, Target: "videosite", Node: cur, Detail: detail(map[string]any{"alternatives": []string{alt, "direct"}})})
	n := fs.last()
	if len(n.Actions) != 2 || !strings.Contains(n.Actions[1].Label, "напрямую") {
		t.Fatalf("альтернативы цели: %+v", n)
	}
	fs.cb(n.ReplaceID, n.Actions[0].Key)
	if st, _ := c.TargetStatus("videosite"); st.Node != alt || st.SetBy != "user" {
		t.Fatalf("цель после нажатия: %+v", st)
	}
	before := fs.count()
	p.Handle(store.Event{Kind: core.EvTargetSwitch, Target: "videosite", Node: alt, Initiator: "user"})
	if fs.count() != before {
		t.Fatal("о ручном переключении уведомлять не нужно")
	}
	p.Handle(store.Event{Kind: core.EvTargetSwitch, Target: "videosite", Node: alt, Initiator: "auto"})
	if fs.count() != before+1 || !strings.Contains(fs.last().Summary, "автоматически") {
		t.Fatalf("автопереключение должно быть показано: %+v", fs.last())
	}
}

// Уровень «important» (по умолчанию): о деградации цели с автопереключением не сообщаем — она переключится сама и придёт уведомление
// о результате; автопереключения одной цели — не чаще раза в 10 минут (нестабильный путь не должен мигать уведомлениями).
func TestAutoFailoverTargetQuiet(t *testing.T) {
	c, fs, p := setup(t)
	if err := c.SetSetting(core.SetNotifyLevel, "important", "user"); err != nil {
		t.Fatal(err)
	}
	n := node(t, c, "n")
	p.Handle(store.Event{Kind: core.EvTargetDegrade, Target: "video", Node: n, Detail: detail(map[string]any{"policy": store.PolicyAutoFailover, "reason": "timeout ×3"})})
	if fs.count() != 0 {
		t.Fatalf("деградация цели с автопереключением показана: %+v", fs.last())
	}
	p.Handle(store.Event{Kind: core.EvTargetDegrade, Target: "video", Node: n, Detail: detail(map[string]any{"policy": store.PolicyStickyManual, "reason": "timeout ×3"})})
	if fs.count() != 1 {
		t.Fatal("деградация цели без автопереключения должна быть показана: человеку решать")
	}
	p.Handle(store.Event{Kind: core.EvTargetSwitch, Target: "video", Node: n, Initiator: "auto"})
	p.Handle(store.Event{Kind: core.EvTargetSwitch, Target: "video", Node: "direct", Initiator: "auto"})
	if fs.count() != 2 {
		t.Fatalf("автопереключения одной цели за 10 минут: уведомлений %d, ожидалось 1", fs.count()-1)
	}
}

func TestRunWiresBusToSender(t *testing.T) {
	c, fs, p := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	c.Bus().Publish(store.Event{Kind: core.EvRestoreFailed, Initiator: "auto"})
	deadline := time.Now().Add(2 * time.Second)
	for fs.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fs.count() != 1 {
		t.Fatal("событие шины не дошло до уведомления")
	}
	cancel()
	<-done
}

// Проверочное уведомление: кнопки только пишут событие и не трогают узлы.
func TestSelfTestButtonsOnlyLogEvents(t *testing.T) {
	c, fs, p := setup(t)
	active := node(t, c, "active")
	_ = c.Connect(active, "user")
	if err := p.SelfTest(); err != nil {
		t.Fatal(err)
	}
	n := fs.last()
	if len(n.Actions) != 2 || n.Urgency == notify.Critical {
		t.Fatalf("уведомление: %+v", n)
	}
	fs.cb(1, n.Actions[1].Key)
	evs, _ := c.Store().Events(0, 0)
	got := ""
	for _, e := range evs {
		if e.Kind == core.EvNotifyTest {
			got = e.Detail
		}
	}
	if got != "нажата кнопка 2" {
		t.Fatalf("событие: %q", got)
	}
	if st, _ := c.Status(); st.Active.ID != active {
		t.Fatal("тестовая кнопка затронула активный узел")
	}
}

// Уровень «important» (по умолчанию): при выключенном VPN об активном узле не беспокоим, но события целей приложений показываем;
// при включённом — короткие уведомления обычной срочности, без повторов по одному узлу чаще раза в 5 минут.
func TestImportantLevel(t *testing.T) {
	c, fs, p := setup(t)
	if err := c.SetSetting(core.SetNotifyLevel, "important", "user"); err != nil {
		t.Fatal(err)
	}
	n := node(t, c, "n1")
	deg := store.Event{Kind: core.EvDegraded, Node: n, Detail: `{"reason":"timeout"}`}

	p.VPNOn = func() bool { return false }
	p.Handle(deg)
	p.Handle(store.Event{Kind: core.EvAlternatives, Node: n, Detail: `{"alternatives":[]}`})
	if fs.count() != 0 {
		t.Fatalf("при выключенном VPN об активном узле уведомлять не нужно: %d", fs.count())
	}
	p.Handle(store.Event{Kind: core.EvTargetDegrade, Target: "videosite", Node: n, Detail: `{"reason":"timeout"}`})
	if fs.count() != 1 {
		t.Fatalf("события целей приложений показываются и при выключенном VPN: %d", fs.count())
	}

	p.VPNOn = func() bool { return true }
	p.Handle(deg)
	if fs.count() != 2 {
		t.Fatalf("при включённом VPN уведомление должно быть: %d", fs.count())
	}
	if l := fs.last(); l.Urgency == notify.Critical || l.Timeout < 0 {
		t.Fatalf("на уровне important уведомление не должно быть критичным и «липким»: %+v", l)
	}
	p.Handle(deg)
	if fs.count() != 2 {
		t.Fatal("повтор по тому же узлу в течение 5 минут показываться не должен")
	}
	// альтернатив нет: обещание «подбираю» закрывается одним сообщением, повторы поиска молчат
	noAlt := store.Event{Kind: core.EvAlternatives, Node: n, Detail: `{"alternatives":[]}`}
	p.Handle(noAlt)
	if l := fs.last(); fs.count() != 3 || l.ReplaceID == 0 || l.Urgency == notify.Critical || l.Timeout < 0 || !strings.Contains(l.Body, "нет") {
		t.Fatalf("без альтернатив нужно заменить «подбираю…» коротким сообщением: %d %+v", fs.count(), l)
	}
	p.Handle(noAlt)
	if fs.count() != 3 {
		t.Fatal("повтор поиска без результата не должен снова беспокоить")
	}

	if err := c.SetSetting(core.SetNotifyLevel, "off", "user"); err != nil {
		t.Fatal(err)
	}
	p.Handle(store.Event{Kind: core.EvTargetDegrade, Target: "videosite", Node: n, Detail: `{"reason":"x"}`})
	if fs.count() != 3 {
		t.Fatal("на уровне off уведомлений быть не должно")
	}
	if err := c.SetSetting(core.SetNotifyLevel, "nope", "user"); err == nil {
		t.Fatal("неизвестный уровень должен отклоняться")
	}
}

// В режиме автопереключения о деградации молчим, сообщаем о результате; без альтернатив — как обычно.
func TestAutoSwapNotification(t *testing.T) {
	c, fs, p := setup(t)
	_ = c.SetSetting(core.SetNotifyLevel, "important", "user")
	_ = c.SetSetting(core.SetDegradeAction, "auto", "user")
	p.VPNOn = func() bool { return true }
	a, b := node(t, c, "old"), node(t, c, "new")
	p.Handle(store.Event{Kind: core.EvDegraded, Node: a, Detail: `{"reason":"timeout"}`})
	p.Handle(store.Event{Kind: core.EvAlternatives, Node: a, Detail: `{"alternatives":["` + b + `"]}`})
	if fs.count() != 0 {
		t.Fatalf("до результата автопереключения уведомлений быть не должно: %d", fs.count())
	}
	p.Handle(store.Event{Kind: core.EvAutoSwap, Node: b, Detail: `{"from":"` + a + `"}`})
	if fs.count() != 1 || fs.last().Summary != "Узел заменён автоматически" {
		t.Fatalf("итог автопереключения: %d %+v", fs.count(), fs.last())
	}
}
