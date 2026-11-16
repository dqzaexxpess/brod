package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync/atomic"
	"time"
)

// Медленная остановка ядра: если Close не уложился в slowStopAfter, в файл рядом с журналом пишутся стеки всех горутин с названием этапа,
// на котором остановка стоит. По имени задания («health», «traffic») и по времени этапа «движок» не видно строку, которая держит остановку
// (хвосты 0,25–1 с при переключении VPN); стек её называет. На работу не влияет: один дамп не чаще раза в slowStopEvery, файл ограничен.

const (
	slowStopAfter = 250 * time.Millisecond
	slowStopEvery = time.Minute
	slowStopFile  = "stop-stacks.log"
	slowStopMax   = 2 << 20 // больше — прежний файл уходит в .1
)

// lastSlowStop — время последнего дампа (общее на процесс: ядро пересоздаётся при каждом переключении VPN).
var lastSlowStop atomic.Int64

// watchSlowStop запускает сторожа остановки; stage возвращает название текущего этапа. Возвращённую функцию вызывают, когда остановка закончилась.
func (c *Core) watchSlowStop(start time.Time, stage func() string) (stop func()) {
	if c.cfg.StateDir == "" {
		return func() {}
	}
	t := time.AfterFunc(slowStopAfter, func() {
		now := time.Now()
		if last := lastSlowStop.Load(); now.UnixNano()-last < int64(slowStopEvery) || !lastSlowStop.CompareAndSwap(last, now.UnixNano()) {
			return
		}
		writeStopStacks(filepath.Join(c.cfg.StateDir, slowStopFile), now, now.Sub(start), stage())
	})
	return func() { t.Stop() }
}

func writeStopStacks(path string, now time.Time, elapsed time.Duration, stage string) {
	if st, err := os.Stat(path); err == nil && st.Size() > slowStopMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("остановка ядра: стеки не записаны: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n===== %s: остановка ядра идёт %s, этап «%s» =====\n", now.Format(time.RFC3339Nano), elapsed.Round(time.Millisecond), stage)
	_ = pprof.Lookup("goroutine").WriteTo(f, 2)
	log.Printf("остановка ядра: дольше %s на этапе «%s», стеки горутин — в %s", slowStopAfter, stage, slowStopFile)
}
