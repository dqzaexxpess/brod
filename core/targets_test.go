package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"brod/engine"
	"brod/monitor"
	"brod/prober"
	"brod/store"
)

// tcheck — управляемая проверка целей: результат зависит от выбранного узла.
type tcheck struct {
	mu    sync.Mutex
	good  map[string]bool // node → проходит
	calls map[string]int
}

func newTCheck() *tcheck { return &tcheck{good: map[string]bool{}, calls: map[string]int{}} }

func (f *tcheck) set(node string, ok bool) { f.mu.Lock(); f.good[node] = ok; f.mu.Unlock() }
func (f *tcheck) fn(_ context.Context, t store.Target, node, _ string, _ *engine.Probe) prober.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[node]++
	if f.good[node] {
		return prober.Result{Level: 4, Target: t.ID, OK: true, LatencyMs: 40}
	}
	return prober.Result{Level: 4, Target: t.ID, Class: prober.ClassBlocked, Detail: "HTTP 403"}
}

func targetCore(t *testing.T, tc *tcheck, mutate func(*Config)) *Core {
	return cfgCore(t, func(cfg *Config) {
		cfg.TargetCheck = tc.fn
		cfg.Check = newFakeCheck().check
		cfg.NetID = func() string { return "net1" }
		if mutate != nil {
			mutate(cfg)
		}
	})
}

func videosite(policy string) store.Target {
	return store.Target{ID: "videosite", Title: "VideoSite", Rules: store.TargetRules{DomainKeyword: []string{"videosite"}},
		Check: store.TargetCheck{Kind: "http", URL: "https://videosite.example/"}, Policy: policy, FailoverK: 2, AllowDirect: true}
}

// putAsApp регистрирует цель как приложение api:myapp, чьи правила и allow_direct одобрил владелец (user): сторонний токен
// сам не может выставить allow_direct и ключевые слова, а тестам ниже они нужны.
func putAsApp(c *Core, t store.Target) error {
	t.Owner = "api:myapp"
	return c.PutTarget(t, "user")
}

// appTarget — цель, которую сторонний токен вправе создать: без ключевых слов и allow_direct.
func appTarget() store.Target {
	t := videosite("")
	t.Rules = store.TargetRules{DomainSuffix: []string{"videosite.example"}}
	t.AllowDirect = false
	return t
}

// приложение с токеном не может направить проверку цели или пробную ссылку на внутренние адреса; владелец может.
func TestAppCannotPointChecksAtInternalAddresses(t *testing.T) {
	c := newCore(t, t.TempDir())
	_ = c.Start(context.Background())
	t.Cleanup(func() { c.Close() }) // иначе цикл traffic ядра живёт дальше теста и гоняется с тестами, меняющими trafficFlushEvery
	bad := []string{
		"http://127.0.0.1:8080/x", "http://localhost/x", "http://LOCALHOST:1/", "http://foo.localhost/", "http://[::1]/", "http://10.1.2.3/",
		"http://192.168.0.1/", "http://172.16.5.5/", "http://169.254.169.254/latest", "http://0.0.0.0/", "http://[fe80::1]/",
		"http://[::ffff:127.0.0.1]/", "http://100.64.0.1/", "https://[fd00::1]:443/", "http://user@127.0.0.1/",
	}
	for _, u := range bad {
		tg := appTarget()
		tg.Check.URL = u
		if err := c.PutTarget(tg, "api:myapp"); !errors.Is(err, ErrInvalid) {
			t.Errorf("цель с проверкой %q принята приложением: %v", u, err)
		}
	}
	for _, u := range []string{"https://videosite.example/", "http://203.0.113.9/x", "https://[2001:db8::1]/"} {
		tg := appTarget()
		tg.Check.URL = u
		if err := c.PutTarget(tg, "api:myapp"); err != nil {
			t.Errorf("внешний адрес %q отклонён: %v", u, err)
		}
	}
	// пробная ссылка (цель videosite создана выше приложением myapp)
	for _, u := range bad {
		if err := c.SetProbeURL("videosite", u, time.Hour, "api:myapp"); !errors.Is(err, ErrInvalid) {
			t.Errorf("пробная ссылка %q принята приложением: %v", u, err)
		}
	}
	if err := c.SetProbeURL("videosite", "https://cdn.videosite.example/a?sig=1", time.Hour, "api:myapp"); err != nil {
		t.Errorf("внешняя пробная ссылка отклонена: %v", err)
	}
	// владелец (user) может проверять и локальные адреса
	tg := appTarget()
	tg.ID, tg.Check.URL = "mine", "http://127.0.0.1:8080/x"
	if err := c.PutTarget(tg, "user"); err != nil {
		t.Errorf("владельцу запрещён локальный адрес: %v", err)
	}

}

func TestBuiltinTargetsSeeded(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	_ = c.Start(context.Background())
	ts, err := c.Targets()
	if err != nil || len(ts) != 3 {
		t.Fatalf("встроенные цели: %d %v", len(ts), err)
	}
	for _, ts := range ts {
		if ts.Owner != "builtin" {
			t.Fatalf("%s: владелец %s", ts.ID, ts.Owner)
		}
	}
	// пользователь меняет политику встроенной цели; перезапуск её не затирает
	yt, _ := c.st.Target("youtube")
	yt.Policy = store.PolicyAutoFailover
	if err := c.PutTarget(yt, "user"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	c2 := startCore(t, dir)
	if got, _ := c2.st.Target("youtube"); got.Policy != store.PolicyAutoFailover || got.Owner != "builtin" {
		t.Fatalf("после перезапуска: %+v", got)
	}
}

func TestTargetOwnershipAndValidation(t *testing.T) {
	c := targetCore(t, newTCheck(), nil)
	appTg := videosite("")
	appTg.Rules, appTg.AllowDirect = store.TargetRules{DomainSuffix: []string{"videosite.example"}}, false
	if err := c.PutTarget(appTg, "api:myapp"); err != nil {
		t.Fatal(err)
	}
	got, _ := c.st.Target("videosite")
	if got.Owner != "api:myapp" || got.Policy != store.PolicyStickyManual {
		t.Fatalf("владелец/политика по умолчанию: %+v", got)
	}
	if err := c.PutTarget(videosite(""), "api:other"); err == nil {
		t.Fatal("чужое приложение не должно менять цель")
	}
	if err := c.DeleteTarget("videosite", "api:other"); err == nil {
		t.Fatal("чужое приложение не должно удалять цель")
	}
	if err := c.PutTarget(store.Target{ID: "youtube", Check: store.TargetCheck{Kind: "youtube"}}, "api:myapp"); err == nil {
		t.Fatal("приложение не должно подменять встроенную цель")
	}
	if err := c.DeleteTarget("youtube", "user"); err == nil {
		t.Fatal("встроенную цель удалять нельзя")
	}
	bad := map[string]store.Target{
		"id":             {ID: "Плохой Id", Check: store.TargetCheck{Kind: "youtube"}},
		"kind":           {ID: "x1", Check: store.TargetCheck{Kind: "нет"}},
		"url":            {ID: "x2", Check: store.TargetCheck{Kind: "http", URL: "ftp://x"}},
		"rule":           {ID: "x3", Check: store.TargetCheck{Kind: "youtube"}, Rules: store.TargetRules{Domain: []string{"a b"}}},
		"policy":         {ID: "x4", Check: store.TargetCheck{Kind: "youtube"}, Policy: "авто"},
		"scheme":         {ID: "x5", Check: store.TargetCheck{Kind: "youtube"}, Rules: store.TargetRules{Domain: []string{"https://x.com"}}},
		"speed-no-range": {ID: "x6", Check: store.TargetCheck{Kind: "http", URL: "https://x.com/", MinKBps: 100}},
		"speed-kind":     {ID: "x7", Check: store.TargetCheck{Kind: "youtube", MinBytes: 1024, MinKBps: 100}},
		"speed-negative": {ID: "x8", Check: store.TargetCheck{Kind: "http", URL: "https://x.com/", MinBytes: 1024, MinKBps: -1}},
	}
	for name, tg := range bad {
		if err := c.PutTarget(tg, "user"); err == nil {
			t.Errorf("%s: ожидали ошибку", name)
		}
	}
	if err := c.PutTarget(videosite(""), "user"); err != nil { // пользователь правит чужую цель, не присваивая её себе
		t.Fatal(err)
	}
	if got, _ := c.st.Target("videosite"); got.Owner != "api:myapp" {
		t.Fatalf("владелец сменился: %s", got.Owner)
	}
	if err := c.DeleteTarget("videosite", "api:myapp"); err != nil {
		t.Fatal(err)
	}
	if c.eng.Has(SwitchTag("videosite")) {
		t.Fatal("switch удалённой цели остался в движке")
	}
}

// Трафик к доменам цели идёт через её узел, а не через активный: цель с «сломанным» узлом рвёт только свои домены.
func TestTargetRoutingThroughAppInbound(t *testing.T) {
	if runtime.GOOS == "windows" {
		// прямой выход движка на Windows разрешает имена через DNS-сервер, без файла hosts: «localhost» не разрешается
		// (через 127.0.0.1 тот же путь работает), а другого имени цели, разрешимого без сети, у теста нет
		t.Skip("на Windows движок не разрешает «localhost» без файла hosts")
	}
	c := targetCore(t, newTCheck(), nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	addr, user, pass := c.AppProxy()
	get := func(host string) error {
		pu, _ := url.Parse("http://" + addr)
		pu.User = url.UserPassword(user, pass)
		cl := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
		u := strings.Replace(srv.URL, "127.0.0.1", host, 1)
		resp, err := cl.Get(u)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
	_ = c.Connect(addNode(t, c, "active"), "user")
	broken := store.NodeID("broken")
	_ = c.st.UpsertNodes("src", 1, []store.NodeRec{{ID: broken, Class: "socks/tcp/none", Proto: "socks",
		Opts: []byte(`{"type":"socks","server":"127.0.0.1","server_port":1}`)}}, time.Now())

	rz := videosite("")
	rz.Rules = store.TargetRules{DomainKeyword: []string{"localhost"}}
	if err := putAsApp(c, rz); err != nil {
		t.Fatal(err)
	}
	if err := get("localhost"); err != nil {
		t.Fatalf("цель без узла не должна ломать трафик (идёт через active): %v", err)
	}
	if _, err := c.SwitchTarget(context.Background(), "videosite", broken, "api:myapp"); err != nil {
		t.Fatal(err)
	}
	if err := get("localhost"); err == nil {
		t.Fatal("домен цели должен идти через её (сломанный) узел")
	}
	if err := get("127.0.0.1"); err != nil {
		t.Fatalf("остальной трафик идёт через active: %v", err)
	}
	if _, err := c.SwitchTarget(context.Background(), "videosite", store.SelDirect, "api:myapp"); err != nil {
		t.Fatal(err)
	}
	if err := get("localhost"); err != nil {
		t.Fatalf("direct как узел цели: %v", err)
	}
	if c.eng.Has(NodeTag(broken)) {
		t.Fatal("узел, с которого ушла цель, остался в движке")
	}
	if err := c.DeleteTarget("videosite", "api:myapp"); err != nil {
		t.Fatal(err)
	}
	if err := get("localhost"); err != nil {
		t.Fatalf("после удаления цели трафик идёт через active: %v", err)
	}
}

func TestTargetSelectionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	tc := newTCheck()
	mk := func() *Core {
		c := newCore(t, dir)
		c.cfg.TargetCheck = tc.fn
		return c
	}
	c := mk()
	_ = c.Start(context.Background())
	n := addNode(t, c, "n")
	_ = putAsApp(c, videosite(""))
	if _, err := c.SwitchTarget(context.Background(), "videosite", n, "user"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	c2 := mk()
	if err := c2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if got := c2.currentOf("videosite"); got != n {
		t.Fatalf("выбор цели не восстановлен: %q", got)
	}
	if tag, _ := c2.eng.SwitchTarget(SwitchTag("videosite")); tag != NodeTag(n) {
		t.Fatalf("switch цели смотрит на %q", tag)
	}
	if c2.eng.MatchDispatch(OutApp, "hdvideosite.ac") != SwitchTag("videosite") {
		t.Fatal("правила цели не восстановлены в dispatch")
	}
	if st, _ := c2.TargetStatus("videosite"); st.Node != n || st.SetBy != "user" {
		t.Fatalf("статус: %+v", st)
	}
}

func TestTargetStickyManualReportsAndNeverSwitches(t *testing.T) {
	tc := newTCheck()
	c := targetCore(t, tc, nil)
	cur, alt1, alt2 := addNode(t, c, "cur"), addNode(t, c, "alt1"), addNode(t, c, "alt2")
	for _, id := range []string{alt1, alt2} {
		_ = c.st.Record([]store.Row{{Node: id, Net: "net1", Level: 2, OK: true, At: time.Now()}})
		tc.set(id, true)
	}
	_ = putAsApp(c, videosite(store.PolicyStickyManual))
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	tc.set(cur, false)
	ctx := context.Background()
	_, _ = c.ProbeTarget(ctx, "videosite")
	if len(eventsOf(c, EvTargetDegrade)) != 0 {
		t.Fatal("K=2: после одного провала деградации ещё нет")
	}
	_, _ = c.ProbeTarget(ctx, "videosite")
	waitFor(t, "альтернативы цели", func() bool { return len(eventsOf(c, EvAlternatives)) == 1 })
	if got := c.currentOf("videosite"); got != cur {
		t.Fatalf("sticky-manual не должен переключать: %q", got)
	}
	if ev := eventsOf(c, EvAlternatives)[0]; ev.Target != "videosite" || !strings.Contains(ev.Detail, alt1) {
		t.Fatalf("%+v", ev)
	}
	if st, _ := c.TargetStatus("videosite"); !st.Degraded || len(st.Fallbacks) == 0 {
		t.Fatalf("статус: %+v", st)
	}
	// узел ожил: два успеха — восстановление
	tc.set(cur, true)
	_, _ = c.ProbeTarget(ctx, "videosite")
	_, _ = c.ProbeTarget(ctx, "videosite")
	if len(eventsOf(c, EvTargetOK)) != 1 {
		t.Fatal("восстановление цели не зафиксировано")
	}
}

func TestTargetAutoFailover(t *testing.T) {
	tc := newTCheck()
	c := targetCore(t, tc, nil)
	cur, dead, good := addNode(t, c, "cur"), addNode(t, c, "dead"), addNode(t, c, "good")
	for _, id := range []string{dead, good} {
		_ = c.st.Record([]store.Row{{Node: id, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	}
	tc.set(good, true) // dead проходит L2 по истории, но проверку цели не проходит
	tg := videosite(store.PolicyAutoFailover)
	tg.AllowDirect = false
	_ = putAsApp(c, tg)
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	ctx := context.Background()
	_, _ = c.ProbeTarget(ctx, "videosite")
	_, _ = c.ProbeTarget(ctx, "videosite")
	waitFor(t, "автопереключение", func() bool { return c.currentOf("videosite") == good })
	ev := eventsOf(c, EvTargetSwitch)
	if last := ev[len(ev)-1]; last.Initiator != "auto" || last.Node != good || last.Target != "videosite" {
		t.Fatalf("журнал переключения: %+v", last)
	}
	if c.eng.Has(NodeTag(cur)) {
		t.Fatal("прежний узел цели остался в движке")
	}
	if got := activeTag(t, c); got != "" {
		t.Fatalf("автопереключение цели не должно трогать активный узел: %q", got)
	}
	if sel, _ := c.st.Selection(store.TargetScope("videosite")); sel.By != "auto" || sel.Node != good {
		t.Fatalf("выбор не сохранён: %+v", sel)
	}
	// кандидат был проверен проверкой ЦЕЛИ: dead не выбран
	if tc.calls[dead] == 0 {
		t.Log("dead не успели проверить (первый же прошедший принят)")
	}
}

// direct проверяется первым и предпочитается, если проходит.
func TestTargetAutoFailoverPrefersDirect(t *testing.T) {
	tc := newTCheck()
	c := targetCore(t, tc, nil)
	cur, other := addNode(t, c, "cur"), addNode(t, c, "other")
	_ = c.st.Record([]store.Row{{Node: other, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	tc.set(other, true)
	tc.set(store.SelDirect, true)
	_ = putAsApp(c, videosite(store.PolicyAutoFailover))
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	_, _ = c.ProbeTarget(context.Background(), "videosite")
	_, _ = c.ProbeTarget(context.Background(), "videosite")
	waitFor(t, "переключение на direct", func() bool { return c.currentOf("videosite") == store.SelDirect })
}

// Нет проверенного кандидата — цель остаётся как есть, и об этом сообщается один раз за эпизод.
func TestTargetAutoFailoverNoCandidates(t *testing.T) {
	tc := newTCheck()
	c := targetCore(t, tc, nil)
	cur := addNode(t, c, "cur")
	other := addNode(t, c, "other")
	_ = c.st.Record([]store.Row{{Node: other, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	_ = putAsApp(c, videosite(store.PolicyAutoFailover))
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	for i := 0; i < 5; i++ {
		_, _ = c.ProbeTarget(context.Background(), "videosite")
		time.Sleep(30 * time.Millisecond)
	}
	waitFor(t, "сообщение об отсутствии кандидатов", func() bool {
		n := 0
		for _, e := range eventsOf(c, EvTargetDegrade) {
			if strings.Contains(e.Detail, "нет кандидатов") {
				n++
			}
		}
		return n == 1
	})
	if got := c.currentOf("videosite"); got != cur {
		t.Fatalf("без кандидатов цель не должна меняться: %q", got)
	}
}

func TestSwitchTargetNextAndErrors(t *testing.T) {
	tc := newTCheck()
	c := targetCore(t, tc, nil)
	cur, a := addNode(t, c, "cur"), addNode(t, c, "a")
	_ = c.st.Record([]store.Row{{Node: a, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	tc.set(a, true)
	tg := videosite("")
	tg.AllowDirect = false
	_ = putAsApp(c, tg)
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	to, err := c.SwitchTarget(context.Background(), "videosite", "next", "api:myapp")
	if err != nil || to != a || c.currentOf("videosite") != a {
		t.Fatalf("next: %q %v", to, err)
	}
	tc.set(a, false)
	if _, err := c.SwitchTarget(context.Background(), "videosite", "next", "api:myapp"); err == nil {
		t.Fatal("нет кандидатов — должна быть ошибка")
	}
	if _, err := c.SwitchTarget(context.Background(), "videosite", "нет-такого-узла", "api:myapp"); err == nil {
		t.Fatal("несуществующий узел")
	}
	if _, err := c.SwitchTarget(context.Background(), "videosite", store.SelWarp, "api:myapp"); err == nil {
		t.Fatal("WARP ещё недоступен")
	}
	if _, err := c.SwitchTarget(context.Background(), "videosite", a, "api:other"); err == nil {
		t.Fatal("чужое приложение переключило цель")
	}
	if _, err := c.SwitchTarget(context.Background(), "youtube", a, "api:myapp"); err == nil {
		t.Fatal("приложение переключило встроенную цель")
	}
	if c.currentOf("videosite") != a {
		t.Fatalf("после неудачных операций выбор изменился: %q", c.currentOf("videosite"))
	}
}

func TestProbeURLIsMemoryOnlyWithTTL(t *testing.T) {
	now := time.Now()
	tc := newTCheck()
	c := targetCore(t, tc, func(cfg *Config) { cfg.Now = func() time.Time { return now } })
	_ = putAsApp(c, videosite(""))
	if err := c.SetProbeURL("videosite", "ftp://x", time.Hour, "api:myapp"); err == nil {
		t.Fatal("не http")
	}
	if err := c.SetProbeURL("videosite", "https://cdn.example/v?sig=секрет", time.Hour, "api:other"); err == nil {
		t.Fatal("чужое приложение")
	}
	if err := c.SetProbeURL("videosite", "https://cdn.example/v?sig=секрет", time.Hour, "api:myapp"); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.TargetStatus("videosite"); !st.HasProbeURL {
		t.Fatal("ссылка не принята")
	}
	if c.probeURLOf("videosite") == "" {
		t.Fatal("ссылка должна быть доступна проверке")
	}
	// секрет не попадает ни в базу, ни в журнал
	evs, _ := c.st.Events(0, 0)
	for _, e := range evs {
		if strings.Contains(e.Detail, "секрет") {
			t.Fatalf("пробная ссылка в журнале: %+v", e)
		}
	}
	if tg, _ := c.st.Target("videosite"); strings.Contains(fmt.Sprintf("%+v", tg), "секрет") {
		t.Fatal("пробная ссылка сохранена в цели")
	}
	now = now.Add(2 * time.Hour)
	if c.probeURLOf("videosite") != "" {
		t.Fatal("ссылка не истекла по TTL")
	}
}

// Пассивный детектор цели — лишь повод для подтверждающих проверок: живая цель не переключается и не объявляется деградировавшей.
func TestTargetPassiveStallNeedsConfirmation(t *testing.T) {
	tc := newTCheck()
	c := targetCore(t, tc, nil)
	cur, other := addNode(t, c, "cur"), addNode(t, c, "other")
	_ = c.st.Record([]store.Row{{Node: other, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	tc.set(cur, true)
	tc.set(other, true)
	tc.set(store.SelDirect, true)
	_ = putAsApp(c, videosite(store.PolicyAutoFailover))
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	for i := 0; i < 3; i++ {
		c.onTargetStall(monitor.Event{Scope: store.TargetScope("videosite"), Stalled: 5})
	}
	time.Sleep(400 * time.Millisecond)
	if got := c.currentOf("videosite"); got != cur {
		t.Fatalf("ложная тревога переключила живую цель: %q", got)
	}
	if len(eventsOf(c, EvTargetDegrade)) != 0 {
		t.Fatal("ложная тревога объявила деградацию цели")
	}
	if tc.calls[cur] == 0 {
		t.Fatal("подтверждающая проверка не запускалась")
	}
	// узел цели действительно мёртв: подтверждение провалится K раз, и сработает политика
	tc.set(cur, false)
	c.onTargetStall(monitor.Event{Scope: store.TargetScope("videosite"), Stalled: 5})
	waitFor(t, "автопереключение после подтверждения", func() bool { return c.currentOf("videosite") != cur })
}

// Встроенные проверки (чужие API с антиботом) идут реже проверок http-целей.
func TestHeavyTargetsAreProbedLessOften(t *testing.T) {
	now := time.Now()
	tc := newTCheck()
	c := targetCore(t, tc, func(cfg *Config) { cfg.Now = func() time.Time { return now } })
	_ = putAsApp(c, videosite(""))
	yt, _ := c.st.Target("youtube")
	tc.set(store.SelDirect, true)
	_, _ = c.SwitchTarget(context.Background(), "videosite", store.SelDirect, "user")
	_, _ = c.SwitchTarget(context.Background(), "youtube", store.SelDirect, "user")
	rz, _ := c.st.Target("videosite")
	if !c.targetDue(rz) || !c.targetDue(yt) {
		t.Fatal("после выбора узла цель должна проверяться сразу")
	}
	_, _ = c.ProbeTarget(context.Background(), "videosite")
	_, _ = c.ProbeTarget(context.Background(), "youtube")
	now = now.Add(c.cfg.TargetInterval + time.Second)
	if !c.targetDue(rz) {
		t.Fatal("http-цель должна проверяться каждые TargetInterval")
	}
	if c.targetDue(yt) {
		t.Fatal("встроенная цель не должна проверяться так часто")
	}
	now = now.Add(c.cfg.HeavyTargetInterval)
	if !c.targetDue(yt) {
		t.Fatal("встроенная цель должна проверяться раз в HeavyTargetInterval")
	}
}
