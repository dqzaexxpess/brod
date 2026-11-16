package memwatch

import "testing"

func TestProcReportsWorkingSetOnWindows(t *testing.T) {
	rss, lazy := Proc()
	if rss < 1024 { // у любого процесса Go рабочий набор больше 1 МиБ
		t.Fatalf("RSS %d КиБ", rss)
	}
	if lazy != 0 {
		t.Fatalf("LazyFree на Windows: %d", lazy)
	}
}
