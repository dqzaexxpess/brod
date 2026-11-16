package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/common/trafficcontrol"

	"brod/store"
)

// Соединения и график трафика (как «Соединения» и график в Throne). Учёт ведёт trafficcontrol.Manager движка (TCP и UDP, счётчики
// байт на каждом соединении); раз в секунду снимается суммарный счётчик → скорость и кольцо отсчётов для графика.

// RouteLabel — куда пошло соединение.
const (
	RouteDirect = "direct" // напрямую
	RouteProxy  = "proxy"  // через активный узел
	RouteTarget = "target" // через узел цели приложения (имя цели в RouteName)
	RouteBlock  = "block"  // отброшено правилом
	RouteOther  = "other"
)

// ConnView — соединение для окна.
type ConnView struct {
	ID        string
	Network   string // tcp | udp
	Inbound   string // tun-in | app-in
	Source    string
	Dest      string // домен:порт, если известен, иначе адрес:порт
	Domain    string
	Process   string // имя процесса, если движок его определил (нужен find_process: правило по процессу)
	Route     string // direct | proxy | target | block | other
	RouteName string // для target — идентификатор цели
	Node      string // подпись выхода: «WARP», «vless host:port», «напрямую»
	Up, Down  int64  // байт
	Started   time.Time
	Closed    time.Time // нулевое время — соединение открыто
}

// TrafficSample — отсчёт скорости за секунду.
type TrafficSample struct {
	At       time.Time
	Up, Down int64 // байт за секунду
}

// TrafficStats — состояние для графика.
type TrafficStats struct {
	Samples   []TrafficSample
	UpNow     int64 // байт/с
	DownNow   int64
	UpTotal   int64 // байт с запуска ядра
	DownTotal int64
	Conns     int // открытых соединений
}

type trafficRing struct {
	mu      sync.Mutex
	samples []TrafficSample
	lastUp  int64
	lastDn  int64
	primed  bool

	// накопленные приращения за день, ещё не сброшенные в базу
	pendUp, pendDn       int64
	pendVPNUp, pendVPNDn int64
	lastVPNUp, lastVPNDn int64
	day                  string
	flushAt              time.Time
	flushing             bool // запись в базу идёт
}

const trafficRingSize = 600 // 10 минут по секунде

// trafficFlushEvery — как часто накопленный трафик сбрасывается в базу (переменная: тест сокращает).
var trafficFlushEvery = 30 * time.Second

func (c *Core) trafficLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.baseCtx.Done():
			return
		case now := <-t.C:
			up, dn := c.traffic.Total()
			c.trafficTick(now, up, dn)
		}
	}
}

// trafficTick учитывает очередной отсчёт и раз в trafficFlushEvery сбрасывает накопленное в базу. Запись идёт отдельной горутиной: цикл
// не стоит в очереди за единственным соединением записи (длинная запись обновления источников) и не держит остановку ядра («traffic»).
func (c *Core) trafficTick(now time.Time, up, dn int64) {
	r := &c.trafficSamples
	r.mu.Lock()
	if r.primed {
		r.samples = append(r.samples, TrafficSample{At: now, Up: max(up-r.lastUp, 0), Down: max(dn-r.lastDn, 0)})
		if len(r.samples) > trafficRingSize {
			r.samples = r.samples[len(r.samples)-trafficRingSize:]
		}
		r.pendUp += max(up-r.lastUp, 0)
		r.pendDn += max(dn-r.lastDn, 0)
		vu, vd := c.vpnTotals()
		r.pendVPNUp += max(vu-r.lastVPNUp, 0)
		r.pendVPNDn += max(vd-r.lastVPNDn, 0)
		r.lastVPNUp, r.lastVPNDn = vu, vd
	}
	r.lastUp, r.lastDn, r.primed = up, dn, true
	day := now.Format("2006-01-02")
	due := r.day != "" && (r.day != day || now.Sub(r.flushAt) >= trafficFlushEvery)
	fday := r.day
	if r.day == "" || due {
		r.day, r.flushAt = day, now
	}
	// предыдущая запись ещё идёт: накопленное остаётся в pend* и уйдёт со следующим сбросом
	if !due || r.flushing || r.pendUp+r.pendDn == 0 {
		r.mu.Unlock()
		return
	}
	pu, pd, pvu, pvd := r.pendUp, r.pendDn, r.pendVPNUp, r.pendVPNDn
	r.pendUp, r.pendDn, r.pendVPNUp, r.pendVPNDn = 0, 0, 0, 0
	r.flushing = true
	r.mu.Unlock()
	go func() {
		add := c.trafficAdd
		if add == nil {
			add = c.st.AddDailyTraffic
		}
		err := add(fday, pu, pd, pvu, pvd)
		r.mu.Lock()
		r.flushing = false
		if err != nil && r.day == fday { // не записалось (база закрыта или занята): вернуть в накопленное
			r.pendUp += pu
			r.pendDn += pd
			r.pendVPNUp += pvu
			r.pendVPNDn += pvd
		}
		r.mu.Unlock()
	}()
}

// Traffic возвращает последние n отсчётов скорости и суммарные счётчики.
func (c *Core) Traffic(n int) TrafficStats {
	if n <= 0 || n > trafficRingSize {
		n = 120
	}
	r := &c.trafficSamples
	r.mu.Lock()
	s := append([]TrafficSample(nil), r.samples[max(len(r.samples)-n, 0):]...)
	r.mu.Unlock()
	st := TrafficStats{Samples: s, Conns: c.traffic.ConnectionsLen()}
	st.UpTotal, st.DownTotal = c.traffic.Total()
	if len(s) > 0 {
		st.UpNow, st.DownNow = s[len(s)-1].Up, s[len(s)-1].Down
	}
	return st
}

// Connections возвращает открытые соединения (новые первыми) и, если closed, недавно закрытые (до limit).
func (c *Core) Connections(closed bool, limit int) []ConnView {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	var out []ConnView
	for _, m := range c.traffic.Connections() {
		out = append(out, c.connView(m, false))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if closed {
		cl := c.traffic.ClosedConnections()
		views := make([]ConnView, 0, len(cl))
		for _, m := range cl {
			views = append(views, c.connView(m, true))
		}
		sort.Slice(views, func(i, j int) bool { return views[i].Closed.After(views[j].Closed) })
		out = append(out, views...)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// CloseConnection закрывает одно открытое соединение.
func (c *Core) CloseConnection(id string) bool {
	for _, m := range c.traffic.Connections() {
		if m.ID.String() == id {
			if t := c.traffic.Connection(m.ID); t != nil {
				_ = t.Close()
				return true
			}
		}
	}
	return false
}

// connView: для открытых соединений ClosedAt не читаем — его пишет движок при закрытии в те же метаданные (гонка); у закрытых это копия.
func (c *Core) connView(m *trafficcontrol.TrackerMetadata, closed bool) ConnView {
	md := m.Metadata
	host := md.Domain
	if host == "" {
		host = md.Destination.Fqdn
	}
	dest := md.Destination.String()
	if host != "" {
		dest = fmt.Sprintf("%s:%d", host, md.Destination.Port)
	}
	v := ConnView{ID: m.ID.String(), Network: md.Network, Inbound: md.Inbound, Source: md.Source.String(), Dest: dest, Domain: host,
		Up: m.Upload.Load(), Down: m.Download.Load(), Started: m.CreatedAt}
	if closed {
		v.Closed = m.ClosedAt
	}
	if md.ProcessInfo != nil && md.ProcessInfo.ProcessPath != "" {
		v.Process = filepath.Base(md.ProcessInfo.ProcessPath)
	}
	v.Route, v.RouteName, v.Node = c.routeOf(m.Outbound, host)
	return v
}

// routeOf определяет, куда пошло соединение: прямые идут в direct, остальное — в dispatch (цель по домену или активный узел).
func (c *Core) routeOf(outbound, host string) (route, name, label string) {
	switch outbound {
	case OutDirect:
		return RouteDirect, "", "напрямую"
	case OutApp, OutAppTUN, OutActive:
		if host != "" {
			tag := c.eng.MatchDispatch(outbound, host)
			if tag == "" && outbound == OutActive {
				tag = ""
			}
			if tag != "" {
				id := c.scopeOfTargetTag(tag)
				return RouteTarget, strings.TrimPrefix(id, "target:"), "цель " + strings.TrimPrefix(id, "target:")
			}
		}
		if outbound == OutAppTUN {
			return RouteDirect, "", "напрямую"
		}
		return RouteProxy, "", c.activeLabel()
	}
	return RouteOther, "", outbound
}

// activeLabel — подпись активного выхода для списка соединений.
func (c *Core) activeLabel() string {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	switch id {
	case "":
		return "нет активного узла"
	case store.SelWarp:
		return "WARP"
	}
	if rec, err := c.st.Node(id); err == nil {
		return fmt.Sprintf("%s %s:%d", rec.Proto, rec.Server, rec.Port)
	}
	return short(id)
}

// vpnTotals — суммарные байты соединений, прошедших через VPN (активный узел и цели): открытые сейчас плюс закрытые, ещё хранящиеся в учёте движка.
// Приближение: закрытые вытесняются из учёта (до 1000), поэтому берётся приращение к прошлому снимку, а не абсолют.
func (c *Core) vpnTotals() (up, down int64) {
	for _, m := range c.traffic.Connections() {
		if r, _, _ := c.routeOf(m.Outbound, hostOf(m)); r == RouteProxy || r == RouteTarget {
			up += m.Upload.Load()
			down += m.Download.Load()
		}
	}
	c.vpnClosedMu.Lock()
	defer c.vpnClosedMu.Unlock()
	for _, m := range c.traffic.ClosedConnections() {
		if _, seen := c.vpnClosedSeen[m.ID.String()]; seen {
			continue
		}
		c.vpnClosedSeen[m.ID.String()] = struct{}{}
		if r, _, _ := c.routeOf(m.Outbound, hostOf(m)); r == RouteProxy || r == RouteTarget {
			c.vpnClosedUp += m.Upload.Load()
			c.vpnClosedDn += m.Download.Load()
		}
	}
	if len(c.vpnClosedSeen) > 5000 {
		c.vpnClosedSeen = map[string]struct{}{}
	}
	return up + c.vpnClosedUp, down + c.vpnClosedDn
}

func hostOf(m *trafficcontrol.TrackerMetadata) string {
	if m.Metadata.Domain != "" {
		return m.Metadata.Domain
	}
	return m.Metadata.Destination.Fqdn
}

// DailyTraffic возвращает трафик по дням за последние n дней.
func (c *Core) DailyTraffic(n int) ([]store.DayTraffic, error) {
	c.flushTraffic()
	if n <= 0 || n > 365 {
		n = 30
	}
	return c.st.DailyTraffic(n)
}

// flushTraffic сбрасывает накопленные приращения в базу (перед чтением и при остановке).
func (c *Core) flushTraffic() {
	r := &c.trafficSamples
	r.mu.Lock()
	pu, pd, pvu, pvd, day := r.pendUp, r.pendDn, r.pendVPNUp, r.pendVPNDn, r.day
	r.pendUp, r.pendDn, r.pendVPNUp, r.pendVPNDn = 0, 0, 0, 0
	r.mu.Unlock()
	if day != "" && pu+pd > 0 {
		_ = c.st.AddDailyTraffic(day, pu, pd, pvu, pvd)
	}
}
