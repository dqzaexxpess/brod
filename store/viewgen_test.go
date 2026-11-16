package store

import (
	"path/filepath"
	"testing"
	"time"
)

// ViewGen растёт при каждой записи, видимой в списке узлов (узлы, пробы, скорость, выход, настройки-подписи), и не растёт от
// шумных таблиц (трафик, события, Telegram-прокси); NodesGen — публичный счётчик таблицы узлов.
func TestViewGen(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	grows := func(name string, f func() error) {
		t.Helper()
		before := s.ViewGen()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.ViewGen() == before {
			t.Errorf("%s: ViewGen не изменился, а список узлов мог измениться", name)
		}
	}
	id := NodeID("a")
	n0 := s.NodesGen()
	grows("UpsertNodes", func() error {
		return s.UpsertNodes("src", now.UnixNano(), []NodeRec{{ID: id, Class: "direct/tcp/none", Proto: "direct", Server: "a.example", Port: 443, Opts: []byte(`{"type":"direct"}`)}}, now)
	})
	if s.NodesGen() == n0 {
		t.Error("NodesGen не вырос после записи узлов")
	}
	grows("Record", func() error { return s.Record([]Row{{Node: id, Net: "n", Level: 2, OK: true, LatencyMs: 5, At: now}}) })
	grows("PutSpeed", func() error { return s.PutSpeed(SpeedRec{Node: id, KBps: 10, At: now}) })
	grows("PutExitInfo", func() error { return s.PutExitInfo(ExitInfoRec{Node: id, Country: "DE", At: now}) })
	grows("SetNodeLabel", func() error { return s.SetNodeLabel(id, "метка") })
	grows("SetSetting", func() error { return s.SetSetting("security.hide_insecure", "1") })

	// шум: не должен сбрасывать метку списка
	before := s.ViewGen()
	if err := s.AddDailyTraffic("2026-11-15", 1, 2, 3, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEvent(Event{Kind: "x"}); err != nil {
		t.Fatal(err)
	}
	if s.ViewGen() != before {
		t.Error("запись трафика или событий изменила ViewGen: метка списка узлов сбрасывалась бы зря")
	}
	// два хранилища не должны выдавать совпадающие метки (перезапуск демона): разные эпохи
	s2, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s.Epoch() == s2.Epoch() {
		t.Error("эпохи двух открытий совпали")
	}
}
