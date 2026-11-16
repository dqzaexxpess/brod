package store

import (
	"sync"
	"time"
)

// Кэш Histories. Сводку по сети раньше строили заново из недельных проб при каждом вызове (десятки тысяч строк через database/sql),
// а зовут её и список узлов, и проверки, и запасные, и цели: на телефоне это половина процессорного времени в простое.
// Проб пишет само ядро (Record), поэтому достаточно один раз прочитать неделю из базы и дальше обновлять сводку при записи.
//
// Храним сырые «последние» времена, а не готовую History: окно 7 суток и FailedLast (≤30 мин) зависят от now и вычисляются при чтении.

const (
	histWindow    = 7 * 24 * time.Hour
	histFailFresh = 30 * time.Minute
	histMaxNets   = 4                // сетей в кэше одновременно (дом, работа, раздача с телефона…): остальные выгружаются
	histSnapTTL   = 30 * time.Second // сколько переиспользуется собранная карта при неизменных пробах
)

// nodeHist — последние события узла в одной сети, unix-секунды (0 = не было). Правило «новее побеждает, при равенстве — позднее
// записанное» повторяет прежний обход строк по порядку (at, rowid).
type nodeHist struct {
	l2OK, l4OK, google, lastFail int64
	l2Ms                         int64
	lastL2                       int64 // время последнего L2-результата, любого
	lastL2Fail                   bool  // и он — провал
	last                         int64 // последняя проба узла любого вида: узел без проб в окне в сводке не появляется
}

func (h *nodeHist) apply(level int, target string, ok bool, at, lat int64) {
	if at >= h.last {
		h.last = at
	}
	switch {
	case level == 2:
		if ok {
			if at >= h.l2OK {
				h.l2OK, h.l2Ms = at, lat
			}
		} else if at >= h.lastFail {
			h.lastFail = at
		}
		if at >= h.lastL2 {
			h.lastL2, h.lastL2Fail = at, !ok
		}
	case ok && level == 4 && target == "youtube":
		if at >= h.l4OK {
			h.l4OK = at
		}
	case ok && level == 4 && target == "google-api":
		if at >= h.google {
			h.google = at
		}
	}
}

// view собирает History для окна, начинающегося в cutoff (unix-секунды); false — в окне проб нет.
func (h *nodeHist) view(cutoff int64, now time.Time) (History, bool) {
	if h.last < cutoff {
		return History{}, false
	}
	var out History
	if h.l2OK >= cutoff && h.l2OK > 0 {
		out.L2OK, out.L2Ms = time.Unix(h.l2OK, 0), h.l2Ms
	}
	if h.l4OK >= cutoff && h.l4OK > 0 {
		out.L4OK = time.Unix(h.l4OK, 0)
	}
	if h.google >= cutoff && h.google > 0 {
		out.GoogleOK = time.Unix(h.google, 0)
	}
	if h.lastFail >= cutoff && h.lastFail > 0 {
		out.LastFail = time.Unix(h.lastFail, 0)
	}
	if h.lastL2Fail && h.lastL2 >= cutoff {
		out.FailedLast = now.Sub(time.Unix(h.lastFail, 0)) <= histFailFresh
	}
	return out, true
}

type netHist struct {
	nodes map[string]*nodeHist
	snap  map[string]History // собранная сводка; только для чтения, отдаётся вызывающим как есть
	snapT time.Time          // now, для которого она собрана
	dirty bool               // после сборки были новые пробы
	from  int64              // начало загруженного окна, unix-секунды
}

type histCache struct {
	mu   sync.Mutex
	nets map[string]*netHist

	pmu    sync.Mutex // приоры классов: отдельно, чтобы запрос к базе не держал сводки
	priors map[string]priorsEntry
}

type priorsEntry struct {
	at time.Time
	m  map[string]float64
}

// loaded возвращает кэш сети, при первом обращении читая неделю проб из базы. Вызывать под h.mu.
func (s *Store) loaded(netID string, now time.Time) (*netHist, error) {
	from := now.Add(-histWindow).Unix()
	if n := s.hc.nets[netID]; n != nil && from >= n.from { // now идёт вперёд; запрос «в прошлое» (тесты) перечитывает окно
		return n, nil
	}
	rows, err := s.rd.Query(`
SELECT node, level, target, ok, at, latency_ms FROM probes WHERE net=? AND at>=? ORDER BY at, rowid`, netID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	n := &netHist{nodes: map[string]*nodeHist{}, from: from, dirty: true}
	for rows.Next() {
		var node, target string
		var level, ok int
		var at, lat int64
		if err := rows.Scan(&node, &level, &target, &ok, &at, &lat); err != nil {
			return nil, err
		}
		h := n.nodes[node]
		if h == nil {
			h = &nodeHist{}
			n.nodes[node] = h
		}
		h.apply(level, target, ok == 1, at, lat)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, reload := s.hc.nets[netID]; s.hc.nets == nil || (!reload && len(s.hc.nets) >= histMaxNets) {
		s.hc.nets = map[string]*netHist{}
	}
	s.hc.nets[netID] = n
	return n, nil
}

// histRecord учитывает записанные пробы в уже загруженных сетях (остальные подтянутся из базы при первом чтении).
func (s *Store) histRecord(rows []Row) {
	s.hc.mu.Lock()
	defer s.hc.mu.Unlock()
	for _, r := range rows {
		n := s.hc.nets[r.Net]
		if n == nil {
			continue
		}
		h := n.nodes[r.Node]
		if h == nil {
			h = &nodeHist{}
			n.nodes[r.Node] = h
		}
		h.apply(r.Level, r.Target, r.OK, r.At.Unix(), r.LatencyMs)
		n.dirty = true
	}
	// метка списка узлов (viewgen.go) ещё раз уже после обновления кэша: читатель, взявший метку между фиксацией и этим местом,
	// соберёт список без новых проб, но следующая метка будет другой
	s.viewGen.Add(1)
}

// histReset выбрасывает кэш: после чистки проб окно могло стать короче семи суток.
func (s *Store) histReset() {
	s.hc.mu.Lock()
	s.hc.nets = nil
	s.hc.mu.Unlock()
	s.hc.pmu.Lock()
	s.hc.priors = nil
	s.hc.pmu.Unlock()
}

// ClassPriorsCached — ClassPriors с переиспользованием результата до ttl: доли успеха по классам статистические, а запрос читает все
// L2-пробы недели (на телефоне десятки тысяч строк при каждом отборе кандидатов). Карту нельзя менять.
func (s *Store) ClassPriorsCached(netID string, now time.Time, ttl time.Duration) (map[string]float64, error) {
	s.hc.pmu.Lock()
	defer s.hc.pmu.Unlock()
	if e, ok := s.hc.priors[netID]; ok {
		if d := now.Sub(e.at); d >= 0 && d < ttl {
			return e.m, nil
		}
	}
	m, err := s.ClassPriors(netID, now)
	if err != nil {
		return nil, err
	}
	if s.hc.priors == nil || len(s.hc.priors) >= histMaxNets {
		s.hc.priors = map[string]priorsEntry{}
	}
	s.hc.priors[netID] = priorsEntry{now, m}
	return m, nil
}

// Histories возвращает сводки по всем узлам в сети netID за последние 7 суток. Карту нельзя менять: её получают все вызывающие.
// Она переиспользуется, пока нет новых проб и now ушло вперёд не больше чем на histSnapTTL (на эту точность «свежесть ≤30 мин» не влияет).
func (s *Store) Histories(netID string, now time.Time) (map[string]History, error) {
	s.hc.mu.Lock()
	defer s.hc.mu.Unlock()
	n, err := s.loaded(netID, now)
	if err != nil {
		return nil, err
	}
	if n.snap != nil && !n.dirty {
		if d := now.Sub(n.snapT); d >= 0 && d < histSnapTTL {
			return n.snap, nil
		}
	}
	cutoff := now.Add(-histWindow).Unix()
	out := make(map[string]History, len(n.nodes))
	for id, h := range n.nodes {
		if v, ok := h.view(cutoff, now); ok {
			out[id] = v
		}
	}
	n.snap, n.snapT, n.dirty = out, now, false
	return out, nil
}

// History возвращает сводку одного узла (ok=false — в окне проб нет): без сборки карты по всей сети.
func (s *Store) History(netID, node string, now time.Time) (History, bool, error) {
	s.hc.mu.Lock()
	defer s.hc.mu.Unlock()
	n, err := s.loaded(netID, now)
	if err != nil {
		return History{}, false, err
	}
	h := n.nodes[node]
	if h == nil {
		return History{}, false, nil
	}
	v, ok := h.view(now.Add(-histWindow).Unix(), now)
	return v, ok, nil
}
