package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brod/aggregator"
	"brod/internal/testport"
	"brod/store"
)

func nextPort() int { return testport.Next() }

func newCore(t *testing.T, dir string) *Core {
	t.Helper()
	c, err := New(Config{StateDir: dir, AppPort: nextPort(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startCore(t *testing.T, dir string) *Core {
	t.Helper()
	c := newCore(t, dir)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// addNode кладёт в базу узел-direct (в тестах узлы отличаются тегами и жизненным циклом, а не маршрутом).
func addNode(t *testing.T, c *Core, key string) string {
	t.Helper()
	id := store.NodeID(key)
	err := c.st.UpsertNodes("src", time.Now().UnixNano(), []store.NodeRec{{
		ID: id, Class: "direct/tcp/none", Proto: "direct", Server: key + ".example", Port: 443, Opts: []byte(`{"type":"direct"}`),
	}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func lastEvent(t *testing.T, c *Core, kind string) store.Event {
	t.Helper()
	evs, _ := c.st.LastEvents(200)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == kind {
			return evs[i]
		}
	}
	t.Fatalf("события %q нет в журнале", kind)
	return store.Event{}
}

func activeTag(t *testing.T, c *Core) string {
	t.Helper()
	tag, err := c.eng.SwitchTarget(OutActive)
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

func TestConnectSwitchesAndCollectsOldNode(t *testing.T) {
	c := startCore(t, t.TempDir())
	a, b := addNode(t, c, "a"), addNode(t, c, "b")

	if got := activeTag(t, c); got != "" {
		t.Fatalf("до выбора цели быть не должно: %q", got)
	}
	if err := c.Connect(a, "user"); err != nil {
		t.Fatal(err)
	}
	if activeTag(t, c) != NodeTag(a) || !c.eng.Has(NodeTag(a)) {
		t.Fatal("a не стал активным")
	}
	if err := c.Connect(b, "user"); err != nil {
		t.Fatal(err)
	}
	if activeTag(t, c) != NodeTag(b) {
		t.Fatal("b не стал активным")
	}
	if c.eng.Has(NodeTag(a)) {
		t.Fatal("прежний узел остался в движке (утечка)")
	}
	sel, err := c.st.Selection(store.ScopeActive)
	if err != nil || sel.Node != b || sel.By != "user" {
		t.Fatalf("выбор не сохранён: %+v %v", sel, err)
	}
	if ev := lastEvent(t, c, EvSwitch); ev.Node != b || ev.Initiator != "user" {
		t.Fatalf("событие: %+v", ev)
	}
	if err := c.Connect(b, "user"); err != nil { // повторный выбор того же узла — не ошибка
		t.Fatal(err)
	}
	st, _ := c.Status()
	if st.Active == nil || st.Active.ID != b || st.ActiveBy != "user" {
		t.Fatalf("статус: %+v", st)
	}
}

// Неудачный выбор не трогает прежний активный узел.
func TestFailedConnectKeepsActive(t *testing.T) {
	c := startCore(t, t.TempDir())
	a := addNode(t, c, "a")
	if err := c.Connect(a, "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(store.NodeID("нет такого"), "user"); err == nil {
		t.Fatal("несуществующий узел")
	}
	bad := store.NodeID("bad")
	_ = c.st.UpsertNodes("src", 1, []store.NodeRec{{ID: bad, Opts: []byte(`{"type":"нет-такого-протокола"}`)}}, time.Now())
	if err := c.Connect(bad, "user"); err == nil {
		t.Fatal("битые опции должны отклоняться")
	}
	if activeTag(t, c) != NodeTag(a) || !c.eng.Has(NodeTag(a)) {
		t.Fatal("после неудачных выборов активный узел изменился")
	}
	if sel, _ := c.st.Selection(store.ScopeActive); sel.Node != a {
		t.Fatalf("сохранённый выбор изменился: %+v", sel)
	}
	if c.eng.Has(NodeTag(bad)) {
		t.Fatal("битый узел остался зарегистрированным")
	}
	if ev := lastEvent(t, c, EvConnectFailed); ev.Node != bad {
		t.Fatalf("событие: %+v", ev)
	}
}

// Выбор «липкий» между запусками: после перезапуска активным становится тот же узел, без участия пользователя.
func TestSelectionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	_ = c.Start(context.Background())
	a := addNode(t, c, "a")
	addNode(t, c, "other")
	_ = c.Connect(a, "user")
	u1, p1 := c.appUser, c.appPass
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := startCore(t, dir)
	if activeTag(t, c2) != NodeTag(a) {
		t.Fatalf("выбор не восстановлен: %q", activeTag(t, c2))
	}
	if ev := lastEvent(t, c2, EvRestored); ev.Node != a {
		t.Fatalf("событие: %+v", ev)
	}
	if sel, _ := c2.st.Selection(store.ScopeActive); sel.By != "user" {
		t.Fatalf("восстановление не должно переписывать автора выбора: %+v", sel)
	}
	if c2.appUser != u1 || c2.appPass != p1 {
		t.Fatal("учётные данные app-входа меняются между запусками")
	}
}

// Сохранённый узел не поднимается — active остаётся без цели, подмены другим узлом нет (P1).
func TestRestoreFailureDoesNotAutoPick(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	_ = c.Start(context.Background())
	_ = c.st.SetSelection(store.Selection{Scope: store.ScopeActive, Node: store.NodeID("исчез"), By: "user"})
	addNode(t, c, "healthy") // рабочий узел есть, но выбирать его никто не просил
	_ = c.Close()

	c2 := startCore(t, dir)
	if got := activeTag(t, c2); got != "" {
		t.Fatalf("автовыбор запрещён, а цель %q", got)
	}
	if ev := lastEvent(t, c2, EvRestoreFailed); ev.Node != store.NodeID("исчез") {
		t.Fatalf("событие: %+v", ev)
	}
	if st, _ := c2.Status(); st.Active != nil {
		t.Fatalf("статус показывает активный узел: %+v", st.Active)
	}
}

// App-вход проксирует через активный узел; без узла — закрыт (fail closed).
func TestAppProxyUsesActive(t *testing.T) {
	c := startCore(t, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	addr, user, pass := c.AppProxy()
	get := func() (string, error) {
		pu, _ := url.Parse("http://" + addr)
		pu.User = url.UserPassword(user, pass)
		cl := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
		resp, err := cl.Get(srv.URL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return string(b), nil
	}
	if _, err := get(); err == nil {
		t.Fatal("без активного узла трафик не должен идти (fail closed)")
	}
	_ = c.Connect(addNode(t, c, "a"), "user")
	if body, err := get(); err != nil || body != "ok" {
		t.Fatalf("через активный узел: %q %v", body, err)
	}
}

func TestStatePermissionsAndSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	c := startCore(t, dir)
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "state.db"): 0o600, filepath.Join(dir, "engine.log"): 0o600} {
		st, err := os.Stat(p)
		if err != nil || (posixPerms && st.Mode().Perm() != want) {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	for _, k := range []string{SetAppUser, SetAppPass, SetAPIToken} {
		if v, ok, _ := c.st.Setting(k); !ok || len(v) < 8 {
			t.Fatalf("секрет %s не создан", k)
		}
	}
}

func TestBusFanoutAndLag(t *testing.T) {
	c := startCore(t, t.TempDir())
	fast := c.bus.Subscribe(16)
	slow := c.bus.Subscribe(1)
	defer fast.Close()
	for i := 0; i < 5; i++ {
		c.bus.Publish(store.Event{Kind: EvNode, Detail: fmt.Sprint(i)})
	}
	if len(fast.C) != 5 {
		t.Fatalf("быстрый подписчик получил %d из 5", len(fast.C))
	}
	if !slow.Lagged() {
		t.Fatal("медленный подписчик должен получить признак отставания")
	}
	if slow.Lagged() {
		t.Fatal("признак сбрасывается после чтения")
	}
	slow.Close()
	c.bus.Publish(store.Event{Kind: EvNode}) // не должно паниковать на закрытой подписке
	evs, _ := c.EventsAfter(0, 0)
	if len(evs) < 6 {
		t.Fatalf("журнал: %d событий", len(evs))
	}
}

func TestAppTokensAndAuthentication(t *testing.T) {
	c := startCore(t, t.TempDir())
	master := c.APIToken()
	if who, ok := c.Authenticate(master); !ok || who != "user" {
		t.Fatalf("главный токен: %q %v", who, ok)
	}
	tok, err := c.CreateAppToken("myapp", "user")
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := c.Authenticate(tok); !ok || who != "api:myapp" {
		t.Fatalf("токен приложения: %q %v", who, ok)
	}
	for _, bad := range []string{"", "x", tok + "x", master + "x"} {
		if _, ok := c.Authenticate(bad); ok {
			t.Fatalf("токен %q принят", bad)
		}
	}
	// в базе — только хэш
	all, _ := c.st.Settings()
	for k, v := range all {
		if v == tok {
			t.Fatalf("токен приложения хранится открыто в %s", k)
		}
	}
	if _, err := c.CreateAppToken("Плохое Имя", "user"); err == nil {
		t.Fatal("имя приложения не проверяется")
	}
	if names, _ := c.AppTokens(); len(names) != 1 || names[0] != "myapp" {
		t.Fatalf("список: %v", names)
	}
	tok2, _ := c.CreateAppToken("myapp", "user") // перевыпуск заменяет
	if _, ok := c.Authenticate(tok); ok {
		t.Fatal("старый токен остался действующим после перевыпуска")
	}
	if _, ok := c.Authenticate(tok2); !ok {
		t.Fatal("новый токен не принят")
	}
	if err := c.RevokeAppToken("myapp", "user"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Authenticate(tok2); ok {
		t.Fatal("отозванный токен действует")
	}
	if err := c.RevokeAppToken("myapp", "user"); err == nil {
		t.Fatal("повторный отзыв")
	}
}

func TestSettingsWhitelistAndLogsRedaction(t *testing.T) {
	c := startCore(t, t.TempDir())
	if err := c.SetSetting("security.hide_insecure", "1", "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting("security.hide_insecure", "да", "user"); err == nil {
		t.Fatal("значение не проверяется")
	}
	if err := c.SetSetting(SetAutoGroup, "1", "user"); err == nil || c.AutoGroupEnabled() {
		t.Fatal("авто-группа не реализована: включение должно отклоняться")
	}
	for _, k := range []string{SetAPIToken, SetAppPass, "api.app.x", "что-то"} {
		if err := c.SetSetting(k, "x", "user"); err == nil {
			t.Fatalf("секретная или неизвестная настройка %q доступна через SetSetting", k)
		}
	}
	if s, _ := c.Settings(); len(s) != 20 || s[SetProxyLAN] != "0" || s[SetFakeIP] != "0" || s[SetUpdateCheck] != "1" || s[SetOTASources] != "1" || s["security.hide_insecure"] != "1" || s[SetViaWarp] != "" || s[SetAutoGroup] != "" || s[SetKillswitch] != killswitchDefault {
		t.Fatalf("настройки: %v", s)
	}
	logPath := filepath.Join(c.cfg.StateDir, "engine.log")
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	fmt.Fprintln(f, "ERROR inbound/mixed[app-in]: http: authentication failed, username=bob, password=hunter2")
	for i := 0; i < 5; i++ {
		fmt.Fprintln(f, "ERROR probe-ep-13: peer(bmXO…fgyo) - failed to send data packets: address family not supported by protocol")
	}
	fmt.Fprintln(f, "ERROR dial tcp 203.0.113.7:443: connect: refused, https://secret.example/path?token=abc")
	f.Close()
	lines, err := c.Logs(50)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "address family") || !strings.Contains(joined, "скрыто однотипных строк шума: 5") {
		t.Fatalf("шум WG-проб не свёрнут:\n%s", joined)
	}
	for _, leak := range []string{"hunter2", "bob", "203.0.113.7", "secret.example", "token=abc"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("лог выдаёт %q:\n%s", leak, joined)
		}
	}
}

func TestSourcesViewHidesTokens(t *testing.T) {
	c := startCore(t, t.TempDir())
	_ = c.AddSource("mine", "https://sub.example.org/path/секрет?token=abc123", "", 0, "user")
	vs, _ := c.Sources()
	for _, v := range vs {
		if v.Name == "mine" {
			if v.Host != "sub.example.org" {
				t.Fatalf("host: %q", v.Host)
			}
			if strings.Contains(fmt.Sprintf("%+v", v), "abc123") {
				t.Fatal("токен подписки в представлении источника")
			}
			return
		}
	}
	t.Fatal("источник не найден")
}

// P3: кэшированный список виден сразу — холодный старт с большой базой и первый показ списка укладываются в секунду.
func TestColdStartWithLargeBase(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	_ = c.Start(context.Background())
	recs := make([]store.NodeRec, 30000)
	for i := range recs {
		recs[i] = store.NodeRec{ID: store.NodeID(fmt.Sprint("n", i)), Class: "vless/tcp/tls", Proto: "vless", Server: fmt.Sprintf("h%d.example", i), Port: 443,
			Opts: []byte(`{"type":"vless","server":"h.example","server_port":443,"uuid":"5b0e5c2e-0000-4000-8000-000000000001"}`)}
	}
	if err := c.st.UpsertNodes("src", 1, recs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(recs[7].ID, "user"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	t0 := time.Now()
	c2 := newCore(t, dir)
	if err := c2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	st, err := c2.Status()
	if err != nil || st.Active == nil || st.Nodes != 30000 {
		t.Fatalf("статус: %+v %v", st, err)
	}
	list, err := c2.Nodes(NodesQuery{Limit: 30})
	if err != nil || len(list) != 30 {
		t.Fatalf("список: %d %v", len(list), err)
	}
	if took := time.Since(t0); took > time.Second && !raceEnabled {
		t.Fatalf("холодный старт с показом списка занял %v (цель ≤1 с)", took)
	} else {
		t.Logf("холодный старт на 30 тыс. узлов: %v", took.Round(time.Millisecond))
	}
}

// Отбор верхушки списка без чтения всей базы даёт ровно тот же результат, что полный просмотр.
func TestNodesTopMatchesFullScan(t *testing.T) {
	c := startCore(t, t.TempDir())
	now := time.Now()
	recs := make([]store.NodeRec, 3000)
	for i := range recs {
		recs[i] = store.NodeRec{ID: store.NodeID(fmt.Sprint("n", i)), Class: "vless/tcp/tls", Proto: "vless", Server: fmt.Sprintf("h%d.example", i), Port: 443, Opts: []byte(`{"type":"direct"}`)}
	}
	// разное время «последнего появления» — иначе порядок равных не проверить
	for i := 0; i < len(recs); i += 100 {
		_ = c.st.UpsertNodes("src", 1, recs[i:i+100], now.Add(-time.Duration(i)*time.Second))
	}
	var rows []store.Row
	for i := 0; i < 3000; i += 7 {
		rows = append(rows, store.Row{Node: recs[i].ID, Net: c.netKey(), Level: 2, OK: true, At: now.Add(-time.Minute)})
		if i%21 == 0 {
			rows = append(rows, store.Row{Node: recs[i].ID, Net: c.netKey(), Level: 4, Target: "google-api", OK: true, At: now.Add(-time.Minute)})
		}
		if i%49 == 0 {
			rows = append(rows, store.Row{Node: recs[i].ID, Net: c.netKey(), Level: 2, OK: false, At: now})
		}
	}
	_ = c.st.Record(rows)
	_ = c.SetFavorite(recs[5].ID, true, "user")
	_ = c.SetPinned(recs[2999].ID, true, "user")

	for _, q := range []NodesQuery{{}, {Alive: true}, {FavoritesOnly: true}} {
		full, err := c.Nodes(q) // Limit 0 — полный просмотр
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{5, 40, 300, 5000} {
			q2 := q
			q2.Limit = limit
			top, err := c.Nodes(q2)
			if err != nil {
				t.Fatal(err)
			}
			want := full
			if len(want) > limit {
				want = want[:limit]
			}
			if len(top) != len(want) {
				t.Fatalf("%+v limit=%d: получено %d, ожидалось %d", q, limit, len(top), len(want))
			}
			for i := range want {
				if top[i].ID != want[i].ID {
					t.Fatalf("%+v limit=%d: позиция %d: %s вместо %s", q, limit, i, short(top[i].ID), short(want[i].ID))
				}
			}
		}
	}
}

// сохранённый активный узел выставляется уже в New (до Start), а не после запуска заданий и настройки DNS: пока цель пуста,
// соединения из TUN отклоняются.
func TestActiveTargetRestoredInNew(t *testing.T) {
	dir := t.TempDir()
	c := startCore(t, dir)
	id := addNode(t, c, "p4")
	if err := c.Connect(id, "user"); err != nil {
		t.Fatal(err)
	}
	c.Close()

	c2 := newCore(t, dir)
	defer c2.Close()
	c2.mu.Lock()
	got := c2.selected[store.ScopeActive]
	c2.mu.Unlock()
	if got != id {
		t.Fatalf("после New активный узел %q, ожидался %q", got, id)
	}
}
