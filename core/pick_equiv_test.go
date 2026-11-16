package core

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"brod/store"
)

func (c *Core) pickCandidatesOld(netID string, opt CheckOptions, now time.Time) ([]store.NodeRec, error) {
	if len(opt.IDs) > 0 {
		return c.nodesByIDs(opt.IDs)
	}
	hist, err := c.st.Histories(netID, now)
	if err != nil {
		return nil, err
	}
	priors, err := c.st.ClassPriors(netID, now)
	if err != nil {
		return nil, err
	}
	nodes, err := c.st.Nodes(store.NodeFilter{OnlyPresent: true, HideInsecure: c.hideInsecure()})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	active := c.selected[store.ScopeActive]
	c.mu.Unlock()
	boost := map[string]bool{}
	if sels, err := c.st.Selections(); err == nil {
		for _, s := range sels {
			for _, f := range s.Fallbacks {
				boost[f] = true
			}
		}
	}
	type scored struct {
		rec   store.NodeRec
		score float64
	}
	var list []scored
	for _, n := range nodes {
		if n.ID == active {
			continue
		}
		h := hist[n.ID]
		if !opt.Force {
			if !h.L2OK.IsZero() && now.Sub(h.L2OK) < c.cfg.CheckTTL {
				continue // свежий успех
			}
			if !h.LastFail.IsZero() && h.L2OK.Before(h.LastFail) && now.Sub(h.LastFail) < c.cfg.RetryAfter {
				continue // недавно провалился: повторять рано, иначе одни и те же мёртвые узлы забивают каждый круг
			}
		}
		pr, ok := priors[n.Class]
		if !ok {
			pr = 0.05
		}
		sc := float64(h.Score(now)) + pr*100
		if n.Favorite || n.Pinned {
			sc += 2000
		}
		if boost[n.ID] {
			sc += 1500
		}
		list = append(list, scored{n, sc})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	limit := opt.Limit
	if limit <= 0 {
		limit = c.cfg.CheckBatch
	}
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]store.NodeRec, len(list))
	for i, s := range list {
		out[i] = s.rec
	}
	return out, nil
}

// randomBase наполняет базу узлами с ничьими по last_seen, разными классами и флагами, пробами и выбором с запасными.
func randomBase(t *testing.T, c *Core, rng *rand.Rand, now time.Time) []string {
	t.Helper()
	classes := []string{"vless/tcp/reality", "vless/ws/tls", "vmess/ws/tls", "trojan/tcp/tls", "ss/tcp/none", "hy2/udp/tls"}
	var ids []string
	for b := 0; b < 4; b++ { // четыре пачки с разным (и повторяющимся) временем: ничьи по last_seen
		var recs []store.NodeRec
		for i := 0; i < 300; i++ {
			key := fmt.Sprintf("n-%d-%d", b, i)
			recs = append(recs, store.NodeRec{ID: store.NodeID(key), Class: classes[rng.Intn(len(classes))], Proto: "direct", Server: key + ".example", Port: 443,
				Opts: []byte(`{"type":"direct"}`), Insecure: rng.Intn(6) == 0, NoSecurity: rng.Intn(8) == 0})
			ids = append(ids, recs[len(recs)-1].ID)
		}
		src := []string{"a", "b"}[b%2]
		if err := c.st.UpsertNodes(src, int64(b+1), recs, now.Add(-time.Duration(b/2)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// часть узлов перестаёт отдаваться источником "a" (stamp новее, их в нём нет)
	if _, err := c.st.EndRefresh("a", 100); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		_ = c.st.SetNodeFlag(ids[rng.Intn(len(ids))], []store.NodeFlag{store.FlagFavorite, store.FlagPinned}[rng.Intn(2)], true)
	}
	var rows []store.Row
	for _, id := range ids {
		if rng.Intn(3) == 0 {
			continue
		}
		for k := 0; k < 1+rng.Intn(4); k++ {
			rows = append(rows, store.Row{Node: id, Net: "net1", NodeClass: classes[rng.Intn(len(classes))], Level: 2, OK: rng.Intn(2) == 0, LatencyMs: int64(rng.Intn(500)),
				At: now.Add(-time.Duration(rng.Intn(24*60)) * time.Minute)})
		}
	}
	if err := c.st.Record(rows); err != nil {
		t.Fatal(err)
	}
	var fb []string
	for i := 0; i < 5; i++ {
		fb = append(fb, ids[rng.Intn(len(ids))])
	}
	if err := c.st.SetSelection(store.Selection{Scope: store.TargetScope("x"), Fallbacks: fb}); err != nil {
		t.Fatal(err)
	}
	return ids
}

func idsOfRecs(rs []store.NodeRec) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// pickCandidates (лёгкий отбор из памяти) выбирает тех же кандидатов в том же порядке, что и прежний обход всей таблицы узлов.
func TestPickCandidatesMatchesFullScan(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rng := rand.New(rand.NewSource(seed))
		c := newCore(t, t.TempDir())
		now := time.Now()
		randomBase(t, c, rng, now)
		for _, hide := range []string{"0", "1"} {
			if err := c.st.SetSetting("security.hide_insecure", hide); err != nil {
				t.Fatal(err)
			}
			for _, opt := range []CheckOptions{{}, {Force: true}, {Limit: 7}, {Limit: 1000, Force: true}, {Limit: 30}} {
				want, err := c.pickCandidatesOld("net1", opt, now)
				if err != nil {
					t.Fatal(err)
				}
				got, err := c.pickCandidates("net1", opt, now)
				if err != nil {
					t.Fatal(err)
				}
				w, g := idsOfRecs(want), idsOfRecs(got)
				if fmt.Sprint(w) != fmt.Sprint(g) {
					t.Fatalf("seed %d hide=%s opt=%+v: порядок кандидатов разошёлся\nстарый: %v\nновый:  %v", seed, hide, opt, w[:min(8, len(w))], g[:min(8, len(g))])
				}
			}
		}
		c.Close()
	}
}

// freshNodes (из истории) совпадает с прежним фильтром по полному списку.
func TestFreshNodesMatchesFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	c := newCore(t, t.TempDir())
	defer c.Close()
	now := time.Now()
	randomBase(t, c, rng, now)
	for _, hide := range []string{"0", "1"} {
		_ = c.st.SetSetting("security.hide_insecure", hide)
		hist, _ := c.st.Histories("net1", now)
		all, err := c.st.Nodes(store.NodeFilter{OnlyPresent: true, HideInsecure: hide == "1"})
		if err != nil {
			t.Fatal(err)
		}
		skip := all[rng.Intn(len(all))].ID
		var want []store.NodeRec
		for _, nd := range all {
			h := hist[nd.ID]
			if nd.ID != skip && !h.L2OK.IsZero() && now.Sub(h.L2OK) <= c.cfg.AltFresh {
				want = append(want, nd)
			}
		}
		got, err := c.freshNodes(hist, now, skip)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(idsOfRecs(want)) != fmt.Sprint(idsOfRecs(got)) || len(want) == 0 {
			t.Fatalf("hide=%s: %d против %d узлов или другой порядок", hide, len(want), len(got))
		}
	}
}
