package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
)

// spawnInstaller на Windows запускает install.ps1 из нового релиза (он останавливает службу и приложение, копирует файлы, снова запускает
// службу). Демон работает как служба от LocalSystem, поэтому прав хватает без UAC; процесс в своей группе и вне задания (job), чтобы
// остановка службы его не унесла. Консоль — скрытая (CREATE_NO_WINDOW), а не DETACHED_PROCESS: без консоли Windows PowerShell 5.1
// завершается сразу с кодом 0 и без вывода (проверено на Windows 11: помощник не запускался, служба оставалась остановленной).
func (c *Core) spawnInstaller(s InstallSpec) error {
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(ps); err != nil {
		return fmt.Errorf("нет powershell.exe: %w", err)
	}
	logf, err := os.OpenFile(s.LogFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	args := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(s.Stage, "install.ps1"),
		"-Dir", s.Target, "-Update", "-Result", s.ResultFile, "-From", s.From, "-To", s.To}
	var last error
	for _, flags := range []uint32{createNewProcessGroup | createNoWindow | createBreakawayFromJob, createNewProcessGroup | createNoWindow} {
		cmd := exec.Command(ps, args...)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if last = cmd.Start(); last == nil {
			return cmd.Process.Release()
		}
	}
	return last
}
