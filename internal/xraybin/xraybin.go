// Package xraybin — путь к bin/xray для приёмочных тестов. Нет файла: локально пропуск, при CI=1 (непустое, не «0»/«false») — ошибка.
package xraybin

import (
	"os"
	"path/filepath"
	"testing"
)

// CIStrict — пропуск тестов считается ошибкой (CI=1; Actions выставляет CI=true).
func CIStrict() bool {
	v := os.Getenv("CI")
	return v != "" && v != "0" && v != "false"
}

// Path возвращает абсолютный путь к bin/xray; rel — путь от каталога пакета до корня репозитория («..», «../..»).
func Path(t testing.TB, rel string) string {
	t.Helper()
	p, _ := filepath.Abs(filepath.Join(rel, "bin", "xray"))
	if _, err := os.Stat(p); err != nil {
		if CIStrict() {
			t.Fatalf("нет %s при CI=1: scripts/fetch-xray.sh должен был скачать закреплённый xray", p)
		}
		t.Skip("нет bin/xray (scripts/fetch-xray.sh)")
	}
	return p
}
