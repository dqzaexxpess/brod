package killswitch

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const capNetAdmin = 12 // CAP_NET_ADMIN

func (n *Nft) bin() (string, error) {
	if n.Bin != "" {
		return n.Bin, nil
	}
	for _, p := range []string{"/usr/sbin/nft", "/sbin/nft", "/usr/bin/nft"} { // PATH демона, запущенного приложением, может не содержать /usr/sbin
		if _, err := exec.LookPath(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("nft"); err == nil {
		return p, nil
	}
	return "", errors.New("команда nft не найдена (пакет nftables)")
}

func (n *Nft) run(stdin string, args ...string) (string, error) {
	bin, err := n.bin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{AmbientCaps: []uintptr{capNetAdmin}}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("запуск nft: %w (у brodd нет права cap_net_admin? sudo deploy/tun-setup.sh)", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return out.String(), fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return "", errors.New("nft не ответил за 15 с")
	}
	return out.String(), nil
}
