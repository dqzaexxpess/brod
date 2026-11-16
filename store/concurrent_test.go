package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// долгое чтение (полный список узлов, загрузка истории) не должно держать короткие обращения
// (настройка, запись): у чтения отдельные соединения пула.
// Долгое чтение имитируем открытым, недочитанным курсором: он держит соединение пула чтения сколько угодно.
func TestLongReadDoesNotBlockShortOps(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recs := make([]NodeRec, 2000)
	for i := range recs {
		id := fmt.Sprintf("n%d", i)
		recs[i] = NodeRec{ID: id, Class: "vless", Proto: "vless", Server: id + ".example.org", Port: 443, Opts: []byte(`{"type":"vless"}`)}
	}
	if err := s.UpsertNodes("src", 1, recs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("k", "v"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.rd.Query(`SELECT id, opts FROM nodes ORDER BY last_seen DESC, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() { // курсор открыт, читатель занят
		t.Fatal("нет строк")
	}

	const limit = 200 * time.Millisecond
	done := make(chan error, 1)
	var maxRead, maxWrite time.Duration
	go func() {
		for i := 0; i < 20; i++ {
			t0 := time.Now()
			if v, ok, err := s.Setting("k"); err != nil || !ok || v != "v" {
				done <- fmt.Errorf("Setting: %q %v %v", v, ok, err)
				return
			}
			maxRead = max(maxRead, time.Since(t0))
			t0 = time.Now()
			if err := s.SetSetting("w", fmt.Sprint(i)); err != nil {
				done <- err
				return
			}
			maxWrite = max(maxWrite, time.Since(t0))
			if v, _, _ := s.Setting("w"); v != fmt.Sprint(i) { // чтение сразу после записи видит её
				done <- fmt.Errorf("после записи прочитано %q, ждали %d", v, i)
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("короткие обращения застряли за долгим чтением")
	}
	t.Logf("макс. чтение настройки %v, макс. запись %v", maxRead, maxWrite)
	if maxRead > limit || maxWrite > limit {
		t.Errorf("короткие обращения ждали: чтение %v, запись %v (предел %v)", maxRead, maxWrite, limit)
	}
}
