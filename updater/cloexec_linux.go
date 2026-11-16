//go:build linux

package updater

import (
	"os"
	"strconv"
	"syscall"
)

// CloexecAll ставит FD_CLOEXEC на все открытые дескрипторы кроме 0–2: дальше запущенный процесс их не наследует.
// Нужно перед запуском помощника обновления: движок открывает /dev/net/tun без O_CLOEXEC, помощник и запущенное им приложение
// получали дескриптор TUN и держали интерфейс brod0 занятым («TUNSETIFF: device or resource busy» у нового демона, пока жило приложение).
// На сам процесс это не влияет: флаг действует только при exec.
func CloexecAll() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		syscall.CloseOnExec(fd)
		n++
	}
	return n
}
