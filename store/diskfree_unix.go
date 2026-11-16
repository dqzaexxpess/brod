//go:build !windows

package store

import "syscall"

// diskFree — свободное место (доступное непривилегированному процессу) на файловой системе каталога.
func diskFree(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
