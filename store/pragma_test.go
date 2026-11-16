package store

import (
	"path/filepath"
	"testing"
)

// на файловой базе записывающее соединение работает с synchronous(NORMAL) и крупным кэшем, читающие остаются query_only.
func TestOpenPragmas(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var sync, cache, ro int
	if err := st.db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil || sync != 1 {
		t.Fatalf("synchronous писателя = %d (%v), нужно 1 (NORMAL)", sync, err)
	}
	if err := st.db.QueryRow(`PRAGMA cache_size`).Scan(&cache); err != nil || cache >= 0 {
		t.Fatalf("cache_size писателя = %d (%v), нужно отрицательное (КиБ)", cache, err)
	}
	if err := st.rd.QueryRow(`PRAGMA query_only`).Scan(&ro); err != nil || ro != 1 {
		t.Fatalf("читатель не query_only: %d (%v)", ro, err)
	}
}
