package store

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// historiesSQL — прежняя реализация (обход недельных проб по порядку): эталон для кэша.
func historiesSQL(s *Store, netID string, now time.Time) (map[string]History, error) {
	rows, err := s.db.Query(`
SELECT node, level, target, ok, at, latency_ms FROM probes WHERE net=? AND at>=? ORDER BY at, rowid`, netID, now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]History{}
	for rows.Next() {
		var node, target string
		var level, ok int
		var at, lat int64
		if err := rows.Scan(&node, &level, &target, &ok, &at, &lat); err != nil {
			return nil, err
		}
		h, t := out[node], time.Unix(at, 0)
		switch {
		case ok == 1 && level == 2:
			h.L2OK, h.L2Ms = t, lat
			h.FailedLast = false
		case ok == 1 && level == 4 && target == "youtube":
			h.L4OK = t
		case ok == 1 && level == 4 && target == "google-api":
			h.GoogleOK = t
		case ok == 0 && level == 2:
			h.LastFail = t
			h.FailedLast = now.Sub(t) <= 30*time.Minute
		}
		out[node] = h
	}
	return out, rows.Err()
}

func randRows(rng *rand.Rand, base time.Time, n int) []Row {
	nodes := []string{"a", "b", "c", "d", "e", "f"}
	nets := []string{"n1", "n2"}
	targets := []string{"", "youtube", "google-api", "gstatic"}
	rows := make([]Row, n)
	for i := range rows {
		// часть проб с одинаковым временем (порядок записи решает), часть — старше окна и младше 30 минут от now
		var age time.Duration
		switch rng.Intn(4) {
		case 0:
			age = time.Duration(rng.Intn(20)) * time.Minute
		case 1:
			age = time.Duration(rng.Intn(48)) * time.Hour
		case 2:
			age = time.Duration(6*24+rng.Intn(60)) * time.Hour // у границы окна 7 суток
		default:
			age = time.Duration(rng.Intn(3)) * time.Minute // тот же момент
		}
		rows[i] = Row{Node: nodes[rng.Intn(len(nodes))], Net: nets[rng.Intn(len(nets))], Level: []int{1, 2, 2, 2, 3, 4}[rng.Intn(6)],
			Target: targets[rng.Intn(len(targets))], OK: rng.Intn(2) == 0, LatencyMs: int64(rng.Intn(900)), At: base.Add(-age)}
	}
	return rows
}

func sameHist(t *testing.T, s *Store, net string, now time.Time, ctx string) {
	t.Helper()
	want, err := historiesSQL(s, net, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Histories(net, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s: сети %s, now=%v\nэталон: %+v\nкэш:    %+v", ctx, net, now, want, got)
	}
	for id, w := range want {
		h, ok, err := s.History(net, id, now)
		if err != nil || !ok || h != w {
			t.Fatalf("%s: History(%s) = %+v ok=%v err=%v, нужно %+v", ctx, id, h, ok, err, w)
		}
	}
	if _, ok, _ := s.History(net, "нет-такого", now); ok {
		t.Fatalf("%s: History для неизвестного узла вернула ok", ctx)
	}
}

// Кэш Histories совпадает с прежним SQL-обходом при любом порядке записи, ничьих по времени, разных now и чистке.
func TestHistoriesCacheMatchesSQL(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		base := time.Unix(1_800_000_000, 0)
		nows := []time.Time{base, base.Add(10 * time.Minute), base.Add(31 * time.Minute), base.Add(26 * time.Hour), base.Add(3 * 24 * time.Hour), base.Add(9 * 24 * time.Hour)}
		if err := s.Record(randRows(rng, base, rng.Intn(60))); err != nil { // до первого чтения: холодная загрузка
			t.Fatal(err)
		}
		for step := 0; step < 8; step++ {
			now := nows[rng.Intn(len(nows))]
			sameHist(t, s, "n1", now, "после записи")
			if step%2 == 0 {
				sameHist(t, s, "n2", now, "вторая сеть")
			}
			if err := s.Record(randRows(rng, base, 1+rng.Intn(25))); err != nil { // инкрементально в загруженный кэш
				t.Fatal(err)
			}
			if rng.Intn(5) == 0 {
				if _, err := s.PruneProbes(base.Add(-time.Duration(1+rng.Intn(6)) * 24 * time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			sameHist(t, s, "n1", nows[rng.Intn(len(nows))], "после записи/чистки")
		}
		s.Close()
	}
}

// Собранная карта переиспользуется, пока нет новых проб; новая проба или чистка её сбрасывают.
func TestHistoriesSnapshotReuse(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_800_000_000, 0)
	if err := s.Record([]Row{{Node: "a", Net: "n", Level: 2, OK: true, At: now}}); err != nil {
		t.Fatal(err)
	}
	h1, _ := s.Histories("n", now)
	h2, _ := s.Histories("n", now.Add(5*time.Second))
	if reflect.ValueOf(h1).Pointer() != reflect.ValueOf(h2).Pointer() {
		t.Fatal("без новых проб карта должна переиспользоваться")
	}
	if err := s.Record([]Row{{Node: "b", Net: "n", Level: 2, OK: true, At: now}}); err != nil {
		t.Fatal(err)
	}
	h3, _ := s.Histories("n", now.Add(5*time.Second))
	if len(h3) != 2 {
		t.Fatalf("новая проба не видна: %+v", h3)
	}
	h4, _ := s.Histories("n", now.Add(time.Minute)) // дольше histSnapTTL: пересборка (FailedLast зависит от now)
	if reflect.ValueOf(h3).Pointer() == reflect.ValueOf(h4).Pointer() {
		t.Fatal("после histSnapTTL карта должна пересобираться")
	}
}

// 40 тыс. проб за неделю, как на телефоне: прежний обход против кэша.
func benchStore(b *testing.B) (*Store, time.Time) {
	s, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	rows := make([]Row, 0, 40000)
	for i := 0; i < 40000; i++ {
		rows = append(rows, Row{Node: NodeID(string(rune('a' + i%2000))), Net: "n", Level: 2 + (i%3)*1, OK: i%3 != 0, LatencyMs: 100, At: now.Add(-time.Duration(i) * 15 * time.Second)})
	}
	if err := s.Record(rows); err != nil {
		b.Fatal(err)
	}
	return s, now
}

func BenchmarkHistoriesSQL(b *testing.B) {
	s, now := benchStore(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := historiesSQL(s, "n", now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHistoriesCache(b *testing.B) {
	s, now := benchStore(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := s.Histories("n", now); err != nil {
			b.Fatal(err)
		}
	}
}
