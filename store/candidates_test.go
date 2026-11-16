package store

import (
	"testing"
	"time"
)

// Миграция 8: база версии 7 получает таблицу кандидатов, прежние данные целы.
func TestMigrateCandidatesTable(t *testing.T) {
	path := oldDB(t, 7)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, ok, _ := s.Setting("marker"); !ok || v != "старая" {
		t.Errorf("прежние данные потеряны: %q %v", v, ok)
	}
	if err := s.UpsertCandidate(CandidateRec{URL: "https://x/a.txt", Name: "a"}, "2026-11-16", time.Now()); err != nil {
		t.Fatal(err)
	}
	if userVersion(t, path) != len(migrations) {
		t.Errorf("версия схемы не доведена до %d", len(migrations))
	}
}

func TestCandidatesLifecycle(t *testing.T) {
	s, _ := open(t)
	t0 := time.Unix(1_800_000_000, 0)
	u := "https://raw.githubusercontent.com/o/r/main/sub.txt"
	rec := CandidateRec{URL: u, Name: "o_r_sub.txt", Repo: "o/r", Links: 100, Schemes: map[string]int{"vless": 90, "trojan": 10}}
	for _, day := range []string{"2026-11-16", "2026-11-16", "2026-11-17"} { // два прохода за один день — один день наблюдения
		rec.Links++
		if err := s.UpsertCandidate(rec, day, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordCandidateSample(u, 20, 5, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCandidateSample(u, 18, 9, t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCandidateSample("https://nope", 1, 1, t0); err != ErrNotFound {
		t.Errorf("неизвестный кандидат: %v", err)
	}
	list, err := s.Candidates()
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %d", err, len(list))
	}
	c := list[0]
	if c.DaysSeen != 2 || c.Links != 103 || c.Schemes["vless"] != 90 || c.Sampled != 38 || c.SampleOK != 14 || c.SampleDays != 2 || c.Dismissed || c.Added {
		t.Errorf("статистика: %+v", c)
	}
	if err := s.DismissCandidate(u); err != nil {
		t.Fatal(err)
	}
	// новое наблюдение не снимает «скрыт» и не обнуляет статистику
	if err := s.UpsertCandidate(rec, "2026-11-18", t0); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Candidates()
	if !list[0].Dismissed || list[0].Sampled != 38 || list[0].DaysSeen != 3 {
		t.Errorf("после повторного наблюдения: %+v", list[0])
	}
	if n, _ := s.PruneCandidates(t0.Add(time.Hour)); n != 0 {
		t.Errorf("скрытый кандидат удалён чисткой")
	}
	if id := CandidateID(u); len(id) != 12 || id != CandidateID(u) || id == CandidateID(u+"x") {
		t.Errorf("id %q", id)
	}
}
