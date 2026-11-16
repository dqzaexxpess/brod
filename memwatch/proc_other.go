//go:build !windows

package memwatch

import (
	"fmt"
	"os"
	"strings"
)

// Proc читает из /proc/self/smaps_rollup резидентную память процесса и «лениво освобождённую» (MADV_FREE: страницы свободны, но числятся
// в RSS, пока ядро не потребует их назад), в КиБ. Вне Linux/Android (кроме Windows, см. proc_windows.go) возвращает нули.
func Proc() (rss, lazy int) {
	b, err := os.ReadFile("/proc/self/smaps_rollup")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		var v int
		switch {
		case strings.HasPrefix(l, "Rss:"):
			fmt.Sscanf(strings.TrimPrefix(l, "Rss:"), "%d", &v)
			rss = v
		case strings.HasPrefix(l, "LazyFree:"):
			fmt.Sscanf(strings.TrimPrefix(l, "LazyFree:"), "%d", &v)
			lazy = v
		}
	}
	return rss, lazy
}
