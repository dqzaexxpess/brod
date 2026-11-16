package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brod/aggregator"
	"brod/store"
	"brod/tgproxy"
)

// feed — управляемая подмена загрузчика: имя источника → что он отдаст при следующем обновлении.
type feed struct {
	mu    sync.Mutex
	links map[string][]string
	errs  map[string]error
	cache map[string]bool
	gate  chan struct{} // если не nil, загрузчик ждёт сигнала (проверка ErrBusy)
	// «не изменилось» (304): как у настоящего загрузчика, при Known тело не отдаётся, иначе отдаётся закэшированное
	tg     map[string][]string // ссылки Telegram-прокси, которые отдаёт источник
	notmod map[string]bool
	known  map[string]bool // что видел загрузчик в поле Known источника при последнем обновлении
	calls  int
}

func newFeed() *feed {
	return &feed{links: map[string][]string{}, errs: map[string]error{}, cache: map[string]bool{}, notmod: map[string]bool{}, known: map[string]bool{}, tg: map[string][]string{}}
}

func (f *feed) set(name string, links ...string) {
	f.mu.Lock()
	f.links[name], f.errs[name], f.cache[name] = links, nil, false
	f.mu.Unlock()
}

func (f *feed) fetch(ctx context.Context, srcs []aggregator.Source, _ time.Duration, _ string) <-chan aggregator.Result {
	out := make(chan aggregator.Result, len(srcs))
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	for _, s := range srcs {
		f.known[s.Name] = s.Known
		if f.notmod[s.Name] {
			if s.Known {
				out <- aggregator.Result{Source: s, NotModified: true}
			} else {
				out <- aggregator.Result{Source: s, NotModified: true, Links: f.links[s.Name]}
			}
			continue
		}
		out <- aggregator.Result{Source: s, Links: f.links[s.Name], TGLinks: f.tg[s.Name], Err: f.errs[s.Name], FromCache: f.cache[s.Name]}
	}
	close(out)
	return out
}

// vlessLink — валидная ссылка; разные n дают разные узлы.
func vlessLink(n int) string {
	return fmt.Sprintf("vless://5b0e5c2e-0000-4000-8000-%012d@h%d.example:443?security=tls&sni=h%d.example&type=tcp#n%d", n, n, n, n)
}

func refreshCore(t *testing.T, f *feed, sources ...string) *Core {
	t.Helper()
	seed := []aggregator.Source{}
	for _, s := range sources {
		seed = append(seed, aggregator.Source{Name: s, URL: "http://" + s + ".invalid/", Priority: 1})
	}
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", SeedSources: seed, Fetch: f.fetch,
		TGProbe: func(context.Context, tgproxy.Proxy) tgproxy.Result { return tgproxy.Result{Class: "connect"} }}) // без настоящей сети
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRefreshIngestsDedupsAndReports(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2), vlessLink(2), "vless://битая", "мусор")
	f.set("B", vlessLink(2), vlessLink(3))
	c := refreshCore(t, f, "A", "B")
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 3 || rep.Present != 3 || rep.Added != 3 {
		t.Fatalf("отчёт: %+v", rep)
	}
	byName := map[string]SourceReport{}
	for _, s := range rep.Sources {
		byName[s.Name] = s
	}
	if a := byName["A"]; a.Links != 5 || a.Nodes != 2 || a.Err != "" {
		t.Fatalf("A: %+v", a)
	}
	if b := byName["B"]; b.Nodes != 2 {
		t.Fatalf("B: %+v", b)
	}
	srcs, _ := c.st.Sources()
	for _, s := range srcs {
		if s.LastOK.IsZero() || s.LastErr != "" {
			t.Fatalf("статистика источника %s: %+v", s.ID, s)
		}
	}
	// узел из двух источников хранится один раз
	if n, _ := c.st.Nodes(store.NodeFilter{Source: "A"}); len(n) != 2 {
		t.Fatalf("в A должно быть 2 узла, а %d", len(n))
	}
	if ev := lastEvent(t, c, EvRefresh); ev.Initiator != "user" {
		t.Fatalf("%+v", ev)
	}
}

// P1: обновление подписок никогда не меняет активный узел, даже когда он пропал из источника.
func TestRefreshNeverTouchesActive(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2))
	c := refreshCore(t, f, "A")
	if _, err := c.Refresh(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	active := idOf(t, vlessLink(1))
	if err := c.Connect(active, "user"); err != nil {
		t.Fatal(err)
	}
	tag := activeTag(t, c)
	switches := func() int {
		n := 0
		evs, _ := c.st.Events(0, 0)
		for _, e := range evs {
			if e.Kind == EvSwitch {
				n++
			}
		}
		return n
	}
	base := switches()

	rounds := [][]string{
		{vlessLink(2), vlessLink(3)},               // активный пропал из источника
		{vlessLink(4)},                             // и всё остальное сменилось
		{vlessLink(1), vlessLink(5)},               // активный вернулся
		{vlessLink(6), vlessLink(7), vlessLink(8)}, // снова пропал
	}
	for i, links := range rounds {
		f.set("A", links...)
		if _, err := c.Refresh(context.Background(), "auto"); err != nil {
			t.Fatal(err)
		}
		if got := activeTag(t, c); got != tag || !c.eng.Has(tag) {
			t.Fatalf("обновление %d: активный узел изменился или удалён из движка (%q)", i, got)
		}
		if sel, _ := c.st.Selection(store.ScopeActive); sel.Node != active || sel.By != "user" {
			t.Fatalf("обновление %d: выбор изменился: %+v", i, sel)
		}
	}
	if switches() != base {
		t.Fatalf("обновления породили %d событий смены узла", switches()-base)
	}
	st, _ := c.Status()
	if st.Active == nil || st.Active.ID != active || st.Active.Present {
		t.Fatalf("активный узел должен быть показан как пропавший из источника: %+v", st.Active)
	}
	// после возвращения в источник — снова присутствует
	f.set("A", vlessLink(1))
	_, _ = c.Refresh(context.Background(), "auto")
	if st, _ := c.Status(); !st.Active.Present {
		t.Fatal("узел вернулся в источник, но помечен пропавшим")
	}
}

func idOf(t *testing.T, link string) string {
	t.Helper()
	nodes := parseLinks([]string{link})
	if len(nodes) != 1 {
		t.Fatalf("ссылка не разобралась: %s", link)
	}
	return nodes[0].ID
}

// Сбой источника (ошибка, устаревшая копия, пустой ответ, мусор) не меняет присутствие его узлов.
func TestFailedSourceKeepsPresence(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2))
	c := refreshCore(t, f, "A")
	_, _ = c.Refresh(context.Background(), "auto")

	cases := map[string]func(){
		"ошибка сети":        func() { f.set("A"); f.errs["A"] = errors.New("timeout") },
		"устаревший кэш":     func() { f.set("A", vlessLink(1)); f.cache["A"] = true },
		"пустой ответ":       func() { f.set("A") },
		"только мусор":       func() { f.set("A", "vless://битая") },
		"HTML вместо ссылок": func() { f.set("A", "http://example.com/") },
	}
	for name, setup := range cases {
		setup()
		rep, err := c.Refresh(context.Background(), "auto")
		if err != nil {
			t.Fatal(err)
		}
		if rep.Sources[0].Err == "" {
			t.Fatalf("%s: сбой не зафиксирован", name)
		}
		if rep.Present != 2 {
			t.Fatalf("%s: узлы «пропали» из-за сбоя (доступно %d из 2)", name, rep.Present)
		}
		srcs, _ := c.st.Sources()
		if srcs[0].LastErr == "" || srcs[0].LastCount != 2 {
			t.Fatalf("%s: статистика источника: %+v", name, srcs[0])
		}
	}
	f.set("A", vlessLink(1), vlessLink(2))
	if rep, _ := c.Refresh(context.Background(), "auto"); rep.Sources[0].Err != "" {
		t.Fatalf("успешная загрузка после сбоев: %+v", rep.Sources[0])
	}
	if srcs, _ := c.st.Sources(); srcs[0].LastErr != "" {
		t.Fatal("ошибка не сброшена после успеха")
	}
}

func TestRefreshBusy(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1))
	f.gate = make(chan struct{})
	c := refreshCore(t, f, "A")
	done := make(chan error, 1)
	go func() { _, err := c.Refresh(context.Background(), "auto"); done <- err }()
	time.Sleep(100 * time.Millisecond)
	if _, err := c.Refresh(context.Background(), "user"); !errors.Is(err, ErrBusy) {
		t.Fatalf("ожидали ErrBusy, получили %v", err)
	}
	close(f.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.Refresh(context.Background(), "user"); err != nil {
		t.Fatalf("после завершения обновление снова доступно: %v", err)
	}
}

// Давно пропавшие узлы вычищаются, но активный, избранный и закреплённый остаются.
func TestPurgeKeepsNeededNodes(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2), vlessLink(3), vlessLink(4))
	var clock atomic.Int64 // фоновые циклы читают время, пока тест его двигает: обычная переменная давала гонку
	clock.Store(time.Now().UnixNano())
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Fetch: f.fetch, NodeRetention: time.Hour,
		SeedSources: []aggregator.Source{{Name: "A", URL: "http://a.invalid/"}}, Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Start(context.Background())
	defer c.Close()
	_, _ = c.Refresh(context.Background(), "auto")
	id := func(n int) string { return parseLinks([]string{vlessLink(n)})[0].ID }
	_ = c.Connect(id(1), "user")
	_ = c.SetFavorite(id(2), true, "user")
	_ = c.SetPinned(id(3), true, "user")

	clock.Add(int64(2 * time.Hour))
	f.set("A", vlessLink(9)) // все прежние пропали, и прошло больше часа
	if _, err := c.Refresh(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int]bool{1: true, 2: true, 3: true, 4: false, 9: true} {
		if _, err := c.st.Node(id(n)); (err == nil) != want {
			t.Fatalf("узел %d: существует=%v, ожидалось %v", n, err == nil, want)
		}
	}
}

func TestSourceManagement(t *testing.T) {
	f := newFeed()
	c := refreshCore(t, f, "A")
	if err := c.AddSource("mine", "https://x.invalid/sub?token=секрет", "", 0, "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddSource("", "x", "", 0, "user"); err == nil {
		t.Fatal("пустое имя")
	}
	if err := c.SetSourceEnabled("A", false, "user"); err != nil {
		t.Fatal(err)
	}
	f.set("A", vlessLink(1))
	f.set("mine", vlessLink(2))
	rep, _ := c.Refresh(context.Background(), "user")
	if len(rep.Sources) != 1 || rep.Sources[0].Name != "mine" {
		t.Fatalf("выключенный источник не должен загружаться: %+v", rep.Sources)
	}
	// перезапуск засевает источники заново, но не включает выключенный
	if err := c.seedSources(); err != nil {
		t.Fatal(err)
	}
	srcs, _ := c.st.Sources()
	for _, s := range srcs {
		if s.ID == "A" && s.Enabled {
			t.Fatal("засев включил выключенный источник")
		}
		if s.ID == "mine" && !s.UserAdded {
			t.Fatal("пометка пользовательского источника потеряна")
		}
	}
	if err := c.RemoveSource("mine", "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveSource("mine", "user"); err == nil {
		t.Fatal("повторное удаление")
	}
}

// P3: источник, не загрузившийся напрямую, повторно качается через активный узел; без активного узла — остаётся сбоем.
func TestFetchFallsBackToActiveNode(t *testing.T) {
	var viaCalls []string
	var viaClient *http.Client
	setup := func(c *Core) {
		c.directFetch = func(_ context.Context, srcs []aggregator.Source, _ time.Duration, _ string) <-chan aggregator.Result {
			out := make(chan aggregator.Result, len(srcs))
			for _, s := range srcs {
				if s.Name == "blocked" {
					out <- aggregator.Result{Source: s, Err: errors.New("connection reset")}
				} else if s.Name == "stale" {
					out <- aggregator.Result{Source: s, FromCache: true, Links: []string{vlessLink(9)}}
				} else {
					out <- aggregator.Result{Source: s, Links: []string{vlessLink(1)}}
				}
			}
			close(out)
			return out
		}
		c.viaFetch = func(_ context.Context, srcs []aggregator.Source, _ time.Duration, _ string, cl *http.Client) <-chan aggregator.Result {
			out := make(chan aggregator.Result, len(srcs))
			viaClient = cl
			for _, s := range srcs {
				viaCalls = append(viaCalls, s.Name)
				out <- aggregator.Result{Source: s, Links: []string{vlessLink(2), vlessLink(3)}}
			}
			close(out)
			return out
		}
	}
	mk := func() *Core {
		c := cfgCore(t, func(cfg *Config) {
			cfg.SeedSources = []aggregator.Source{{Name: "ok", URL: "http://ok.invalid/"}, {Name: "blocked", URL: "http://b.invalid/"}, {Name: "stale", URL: "http://s.invalid/"}}
		})
		setup(c)
		return c
	}

	// без активного узла запасного пути нет
	c := mk()
	rep, _ := c.Refresh(context.Background(), "user")
	bySrc := map[string]SourceReport{}
	for _, s := range rep.Sources {
		bySrc[s.Name] = s
	}
	if len(viaCalls) != 0 || bySrc["blocked"].Err == "" || bySrc["stale"].Err == "" || bySrc["ok"].Err != "" {
		t.Fatalf("без активного узла: via=%v отчёт=%+v", viaCalls, rep.Sources)
	}

	// с активным — не загрузившиеся (ошибка и устаревшая копия) идут через узел, остальные нет
	c2 := mk()
	_ = c2.Connect(addNode(t, c2, "active"), "user")
	viaCalls = nil
	rep, _ = c2.Refresh(context.Background(), "user")
	bySrc = map[string]SourceReport{}
	for _, s := range rep.Sources {
		bySrc[s.Name] = s
	}
	if len(viaCalls) != 2 {
		t.Fatalf("через узел должны пойти 2 источника, пошли %v", viaCalls)
	}
	if b := bySrc["blocked"]; b.Err != "" || !b.ViaActive || b.Nodes != 2 {
		t.Fatalf("blocked: %+v", b)
	}
	if o := bySrc["ok"]; o.ViaActive || o.Err != "" {
		t.Fatalf("ok не должен идти через узел: %+v", o)
	}
	// клиент идёт именно через наш app-вход с учётными данными
	tr, _ := viaClient.Transport.(*http.Transport)
	pu, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "x.example"}})
	addr, user, pass := c2.AppProxy()
	if err != nil || pu.Host != addr || pu.User.Username() != user {
		t.Fatalf("прокси клиента: %v (ожидали %s)", pu, addr)
	}
	if p, _ := pu.User.Password(); p != pass {
		t.Fatal("пароль прокси")
	}
}

// Хранилище не растёт без предела: старые проверки, журнал и лог движка чистятся после обновления.
func TestHousekeepingBoundsStorage(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1))
	now := time.Now()
	c := cfgCore(t, func(cfg *Config) {
		cfg.Fetch = f.fetch
		cfg.SeedSources = []aggregator.Source{{Name: "A", URL: "http://a.invalid/"}}
		cfg.EventRetention = 20
		cfg.LogMaxBytes = 1000
		cfg.ProbeRetention = 24 * time.Hour
		cfg.Now = func() time.Time { return now }
	})
	_ = c.st.Record([]store.Row{
		{Node: "old", Net: "n", Level: 2, OK: true, At: now.Add(-48 * time.Hour)},
		{Node: "new", Net: "n", Level: 2, OK: true, At: now.Add(-time.Hour)},
	})
	for i := 0; i < 100; i++ {
		c.bus.Publish(store.Event{Kind: EvNode})
	}
	logPath := filepath.Join(c.cfg.StateDir, "engine.log")
	if err := os.WriteFile(logPath, []byte(strings.Repeat("строка лога\n", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Refresh(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	if h, _ := c.st.Histories("n", now); len(h) != 1 {
		t.Fatalf("старые результаты проверок не удалены: %+v", h)
	}
	evs, _ := c.st.Events(0, 0)
	if len(evs) > 21 {
		t.Fatalf("журнал не обрезан: %d событий", len(evs))
	}
	if st, _ := os.Stat(logPath); st.Size() != 0 {
		t.Fatalf("лог не ротирован: %d байт", st.Size())
	}
	old, err := os.Stat(logPath + ".1")
	if err != nil || old.Size() == 0 || (posixPerms && old.Mode().Perm() != 0o600) {
		t.Fatalf("копия лога: %v %v", old, err)
	}
}

// Сведения источника сохраняются, а автоматическое обновление уважает его период; ручное — нет.
func TestSourceMetaAndUpdateInterval(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1))
	now := time.Now()
	c := cfgCore(t, func(cfg *Config) {
		cfg.SeedSources = []aggregator.Source{{Name: "A", URL: "http://a.invalid/"}}
		cfg.Now = func() time.Time { return now }
	})
	var calls int
	c.cfg.Fetch = func(ctx context.Context, srcs []aggregator.Source, d time.Duration, dir string) <-chan aggregator.Result {
		calls += len(srcs)
		out := make(chan aggregator.Result, len(srcs))
		for _, s := range srcs {
			out <- aggregator.Result{Source: s, Links: []string{vlessLink(1)},
				Meta: aggregator.Meta{Title: "Мой профиль", UpdateHours: 6, Upload: 100, Download: 200, Total: 1000, Expire: now.Add(30 * 24 * time.Hour).Unix()}}
		}
		close(out)
		return out
	}
	if _, err := c.Refresh(context.Background(), "auto"); err != nil || calls != 1 {
		t.Fatalf("первое обновление: calls=%d %v", calls, err)
	}
	vs, _ := c.Sources()
	if v := vs[0]; v.Title != "Мой профиль" || v.UpdateHours != 6 || v.UsedBytes != 300 || v.TotalBytes != 1000 || v.Expire.IsZero() {
		t.Fatalf("сведения источника: %+v", v)
	}
	now = now.Add(time.Hour)
	_, _ = c.Refresh(context.Background(), "auto")
	if calls != 1 {
		t.Fatalf("авто-обновление раньше периода источника (6 ч): calls=%d", calls)
	}
	_, _ = c.Refresh(context.Background(), "user")
	if calls != 2 {
		t.Fatalf("ручное обновление должно игнорировать период: calls=%d", calls)
	}
	now = now.Add(7 * time.Hour)
	_, _ = c.Refresh(context.Background(), "auto")
	if calls != 3 {
		t.Fatalf("после периода источник обновляется: calls=%d", calls)
	}
}

// Источник ответил «не изменилось»: ничего не разбирается и не пишется заново, но узлы остаются присутствующими; изменение потом видно.
func TestRefreshNotModifiedFastPath(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2), vlessLink(3))
	c := refreshCore(t, f, "A")
	if _, err := c.Refresh(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	if f.known["A"] {
		t.Fatal("при первой загрузке источник не может быть «известным»")
	}
	f.notmod["A"] = true
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if !f.known["A"] {
		t.Fatal("после удачного разбора источник должен помечаться известным (быстрый путь)")
	}
	if a := rep.Sources[0]; !a.NotModified || a.Err != "" || a.Nodes != 3 || a.Lost != 0 {
		t.Fatalf("быстрый путь: %+v", a)
	}
	if rep.Present != 3 || rep.Total != 3 || rep.Added != 0 {
		t.Fatalf("узлы должны остаться присутствующими: %+v", rep)
	}
	// ещё раз подряд: метка присутствия обновляется, узлы не «пропадают» от обновления к обновлению
	if rep2, _ := c.Refresh(context.Background(), "user"); rep2.Present != 3 {
		t.Fatalf("третье обновление: %+v", rep2)
	}
	// источник изменился: узел 3 пропал, он помечается пропавшим
	f.notmod["A"] = false
	f.set("A", vlessLink(1), vlessLink(2))
	rep3, err := c.Refresh(context.Background(), "user")
	if err != nil || rep3.Sources[0].Lost != 1 || rep3.Present != 2 {
		t.Fatalf("после изменения: %+v %v", rep3, err)
	}
}

// База не подтверждает «не изменилось» (узлов источника нет — база заменена или восстановлена): источник загружается и разбирается полностью.
func TestRefreshNotModifiedFallsBackWhenDatabaseLostNodes(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2), vlessLink(3), vlessLink(4))
	c := refreshCore(t, f, "A")
	if _, err := c.Refresh(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.st.EndRefresh("A", time.Now().Add(time.Hour).UnixNano()); err != nil { // все узлы источника «пропали» из базы
		t.Fatal(err)
	}
	f.notmod["A"] = true
	before := f.calls
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if f.calls-before != 2 {
		t.Fatalf("нужна повторная полная загрузка источника: вызовов загрузчика %d", f.calls-before)
	}
	if rep.Present != 4 || len(rep.Sources) != 1 || rep.Sources[0].Err != "" {
		t.Fatalf("узлы должны вернуться: %+v", rep)
	}
}

// Версия разбора изменилась: «не изменилось» больше не отменяет разбор (иначе в базе остались бы узлы, разобранные старой версией).
func TestRefreshIngestRevisionForcesFullIngest(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2))
	c := refreshCore(t, f, "A")
	if _, err := c.Refresh(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := c.st.Setting(settingIngestRev); !ok || v != ingestRev {
		t.Fatalf("версия разбора не записана: %q %v", v, ok)
	}
	_ = c.st.SetSetting(settingIngestRev, "старая")
	f.notmod["A"] = true
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil || f.known["A"] {
		t.Fatalf("при смене версии быстрый путь не должен включаться: known=%v err=%v", f.known["A"], err)
	}
	if rep.Sources[0].Nodes != 2 || rep.Present != 2 {
		t.Fatalf("%+v", rep)
	}
	if v, _, _ := c.st.Setting(settingIngestRev); v != ingestRev {
		t.Fatalf("после полного разбора версия должна обновиться: %q", v)
	}
}

// Много источников разбираются параллельно, и ни один не теряется и не дублируется в отчёте.
func TestRefreshParallelParsingKeepsEverySource(t *testing.T) {
	f := newFeed()
	var names []string
	n := 0
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("S%02d", i)
		names = append(names, name)
		var links []string
		for j := 0; j < 40; j++ {
			n++
			links = append(links, vlessLink(n))
		}
		f.set(name, links...)
	}
	c := refreshCore(t, f, names...)
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sources) != 30 || rep.Total != 1200 || rep.Present != 1200 {
		t.Fatalf("источников %d, узлов %d/%d", len(rep.Sources), rep.Total, rep.Present)
	}
	seen := map[string]bool{}
	for _, s := range rep.Sources {
		if seen[s.Name] || s.Nodes != 40 || s.Err != "" {
			t.Fatalf("источник %s: %+v (повтор=%v)", s.Name, s, seen[s.Name])
		}
		seen[s.Name] = true
	}
}

const (
	tgMT1  = "https://t.me/proxy?server=one.example.&port=443&secret=ee00112233445566778899aabbccddeeff6578616d706c652e636f6d"
	tgMT2  = "tg://proxy?server=two.example&port=8443&secret=dd10400103324995b07c030386e886e7f1"
	tgSock = "https://t.me/socks?server=three.example&port=1080&user=u&pass=p"
	tgWeb  = "https://t.me/webproxy?server=web.example.com&secret=000102030405060708090a0b0c0d0e0f"
)

// Источник, в котором только прокси Telegram: не сбой, прокси попадают в базу (вместе с дубликатами схлопнутыми и мусором пропущенным),
// узлы обычных источников не затронуты; повторное «не изменилось» подтверждает присутствие без повторной загрузки; изменение видно.
func TestRefreshIngestsTelegramProxies(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2))
	f.set("T")
	f.tg["T"] = []string{tgMT1, tgMT1, tgMT2, tgSock, tgWeb, "tg://proxy?server=&port=1&secret=00", "https://t.me/proxy?server=x.example&port=99999&secret=ee00"}
	c := refreshCore(t, f, "A", "T")
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SourceReport{}
	for _, s := range rep.Sources {
		by[s.Name] = s
	}
	if by["T"].Err != "" || by["T"].TGProxies != 4 || by["T"].Nodes != 0 || by["A"].Nodes != 2 || by["A"].TGProxies != 0 {
		t.Fatalf("отчёт: T=%+v A=%+v", by["T"], by["A"])
	}
	if rep.Present != 2 {
		t.Fatalf("узлы A не должны пострадать: %+v", rep)
	}
	l, err := c.TGProxies(false, false, "", 0)
	if err != nil || len(l.Items) != 4 || l.Total != 4 {
		t.Fatalf("список: %+v %v", l, err)
	}
	modes := map[string]string{}
	for _, it := range l.Items {
		modes[it.Server] = it.Kind + "/" + it.Mode
		if !strings.HasPrefix(it.Link, "tg://") {
			t.Fatalf("ссылка для открытия: %q", it.Link)
		}
	}
	if modes["one.example"] != "mtproto/faketls" || modes["two.example"] != "mtproto/secure" || modes["three.example"] != "socks5/" || modes["web.example.com"] != "webproxy/" {
		t.Fatalf("виды: %v", modes)
	}
	if only, _ := c.TGProxies(false, false, "socks5", 0); len(only.Items) != 1 {
		t.Fatalf("фильтр по виду: %+v", only)
	}

	// «не изменилось»: источник только с прокси не требует повторной полной загрузки
	f.notmod["T"], f.notmod["A"] = true, true
	before := f.calls
	rep2, err := c.Refresh(context.Background(), "user")
	if err != nil || f.calls-before != 1 {
		t.Fatalf("быстрый путь: вызовов загрузчика %d, ошибка %v, отчёт %+v", f.calls-before, err, rep2)
	}
	if l2, _ := c.TGProxies(false, false, "", 0); len(l2.Items) != 4 {
		t.Fatalf("прокси должны остаться: %+v", l2)
	}

	// источник изменился: один прокси пропал и не показывается
	f.notmod["T"], f.notmod["A"] = false, false
	f.tg["T"] = []string{tgMT1, tgSock, tgWeb}
	if _, err := c.Refresh(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	if l3, _ := c.TGProxies(false, false, "", 0); len(l3.Items) != 3 {
		t.Fatalf("пропавший прокси не должен показываться: %+v", l3)
	}
	// источник вовсе пустой — это сбой, а не «все прокси пропали»
	f.tg["T"] = nil
	rep4, _ := c.Refresh(context.Background(), "user")
	for _, s := range rep4.Sources {
		if s.Name == "T" && s.Err == "" {
			t.Fatal("пустой источник должен считаться сбоем")
		}
	}
	if l4, _ := c.TGProxies(false, false, "", 0); len(l4.Items) != 3 {
		t.Fatalf("пустой ответ не должен убивать прокси: %+v", l4)
	}
}

func TestTGProxiesCheckOwnAndFavorites(t *testing.T) {
	alive := map[string]bool{"one.example": true, "three.example": true}
	var calls int
	var mu sync.Mutex
	c := cfgCore(t, func(cfg *Config) {
		cfg.TGProbe = func(_ context.Context, p tgproxy.Proxy) tgproxy.Result {
			mu.Lock()
			calls++
			mu.Unlock()
			if alive[p.Server] {
				return tgproxy.Result{OK: true, LatencyMs: 300}
			}
			return tgproxy.Result{Class: "timeout"}
		}
	})
	if n, err := c.AddTGProxies("мусор без ссылок", "user"); err == nil || n != 0 {
		t.Fatalf("без ссылок — ошибка: %d %v", n, err)
	}
	n, err := c.AddTGProxies(tgMT1+"\n"+tgMT2+" и ещё "+tgSock+" <a href=\""+tgMT1+"\">", "user")
	if err != nil || n != 3 {
		t.Fatalf("добавлено %d: %v", n, err)
	}
	l, _ := c.TGProxies(false, false, "", 0)
	if len(l.Items) != 3 {
		t.Fatalf("свои: %+v", l)
	}
	for _, it := range l.Items {
		if !it.Own || !it.Favorite || it.Checked {
			t.Fatalf("свой прокси: избранный, ещё не проверен: %+v", it)
		}
	}
	rep, err := c.CheckTGProxies(context.Background(), 0)
	if err != nil || rep.Picked != 3 || rep.OK != 2 || calls != 3 {
		t.Fatalf("проверка: %+v %v вызовов %d", rep, err, calls)
	}
	live, _ := c.TGProxies(true, false, "", 0)
	if len(live.Items) != 2 || live.Alive != 2 || live.Total != 3 {
		t.Fatalf("живые: %+v", live)
	}
	for _, it := range live.Items {
		if !it.OK || it.LatencyMs != 300 || !it.Checked {
			t.Fatalf("результат не записан: %+v", it)
		}
	}
	// удаление своего и избранное
	var two string
	for _, it := range l.Items {
		if it.Server == "two.example" {
			two = it.ID
		}
	}
	if err := c.SetTGFavorite(two, false); err != nil {
		t.Fatal(err)
	}
	if favs, _ := c.TGProxies(false, true, "", 0); len(favs.Items) != 2 {
		t.Fatalf("избранные: %+v", favs)
	}
	if err := c.DeleteTGProxy(two); err != nil {
		t.Fatal(err)
	}
	if after, _ := c.TGProxies(false, false, "", 0); len(after.Items) != 2 {
		t.Fatalf("после удаления: %+v", after)
	}
	if err := c.DeleteTGProxy("нет-такого"); err == nil {
		t.Fatal("удаление несуществующего должно сообщать об ошибке")
	}
}

// Пока идёт обновление подписок, демон сообщает об этом (индикация в приложении), по окончании — снимает.
func TestActivityShowsRefreshing(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1))
	f.gate = make(chan struct{})
	c := refreshCore(t, f, "A")
	if c.Activity().Refreshing {
		t.Fatal("до обновления активности нет")
	}
	done := make(chan struct{})
	go func() { _, _ = c.Refresh(context.Background(), "user"); close(done) }()
	waitFor(t, "обновление идёт", func() bool { return c.Activity().Refreshing })
	close(f.gate)
	<-done
	if c.Activity().Refreshing {
		t.Fatal("после обновления активность должна сниматься")
	}
}

// Источник, из которого за неделю не ожил ни один из ≥50 проверенных узлов, автоматически обновляется не чаще раза в сутки; ручное обновление,
// источники пользователя и живые источники — как обычно.
func TestDeadWeightSourceIsRefreshedRarely(t *testing.T) {
	now := time.Now()
	c := cfgCore(t, func(cfg *Config) {
		cfg.SeedSources = []aggregator.Source{{Name: "dead", URL: "http://d.invalid/"}, {Name: "live", URL: "http://l.invalid/"}}
		cfg.Now = func() time.Time { return now }
	})
	var links []string
	for i := 1; i <= 60; i++ {
		links = append(links, vlessLink(i))
	}
	for i := 101; i <= 160; i++ {
		links = append(links, vlessLink(i))
	}
	fetched := map[string]int{}
	c.cfg.Fetch = func(ctx context.Context, srcs []aggregator.Source, d time.Duration, dir string) <-chan aggregator.Result {
		out := make(chan aggregator.Result, len(srcs))
		for _, s := range srcs {
			fetched[s.Name]++
			l := links[:60]
			if s.Name == "live" {
				l = links[60:]
			}
			out <- aggregator.Result{Source: s, Links: l}
		}
		close(out)
		return out
	}
	if _, err := c.Refresh(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	// все 120 узлов проверены; в «live» один ответил, в «dead» никто
	nodes, _ := c.st.Nodes(store.NodeFilter{OnlyPresent: true})
	var rows []store.Row
	for _, n := range nodes {
		ok := n.Server == "h101.example"
		rows = append(rows, store.Row{Node: n.ID, NodeClass: n.Class, Net: "n", Level: 2, OK: ok, At: now})
	}
	if err := c.st.Record(rows); err != nil {
		t.Fatal(err)
	}
	// трое суток не прошло: источник новый, рейтинг его не трогает
	now = now.Add(2 * time.Hour)
	c.yieldAt = time.Time{}
	_, _ = c.Refresh(context.Background(), "auto")
	if fetched["dead"] != 2 {
		t.Fatalf("новый источник пропущен: %v", fetched)
	}
	now = now.Add(4 * 24 * time.Hour)
	c.yieldAt = time.Time{}
	_, _ = c.Refresh(context.Background(), "auto")
	if fetched["dead"] != 3 { // последний успех был >24 ч назад: обновляется
		t.Fatalf("после суток источник обновляется: %v", fetched)
	}
	now = now.Add(time.Hour)
	c.yieldAt = time.Time{}
	_, _ = c.Refresh(context.Background(), "auto")
	if fetched["dead"] != 3 || fetched["live"] != 4 {
		t.Fatalf("мёртвый груз пропускается, живой нет: %v", fetched)
	}
	vs, _ := c.Sources()
	for _, v := range vs {
		switch v.Name {
		case "dead":
			if !v.Rarely || v.Checked != 60 || v.Alive != 0 {
				t.Fatalf("мёртвый источник в списке: %+v", v)
			}
		case "live":
			if v.Rarely || v.Alive != 1 {
				t.Fatalf("живой источник в списке: %+v", v)
			}
		}
	}
	_, _ = c.Refresh(context.Background(), "user")
	if fetched["dead"] != 4 {
		t.Fatalf("ручное обновление не пропускает: %v", fetched)
	}
}

// общий потолок узлов в базе: лишние вытесняются после обновления, активный и избранный остаются.
func TestRefreshCapsTotalNodes(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2), vlessLink(3))
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Fetch: f.fetch, MaxNodes: 5,
		SeedSources: []aggregator.Source{{Name: "A", URL: "http://a.invalid/"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Start(context.Background())
	defer c.Close()
	_, _ = c.Refresh(context.Background(), "auto")
	id := func(n int) string { return parseLinks([]string{vlessLink(n)})[0].ID }
	_ = c.Connect(id(1), "user")
	_ = c.SetFavorite(id(2), true, "user")

	var links []string
	for i := 1; i <= 8; i++ {
		links = append(links, vlessLink(i))
	}
	f.set("A", links...)
	if _, err := c.Refresh(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	if total, _, _ := c.st.CountNodes(); total != 5 {
		t.Fatalf("в базе %d узлов, потолок 5", total)
	}
	for _, n := range []int{1, 2} {
		if _, err := c.st.Node(id(n)); err != nil {
			t.Fatalf("защищённый узел %d вытеснен: %v", n, err)
		}
	}
}
