package store

import (
	"path/filepath"
	"testing"
	"time"
)

func touchRecs(keys ...string) []NodeRec {
	var out []NodeRec
	for _, k := range keys {
		out = append(out, NodeRec{ID: NodeID(k), Class: "direct/tcp/none", Proto: "direct", Server: k + ".example", Port: 443, Opts: []byte(`{"type":"direct"}`)})
	}
	return out
}

// ответ 304 не перезаписывает метки строк источника (раньше UPDATE по всем строкам и индексу занимал секунды), но счёт верен,
// а жизненный цикл «источник изменился, узел пропал» работает по-прежнему.
func TestTouchSourceLeavesStampsAndLifecycleHolds(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	if err := s.UpsertNodes("src", 100, touchRecs("a", "b"), now); err != nil {
		t.Fatal(err)
	}
	n, err := s.TouchSource("src", 200, now, now.Add(-24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("TouchSource: n=%d err=%v", n, err)
	}
	var minS, maxS int64
	if err := s.db.QueryRow(`SELECT MIN(stamp), MAX(stamp) FROM node_sources WHERE source='src'`).Scan(&minS, &maxS); err != nil {
		t.Fatal(err)
	}
	if minS != 100 || maxS != 100 {
		t.Fatalf("метки после 304 изменились: %d..%d", minS, maxS)
	}
	// источник изменился: остался только a
	if err := s.UpsertNodes("src", 300, touchRecs("a"), now); err != nil {
		t.Fatal(err)
	}
	gone, err := s.EndRefresh("src", 300)
	if err != nil || gone != 1 {
		t.Fatalf("EndRefresh: gone=%d err=%v", gone, err)
	}
	if _, present, _ := s.CountNodes(); present != 1 {
		t.Fatalf("присутствует %d узлов, ожидался 1", present)
	}
}
