package core

import (
	"testing"
	"time"

	"brod/store"
)

// запрос статуса с активным узлом (Status + NodeByID, как в GET /v1/status) не должен каждый раз
// перечитывать историю проб из базы: после первой загрузки (её же греет healthLoop) он идёт из кэша в памяти.
// Тест держит эту связь: при таблице в тысячи проб сотня запросов подряд укладывается в малую долю секунды;
// перечитывание на каждый запрос дало бы секунды.
func TestStatusDoesNotReloadProbeHistory(t *testing.T) {
	c := startCore(t, t.TempDir())
	id := addNode(t, c, "status-hist")
	now := time.Now()
	rows := make([]store.Row, 0, 8000)
	for i := 0; i < 8000; i++ {
		rows = append(rows, store.Row{Node: id, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, LatencyMs: 100,
			At: now.Add(-time.Duration(i) * time.Second / 10)})
	}
	if err := c.st.Record(rows); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NodeByID(id); err != nil { // первая загрузка истории
		t.Fatal(err)
	}
	t0 := time.Now()
	for i := 0; i < 100; i++ {
		if _, err := c.Status(); err != nil {
			t.Fatal(err)
		}
		v, err := c.NodeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if v.LatencyMs != 100 {
			t.Fatalf("история не прочитана: %+v", v.LatencyMs)
		}
	}
	if d := time.Since(t0); d > 1500*time.Millisecond {
		t.Fatalf("100 запросов статуса заняли %s: история перечитывается из базы", d)
	}
}
