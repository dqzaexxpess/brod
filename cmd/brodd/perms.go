package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// tightenStateFiles приводит обычные файлы каталога состояния к 0600. Часть файлов создают не мы: fakeip.db — движок,
// app-timing.log — приложение; они появляются с правами по umask (0664). Каталог закрыт 0700, наружу это не видно, но права
// должны быть едины. Вызывается при запуске: файлы, созданные во время работы, исправятся при следующем. На Windows доступ задаёт ACL.
func tightenStateFiles(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(p, 0o600)
		}
	}
}
