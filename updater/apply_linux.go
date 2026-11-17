//go:build linux

package updater

import (
	"errors"
	"os/exec"
	"syscall"
)

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// helperInstalled — помощник поставлен как положено (root:root, без записи для группы и остальных) и есть каталог установки демона.
// Создать такие файлы может только root, поэтому процесс пользователя не может «включить» или «выключить» помощника подменой файлов.
func helperInstalled() bool { return rootOwned(HelperPath, false) && rootOwned(DaemonDir, true) }

func rootOwned(p string, dir bool) bool {
	var st syscall.Stat_t
	if syscall.Lstat(p, &st) != nil || st.Uid != 0 || st.Mode&0o022 != 0 {
		return false
	}
	if dir {
		return st.Mode&syscall.S_IFMT == syscall.S_IFDIR
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG
}

func startDetached(exe string) error {
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	CloexecAll()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
