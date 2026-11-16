package monitor

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

// stalledConns открывает n соединений в области scope: приложение отправило данные, ответа нет (answer=false) либо есть.
func stalledConns(t *testing.T, m *Monitor, host string, n int, answer bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		app, srv := net.Pipe()
		t.Cleanup(func() { app.Close(); srv.Close() })
		w := m.RoutedConnection(context.Background(), srv, adapter.InboundContext{Destination: M.ParseSocksaddr(host + ":443")}, nil, nil)
		go func() { _, _ = app.Write([]byte("x")) }()
		buf := make([]byte, 1)
		if _, err := w.Read(buf); err != nil {
			t.Fatal(err)
		}
		if answer {
			go func() { _, _ = app.Read(make([]byte, 8)) }()
			if _, err := w.Write([]byte("ok")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Области независимы: зависание соединений цели не поднимает тревогу активного узла и наоборот.
func TestScopesAreIndependent(t *testing.T) {
	var mu sync.Mutex
	var got []Event
	m := New(Config{StallAfter: 100 * time.Millisecond, MinStalled: 3, Tick: 20 * time.Millisecond}, func(e Event) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	m.Scope = func(md adapter.InboundContext, _ adapter.Outbound) string {
		switch md.Destination.Fqdn {
		case "vidsite.example":
			return "target:vidsite"
		case "other.example":
			return ScopeActive
		}
		return ""
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	stalledConns(t, m, "vidsite.example", 3, false) // цель зависла
	stalledConns(t, m, "other.example", 3, true)    // активный отвечает
	stalledConns(t, m, "ignored.example", 5, false)

	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Scope != "target:vidsite" || got[0].Stalled != 3 {
		t.Fatalf("события: %+v", got)
	}
}

func TestResetStartsNewEpisode(t *testing.T) {
	var mu sync.Mutex
	n := 0
	m := New(Config{StallAfter: 50 * time.Millisecond, MinStalled: 2, Tick: 10 * time.Millisecond}, func(Event) { mu.Lock(); n++; mu.Unlock() })
	m.Scope = func(adapter.InboundContext, adapter.Outbound) string { return ScopeActive }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	stalledConns(t, m, "a.example", 2, false)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	first := n
	mu.Unlock()
	if first != 1 {
		t.Fatalf("первый эпизод: %d событий", first)
	}
	time.Sleep(100 * time.Millisecond) // эпизод продолжается — повторных событий нет
	mu.Lock()
	if n != 1 {
		t.Fatalf("повтор в том же эпизоде: %d", n)
	}
	mu.Unlock()
	m.Reset(ScopeActive) // смена узла: эпизод начинается заново, зависшие соединения снова считаются
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Fatalf("после Reset ожидали новый эпизод, событий %d", n)
	}
}
