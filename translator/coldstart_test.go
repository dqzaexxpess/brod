package translator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// P3: холодный старт с показом кэша ≤1 с. Здесь измеряется нижняя часть этого пути: чтение кэша подписок с диска и разбор
// (интерфейса пока нет). Пропускается, если кэша нет (он лежит в work/cache и не входит в git).
func TestColdStartFromCache(t *testing.T) {
	files, _ := filepath.Glob("../work/cache/*.txt")
	if len(files) == 0 {
		t.Skip("нет work/cache — запусти bin/probe один раз")
	}
	start := time.Now()
	var n, ok int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); strings.Contains(l, "://") {
				n++
				if _, err := Parse(l); err == nil {
					ok++
				}
			}
		}
	}
	el := time.Since(start)
	t.Logf("кэш: %d ссылок, разобрано %d за %v", n, ok, el.Round(time.Millisecond))
	if limit := time.Second * raceSlowdown; el > limit {
		t.Fatalf("чтение и разбор кэша %v > %v", el, limit)
	}
}
