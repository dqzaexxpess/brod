package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"brod/engine"
	"brod/prober"
	"brod/store"
)

func exitReturning(ip, country string) ExitFunc {
	return func(ctx context.Context, n store.NodeRec, p *engine.Probe) prober.ExitInfo {
		if ip == "" {
			return prober.ExitInfo{Detail: "timeout"}
		}
		t := true
		return prober.ExitInfo{IP: ip, Country: country, Colo: "FRA", GoogleOK: &t}
	}
}

func e2eCore(t *testing.T, ex ExitFunc) *Core {
	return cfgCore(t, func(cfg *Config) {
		cfg.Exit = ex
		cfg.Check = newFakeCheck().check
	})
}

// Сквозная проверка: узел отвечает — выход с IP и страной; не отвечает — понятная причина, а не «готово».
func TestCheckNodeE2E(t *testing.T) {
	c := e2eCore(t, exitReturning("203.0.113.9", "TR"))
	id := addNode(t, c, "n1")
	r, err := c.CheckNodeE2E(context.Background(), id)
	if err != nil || !r.OK || r.IP != "203.0.113.9" || r.Country != "TR" || r.Google != "ok" || r.Site != "?" || r.Node != id {
		t.Fatalf("%+v %v", r, err)
	}
	c2 := e2eCore(t, exitReturning("", ""))
	id2 := addNode(t, c2, "n2")
	r2, err := c2.CheckNodeE2E(context.Background(), id2)
	if err != nil || r2.OK || r2.Detail == "" {
		t.Fatalf("мёртвый узел: %+v %v", r2, err)
	}
	if _, err := c2.CheckNodeE2E(context.Background(), "нет-такого"); err == nil {
		t.Fatal("несуществующий узел принят")
	}
	if _, err := c2.CheckNodeE2E(context.Background(), "active"); !errors.Is(err, ErrNoNode) {
		t.Fatalf("без активного узла: %v", err)
	}
}

func fakeEnv(lookup map[string][]string, traces map[string]string) selfTestEnv {
	return selfTestEnv{
		lookup: func(ctx context.Context, host string) ([]string, error) {
			if ips, ok := lookup[host]; ok {
				return ips, nil
			}
			return nil, errors.New("no such host")
		},
		get: func(ctx context.Context, url string) (int, string, error) {
			if body, ok := traces[url]; ok {
				return http.StatusOK, body, nil
			}
			return 0, "", errors.New("timeout")
		},
	}
}

func selfTestSteps(r SelfTest) map[string]SelfTestStep {
	m := map[string]SelfTestStep{}
	for _, s := range r.Steps {
		m[s.Name] = s
	}
	return m
}

// Самотест: всё в порядке в режиме all; IP через туннель не совпал с IP узла — провал (трафик идёт мимо узла); DNS-заглушка — провал.
func TestVPNSelfTest(t *testing.T) {
	c := e2eCore(t, exitReturning("203.0.113.9", "TR"))
	id := addNode(t, c, "n1")
	_ = c.Connect(id, "user")
	c.cfg.TUN.Enabled, c.cfg.TUN.Mode, c.cfg.TUN.Interface = true, ModeAll, "Brod"
	good := fakeEnv(map[string][]string{"instagram.com": {"157.240.1.1"}}, map[string]string{
		selfTestTraceURL:             "fl=1\nip=203.0.113.9\nloc=TR\n",
		"https://www.instagram.com/": "<html>",
	})
	r := c.vpnSelfTest(context.Background(), good)
	if !r.OK {
		t.Fatalf("всё исправно, а самотест провален: %+v", r)
	}
	// трафик идёт мимо узла: IP через туннель домашний
	bad := fakeEnv(map[string][]string{"instagram.com": {"157.240.1.1"}}, map[string]string{
		selfTestTraceURL:             "ip=198.51.100.7\nloc=RU\n",
		"https://www.instagram.com/": "<html>",
	})
	r = c.vpnSelfTest(context.Background(), bad)
	st := selfTestSteps(r)
	if r.OK || st["внешний запрос через туннель"].OK || !strings.Contains(st["внешний запрос через туннель"].Detail, "не совпал") {
		t.Fatalf("обход узла не пойман: %+v", r)
	}
	// DNS вернул заглушку/локальный адрес
	stub := fakeEnv(map[string][]string{"instagram.com": {"10.77.77.2", "0.0.0.0"}}, map[string]string{
		selfTestTraceURL: "ip=203.0.113.9\nloc=TR\n", "https://www.instagram.com/": "x",
	})
	if r = c.vpnSelfTest(context.Background(), stub); selfTestSteps(r)["DNS заблокированного"].OK {
		t.Fatalf("локальный адрес принят за настоящий: %+v", r)
	}
	// режим blocked: IP может отличаться (прямой выход) — не ошибка
	c.cfg.TUN.Mode = ModeBlocked
	if r = c.vpnSelfTest(context.Background(), bad); !selfTestSteps(r)["внешний запрос через туннель"].OK {
		t.Fatalf("в режиме blocked прямой выход — норма: %+v", r)
	}
	// VPN выключен
	c.cfg.TUN.Enabled = false
	if r = c.vpnSelfTest(context.Background(), good); r.OK || len(r.Steps) != 1 {
		t.Fatalf("при выключенном VPN: %+v", r)
	}
}
