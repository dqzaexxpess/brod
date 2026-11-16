package store

import (
	"testing"
	"time"
)

// чистка проб идёт порциями (единственное соединение не занято одним долгим запросом), результат тот же.
func TestPruneProbesInChunks(t *testing.T) {
	s, _ := open(t)
	old := pruneChunk
	pruneChunk = 100
	t.Cleanup(func() { pruneChunk = old })
	now := time.Unix(10_000_000, 0)
	var rows []Row
	for i := 0; i < 1050; i++ {
		rows = append(rows, Row{Node: "a", Net: "n", Level: 2, OK: true, At: now.Add(-10*24*time.Hour - time.Duration(i)*time.Second)})
	}
	for i := 0; i < 5; i++ {
		rows = append(rows, Row{Node: "b", Net: "n", Level: 2, OK: true, At: now.Add(-time.Duration(i) * time.Second)})
	}
	if err := s.Record(rows); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneProbes(now.Add(-8 * 24 * time.Hour))
	if err != nil || n != 1050 {
		t.Fatalf("удалено %d (%v), ожидалось 1050", n, err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT count(*) FROM probes`).Scan(&left); err != nil || left != 5 {
		t.Fatalf("осталось %d (%v), ожидалось 5", left, err)
	}
	if n, err := s.PruneProbes(now.Add(-8 * 24 * time.Hour)); err != nil || n != 0 {
		t.Fatalf("повторная чистка: %d %v", n, err)
	}
}
