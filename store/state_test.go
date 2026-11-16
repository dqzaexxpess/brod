package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func rec(key, proto string) NodeRec {
	return NodeRec{ID: NodeID(key), Class: proto + "/tcp/tls", Proto: proto, Server: key + ".example", Port: 443, Opts: []byte(`{"type":"` + proto + `"}`)}
}

func TestOpenPermissionsAndMigrations(t *testing.T) {
	s, path := open(t)
	for _, p := range []string{path, path + "-wal"} {
		if st, err := os.Stat(p); err == nil && runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // на Windows доступ задаёт ACL
			t.Fatalf("%s: права %v, нужны 0600", p, st.Mode().Perm())
		}
	}
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version=%d err=%v", v, err)
	}
	// повторное открытие не ломает и не откатывает
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
}

// База Ф2 (только таблица probes, user_version=0) обновляется без потери данных.
func TestMigrateFromLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO probes(node,net,level,ok,at) VALUES('x','n',2,1,100)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM probes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("старые пробы потеряны: n=%d err=%v", n, err)
	}
	if _, err := s.Sources(); err != nil {
		t.Fatalf("новые таблицы не созданы: %v", err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	db, _ := sql.Open("sqlite", path)
	if _, err := db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("база от более новой версии должна отклоняться")
	}
}

func TestSources(t *testing.T) {
	s, _ := open(t)
	now := time.Unix(1000, 0)
	if err := s.UpsertSource(SourceRec{ID: "a", URL: "https://x/1", Priority: 1, Enabled: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSource(SourceRec{ID: "b", URL: "https://x/2", Priority: 0, Enabled: true, UserAdded: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFetch("a", 42, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSourceEnabled("a", false); err != nil {
		t.Fatal(err)
	}
	// повторный Upsert меняет адрес, но не включённость и не статистику
	if err := s.UpsertSource(SourceRec{ID: "a", URL: "https://x/NEW", Priority: 2, Enabled: true}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// ошибка загрузки не затирает счётчик и время последнего успеха
	if err := s.RecordFetch("a", 0, "timeout", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Sources()
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("порядок по приоритету: %+v", list)
	}
	a := list[1]
	if a.URL != "https://x/NEW" || a.Priority != 2 || a.Enabled || a.LastCount != 42 || a.LastErr != "timeout" ||
		!a.LastOK.Equal(now.Add(time.Minute)) || !a.LastFetch.Equal(now.Add(2*time.Minute)) || !a.AddedAt.Equal(now) {
		t.Fatalf("a: %+v", a)
	}
	if !list[0].UserAdded {
		t.Fatal("user_added потерян")
	}
	if err := s.SetSourceEnabled("нет", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидали ErrNotFound, получили %v", err)
	}
}

func TestNodesRefreshAndPresence(t *testing.T) {
	s, _ := open(t)
	t0 := time.Unix(1000, 0)
	n1, n2, n3 := rec("n1", "vless"), rec("n2", "trojan"), rec("n3", "vmess")
	// n1 есть в обоих источниках, n2 только в A, n3 только в B
	if err := s.UpsertNodes("A", 1, []NodeRec{n1, n2}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNodes("B", 1, []NodeRec{n1, n3}, t0); err != nil {
		t.Fatal(err)
	}
	if lost, err := s.EndRefresh("A", 1); err != nil || lost != 0 {
		t.Fatalf("первый EndRefresh: lost=%d err=%v", lost, err)
	}
	// пользователь закрепил n2 и добавил n1 в избранное
	if err := s.SetNodeFlag(n2.ID, FlagPinned, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeFlag(n1.ID, FlagFavorite, true); err != nil {
		t.Fatal(err)
	}
	// второе обновление A: n2 пропал, n1 остался; опции n1 обновились
	n1b := n1
	n1b.Opts = []byte(`{"type":"vless","v":2}`)
	if err := s.UpsertNodes("A", 2, []NodeRec{n1b}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	lost, err := s.EndRefresh("A", 2)
	if err != nil || lost != 1 {
		t.Fatalf("пропавших должно быть 1, получили %d (%v)", lost, err)
	}
	got, err := s.Node(n2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Present || !got.Pinned {
		t.Fatalf("n2: пропал из единственного источника, но остаётся закреплённым: %+v", got)
	}
	got1, _ := s.Node(n1.ID)
	if !got1.Present || !got1.Favorite || string(got1.Opts) != `{"type":"vless","v":2}` || !got1.FirstSeen.Equal(t0) || !got1.LastSeen.Equal(t0.Add(time.Hour)) {
		t.Fatalf("n1: %+v", got1)
	}
	// n3 источник B ещё держит; пропажа в A его не касается
	if g3, _ := s.Node(n3.ID); !g3.Present {
		t.Fatal("n3 присутствует в B")
	}
	// узел вернулся в источник — снова присутствует
	if err := s.UpsertNodes("A", 3, []NodeRec{n1b, n2}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if g2, _ := s.Node(n2.ID); !g2.Present {
		t.Fatal("n2 вернулся в источник")
	}
	total, present, _ := s.CountNodes()
	if total != 3 || present != 3 {
		t.Fatalf("всего %d, присутствуют %d", total, present)
	}
	// список без Opts, с Opts — по запросу
	list, _ := s.Nodes(NodeFilter{})
	if len(list) != 3 || list[0].Opts != nil {
		t.Fatalf("список: %d узлов, opts=%v", len(list), list[0].Opts)
	}
	if one, _ := s.Nodes(NodeFilter{IDs: []string{n3.ID}, WithOpts: true}); len(one) != 1 || len(one[0].Opts) == 0 {
		t.Fatalf("WithOpts: %+v", one)
	}
	if inB, _ := s.Nodes(NodeFilter{Source: "B"}); len(inB) != 2 {
		t.Fatalf("в B два узла, получили %d", len(inB))
	}
	if _, err := s.Node("нет"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидали ErrNotFound: %v", err)
	}
}

// Ошибка загрузки не должна «убивать» узлы: EndRefresh вызывается только после успеха, а без него presence не меняется.
func TestFailedRefreshKeepsPresence(t *testing.T) {
	s, _ := open(t)
	n := rec("n", "vless")
	_ = s.UpsertNodes("A", 1, []NodeRec{n}, time.Unix(1, 0))
	// обновление 2 провалилось: ни UpsertNodes, ни EndRefresh не вызывались
	if g, _ := s.Node(n.ID); !g.Present {
		t.Fatal("presence изменился без EndRefresh")
	}
}

func TestNodeFilterSecurityDefaults(t *testing.T) {
	s, _ := open(t)
	plain, insecure, ok := rec("plain", "vless"), rec("insecure", "vless"), rec("ok", "vless")
	plain.NoSecurity = true
	insecure.Insecure = true
	_ = s.UpsertNodes("A", 1, []NodeRec{plain, insecure, ok}, time.Unix(1, 0))
	ids := func(f NodeFilter) map[string]bool {
		l, err := s.Nodes(f)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, r := range l {
			m[r.ID] = true
		}
		return m
	}
	def := ids(NodeFilter{})
	if def[plain.ID] || !def[insecure.ID] || !def[ok.ID] {
		t.Fatalf("по умолчанию security=none скрыт, insecure виден с предупреждением: %v", def)
	}
	if h := ids(NodeFilter{HideInsecure: true}); h[insecure.ID] || h[plain.ID] || !h[ok.ID] {
		t.Fatalf("HideInsecure: %v", h)
	}
	if all := ids(NodeFilter{IncludeNoSecurity: true}); !all[plain.ID] {
		t.Fatal("IncludeNoSecurity должен показывать")
	}
	// явный выбор пользователя прячется не должен
	_ = s.SetNodeFlag(plain.ID, FlagFavorite, true)
	if !ids(NodeFilter{})[plain.ID] {
		t.Fatal("избранный узел без безопасности не должен прятаться")
	}
}

func TestPurgeNodes(t *testing.T) {
	s, _ := open(t)
	old := time.Unix(1000, 0)
	var recs []NodeRec
	for _, k := range []string{"gone", "active", "fav", "pinned", "still"} {
		recs = append(recs, rec(k, "vless"))
	}
	_ = s.UpsertNodes("A", 1, recs, old)
	_ = s.SetNodeFlag(NodeID("fav"), FlagFavorite, true)
	_ = s.SetNodeFlag(NodeID("pinned"), FlagPinned, true)
	// в обновлении 2 остался только still
	_ = s.UpsertNodes("A", 2, []NodeRec{rec("still", "vless")}, old.Add(time.Hour))
	_, _ = s.EndRefresh("A", 2)

	n, err := s.PurgeNodes(old.Add(2*time.Hour), []string{NodeID("active")})
	if err != nil || n != 1 {
		t.Fatalf("удалить нужно один (gone), удалено %d (%v)", n, err)
	}
	for k, want := range map[string]bool{"gone": false, "active": true, "fav": true, "pinned": true, "still": true} {
		_, err := s.Node(NodeID(k))
		if (err == nil) != want {
			t.Fatalf("%s: существует=%v, ожидалось %v", k, err == nil, want)
		}
	}
	// свежие пропавшие (моложе порога) не удаляются
	_ = s.UpsertNodes("A", 3, []NodeRec{rec("fresh", "vless")}, old.Add(3*time.Hour))
	_, _ = s.EndRefresh("A", 4)
	if n, _ := s.PurgeNodes(old.Add(time.Hour), []string{NodeID("active")}); n != 0 {
		t.Fatalf("моложе порога: удалено %d", n)
	}
	// привязки удалённых узлов вычищены
	var left int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM node_sources WHERE node NOT IN (SELECT id FROM nodes)`).Scan(&left)
	if left != 0 {
		t.Fatalf("висячих привязок: %d", left)
	}
}

func TestDeleteSourceDropsLinks(t *testing.T) {
	s, _ := open(t)
	_ = s.UpsertSource(SourceRec{ID: "A", URL: "u", Enabled: true}, time.Unix(1, 0))
	_ = s.UpsertNodes("A", 1, []NodeRec{rec("x", "vless")}, time.Unix(1, 0))
	if err := s.DeleteSource("A"); err != nil {
		t.Fatal(err)
	}
	if g, err := s.Node(NodeID("x")); err != nil || g.Present {
		t.Fatalf("узел остаётся, но без источника: %+v %v", g, err)
	}
	if err := s.DeleteSource("A"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("повторное удаление: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := open(t)
	if _, ok, err := s.Setting("a"); ok || err != nil {
		t.Fatalf("нет настройки: ok=%v err=%v", ok, err)
	}
	_ = s.SetSetting("a", "1")
	_ = s.SetSetting("a", "2")
	_ = s.SetSetting("b", "x")
	if v, ok, _ := s.Setting("a"); !ok || v != "2" {
		t.Fatalf("a=%q", v)
	}
	all, _ := s.Settings()
	if !reflect.DeepEqual(all, map[string]string{"a": "2", "b": "x"}) {
		t.Fatalf("%v", all)
	}
	_ = s.DeleteSetting("a")
	if _, ok, _ := s.Setting("a"); ok {
		t.Fatal("удалённая настройка")
	}
}

func TestEvents(t *testing.T) {
	s, _ := open(t)
	base := time.Unix(5000, 0)
	var last Event
	for i := 0; i < 10; i++ {
		e, err := s.AddEvent(Event{At: base.Add(time.Duration(i) * time.Second), Kind: "k", Initiator: "auto", Detail: string(rune('a' + i))})
		if err != nil {
			t.Fatal(err)
		}
		last = e
	}
	if last.ID != 10 {
		t.Fatalf("id=%d", last.ID)
	}
	after, _ := s.Events(7, 0)
	if len(after) != 3 || after[0].ID != 8 || after[2].Detail != "j" {
		t.Fatalf("after: %+v", after)
	}
	if lim, _ := s.Events(0, 2); len(lim) != 2 || lim[1].ID != 2 {
		t.Fatalf("limit: %+v", lim)
	}
	tail, _ := s.LastEvents(3)
	if len(tail) != 3 || tail[0].ID != 8 || tail[2].ID != 10 || !tail[0].At.Equal(base.Add(7*time.Second)) {
		t.Fatalf("tail: %+v", tail)
	}
	if err := s.TrimEvents(4); err != nil {
		t.Fatal(err)
	}
	rest, _ := s.Events(0, 0)
	if len(rest) != 4 || rest[0].ID != 7 {
		t.Fatalf("после Trim: %+v", rest)
	}
	// ID не переиспользуются после Trim (AUTOINCREMENT): SSE-клиенты опираются на возрастание
	e, _ := s.AddEvent(Event{Kind: "k"})
	if e.ID != 11 {
		t.Fatalf("id после trim: %d", e.ID)
	}
}

func TestTargetsAndSelection(t *testing.T) {
	s, _ := open(t)
	now := time.Unix(100, 0)
	tg := Target{
		ID: "videosite", Owner: "myapp", Title: "VideoSite",
		Rules:  TargetRules{DomainKeyword: []string{"videosite"}, DomainSuffix: []string{"hdvideosite.ac"}},
		Check:  TargetCheck{Kind: "http", URL: "https://videosite.example/", ExpectCodes: []int{200, 403}},
		Policy: PolicyAutoFailover, FailoverK: 3, AllowDirect: true, AllowWarp: true,
	}
	if err := s.PutTarget(tg, now); err != nil {
		t.Fatal(err)
	}
	tg.Title = "VideoSite 2"
	if err := s.PutTarget(tg, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Target("videosite")
	if err != nil {
		t.Fatal(err)
	}
	tg.CreatedAt, tg.UpdatedAt = now, now.Add(time.Hour)
	if !reflect.DeepEqual(got, tg) {
		t.Fatalf("цель не сохранилась как была:\n got %+v\nwant %+v", got, tg)
	}
	if _, err := s.Target("нет"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}

	if _, err := s.Selection(ScopeActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("выбора ещё нет: %v", err)
	}
	if err := s.SetSelection(Selection{Scope: ScopeActive, Node: "n1", Fallbacks: []string{"n2", "n3"}, By: "user", SetAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSelection(Selection{Scope: TargetScope("videosite"), Node: SelDirect, By: "api:myapp", SetAt: now}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Selection(ScopeActive)
	if a.Node != "n1" || !reflect.DeepEqual(a.Fallbacks, []string{"n2", "n3"}) || a.By != "user" || !a.SetAt.Equal(now) {
		t.Fatalf("active: %+v", a)
	}
	// перезапись
	_ = s.SetSelection(Selection{Scope: ScopeActive, Node: "n9", SetAt: now})
	if a, _ = s.Selection(ScopeActive); a.Node != "n9" || len(a.Fallbacks) != 0 {
		t.Fatalf("перезапись: %+v", a)
	}
	if all, _ := s.Selections(); len(all) != 2 {
		t.Fatalf("selections: %+v", all)
	}
	// удаление цели забирает и её выбор
	if err := s.DeleteTarget("videosite"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Selection(TargetScope("videosite")); !errors.Is(err, ErrNotFound) {
		t.Fatal("выбор удалённой цели остался")
	}
	if err := s.DeleteTarget("videosite"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

// Состояние переживает перезапуск: именно на этом держится «липкий» выбор между запусками.
func TestPersistsAcrossReopen(t *testing.T) {
	s, path := open(t)
	_ = s.SetSelection(Selection{Scope: ScopeActive, Node: "keep-me", By: "user"})
	_ = s.UpsertNodes("A", 1, []NodeRec{rec("n", "vless")}, time.Unix(1, 0))
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if sel, err := s2.Selection(ScopeActive); err != nil || sel.Node != "keep-me" {
		t.Fatalf("%+v %v", sel, err)
	}
	if _, err := s2.Node(NodeID("n")); err != nil {
		t.Fatal(err)
	}
}

func TestNodeIDsByPrefix(t *testing.T) {
	s, _ := open(t)
	var recs []NodeRec
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		recs = append(recs, rec(k, "vless"))
	}
	_ = s.UpsertNodes("A", 1, recs, time.Unix(1, 0))
	id := recs[0].ID
	got, err := s.NodeIDsByPrefix(id[:8], 5)
	if err != nil || len(got) != 1 || got[0] != id {
		t.Fatalf("%v %v", got, err)
	}
	if all, _ := s.NodeIDsByPrefix("", 10); len(all) != 5 {
		t.Fatalf("пустой префикс: %d", len(all))
	}
	for _, evil := range []string{"%", "_", `\`, "' OR 1=1 --"} { // спецсимволы LIKE и SQL экранируются
		if got, _ := s.NodeIDsByPrefix(evil, 10); len(got) != 0 {
			t.Fatalf("префикс %q совпал: %v", evil, got)
		}
	}
}

func TestPruneProbes(t *testing.T) {
	s, _ := open(t)
	now := time.Unix(10_000_000, 0)
	_ = s.Record([]Row{
		{Node: "a", Net: "n", Level: 2, OK: true, At: now.Add(-10 * 24 * time.Hour)},
		{Node: "b", Net: "n", Level: 2, OK: true, At: now.Add(-2 * 24 * time.Hour)},
		{Node: "c", Net: "n", Level: 2, OK: true, At: now},
	})
	n, err := s.PruneProbes(now.Add(-8 * 24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("удалено %d (%v), ожидалась 1 строка старше 8 суток", n, err)
	}
	h, _ := s.Histories("n", now)
	if len(h) != 2 {
		t.Fatalf("история после чистки: %+v", h)
	}
}

func TestSourceMetaKeepsOldValuesOnEmpty(t *testing.T) {
	s, _ := open(t)
	_ = s.UpsertSource(SourceRec{ID: "a", URL: "u", Enabled: true}, time.Unix(1, 0))
	if err := s.RecordSourceMeta("a", "Профиль", 12, 300, 1000, 1893456000); err != nil {
		t.Fatal(err)
	}
	// следующий ответ без заголовков не затирает известное
	if err := s.RecordSourceMeta("a", "", 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	l, _ := s.Sources()
	if l[0].Title != "Профиль" || l[0].UpdateHours != 12 || l[0].UsedBytes != 300 || l[0].TotalBytes != 1000 || l[0].Expire.Unix() != 1893456000 {
		t.Fatalf("%+v", l[0])
	}
	if err := s.RecordSourceMeta("нет", "x", 0, 0, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestExitInfo(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	now := time.Unix(1893456000, 0)
	if err := s.PutExitInfo(ExitInfoRec{Node: "a", IP: "203.0.113.1", Country: "DE", Colo: "FRA", GoogleOK: 1, SiteOK: -1, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutExitInfo(ExitInfoRec{Node: "a", IP: "203.0.113.2", Country: "NL", GoogleOK: 0, SiteOK: 1, Detail: "x", At: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	m, err := s.ExitInfos()
	if err != nil || len(m) != 1 {
		t.Fatalf("%v %v", m, err)
	}
	if r := m["a"]; r.IP != "203.0.113.2" || r.Country != "NL" || r.GoogleOK != 0 || r.SiteOK != 1 || r.Detail != "x" || !r.At.Equal(now.Add(time.Hour)) {
		t.Fatalf("запись должна замениться: %+v", r)
	}
}

func TestSpeedAndLatency(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	now := time.Unix(1893456000, 0)
	if err := s.PutSpeed(SpeedRec{Node: "a", KBps: 1500, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSpeed(SpeedRec{Node: "a", KBps: 2500.5, At: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Speeds()
	if err != nil || len(m) != 1 || m["a"].KBps != 2500.5 || !m["a"].At.Equal(now.Add(time.Hour)) {
		t.Fatalf("%v %v", m, err)
	}
	if err := s.Record([]Row{
		{Node: "n", Net: "net1", Level: 2, OK: true, LatencyMs: 800, At: now.Add(-3 * time.Minute)},
		{Node: "n", Net: "net1", Level: 2, OK: true, LatencyMs: 420, At: now.Add(-1 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := s.Histories("net1", now)
	if err != nil || h["n"].L2Ms != 420 {
		t.Fatalf("пинг — задержка последнего успешного L2: %+v %v", h["n"], err)
	}
}

// параметры известного узла меняет только источник, давший его первым из ещё отдающих; собственный сервер
// другим источникам не перезаписывается, а сам владелец его меняет.
func TestUpsertNodesDoesNotLetOtherSourceOverwrite(t *testing.T) {
	s, _ := open(t)
	t0 := time.Unix(1000, 0)
	opts := func(n NodeRec, o string) NodeRec { n.Opts = []byte(o); return n }
	get := func(id string) string {
		n, err := s.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		return string(n.Opts)
	}
	base := rec("n1", "vless")
	id := base.ID
	_ = s.UpsertNodes("A", 1, []NodeRec{opts(base, `{"v":"a1"}`)}, t0)
	_ = s.UpsertNodes("B", 1, []NodeRec{opts(base, `{"v":"evil"}`)}, t0)
	if got := get(id); got != `{"v":"a1"}` {
		t.Fatalf("второй источник подменил параметры первого: %s", got)
	}
	_ = s.UpsertNodes("A", 2, []NodeRec{opts(base, `{"v":"a2"}`)}, t0)
	if got := get(id); got != `{"v":"a2"}` {
		t.Fatalf("первый источник не обновил свой узел: %s", got)
	}
	// собственный сервер: A тоже отдаёт тот же ключ, но владелец сильнее
	own := rec("own1", "vless")
	_ = s.UpsertNodes("A", 3, []NodeRec{opts(own, `{"v":"a"}`)}, t0)
	_ = s.UpsertNodes(OwnSource, 1, []NodeRec{opts(own, `{"v":"mine"}`)}, t0)
	if got := get(own.ID); got != `{"v":"mine"}` {
		t.Fatalf("владелец не смог задать параметры своего сервера: %s", got)
	}
	_ = s.UpsertNodes("A", 4, []NodeRec{opts(own, `{"v":"a-again"}`)}, t0)
	_ = s.UpsertNodes("C", 1, []NodeRec{opts(own, `{"v":"evil"}`)}, t0)
	if got := get(own.ID); got != `{"v":"mine"}` {
		t.Fatalf("источник перезаписал собственный сервер: %s", got)
	}
}

// общий потолок узлов. Вытесняются сначала не отдаваемые источниками, затем самые давно виденные; избранные, закреплённые и
// из keep остаются, даже если потолок из-за них не достигается.
func TestEvictNodesCapsTotal(t *testing.T) {
	s, _ := open(t)
	base := time.Unix(1000, 0)
	// 10 узлов источника A: k0 — самый старый, k9 — самый новый
	for i := 0; i < 10; i++ {
		_ = s.UpsertNodes("A", int64(i+1), []NodeRec{rec(fmt.Sprintf("k%d", i), "vless")}, base.Add(time.Duration(i)*time.Hour))
	}
	_ = s.UpsertNodes("B", 1, []NodeRec{rec("gone", "vless")}, base.Add(100*time.Hour)) // самый свежий по last_seen, но источник его уже не отдаёт
	_, _ = s.EndRefresh("B", 2)
	_ = s.SetNodeFlag(NodeID("k0"), FlagFavorite, true)
	_ = s.SetNodeFlag(NodeID("k1"), FlagPinned, true)

	if n, err := s.EvictNodes(100, nil); err != nil || n != 0 {
		t.Fatalf("под потолком ничего не удаляется: %d %v", n, err)
	}
	// всего 11; потолок 6 → удалить 5: gone (не отдаётся), затем k3, k4, k5, k6 (самые давние из обычных); k0, k1 защищены, k2 в keep
	n, err := s.EvictNodes(6, []string{NodeID("k2")})
	if err != nil || n != 5 {
		t.Fatalf("удалено %d (%v), нужно 5", n, err)
	}
	for k, want := range map[string]bool{"gone": false, "k0": true, "k1": true, "k2": true, "k3": false, "k4": false, "k5": false, "k6": false, "k7": true, "k8": true, "k9": true} {
		_, err := s.Node(NodeID(k))
		if (err == nil) != want {
			t.Errorf("%s: существует=%v, ожидалось %v", k, err == nil, want)
		}
	}
	var left int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM node_sources WHERE node NOT IN (SELECT id FROM nodes)`).Scan(&left)
	if left != 0 {
		t.Fatalf("висячих привязок: %d", left)
	}
	if total, _, _ := s.CountNodes(); total != 6 {
		t.Fatalf("всего %d, нужно 6", total)
	}
	// все оставшиеся защищены: потолок не достигается, ошибки нет
	for _, k := range []string{"k7", "k8", "k9"} {
		_ = s.SetNodeFlag(NodeID(k), FlagFavorite, true)
	}
	if n, err := s.EvictNodes(1, []string{NodeID("k2")}); err != nil || n != 0 {
		t.Fatalf("защищённые не удаляются: %d %v", n, err)
	}
}
