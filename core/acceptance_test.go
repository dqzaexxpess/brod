//go:build !windows

// Приёмочные тесты с живыми процессами xray (SIGSTOP/SIGCONT) — только POSIX.

package core

// Приёмка P1 и P7 на настоящих узлах: Xray на loopback, узел «умирает» через SIGSTOP (процесс жив и принимает TCP, но молчит —
// так выглядит заморозка ТСПУ). Пороги в тестах сжаты (секунды вместо десятков секунд), логика та же.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"brod/aggregator"
	"brod/engine"
	"brod/internal/xraybin"
	"brod/monitor"
	"brod/prober"
	"brod/store"
)

const accUUID = "5b0e5c2e-0000-4000-8000-0000000000a1"

var xrayPort atomic.Int32

// xrayNode — запущенный Xray-сервер vless на loopback.
type xrayNode struct {
	t    *testing.T
	cmd  *exec.Cmd
	port int
	id   string // NodeID
}

func startXrayNode(t *testing.T) *xrayNode {
	t.Helper()
	xray := xraybin.Path(t, "..")
	// порт из 38410+ лежит в эфемерном диапазоне системы: его может занять чужое исходящее соединение, и xray тогда не поднимается («address already in use»,
	// процесс выходит сразу). Поэтому занятый порт распознаётся по выходу процесса и берётся следующий.
	var last error
	for try := 0; try < 5; try++ {
		port := int(38410 + xrayPort.Add(1))
		n, err := tryStartXray(t, xray, port)
		if err == nil {
			return n
		}
		last = err
	}
	t.Fatalf("xray не поднялся: %v", last)
	return nil
}

func tryStartXray(t *testing.T, xray string, port int) (*xrayNode, error) {
	t.Helper()
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("порт %d занят", port)
	}
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "error"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "vless",
			"settings":       map[string]any{"clients": []any{map[string]any{"id": accUUID}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"}}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []any{"127.0.0.0/8"}}}}}},
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "srv.json")
	_ = os.WriteFile(path, b, 0o644)
	cmd := exec.Command(xray, "run", "-c", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	stop := func() { _ = cmd.Process.Signal(syscall.SIGCONT); _ = cmd.Process.Kill(); <-exited }
	for i := 0; i < 150; i++ { // 15 с: под -race и при параллельном запуске пакетов xray поднимается заметно дольше 5 с
		select {
		case <-exited:
			return nil, fmt.Errorf("xray на порту %d завершился при запуске", port)
		default:
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			c.Close()
			n := &xrayNode{t: t, cmd: cmd, port: port, id: store.NodeID(fmt.Sprintf("xray-%d", port))}
			t.Cleanup(stop)
			return n, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return nil, fmt.Errorf("xray на порту %d не открыл порт за 15 с", port)
}

func (n *xrayNode) freeze() { _ = n.cmd.Process.Signal(syscall.SIGSTOP) }
func (n *xrayNode) thaw()   { _ = n.cmd.Process.Signal(syscall.SIGCONT) }

func (n *xrayNode) rec() store.NodeRec {
	return store.NodeRec{ID: n.id, Class: "vless/tcp/none", Proto: "vless", Server: "127.0.0.1", Port: n.port,
		Opts: []byte(fmt.Sprintf(`{"type":"vless","server":"127.0.0.1","server_port":%d,"uuid":%q}`, n.port, accUUID))}
}

// echo — цель, до которой узлы добираются: «внешний мир» теста.
func echoServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" { // долгий ответ (как long-poll): узел жив, но приложение ждёт дольше порога детектора
			time.Sleep(4 * time.Second)
		}
		fmt.Fprint(w, "pong")
	}))
	t.Cleanup(srv.Close)
	return srv, srv.URL[strings.LastIndex(srv.URL, ":")+1:]
}

// liveThroughPath проверяет, что через outbound p доходит запрос до echo-сервера (вместо gstatic в боевой проверке).
func liveThroughPath(port string) func(ctx context.Context, p *engine.Probe) prober.Result {
	return func(ctx context.Context, p *engine.Probe) prober.Result {
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/", nil)
		resp, err := p.HTTPClient(2*time.Second, 0).Do(req)
		if err != nil {
			return prober.Result{Level: 2, Class: prober.Classify(err, 0), LatencyMs: time.Since(start).Milliseconds(), Detail: "через узел"}
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return prober.Result{Level: 2, OK: resp.StatusCode == 200 && string(b) == "pong", LatencyMs: time.Since(start).Milliseconds()}
	}
}

func accCore(t *testing.T, port string, mutate func(*Config)) *Core {
	t.Helper()
	cfg := Config{
		StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", SeedSources: []aggregator.Source{},
		NetID:    func() string { return "acc-net" },
		Liveness: func(ctx context.Context, p *engine.Probe) prober.Result { return liveThroughPath(port)(ctx, p) },
		Check: func(ctx context.Context, _ store.NodeRec, p *engine.Probe) []prober.Result {
			return []prober.Result{liveThroughPath(port)(ctx, p)}
		},
		TargetCheck: func(ctx context.Context, t store.Target, _, _ string, p *engine.Probe) prober.Result {
			r := liveThroughPath(port)(ctx, p)
			r.Level, r.Target = 4, t.ID
			return r
		},
		HealthInterval: 400 * time.Millisecond, HealthTimeout: 1500 * time.Millisecond, HealthFails: 2,
		TargetInterval: 400 * time.Millisecond, AltFresh: time.Minute,
		RefreshInterval: time.Hour, CheckInterval: time.Hour,
		Monitor: monitor.Config{StallAfter: 2 * time.Second, MinStalled: 3, Tick: 200 * time.Millisecond},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (c *Core) putNode(t *testing.T, n *xrayNode) { c.putNodeIn(t, "acc", n) }

func (c *Core) putNodeIn(t *testing.T, source string, n *xrayNode) {
	t.Helper()
	if err := c.st.UpsertNodes(source, time.Now().UnixNano(), []store.NodeRec{n.rec()}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func proxyGet(c *Core, host, port string) (string, error) {
	addr, user, pass := c.AppProxy()
	pu, _ := url.Parse("http://" + addr)
	pu.User = url.UserPassword(user, pass)
	cl := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
	resp, err := cl.Get("http://" + host + ":" + port + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// P1 (б): активный узел «умирает» — уведомление (событие деградации) за считанные секунды, альтернативы проверены живьём,
// узел сам не меняется; после «оживления» — событие восстановления. Трафик в это время реально зависает (заморозка).
func TestP1ActiveNodeFreezeIsReportedNeverSwitched(t *testing.T) {
	_, port := echoServer(t)
	a, b, spare := startXrayNode(t), startXrayNode(t), startXrayNode(t)
	c := accCore(t, port, nil)
	for _, n := range []*xrayNode{a, b, spare} {
		c.putNode(t, n)
		_ = c.st.Record([]store.Row{{Node: n.id, Net: "acc-net", Level: 2, OK: true, At: time.Now()}})
	}
	if err := c.Connect(a.id, "user"); err != nil {
		t.Fatal(err)
	}
	if body, err := proxyGet(c, "127.0.0.1", port); err != nil || body != "pong" {
		t.Fatalf("трафик через живой узел: %q %v", body, err)
	}
	tag := activeTag(t, c)

	frozen := time.Now()
	a.freeze()
	// фоновый трафик пользователя — именно он питает пассивный детектор
	stopTraffic := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopTraffic:
					return
				default:
					_, _ = proxyGet(c, "127.0.0.1", port)
				}
			}
		}()
	}
	defer func() { close(stopTraffic); wg.Wait() }()

	waitFor(t, "событие деградации", func() bool { return len(eventsOf(c, EvDegraded)) >= 1 })
	took := time.Since(frozen)
	if took > 10*time.Second {
		t.Fatalf("деградация замечена через %v (цель плана ≤30 с при штатных порогах, в тесте ≤10 с)", took)
	}
	t.Logf("деградация замечена через %v", took.Round(100*time.Millisecond))
	waitFor(t, "альтернативы", func() bool { return len(eventsOf(c, EvAlternatives)) >= 1 })
	var d struct {
		Alternatives []string `json:"alternatives"`
	}
	_ = json.Unmarshal([]byte(eventsOf(c, EvAlternatives)[0].Detail), &d)
	if len(d.Alternatives) != 2 {
		t.Fatalf("альтернативы: %v (живых кроме активного два)", d.Alternatives)
	}
	for _, id := range d.Alternatives {
		if id == a.id {
			t.Fatal("замороженный узел в альтернативах")
		}
	}
	// главное: сам узел не менялся, единственная смена — пользовательская
	if got := activeTag(t, c); got != tag {
		t.Fatalf("активный узел сменился сам: %q", got)
	}
	if n := len(eventsOf(c, EvSwitch)); n != 1 {
		t.Fatalf("смен узла %d, ожидалась одна пользовательская", n)
	}
	// пользователь переключается по предложению (то, что делает кнопка уведомления) — и трафик ожил
	if err := c.Connect(d.Alternatives[0], "user"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "трафик после ручного переключения", func() bool { body, err := proxyGet(c, "127.0.0.1", port); return err == nil && body == "pong" })
	if c.Degraded() {
		t.Fatal("после смены узла деградация должна сбрасываться")
	}
	a.thaw()
}

// Восстановление без вмешательства: узел оттаял — событие recovered, выбор тот же.
func TestP1RecoveryAfterThaw(t *testing.T) {
	_, port := echoServer(t)
	a := startXrayNode(t)
	c := accCore(t, port, nil)
	c.putNode(t, a)
	_ = c.Connect(a.id, "user")
	a.freeze()
	waitFor(t, "деградация", func() bool { return c.Degraded() })
	a.thaw()
	waitFor(t, "восстановление", func() bool { return !c.Degraded() && len(eventsOf(c, EvRecovered)) == 1 })
	if n := len(eventsOf(c, EvSwitch)); n != 1 {
		t.Fatalf("смен узла %d", n)
	}
}

// P1 (а): ручной режим под нагрузкой. Подписки обновляются много раз (в том числе активный узел пропадает из источника),
// сеть «мигает», идут проверки — активный узел ни разу не сменился без пользователя, а трафик через него не прервался.
func TestP1ManualModeSurvivesRefreshesAndNetworkFlaps(t *testing.T) {
	_, port := echoServer(t)
	a := startXrayNode(t)
	f := newFeed()
	var netFlip atomic.Int32
	c := accCore(t, port, func(cfg *Config) {
		cfg.SeedSources = []aggregator.Source{{Name: "A", URL: "http://a.invalid/"}}
		cfg.Fetch = f.fetch
		cfg.RefreshInterval, cfg.CheckInterval = 150*time.Millisecond, 150*time.Millisecond
		cfg.NetInterval = 100 * time.Millisecond
		cfg.NoNetlink = true
		cfg.NetID = func() string { return fmt.Sprintf("net-%d", netFlip.Load()%2) }
	})
	// активный узел сначала присутствует в источнике A, затем пропадает из него
	c.putNodeIn(t, "A", a)
	f.set("A", vlessLink(1), vlessLink(2))
	if err := c.Connect(a.id, "user"); err != nil {
		t.Fatal(err)
	}
	tag := activeTag(t, c)

	stop := make(chan struct{})
	var fails, total atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			total.Add(1)
			if body, err := proxyGet(c, "127.0.0.1", port); err != nil || body != "pong" {
				fails.Add(1)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}()
	round := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		round++
		f.set("A", vlessLink(10+round), vlessLink(20+round%3)) // состав источника всё время меняется
		netFlip.Add(1)
		time.Sleep(250 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	if refreshes := len(eventsOf(c, EvRefresh)); refreshes < 3 {
		t.Fatalf("обновлений подписок %d, нужно ≥3", refreshes)
	}
	if len(eventsOf(c, EvNetChanged)) == 0 {
		t.Fatal("смены сети не замечены")
	}
	if got := activeTag(t, c); got != tag || !c.eng.Has(tag) {
		t.Fatalf("активный узел изменился или удалён из движка: %q", got)
	}
	if n := len(eventsOf(c, EvSwitch)); n != 1 {
		t.Fatalf("смен активного узла %d (должна быть одна — пользовательская)", n)
	}
	if st, _ := c.Status(); st.Active == nil || st.Active.ID != a.id || st.Active.Present {
		t.Fatalf("активный узел должен остаться выбранным и быть помечен пропавшим из источника: %+v", st.Active)
	}
	if fails.Load() != 0 {
		t.Fatalf("трафик через активный узел прерывался: %d ошибок из %d запросов", fails.Load(), total.Load())
	}
	t.Logf("обновлений %d, смен сети %d, запросов через узел %d без ошибок", len(eventsOf(c, EvRefresh)), len(eventsOf(c, EvNetChanged)), total.Load())
}

// P7 (б), auto-failover: узел цели «умирает» — цель сама переключается на проверенного кандидата, остальной трафик (active) не страдает.
func TestP7AutoFailoverOnFrozenTargetNode(t *testing.T) {
	_, port := echoServer(t)
	active, bad, good := startXrayNode(t), startXrayNode(t), startXrayNode(t)
	c := accCore(t, port, nil)
	for _, n := range []*xrayNode{active, bad, good} {
		c.putNode(t, n)
		_ = c.st.Record([]store.Row{{Node: n.id, Net: "acc-net", Level: 2, OK: true, At: time.Now()}})
	}
	_ = c.Connect(active.id, "user")
	tg := store.Target{ID: "videosite", Rules: store.TargetRules{DomainKeyword: []string{"localhost"}}, Check: store.TargetCheck{Kind: "http", URL: "http://127.0.0.1:" + port + "/"},
		Policy: store.PolicyAutoFailover, FailoverK: 2}
	if err := putAsApp(c, tg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SwitchTarget(context.Background(), "videosite", bad.id, "api:myapp"); err != nil {
		t.Fatal(err)
	}
	// домен цели идёт через её узел, всё остальное — через active
	if body, err := proxyGet(c, "localhost", port); err != nil || body != "pong" {
		t.Fatalf("трафик цели через её узел: %q %v", body, err)
	}
	frozen := time.Now()
	bad.freeze()
	waitForD(t, 12*time.Second, "автопереключение цели", func() bool { return c.currentOf("videosite") != bad.id })
	took := time.Since(frozen)
	if took > 10*time.Second {
		t.Fatalf("переключение заняло %v (цель плана ≤60 с)", took)
	}
	t.Logf("цель переключена через %v", took.Round(100*time.Millisecond))
	if got := c.currentOf("videosite"); got != good.id && got != active.id {
		t.Fatalf("цель переключена на непроверенный узел %q", got)
	}
	waitFor(t, "воспроизведение восстановлено", func() bool { body, err := proxyGet(c, "localhost", port); return err == nil && body == "pong" })
	if got := activeTag(t, c); got != NodeTag(active.id) {
		t.Fatal("автопереключение цели затронуло активный узел")
	}
	ev := eventsOf(c, EvTargetSwitch)
	if last := ev[len(ev)-1]; last.Initiator != "auto" || last.Target != "videosite" {
		t.Fatalf("журнал: %+v", last)
	}
	bad.thaw()
}

// P7 (б), sticky-manual: то же, но цель сама не переключается — только уведомление и проверенные альтернативы.
func TestP7StickyManualOnFrozenTargetNode(t *testing.T) {
	_, port := echoServer(t)
	active, bad, good := startXrayNode(t), startXrayNode(t), startXrayNode(t)
	c := accCore(t, port, nil)
	for _, n := range []*xrayNode{active, bad, good} {
		c.putNode(t, n)
		_ = c.st.Record([]store.Row{{Node: n.id, Net: "acc-net", Level: 2, OK: true, At: time.Now()}})
	}
	_ = c.Connect(active.id, "user")
	tg := store.Target{ID: "videosite", Rules: store.TargetRules{DomainKeyword: []string{"localhost"}}, Check: store.TargetCheck{Kind: "http", URL: "http://127.0.0.1:" + port + "/"},
		Policy: store.PolicyStickyManual, FailoverK: 2}
	_ = putAsApp(c, tg)
	_, _ = c.SwitchTarget(context.Background(), "videosite", bad.id, "api:myapp")
	frozen := time.Now()
	bad.freeze()
	waitForD(t, 12*time.Second, "деградация цели", func() bool { return len(eventsOf(c, EvTargetDegrade)) >= 1 })
	if took := time.Since(frozen); took > 10*time.Second {
		t.Fatalf("деградация цели замечена через %v (цель плана ≤30 с)", took)
	}
	waitForD(t, 12*time.Second, "альтернативы цели", func() bool { return len(eventsOf(c, EvAlternatives)) >= 1 })
	time.Sleep(1500 * time.Millisecond) // дать шанс ошибочному автопереключению
	if got := c.currentOf("videosite"); got != bad.id {
		t.Fatalf("sticky-manual переключил цель сам: %q", got)
	}
	bad.thaw()
}

// Ложные срабатывания пассивного детектора (долгие ответы при живом узле) не объявляют деградацию и не меняют узел.
func TestP1SlowResponsesDoNotTriggerDegradation(t *testing.T) {
	_, port := echoServer(t)
	a := startXrayNode(t)
	c := accCore(t, port, func(cfg *Config) {
		cfg.Monitor = monitor.Config{StallAfter: time.Second, MinStalled: 3, Tick: 100 * time.Millisecond}
	})
	c.putNode(t, a)
	_ = c.Connect(a.id, "user")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr, user, pass := c.AppProxy()
			pu, _ := url.Parse("http://" + addr)
			pu.User = url.UserPassword(user, pass)
			cl := &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
			if resp, err := cl.Get("http://127.0.0.1:" + port + "/slow"); err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	time.Sleep(time.Second)
	if c.Degraded() || len(eventsOf(c, EvDegraded)) != 0 {
		t.Fatal("медленные ответы при живом узле объявлены деградацией")
	}
	if n := len(eventsOf(c, EvSwitch)); n != 1 {
		t.Fatalf("смен узла %d", n)
	}
}

// openTunnel открывает CONNECT-туннель через app-вход к 127.0.0.1:port (долгоживущее соединение, как HTTP/2 в браузере).
func openTunnel(t *testing.T, c *Core, target string) net.Conn {
	t.Helper()
	addr, user, pass := c.AppProxy()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		t.Fatalf("CONNECT: %q %v", line, err)
	}
	for { // дочитать заголовки ответа
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	return conn
}

// После ручной смены узла уже открытые туннели старого узла рвутся сразу, а не висят до своих таймаутов
// (браузер с долгоживущим CONNECT иначе «залипал» бы на мёртвом узле).
func TestP1ManualSwitchDropsStuckTunnels(t *testing.T) {
	_, port := echoServer(t)
	a, b := startXrayNode(t), startXrayNode(t)
	c := accCore(t, port, nil)
	c.putNode(t, a)
	c.putNode(t, b)
	_ = c.Connect(a.id, "user")
	tunnel := openTunnel(t, c, "127.0.0.1:"+port)
	defer tunnel.Close()
	// убеждаемся, что туннель действительно установлен через узел (CONNECT отвечает 200 раньше, чем завершится дозвон)
	fmt.Fprintf(tunnel, "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive\r\n\r\n")
	_ = tunnel.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	n, err := tunnel.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("туннель не работает: %q %v", buf[:n], err)
	}
	a.freeze() // узел замёрз вместе с открытым туннелем
	if err := c.Connect(b.id, "user"); err != nil {
		t.Fatal(err)
	}
	_ = tunnel.SetReadDeadline(time.Now().Add(3 * time.Second))
	t0 := time.Now()
	_, err = tunnel.Read(make([]byte, 16))
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("туннель старого узла остался висеть (Read: %v через %v)", err, time.Since(t0))
	}
	a.thaw()
	if body, err := proxyGet(c, "127.0.0.1", port); err != nil || body != "pong" {
		t.Fatalf("новые соединения через новый узел: %q %v", body, err)
	}
}
