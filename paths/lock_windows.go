//go:build windows

package paths

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// lockOffset: блокируется байт далеко за концом файла, а не начало: так PID, записанный в файл, остаётся читаемым другим процессам
// (на Windows чтение заблокированного диапазона запрещено).
const lockOffset = 1 << 30

// Lock держит блокировку ОС (LockFileEx) на открытом дескрипторе всё время жизни процесса. ОС снимает её сама, когда процесс умирает,
// в том числе после отключения питания и выхода по ошибке: поэтому файл-флаг, остающийся после сбоя, не мешал бы старту.
// Сам файл не удаляется и по его наличию ничего не решается.
func Lock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ol := &windows.Overlapped{Offset: lockOffset}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		who := holderPID(f)
		f.Close()
		return nil, fmt.Errorf("каталог состояния уже занят другим brodd%s (%s)", who, path)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{Offset: lockOffset})
		f.Close()
	}, nil
}

func holderPID(f *os.File) string {
	b := make([]byte, 16)
	n, _ := f.ReadAt(b, 0)
	if pid := strings.TrimSpace(string(b[:n])); pid != "" {
		return " (PID " + pid + ")"
	}
	return ""
}
