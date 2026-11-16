package core

import (
	"bufio"
	"compress/gzip"
	"context"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"brod/store"
)

// firstListedIP читает из встроенного снимка первый IPv4-адрес белого списка (тест не должен зависеть от конкретного значения).
func firstListedIP(t *testing.T) string {
	t.Helper()
	f, err := os.Open("../mobile/data/ipwhitelist.txt.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		if a, err := netip.ParseAddr(strings.TrimSpace(sc.Text())); err == nil && a.Is4() {
			return a.String()
		}
	}
	t.Fatal("в снимке нет IPv4-адресов")
	return ""
}

func TestIsMobileNet(t *testing.T) {
	for id, want := range map[string]bool{"cell:25001": true, "mobile-tether:wifi:10.0.0.1": true, "wifi:192.168.0.1": false, "eth:x": false, "offline": false, "": false} {
		if got := isMobileNet(id); got != want {
			t.Errorf("%q: %v, ожидалось %v", id, got, want)
		}
	}
}

// В мобильной сети узел с адресом из белого списка попадает в верх очереди проверок, в обычной сети порядок обычный.
func TestPickCandidatesBoostsWhitelistedIPOnMobile(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	now := time.Now()
	listed := firstListedIP(t)
	mk := func(key, server string) store.NodeRec {
		return store.NodeRec{ID: store.NodeID(key), Class: "vless/tcp/reality", Proto: "direct", Server: server, Port: 443, Opts: []byte(`{"type":"direct"}`)}
	}
	recs := []store.NodeRec{mk("a", "192.0.2.10"), mk("b", "192.0.2.11"), mk("c", "192.0.2.12"), mk("z-listed", listed)}
	if err := c.st.UpsertNodes("src", 1, recs, now); err != nil {
		t.Fatal(err)
	}
	want := recs[3].ID
	pick := func(net string) string {
		c.cfg.NetID = func() string { return net }
		got, err := c.pickCandidates(net, CheckOptions{Limit: 1}, now)
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %v, %d кандидатов", net, err, len(got))
		}
		return got[0].ID
	}
	if got := pick("cell:25001"); got != want {
		t.Fatalf("мобильная сеть: первым ожидался узел из белого списка, получен %s", got)
	}
	if got := pick("wifi:192.168.0.1"); got == want {
		t.Fatal("в обычной сети узел из белого списка не должен получать добавку")
	}
}

func TestCheckParallelShrinksOnMetered(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	c.cfg.CheckParallel = 48
	if c.checkParallel() != 48 {
		t.Fatalf("без платной сети: %d", c.checkParallel())
	}
	c.cfg.Metered = func() bool { return true }
	if c.checkParallel() != mobileCheckParallel {
		t.Fatalf("в платной сети: %d", c.checkParallel())
	}
	c.cfg.CheckParallel = 4
	if c.checkParallel() != 4 {
		t.Fatalf("меньше предела не увеличивать: %d", c.checkParallel())
	}
}

func TestDetectNetMode(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	reach := map[string]bool{}
	c.cfg.NetModeProbe = func(_ context.Context, url string) bool { return reach[url] }
	set := func(urls []string, v bool) {
		for _, u := range urls {
			reach[u] = v
		}
	}
	ctx := context.Background()
	set(netModeAllowed, true)
	set(netModeForeign, false)
	if got := c.DetectNetMode(ctx); got != NetModeWhitelist {
		t.Fatalf("только российские: %s", got)
	}
	reach[netModeForeign[1]] = true // достаточно одного зарубежного
	if got := c.DetectNetMode(ctx); got != NetModeOpen {
		t.Fatalf("есть зарубежный: %s", got)
	}
	set(netModeAllowed, false)
	set(netModeForeign, false)
	if got := c.DetectNetMode(ctx); got != NetModeOffline {
		t.Fatalf("ничего не открывается: %s", got)
	}
}

// Режим определяется только в мобильной сети и при выключенном VPN; в статусе виден, пока относится к текущей сети.
func TestNetModeOnlyOnMobileWithoutVPN(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	var calls atomic.Int32
	c.cfg.NetModeProbe = func(_ context.Context, url string) bool {
		calls.Add(1)
		for _, u := range netModeAllowed {
			if u == url {
				return true
			}
		}
		return false
	}
	net := "wifi:192.168.0.1"
	c.cfg.NetID = func() string { return net }
	ctx := context.Background()
	c.netModeOnce(ctx)
	if calls.Load() != 0 || c.NetMode() != "" {
		t.Fatalf("в Wi-Fi режим не определяется: вызовов %d, режим %q", calls.Load(), c.NetMode())
	}
	net = "cell:25001"
	c.cfg.TUN.Enabled = true
	c.netModeOnce(ctx)
	if calls.Load() != 0 {
		t.Fatal("при включённом VPN прямая проверка бессмысленна")
	}
	c.cfg.TUN.Enabled = false
	c.netModeOnce(ctx)
	if c.NetMode() != NetModeWhitelist {
		t.Fatalf("режим %q", c.NetMode())
	}
	if st, _ := c.Status(); st.NetMode != NetModeWhitelist {
		t.Fatalf("статус: %q", st.NetMode)
	}
	n := calls.Load()
	c.netModeOnce(ctx) // свежий результат не перепроверяется
	if calls.Load() != n {
		t.Fatal("свежий результат перепроверен")
	}
	net = "cell:25099" // другая сеть: результат старой не показывается
	if c.NetMode() != "" {
		t.Fatalf("результат прежней сети: %q", c.NetMode())
	}
}

// Потеря связи и возврат в ту же сотовую сеть: провалы, записанные до возврата, больше не держат узлы в очереди «повторять рано».
func TestCellReattachForgivesRecentFailures(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	now := time.Now()
	n := store.NodeRec{ID: store.NodeID("fail"), Class: "vless/tcp/reality", Proto: "direct", Server: "192.0.2.77", Port: 443, Opts: []byte(`{"type":"direct"}`)}
	if err := c.st.UpsertNodes("src", 1, []store.NodeRec{n}, now); err != nil {
		t.Fatal(err)
	}
	net := "cell:25060"
	c.cfg.NetID = func() string { return net }
	if err := c.st.Record([]store.Row{{Node: n.ID, Net: net, NodeClass: n.Class, Level: 2, OK: false, At: now.Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	pick := func() int {
		got, err := c.pickCandidates(net, CheckOptions{Limit: 5}, now)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	c.CheckNetwork() // первая запись о сети
	if pick() != 0 {
		t.Fatal("свежий провал должен держать узел вне очереди")
	}
	net = "offline"
	c.CheckNetwork()
	net = "cell:25060"
	c.CheckNetwork() // возврат в ту же сеть
	if pick() != 1 {
		t.Fatal("после возврата в сотовую сеть провал до возврата должен быть прощён")
	}
	// другая сеть после потери связи прощения не даёт: у неё свой ключ истории
	if c.reattachAt.Load() == 0 {
		t.Fatal("момент возврата не записан")
	}
}

func TestWifiReturnDoesNotForgiveFailures(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	net := "wifi:192.168.0.1"
	c.cfg.NetID = func() string { return net }
	c.CheckNetwork()
	net = "offline"
	c.CheckNetwork()
	net = "wifi:192.168.0.1"
	c.CheckNetwork()
	if c.reattachAt.Load() != 0 {
		t.Fatal("прощение только для сотовой сети")
	}
}
