//go:build with_xhttp && !windows

package monitor_test

import (
	"brod/internal/testport"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"brod/engine"
	"brod/internal/xraybin"
	"brod/monitor"
)

const (
	uuid     = "5b0e5c2e-0000-4000-8000-000000000001"
	nodeName = "node"
)

func waitPort(t *testing.T, port int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("порт %d не открылся", port)
}

// Приёмка P1(б) в части детектора: когда сервер активного узла «замирает» (принимает данные, но не отвечает),
// монитор обязан сработать не позже 30 с. Здесь порог StallAfter=3 с; замораживаем настоящий Xray-сервер SIGSTOP.
func TestPassiveStallDetection(t *testing.T) {
	srvPort, mixPort := testport.Next(), testport.Next() // свободные порты, а не фиксированные: чужое соединение иногда занимало 38202
	xray := xraybin.Path(t, "..")
	dir := t.TempDir()
	srvCfg := map[string]any{
		"log": map[string]any{"loglevel": "error"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": srvPort, "protocol": "vless",
			"settings":       map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "xhttp", "security": "none", "xhttpSettings": map[string]any{"path": "/xh", "mode": "auto"}}}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []any{"127.0.0.0/8"}}}}}},
	}
	b, _ := json.Marshal(srvCfg)
	cfgPath := filepath.Join(dir, "srv.json")
	_ = os.WriteFile(cfgPath, b, 0o644)
	srv := exec.Command(xray, "run", "-c", cfgPath)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(srv.Process.Pid, syscall.SIGCONT); _ = srv.Process.Kill(); _ = srv.Wait() }()
	waitPort(t, srvPort)

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer target.Close()

	cfg := fmt.Sprintf(`{"inbounds":[{"type":"mixed","tag":"in","listen":"127.0.0.1","listen_port":%d}],
	 "outbounds":[{"type":"vless","tag":%q,"server":"127.0.0.1","server_port":%d,"uuid":%q,"transport":{"type":"xhttp","path":"/xh","mode":"packet-up"}}],
	 "route":{"final":%q}}`, mixPort, nodeName, srvPort, uuid, nodeName)
	eng, err := engine.NewFromJSON("error", []byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	events := make(chan monitor.Event, 8)
	mon := monitor.New(monitor.Config{StallAfter: 3 * time.Second, MinStalled: 3, Tick: 500 * time.Millisecond}, func(e monitor.Event) { events <- e })
	mon.SetActive(nodeName)
	eng.AppendTracker(mon)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mon.Run(ctx)

	tr := target.Client().Transport.(*http.Transport).Clone()
	pu, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", mixPort))
	tr.Proxy = http.ProxyURL(pu)
	tr.DisableKeepAlives = true
	client := &http.Client{Transport: tr, Timeout: 40 * time.Second}

	// 1) здоровый узел: запросы идут, ложных срабатываний нет
	for i := 0; i < 4; i++ {
		resp, err := client.Get(target.URL)
		if err != nil {
			t.Fatalf("запрос %d через здоровый узел: %v", i, err)
		}
		resp.Body.Close()
	}
	select {
	case e := <-events:
		t.Fatalf("ложное срабатывание на здоровом узле: %+v", e)
	case <-time.After(5 * time.Second):
	}

	// 2) заморозка сервера узла: данные уходят, ответов нет
	if err := syscall.Kill(srv.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for i := 0; i < 4; i++ {
		go func() {
			if resp, err := client.Get(target.URL); err == nil {
				resp.Body.Close()
			}
		}()
	}
	select {
	case e := <-events:
		el := time.Since(t0)
		t.Logf("монитор сработал через %v: зависших соединений %d (%s)", el.Round(100*time.Millisecond), e.Stalled, e.Reason)
		if el > 30*time.Second {
			t.Fatalf("сработал за %v > 30 с (приёмка P1(б))", el)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("монитор не сработал за 30 с")
	}
}
