package mobile

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluate(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "whitelist.txt"), []byte("yandex.ru\nmax.ru\n"), 0o644)
	os.WriteFile(filepath.Join(d, "cidrwhitelist.txt"), []byte("203.0.113.0/24\n2001:db8::/32\n"), 0o644)
	os.WriteFile(filepath.Join(d, "ipwhitelist.txt"), []byte("198.51.100.7\n"), 0o644)
	l, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		opts  map[string]any
		score int
	}{
		{map[string]any{"server": "203.0.113.9", "tls": map[string]any{"server_name": "api.yandex.ru"}}, 3}, // подсеть + поддомен
		{map[string]any{"server": "198.51.100.7", "tls": map[string]any{"server_name": "example.org"}}, 2},  // точный IP
		{map[string]any{"server": "192.0.2.1", "tls": map[string]any{"server_name": "max.ru"}}, 1},          // только SNI
		{map[string]any{"server": "192.0.2.1", "tls": map[string]any{"server_name": "notyandex.ru"}}, 0},    // не поддомен
		{map[string]any{"server": "2001:db8::5", "transport": map[string]any{"host": "yandex.ru"}}, 3},      // IPv6 + Host
		{map[string]any{"server": "node.example.net"}, 0},                                                   // домен-хост без резолва
	}
	for i, c := range cases {
		if got := l.Evaluate(c.opts).Score(); got != c.score {
			t.Errorf("случай %d: score=%d, ожидалось %d", i, got, c.score)
		}
	}
}

func TestEvaluateResolved(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "whitelist.txt"), []byte("yandex.ru\n"), 0o644)
	os.WriteFile(filepath.Join(d, "cidrwhitelist.txt"), []byte("203.0.113.0/24\n"), 0o644)
	l, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r := &Resolver{Budget: 2, Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		calls++
		if host == "good.example.net" {
			return []netip.Addr{netip.MustParseAddr("203.0.113.5")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.9")}, nil
	}}
	ctx := context.Background()
	good := map[string]any{"server": "good.example.net", "tls": map[string]any{"server_name": "yandex.ru"}}
	if v := l.EvaluateResolved(ctx, good, r); v.Score() != 3 || !v.Resolved {
		t.Fatalf("домен с адресом в списке и SNI: %+v", v)
	}
	l.EvaluateResolved(ctx, good, r) // из кэша
	if calls != 1 {
		t.Fatalf("повторный запрос не из кэша: %d вызовов", calls)
	}
	if v := l.EvaluateResolved(ctx, map[string]any{"server": "bad.example.net", "tls": map[string]any{"server_name": "yandex.ru"}}, r); v.Score() != 1 || !v.Resolved {
		t.Fatalf("адрес вне списка: %+v", v)
	}
	// без разрешённого SNI домен не резолвится вовсе
	if l.EvaluateResolved(ctx, map[string]any{"server": "other.example.net", "tls": map[string]any{"server_name": "example.org"}}, r); calls != 2 {
		t.Fatalf("резолв домена без разрешённого SNI: %d вызовов", calls)
	}
	// бюджет исчерпан: резолва нет, оценка как у Evaluate
	if v := l.EvaluateResolved(ctx, map[string]any{"server": "third.example.net", "tls": map[string]any{"server_name": "yandex.ru"}}, r); v.Resolved || v.Score() != 1 || calls != 2 {
		t.Fatalf("после бюджета: %+v, вызовов %d", v, calls)
	}
}

func TestEmbeddedSnapshot(t *testing.T) {
	l, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	d, i, c := l.Sizes()
	if d < 500 || i < 100_000 || c < 20_000 {
		t.Fatalf("встроенный снимок неполон: доменов %d, IP %d, CIDR %d", d, i, c)
	}
	if !l.Domain("yandex.ru") || !l.Domain("api.yandex.ru") || l.Domain("notyandex.ru") {
		t.Fatal("домены встроенного списка")
	}
	// адрес из самого списка и соседний вне его
	for _, a := range l.ips[:3] {
		if !l.IP(a) {
			t.Fatalf("%s из списка не найден", a)
		}
	}
	if l.IP(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("адрес документации не должен быть в списке")
	}
}
