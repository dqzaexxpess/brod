//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// файлы состояния, созданные движком и приложением с правами 0664, приводятся к 0600; каталоги не трогаются.
func TestTightenStateFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"fakeip.db", "app-timing.log", "state.db"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o664); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o664); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(dir, "rulesets")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tightenStateFiles(dir)
	for _, n := range []string{"fakeip.db", "app-timing.log", "state.db"} {
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", n, st, err)
		}
	}
	if st, _ := os.Stat(sub); st.Mode().Perm() != 0o755 {
		t.Fatalf("каталог тронут: %v", st.Mode().Perm())
	}
	tightenStateFiles(filepath.Join(dir, "нет-такого")) // не падает
}
