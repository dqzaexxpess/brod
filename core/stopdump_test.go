package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Остановка дольше порога оставляет стеки горутин с названием этапа; быстрая — нет; повтор в пределах минуты не пишется.
func TestSlowStopWritesStacksOncePerMinute(t *testing.T) {
	lastSlowStop.Store(0)
	c := &Core{cfg: Config{StateDir: t.TempDir()}}
	path := filepath.Join(c.cfg.StateDir, slowStopFile)

	stop := c.watchSlowStop(time.Now(), func() string { return "движок" })
	stop() // быстрая остановка
	time.Sleep(slowStopAfter + 100*time.Millisecond)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("быстрая остановка не должна оставлять стеки")
	}

	stop = c.watchSlowStop(time.Now(), func() string { return "движок" })
	time.Sleep(slowStopAfter + 200*time.Millisecond)
	stop()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "этап «движок»") || !strings.Contains(string(b), "goroutine ") {
		t.Fatalf("в файле нет этапа или стеков: %.200s", b)
	}

	stop = c.watchSlowStop(time.Now(), func() string { return "база" })
	time.Sleep(slowStopAfter + 200*time.Millisecond)
	stop()
	b2, _ := os.ReadFile(path)
	if len(b2) != len(b) {
		t.Fatal("второй дамп в пределах минуты не должен писаться")
	}
}
