package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"brod/store"
)

// socksNode кладёт в базу узел socks на закрытый локальный порт: для движка это настоящий исходящий, но трафик в нём не нужен.
func socksNode(t *testing.T, c *Core, key string, port int, proto string) string {
	t.Helper()
	opts, _ := json.Marshal(map[string]any{"type": "socks", "server": "127.0.0.1", "server_port": port})
	id := store.NodeID(key)
	now := time.Now()
	if err := c.st.UpsertNodes("src", now.UnixNano(), []store.NodeRec{{ID: id, Class: "socks/tcp/none", Proto: proto, Server: "127.0.0.1", Port: port, Opts: opts}}, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAddChainValidation(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	a := socksNode(t, c, "chain-a", 1, "socks")
	b := socksNode(t, c, "chain-b", 2, "socks")
	d := socksNode(t, c, "chain-d", 3, "socks")
	e := socksNode(t, c, "chain-e", 4, "socks")
	wg := socksNode(t, c, "chain-wg", 5, "wireguard")

	for name, hops := range map[string][]string{"одно звено": {a}, "четыре звена": {a, b, d, e}, "дубль": {a, a}, "WireGuard": {a, wg}, "нет узла": {a, "deadbeef"}} {
		if _, err := c.AddChain("", hops, "test"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: ждали ErrInvalid, получили %v", name, err)
		}
	}
	id, err := c.AddChain("Моя цепочка", []string{a, b, d}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddChain("", []string{a, id}, "test"); !errors.Is(err, ErrInvalid) {
		t.Errorf("вложенная цепочка принята: %v", err)
	}
	rec, err := c.st.Node(id)
	if err != nil || rec.Proto != chainProto || !strings.HasPrefix(rec.Class, "chain/") {
		t.Fatalf("запись цепочки: %+v %v", rec, err)
	}
	own, _ := c.st.OwnIDs()
	if !own[id] {
		t.Error("цепочка должна быть собственным узлом: удаляется тем же способом")
	}
	v := chainViewOf(rec)
	if len(v) != 3 || v[0].ID != a || v[2].ID != d || v[1].Port != 2 {
		t.Errorf("звенья: %+v", v)
	}
	if got := nodeServerNames(rec); len(got) != 3 {
		t.Errorf("серверы звеньев: %v", got)
	}
	if err := c.EditOwn(id, EditOwnReq{Server: ptr("x")}, "test"); !errors.Is(err, ErrInvalid) {
		t.Errorf("адрес цепочки изменился: %v", err)
	}
	if err := c.EditOwn(id, EditOwnReq{Label: ptr("Новое имя")}, "test"); err != nil {
		t.Errorf("название цепочки не меняется: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// Исходящее цепочки: вспомогательные звенья регистрируются в движке, последнее звено — выход с detour на предыдущее; пробный outbound создаётся;
// удаление цепочки убирает звенья.
func TestChainOutboundsAndCleanup(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	a := socksNode(t, c, "chain-a", 1, "socks")
	b := socksNode(t, c, "chain-b", 2, "socks")
	d := socksNode(t, c, "chain-d", 3, "socks")
	id, err := c.AddChain("", []string{a, b, d}, "test")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := c.st.Node(id)
	raw, err := c.nodeOutboundJSON(rec, "exit-tag", false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	h1 := chainTag(id, false, 1)
	if m["tag"] != "exit-tag" || m["detour"] != h1 || m["server_port"].(float64) != 3 {
		t.Fatalf("выход цепочки: %s", raw)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.eng.Existing(chainTag(id, false, i)); err != nil {
			t.Errorf("звено %d не зарегистрировано: %v", i, err)
		}
	}
	// повторный вызов безопасен
	if _, err := c.nodeOutboundJSON(rec, "exit-tag", false); err != nil {
		t.Fatalf("повторная регистрация: %v", err)
	}
	// через WARP первое звено получает detour на warp
	h0, err := hopOutbound(ChainHop{Opts: json.RawMessage(`{"type":"socks","server":"x","server_port":1}`)}, "t0", OutWarp)
	if err != nil || !strings.Contains(string(h0), `"detour":"warp"`) {
		t.Errorf("detour на WARP: %s %v", h0, err)
	}
	opts, err := c.probeOpts(rec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		t.Fatalf("пробный outbound цепочки не создаётся: %v", err)
	}
	p.Close()
	if err := c.RemoveOwn(id, "test"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.eng.Existing(chainTag(id, false, i)); err == nil {
			t.Errorf("звено %d осталось в движке после удаления цепочки", i)
		}
	}
}

// Цепочки не попадают в автоподбор: ни в «лучший узел», ни в кандидаты на замену.
func TestChainsAreNotAutoSelected(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	a := socksNode(t, c, "chain-a", 1, "socks")
	b := socksNode(t, c, "chain-b", 2, "socks")
	id, err := c.AddChain("", []string{a, b}, "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []store.Row{}
	for _, n := range []string{a, id} {
		rows = append(rows, store.Row{Node: n, NodeClass: "x/y/z", Net: c.netKey(), Level: 2, OK: true, LatencyMs: 50, At: now})
	}
	if err := c.st.Record(rows); err != nil {
		t.Fatal(err)
	}
	best, err := c.BestNodes(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range best {
		if v.ID == id {
			t.Fatal("цепочка попала в «лучшие узлы»")
		}
	}
	if len(best) != 1 || best[0].ID != a {
		t.Errorf("лучшие узлы: %+v", best)
	}
	recs, _ := c.st.Nodes(store.NodeFilter{IncludeNoSecurity: true})
	if len(withoutChains(recs)) != len(recs)-1 {
		t.Error("withoutChains не убрал цепочку")
	}
}

// miniSocks — минимальный SOCKS5-прокси без авторизации: принимает CONNECT, запоминает адрес назначения и связывает с ним клиента.
type miniSocks struct {
	ln      net.Listener
	mu      sync.Mutex
	targets []string
}

func startMiniSocks(t *testing.T) *miniSocks {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("нет loopback TCP:", err)
	}
	s := &miniSocks{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *miniSocks) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *miniSocks) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *miniSocks) serve(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 262)
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 5 {
		return
	}
	if _, err := io.ReadFull(c, buf[:int(buf[1])]); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 0})
	if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[1] != 1 {
		return
	}
	var host string
	switch buf[3] {
	case 1:
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 3:
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		n := int(buf[0])
		if _, err := io.ReadFull(c, buf[:n]); err != nil {
			return
		}
		host = string(buf[:n])
	default:
		return
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(buf[0])<<8|int(buf[1])))
	s.mu.Lock()
	s.targets = append(s.targets, addr)
	s.mu.Unlock()
	up, err := net.Dial("tcp", addr)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
	_, _ = io.Copy(c, up)
}

// Сквозная проверка: клиент → звено 1 (SOCKS) → звено 2 (SOCKS) → эхо-сервер. Первое звено видит запрос на соединение со вторым, второе — с эхо-сервером;
// байты доходят и возвращаются.
func TestChainCarriesTrafficThroughBothHops(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("нет loopback TCP:", err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	s1, s2 := startMiniSocks(t), startMiniSocks(t)

	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	a := socksNode(t, c, "e2e-a", s1.port(), "socks")
	b := socksNode(t, c, "e2e-b", s2.port(), "socks")
	id, err := c.AddChain("", []string{a, b}, "test")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := c.st.Node(id)
	opts, err := c.probeOpts(rec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := p.DialContext(ctx, "tcp", echo.Addr().String())
	if err != nil {
		t.Fatalf("соединение через цепочку: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping-через-цепочку")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("ping-через-цепочку"))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ping-через-цепочку" {
		t.Fatalf("эхо: %q %v", got, err)
	}
	if seen := s1.seen(); len(seen) != 1 || seen[0] != net.JoinHostPort("127.0.0.1", strconv.Itoa(s2.port())) {
		t.Errorf("первое звено должно получить запрос на соединение со вторым звеном, получило %v", seen)
	}
	if seen := s2.seen(); len(seen) != 1 || seen[0] != echo.Addr().String() {
		t.Errorf("второе звено должно получить запрос на эхо-сервер, получило %v", seen)
	}
}

// Цепочка как активный узел: подключение регистрирует выход и звенья, смена узла убирает выход, удаление цепочки — звенья.
func TestChainAsActiveNode(t *testing.T) {
	c := startCore(t, t.TempDir())
	a := socksNode(t, c, "act-a", 1, "socks")
	b := socksNode(t, c, "act-b", 2, "socks")
	other := socksNode(t, c, "act-other", 3, "socks")
	id, err := c.AddChain("", []string{a, b}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(id, "user"); err != nil {
		t.Fatalf("подключение цепочкой: %v", err)
	}
	c.mu.Lock()
	_, registered := c.reg[id]
	c.mu.Unlock()
	if !registered {
		t.Fatal("выход цепочки не зарегистрирован")
	}
	if _, err := c.eng.Existing(chainTag(id, false, 0)); err != nil {
		t.Errorf("звено не зарегистрировано: %v", err)
	}
	if err := c.RemoveOwn(id, "test"); !errors.Is(err, ErrBusy) {
		t.Errorf("активную цепочку удалять нельзя: %v", err)
	}
	if err := c.Connect(other, "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveOwn(id, "test"); err != nil {
		t.Fatalf("удаление неактивной цепочки: %v", err)
	}
	if _, err := c.eng.Existing(chainTag(id, false, 0)); err == nil {
		t.Error("звено осталось в движке")
	}
}
