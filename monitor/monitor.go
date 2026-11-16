// Package monitor — пассивный детектор зависания активного узла (P2).
//
// Подключается к роутеру движка как ConnectionTracker и следит за реальным трафиком пользователя, без дополнительных
// запросов. Признак зависания: на нескольких соединениях подряд приложение отправило данные (Read со стороны входа),
// а ответ узла (Write в сторону приложения) не приходит дольше StallAfter. Срабатывание — один раз за эпизод;
// дальше вызывающая сторона запускает внеочередную проверку L2–L4 и показывает уведомление по P1.
package monitor

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-tun"
	N "github.com/sagernet/sing/common/network"
)

// Config — пороги детектора (настраиваемые: ТСПУ меняет поведение).
type Config struct {
	StallAfter time.Duration // сколько ждать ответа на отправленные данные (по умолчанию 8 с)
	MinStalled int           // сколько соединений должны быть «зависшими» одновременно или недавно (по умолчанию 3)
	Window     time.Duration // окно для недавно закрытых без ответа (по умолчанию 30 с)
	Tick       time.Duration // период проверки (по умолчанию 1 с)
}

func (c *Config) defaults() {
	if c.StallAfter == 0 {
		c.StallAfter = 8 * time.Second
	}
	if c.MinStalled == 0 {
		c.MinStalled = 3
	}
	if c.Window == 0 {
		c.Window = 30 * time.Second
	}
	if c.Tick == 0 {
		c.Tick = time.Second
	}
}

// Event — срабатывание детектора.
type Event struct {
	At       time.Time
	Scope    string // область: "active" либо идентификатор цели (см. Scoper)
	Outbound string
	Stalled  int // сколько соединений признано зависшими (открытые + недавно закрытые без ответа)
	Reason   string
}

// Scoper относит маршрутизируемое соединение к области наблюдения (активный узел, цель) или возвращает "" — не наблюдать.
// Роутер передаёт трекеру первый outbound маршрута (для app-входа это dispatch, а не узел), поэтому область определяется
// назначением соединения, а не тегом.
type Scoper func(md adapter.InboundContext, out adapter.Outbound) string

// ScopeActive — область, которую используют SetActive и поведение по умолчанию.
const ScopeActive = "active"

// scopeState — состояние детектора одной области.
type scopeState struct {
	conns  map[*conn]struct{}
	closed []time.Time // моменты закрытия соединений, так и не получивших ответа
	fired  bool
}

// Monitor реализует adapter.ConnectionTracker.
type Monitor struct {
	cfg        Config
	OnDegraded func(Event)
	// Scope определяет область соединения; по умолчанию — ScopeActive для outbound'а с тегом, заданным SetActive.
	Scope Scoper

	mu     sync.Mutex
	active string
	scopes map[string]*scopeState
}

// New создаёт монитор.
func New(cfg Config, onDegraded func(Event)) *Monitor {
	cfg.defaults()
	return &Monitor{cfg: cfg, OnDegraded: onDegraded, scopes: map[string]*scopeState{}}
}

func (m *Monitor) state(scope string) *scopeState {
	st := m.scopes[scope]
	if st == nil {
		st = &scopeState{conns: map[*conn]struct{}{}}
		m.scopes[scope] = st
	}
	return st
}

// SetActive задаёт тег активного outbound'а для области по умолчанию (без Scoper): только его соединения считаются.
func (m *Monitor) SetActive(tag string) {
	m.mu.Lock()
	m.active = tag
	m.scopes[ScopeActive] = &scopeState{conns: map[*conn]struct{}{}}
	m.mu.Unlock()
}

// Reset сбрасывает состояние области (после смены узла эпизод деградации начинается заново).
func (m *Monitor) Reset(scope string) {
	m.mu.Lock()
	if st := m.scopes[scope]; st != nil {
		st.closed, st.fired = nil, false
	}
	m.mu.Unlock()
}

type conn struct {
	net.Conn
	m            *Monitor
	scope        string
	mu           sync.Mutex
	waitingSince time.Time // с какого момента приложение ждёт ответа (нулевое = не ждёт)
	answered     bool      // получил ли ответ хоть раз
	done         bool
}

func (c *conn) Read(b []byte) (int, error) { // данные от приложения → уходят в узел
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mu.Lock()
		if c.waitingSince.IsZero() {
			c.waitingSince = time.Now()
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *conn) Write(b []byte) (int, error) { // ответ узла → приложению
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.mu.Lock()
		c.waitingSince, c.answered = time.Time{}, true
		c.mu.Unlock()
	}
	return n, err
}

func (c *conn) Close() error {
	c.mu.Lock()
	wasWaiting := !c.waitingSince.IsZero() && time.Since(c.waitingSince) > c.m.cfg.StallAfter/2
	c.done = true
	c.mu.Unlock()
	c.m.mu.Lock()
	st := c.m.state(c.scope)
	delete(st.conns, c)
	if wasWaiting {
		st.closed = append(st.closed, time.Now())
	}
	c.m.mu.Unlock()
	return c.Conn.Close()
}

// RoutedConnection — adapter.ConnectionTracker.
func (m *Monitor) RoutedConnection(_ context.Context, c net.Conn, md adapter.InboundContext, _ adapter.Rule, out adapter.Outbound) net.Conn {
	scope := ""
	switch {
	case m.Scope != nil:
		scope = m.Scope(md, out)
	default:
		m.mu.Lock()
		if out != nil && out.Tag() == m.active {
			scope = ScopeActive
		}
		m.mu.Unlock()
	}
	if scope == "" {
		return c
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w := &conn{Conn: c, m: m, scope: scope}
	m.state(scope).conns[w] = struct{}{}
	return w
}

// RoutedPacketConnection — UDP не отслеживается.
func (m *Monitor) RoutedPacketConnection(_ context.Context, c N.PacketConn, _ adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	return c
}

// RoutedFlow — потоки TUN не отслеживаются (nil допустим для роутера).
func (m *Monitor) RoutedFlow(context.Context, adapter.InboundContext, adapter.Rule, adapter.Outbound) tun.FlowTracker {
	return nil
}

// Run запускает цикл проверки до отмены контекста.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.check(now)
		}
	}
}

func (m *Monitor) check(now time.Time) {
	var events []Event
	m.mu.Lock()
	for name, st := range m.scopes {
		stalled := 0
		for c := range st.conns {
			c.mu.Lock()
			if !c.waitingSince.IsZero() && now.Sub(c.waitingSince) > m.cfg.StallAfter {
				stalled++
			}
			c.mu.Unlock()
		}
		keep := st.closed[:0]
		for _, ts := range st.closed {
			if now.Sub(ts) <= m.cfg.Window {
				keep = append(keep, ts)
			}
		}
		st.closed = keep
		total := stalled + len(st.closed)
		switch {
		case total >= m.cfg.MinStalled && !st.fired:
			st.fired = true
			events = append(events, Event{At: now, Scope: name, Outbound: m.active, Stalled: total, Reason: "нет ответа узла на отправленные данные"})
		case total == 0:
			st.fired = false // эпизод закончился, детектор снова взведён
		}
	}
	m.mu.Unlock()
	if m.OnDegraded != nil {
		for _, ev := range events {
			m.OnDegraded(ev)
		}
	}
}
