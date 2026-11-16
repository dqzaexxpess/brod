package core

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"brod/updater"
)

// spawnInstaller запускает помощника: копию brodctl прежней версии (она не зависит от заменяемого каталога) с командой update-apply.
// Помощник отсоединён от демона (новый сеанс) и переживает его выход.
func (c *Core) spawnInstaller(s InstallSpec) error {
	ctl := filepath.Join(s.Target, "brodctl")
	if _, err := os.Stat(ctl); err != nil {
		return fmt.Errorf("нет %s", ctl)
	}
	dir := filepath.Join(c.updatesDir(), "apply")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	helper := filepath.Join(dir, "brodctl")
	if err := copyExec(ctl, helper); err != nil {
		return err
	}
	pids := make([]string, len(s.WaitPIDs))
	for i, p := range s.WaitPIDs {
		pids[i] = strconv.Itoa(p)
	}
	logf, err := os.OpenFile(s.LogFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(helper, "update-apply", "--stage", s.Stage, "--target", s.Target, "--from", s.From, "--to", s.To,
		"--result", s.ResultFile, "--wait", strings.Join(pids, ","), "--relaunch")
	if s.Archive != "" {
		cmd.Args = append(cmd.Args, "--archive", s.Archive, "--manifest", s.Manifest, "--sig", s.Sig)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	updater.CloexecAll() // иначе помощник (и запущенное им приложение) унаследует дескриптор TUN и удержит brod0 занятым
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func copyExec(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
