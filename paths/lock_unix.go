//go:build !windows

package paths

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Lock берёт эксклюзивную блокировку файла: второй демон с тем же каталогом состояния не запустится (общие база и порты).
func Lock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		who := ""
		if b, _ := os.ReadFile(path); len(strings.TrimSpace(string(b))) > 0 {
			who = " (PID " + strings.TrimSpace(string(b)) + ")"
		}
		f.Close()
		return nil, fmt.Errorf("каталог состояния уже занят другим brodd%s (%s)", who, path)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0) // для сообщения «занят процессом PID …» у второго экземпляра
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
