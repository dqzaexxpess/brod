package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileKeepsThreeFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "brodd.log")
	w, err := openRotating(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.f.Close() }) // на Windows открытый файл не даёт удалить временный каталог
	line := strings.Repeat("x", 1000) + "\n"
	for i := 0; i < 3500; i++ { // ~3,5 МБ при пределе 1 МБ на файл
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"brodd.log", "brodd.log.1", "brodd.log.2"} {
		st, err := os.Stat(filepath.Join(filepath.Dir(p), name))
		if err != nil || st.Size() == 0 || st.Size() > logMaxSize {
			t.Fatalf("%s: %v %v", name, st, err)
		}
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Fatal("старше трёх файлов хранить не нужно")
	}
}
