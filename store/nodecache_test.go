package store

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func litesVsDB(t *testing.T, s *Store, ctx string) {
	t.Helper()
	got, err := s.PresentLites()
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.Nodes(NodeFilter{OnlyPresent: true, IncludeNoSecurity: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: в кэше %d узлов, в базе %d", ctx, len(got), len(want))
	}
	g := map[string]NodeLite{}
	for _, l := range got {
		g[l.ID] = l
	}
	ids := make([]string, 0, len(want))
	for _, w := range want {
		ids = append(ids, w.ID)
		l, ok := g[w.ID]
		if !ok || l.Class != w.Class || l.Favorite != w.Favorite || l.Pinned != w.Pinned || l.Insecure != w.Insecure || l.NoSecurity != w.NoSecurity ||
			l.LastSeen != w.LastSeen.Unix() || !l.Present {
			t.Fatalf("%s: узел %s: кэш %+v, база %+v", ctx, w.ID, l, w)
		}
	}
	sort.Strings(ids)
}

// Кэш лёгких записей совпадает с базой после каждой записи в nodes/node_sources.
func TestPresentLitesFollowsWrites(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_800_000_000, 0)
	mk := func(prefix string, n int) []NodeRec {
		var rs []NodeRec
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("%s-%d", prefix, i)
			rs = append(rs, NodeRec{ID: NodeID(k), Class: "vless/tcp/tls", Proto: "direct", Server: k, Port: 443, Opts: []byte(`{}`), Insecure: i%3 == 0, NoSecurity: i%5 == 0})
		}
		return rs
	}
	litesVsDB(t, s, "пусто")
	a := mk("a", 20)
	if err := s.UpsertNodes("src1", 1, a, now); err != nil {
		t.Fatal(err)
	}
	litesVsDB(t, s, "после UpsertNodes")
	if err := s.UpsertNodes("src2", 2, mk("b", 10), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	litesVsDB(t, s, "второй источник")
	if err := s.SetNodeFlag(a[1].ID, FlagFavorite, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeFlag(a[2].ID, FlagPinned, true); err != nil {
		t.Fatal(err)
	}
	litesVsDB(t, s, "после флагов")
	if _, err := s.TouchSource("src1", 3, now.Add(2*time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	litesVsDB(t, s, "после TouchSource (last_seen)")
	if _, err := s.EndRefresh("src1", 4); err != nil { // все узлы src1 пропали из источника
		t.Fatal(err)
	}
	litesVsDB(t, s, "после EndRefresh")
	if _, err := s.PurgeNodes(now.Add(10*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	litesVsDB(t, s, "после PurgeNodes")
	if err := s.UpsertSource(SourceRec{ID: "src2", URL: "https://example.org/s", Priority: 1, Enabled: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource("src2"); err != nil {
		t.Fatal(err)
	}
	litesVsDB(t, s, "после DeleteSource")
}

// CountNodes из кэша совпадает с подсчётом по базе после каждой записи.
func TestCountNodesFollowsWrites(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_800_000_000, 0)
	check := func(ctx string) {
		t.Helper()
		total, present, err := s.CountNodes()
		if err != nil {
			t.Fatal(err)
		}
		var wt, wp int
		if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1)),0) FROM nodes n`).Scan(&wt, &wp); err != nil {
			t.Fatal(err)
		}
		if total != wt || present != wp {
			t.Fatalf("%s: кэш %d/%d, база %d/%d", ctx, total, present, wt, wp)
		}
	}
	check("пусто")
	var recs []NodeRec
	for i := 0; i < 10; i++ {
		recs = append(recs, NodeRec{ID: NodeID(fmt.Sprint("c", i)), Class: "x", Proto: "direct", Server: "h", Port: 1, Opts: []byte(`{}`)})
	}
	if err := s.UpsertNodes("a", 1, recs, now); err != nil {
		t.Fatal(err)
	}
	check("после UpsertNodes")
	check("повторно из кэша")
	if _, err := s.EndRefresh("a", 2); err != nil {
		t.Fatal(err)
	}
	check("после EndRefresh")
	if _, err := s.PurgeNodes(now.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	check("после PurgeNodes")
}
