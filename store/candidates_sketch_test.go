package store

import (
	"testing"
	"time"
)

// Миграция 9: колонка sketch; кандидат до миграции остаётся с пустым отпечатком, пустой отпечаток нового прохода прежний не затирает.
func TestMigrateCandidatesSketch(t *testing.T) {
	path := oldDB(t, 8)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(migrations) < 9 || userVersion(t, path) != len(migrations) {
		t.Fatalf("версия схемы %d", userVersion(t, path))
	}
	now := time.Unix(1_800_000_000, 0)
	rec := CandidateRec{URL: "https://x/a.txt", Name: "a"}
	if err := s.UpsertCandidate(rec, "2026-11-16", now); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Candidates(); len(l) != 1 || len(l[0].Sketch) != 0 {
		t.Fatalf("без отпечатка: %+v", l)
	}
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	rec.Sketch = want
	if err := s.UpsertCandidate(rec, "2026-11-17", now); err != nil {
		t.Fatal(err)
	}
	rec.Sketch = nil
	if err := s.UpsertCandidate(rec, "2026-11-18", now); err != nil {
		t.Fatal(err)
	}
	l, _ := s.Candidates()
	if len(l) != 1 || string(l[0].Sketch) != string(want) {
		t.Errorf("отпечаток: %v", l[0].Sketch)
	}
}
