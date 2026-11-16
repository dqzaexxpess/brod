package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"brod/engine"
)

// Живая проверка условий правил настоящим движком, без root и TUN: вход mixed на loopback, два direct-выхода с подменой адреса на локальные
// серверы A («правило сработало») и B («не сработало»). Правила собираются нашим routeObject, как в боевом конфиге. Запрос идёт от самого теста,
// поэтому его процесс, путь и адрес источника известны.

type matchRig struct {
	t     *testing.T
	proxy string
}

func portOf(u string) int {
	p, _ := strconv.Atoi(u[strings.LastIndex(u, ":")+1:])
	return p
}

func newMatchRig(t *testing.T, rules []RouteRule) *matchRig {
	t.Helper()
	srv := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, name) }))
		t.Cleanup(s.Close)
		return s
	}
	a, b := srv("A"), srv("B")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	// подмена адреса назначения делается опциями маршрута правила (в direct-выходе эти поля удалены из sing-box 1.13)
	hit := func(o map[string]any, srv *httptest.Server) map[string]any {
		o["action"], o["outbound"] = "route", "direct"
		o["override_address"], o["override_port"] = "127.0.0.1", portOf(srv.URL)
		return o
	}
	var routes []any
	routes = append(routes, map[string]any{"action": "sniff"})
	for i := range rules {
		if err := rules[i].validate(); err != nil {
			t.Fatalf("правило %d: %v", i, err)
		}
		routes = append(routes, hit(rules[i].routeObject(), a))
	}
	routes = append(routes, hit(map[string]any{}, b)) // последнее правило без условий: «не сработало»
	cfg := map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": proxyPort}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"rules": routes, "final": "direct", "find_process": true},
	}
	raw, _ := json.Marshal(cfg)
	e, err := engine.NewFromJSON("error", raw)
	if err != nil {
		t.Fatalf("движок не принял конфиг: %v\n%s", err, raw)
	}
	t.Cleanup(func() { e.Close() })
	return &matchRig{t: t, proxy: fmt.Sprintf("127.0.0.1:%d", proxyPort)}
}

// get делает запрос на host через прокси движка; srcPort != 0 фиксирует порт источника. Возвращает «A», «B» или ошибку.
func (r *matchRig) get(host string, srcPort int) string {
	r.t.Helper()
	pu, _ := url.Parse("http://" + r.proxy)
	d := &net.Dialer{Timeout: 3 * time.Second}
	if srcPort != 0 {
		d.LocalAddr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: srcPort}
	}
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DialContext: d.DialContext, DisableKeepAlives: true}}
	var last error
	for i := 0; i < 20; i++ { // движок мог ещё не начать слушать
		resp, err := c.Get("http://" + host + "/")
		if err != nil {
			last = err
			time.Sleep(150 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	return "ошибка: " + last.Error()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRuleConditionsMatchInEngine(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type tc struct {
		name    string
		match   func(srcPort int) RouteMatch // порт источника у каждого подтеста свой: фиксированный упирался бы в TIME_WAIT прошлых
		host    string
		usePort bool
		want    string
	}
	m := func(x RouteMatch) func(int) RouteMatch { return func(int) RouteMatch { return x } }
	cases := []tc{
		{"домен: совпал", m(RouteMatch{DomainSuffix: []string{"match.example"}}), "x.match.example", false, "A"},
		{"домен: не совпал", m(RouteMatch{DomainSuffix: []string{"match.example"}}), "other.example", false, "B"},
		{"путь процесса: этот тест", m(RouteMatch{ProcessPath: []string{exe}}), "any.example", false, "A"},
		{"путь процесса: чужой", m(RouteMatch{ProcessPath: []string{"/usr/bin/нет-такого"}}), "any.example", false, "B"},
		{"регулярка пути процесса: совпала", m(RouteMatch{ProcessPathRegex: []string{`\.test(\.exe)?$`}}), "any.example", false, "A"}, // на Windows core.test.exe
		{"регулярка пути процесса: не совпала", m(RouteMatch{ProcessPathRegex: []string{`^/нет/`}}), "any.example", false, "B"},
		{"имя процесса: этот тест", m(RouteMatch{ProcessName: []string{filepath.Base(exe)}}), "any.example", false, "A"},
		{"адрес источника: loopback", m(RouteMatch{SourceIPCIDR: []string{"127.0.0.0/8"}}), "any.example", false, "A"},
		{"адрес источника: чужая сеть", m(RouteMatch{SourceIPCIDR: []string{"10.0.0.0/8"}}), "any.example", false, "B"},
		{"источник локальный (loopback — частный)", m(RouteMatch{SourceIPIsPrivate: true}), "any.example", false, "A"},
		{"порт источника: тот самый", func(p int) RouteMatch { return RouteMatch{SourcePort: []int{p}} }, "any.example", true, "A"},
		{"порт источника: другой", func(p int) RouteMatch { return RouteMatch{SourcePort: []int{p + 1}} }, "any.example", true, "B"},
		{"диапазон портов источника: внутри", func(p int) RouteMatch { return RouteMatch{SourcePortRange: []string{fmt.Sprintf("%d:%d", p-1, p+1)}} }, "any.example", true, "A"},
		{"инверсия: домен не тот — применяется", m(RouteMatch{DomainSuffix: []string{"match.example"}, Invert: true}), "other.example", false, "A"},
		{"инверсия: домен тот — не применяется", m(RouteMatch{DomainSuffix: []string{"match.example"}, Invert: true}), "x.match.example", false, "B"},
		{"протокол http (sniff)", m(RouteMatch{Protocol: []string{"http"}, DomainSuffix: []string{"match.example"}}), "x.match.example", false, "A"},
		{"протокол tls не совпадает с http-запросом", m(RouteMatch{Protocol: []string{"tls"}, DomainSuffix: []string{"match.example"}}), "x.match.example", false, "B"},
		{"сеть tcp", m(RouteMatch{Network: "tcp", DomainSuffix: []string{"match.example"}}), "x.match.example", false, "A"},
		{"сеть udp для tcp-запроса", m(RouteMatch{Network: "udp", DomainSuffix: []string{"match.example"}}), "x.match.example", false, "B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			port := 0
			if c.usePort {
				port = freePort(t)
			}
			rig := newMatchRig(t, []RouteRule{{Enabled: true, Action: ActionDirect, Match: c.match(port)}})
			if got := rig.get(c.host, port); got != c.want {
				t.Fatalf("ответил %q, ожидался %q", got, c.want)
			}
		})
	}
}

// Первое совпавшее правило решает; выключенные не участвуют (в конфиг не попадают).
func TestRuleOrderInEngine(t *testing.T) {
	rig := newMatchRig(t, []RouteRule{
		{Enabled: true, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"first.example"}}},
		{Enabled: true, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"second.example"}}},
	})
	if got := rig.get("a.first.example", 0); got != "A" {
		t.Fatalf("a.first.example: %q", got)
	}
	if got := rig.get("x.second.example", 0); got != "A" {
		t.Fatalf("x.second.example: %q (второе правило должно сработать)", got)
	}
	if got := rig.get("other.test", 0); got != "B" {
		t.Fatalf("other.test: %q (не должно совпасть)", got)
	}
	_ = context.Background
}
