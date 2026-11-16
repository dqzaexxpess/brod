package core

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"

	"brod/store"
)

// DNS системы направляется в TUN: иначе systemd-resolved спрашивает роутер (приватный адрес → напрямую), и это утечка.
// Демон делает это сам через resolvectl (на интерфейсе TUN, не трогая остальные); при остановке настройки интерфейса сбрасываются
// (интерфейс всё равно исчезает вместе с процессом). Нужны права polkit на действия resolve1 — deploy/tun-setup.sh ставит правило.

// resolvectlRun выполняет resolvectl; подменяется в тестах.
type resolvectlRun func(ctx context.Context, args ...string) error

func execResolvectl(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "resolvectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolvectl %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tunDNSAddr — адрес DNS внутри TUN: соседний хост в подсети интерфейса (10.77.77.1/30 → 10.77.77.2). sing-box перехватывает запросы к нему.
func tunDNSAddr(cidr string) (string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return "", fmt.Errorf("адрес TUN %q: нужен IPv4 с маской", cidr)
	}
	next := p.Addr().Next()
	if !p.Contains(next) {
		return "", fmt.Errorf("в подсети %s нет соседнего адреса для DNS", cidr)
	}
	return next.String(), nil
}

func setSystemDNS(ctx context.Context, run resolvectlRun, iface, addr string) error {
	for _, a := range [][]string{
		{"dns", iface, addr},
		{"domain", iface, "~."}, // весь DNS системы — на этот интерфейс
		{"default-route", iface, "yes"},
	} {
		if err := run(ctx, a...); err != nil {
			_ = run(ctx, "revert", iface) // не оставляем наполовину настроенный интерфейс
			return err
		}
	}
	return nil
}

// setupSystemDNS вызывается из Start; сбой не останавливает демон, но фиксируется событием и в лог (DNS-утечка возможна — пользователь должен знать).
func (c *Core) setupSystemDNS(ctx context.Context) {
	t := c.cfg.TUN.withDefaults()
	if !t.Enabled || t.NoAutoRoute || t.NoSystemDNS {
		return
	}
	run := c.cfg.Resolvectl
	if run == nil {
		run = execResolvectl
	}
	addr, err := tunDNSAddr(t.Address)
	if err == nil {
		err = setSystemDNS(ctx, run, t.Interface, addr)
	}
	if err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto",
			Detail: "DNS системы не направлен в TUN (возможна утечка DNS): " + err.Error() + "; выполните deploy/tun-setup.sh или передайте --tun-no-dns"})
		return
	}
	c.dnsSet = true
}

func (c *Core) revertSystemDNS() {
	if !c.dnsSet {
		return
	}
	run := c.cfg.Resolvectl
	if run == nil {
		run = execResolvectl
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = run(ctx, "revert", c.cfg.TUN.withDefaults().Interface)
	c.dnsSet = false
}
