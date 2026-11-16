// Package memwatch пишет в журнал расход памяти процесса (куча Go, RSS, «лениво освобождённые» страницы) и возвращает системе лишнее.
// Общий для демона Linux и ядра Android: по этой строке подбирается мягкий лимит памяти и ловится рост (на Linux демон без лимита раздувался
// до 3,4 ГБ, на Android RSS держали страницы, отданные через MADV_FREE).
package memwatch

import (
	"context"
	"log"
	"runtime"
	"runtime/debug"
	"time"
)

// Run раз в every пишет строку «память: …» и вызывает debug.FreeOSMemory, если свободных, но не отданных страниц кучи больше idleMiB.
func Run(ctx context.Context, every time.Duration, idleMiB uint64) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			idle := m.HeapIdle - m.HeapReleased
			rss, lazy := Proc()
			log.Printf("память: куча в работе %d МиБ, выделено %d МиБ, свободных не отданных %d МиБ, всего у Go %d МиБ, RSS процесса %d МиБ (из них LazyFree %d МиБ), сборок %d, горутин %d",
				m.HeapAlloc>>20, m.HeapInuse>>20, idle>>20, m.Sys>>20, rss>>10, lazy>>10, m.NumGC, runtime.NumGoroutine())
			if idleMiB > 0 && idle > idleMiB<<20 {
				debug.FreeOSMemory()
			}
		}
	}
}
