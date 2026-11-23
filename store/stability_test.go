package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNodeStability(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "st.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	a, b, none := NodeID("a"), NodeID("b"), NodeID("none")
	at := func(min int) time.Time { return now.Add(-time.Duration(min) * time.Minute) }
	rows := []Row{
		// a: ок, провал, ок, провал (давно), потом провал 5 минут назад — три смены исхода подряд плюс ещё одна
		{Node: a, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: true, At: at(300)},
		{Node: a, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: false, At: at(200)},
		{Node: a, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: true, At: at(100)},
		{Node: a, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: false, At: at(5)},
		// b: только успехи; пробы другого уровня, чужой сети и старше суток не считаются
		{Node: b, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: true, At: at(50)},
		{Node: b, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: true, At: at(40)},
		{Node: b, NodeClass: "x/y/z", Net: "n1", Level: 3, OK: false, At: at(30)},
		{Node: b, NodeClass: "x/y/z", Net: "OTHER", Level: 2, OK: false, At: at(30)},
		{Node: b, NodeClass: "x/y/z", Net: "n1", Level: 2, OK: false, At: at(60 * 25)},
	}
	if err := s.Record(rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.NodeStability("n1", []string{a, b, none}, now.Add(-24*time.Hour), now.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if st := got[a]; st.OK != 2 || st.Fail != 2 || st.Flips != 3 || st.Recent != 1 {
		t.Errorf("a: %+v", st)
	}
	if st := got[b]; st.OK != 2 || st.Fail != 0 || st.Flips != 0 || st.Recent != 0 {
		t.Errorf("b: %+v", st)
	}
	if _, ok := got[none]; ok {
		t.Error("узел без проверок не должен попадать в результат")
	}
	if r, ok := got[a].Ratio(); !ok || r != 0.5 {
		t.Errorf("доля успехов a: %v %v", r, ok)
	}
}
