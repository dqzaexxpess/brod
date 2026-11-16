package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// oldDB создаёт файл базы со схемой версии v (первые v миграций) и одной настройкой.
func oldDB(t *testing.T, v int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for i := 0; i < v; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(v)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(k,v) VALUES('marker','старая')`); err != nil {
		t.Fatal(err)
	}
	return path
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// перед миграцией схемы рядом кладётся копия базы прежней версии; прежние копии после успеха удаляются (хранится одна).
func TestMigrateBackupKeepsOneCopy(t *testing.T) {
	path := oldDB(t, 3)
	stale := path + ".bak-v2"
	if err := os.WriteFile(stale, []byte("старая копия"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	bak := path + ".bak-v3"
	if !exists(bak) {
		t.Fatal("копия перед миграцией не создана")
	}
	if exists(stale) {
		t.Error("прежняя копия не удалена после успешной миграции")
	}
	if fi, _ := os.Stat(bak); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // на Windows доступ задают ACL каталога состояния
		t.Errorf("права копии %v, ждали 0600: в ней ссылки и ключи", fi.Mode().Perm())
	}
	if v := userVersion(t, bak); v != 3 {
		t.Errorf("в копии версия схемы %d, ждали 3", v)
	}
	db, err := sql.Open("sqlite", bak)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var m string
	if err := db.QueryRow(`SELECT v FROM settings WHERE k='marker'`).Scan(&m); err != nil || m != "старая" {
		t.Errorf("данные в копии: %q %v", m, err)
	}
	if v := userVersion(t, path); v != len(migrations) {
		t.Errorf("база не доведена до версии %d: %d", len(migrations), v)
	}
	// повторное открытие: версия не меняется, новой копии нет, прежняя остаётся
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if !exists(bak) {
		t.Error("копия пропала при открытии без миграции")
	}
}

func TestMigrateNoBackupWhenNothingToMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path) // новая база: копировать нечего
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	m, _ := filepath.Glob(path + ".bak-v*")
	if len(m) != 0 {
		t.Errorf("для новой базы созданы копии: %v", m)
	}
}

// Не хватает места: миграция не выполняется, база остаётся прежней, ошибка понятная.
func TestMigrateBackupNoSpace(t *testing.T) {
	path := oldDB(t, 3)
	old := freeBytes
	freeBytes = func(string) (uint64, error) { return 1024, nil }
	defer func() { freeBytes = old }()
	_, err := Open(path)
	if err == nil {
		t.Fatal("ждали ошибку: места для копии нет")
	}
	if !strings.Contains(err.Error(), "места") {
		t.Errorf("ошибка непонятная: %v", err)
	}
	if v := userVersion(t, path); v != 3 {
		t.Errorf("база мигрировала вопреки ошибке: версия %d", v)
	}
	if m, _ := filepath.Glob(path + ".bak-v*"); len(m) != 0 {
		t.Errorf("остались копии: %v", m)
	}
}

// Сбой миграции: копия остаётся, прежняя копия тоже не удаляется.
func TestMigrateFailureKeepsBackups(t *testing.T) {
	path := oldDB(t, 3)
	prev := path + ".bak-v2"
	if err := os.WriteFile(prev, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := migrations
	migrations = append(append([]string{}, migrations...), `THIS IS NOT SQL`)
	defer func() { migrations = saved }()
	if _, err := Open(path); err == nil {
		t.Fatal("ждали ошибку миграции")
	}
	if !exists(path + ".bak-v3") {
		t.Error("копия перед неудачной миграцией пропала")
	}
	if !exists(prev) {
		t.Error("прежняя копия удалена, хотя новая миграция не прошла")
	}
}
