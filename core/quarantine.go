package core

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"sync"
	"time"

	"brod/discover"
	"brod/engine"
	"brod/prober"
	"brod/store"
	"brod/translator"
)

// «Мягкий карантин» кандидатов: выборка до quarantineSample ссылок раз в календарные сутки на кандидата, разбор обычным translator'ом и
// обычная проба L2 (engine.NewProbe + prober.L2). Узлы в базу не пишутся, активный выбор и очередь проверок не затрагиваются: проба —
// отдельный временный outbound, который тут же закрывается. Параллельность мала, у всего этапа общий потолок времени. В базу идут только
// счётчики («проверено», «ответило»); ссылки выборки живут в памяти до конца прохода.
const (
	quarantineSample   = 20
	quarantineParallel = 4
	quarantineCeiling  = 5 * time.Minute
)

// quarantineCanProbe: выборку проверяем только при включённом VPN и не на Android: на телефоне проба — это
// батарея и процессор, а при выключенном VPN проверка шла бы из сети без туннеля. Иначе только считаются ссылки.
func (c *Core) quarantineCanProbe() bool {
	if c.cfg.DiscoverCanProbe != nil {
		return c.cfg.DiscoverCanProbe()
	}
	return runtime.GOOS != "android" && c.cfg.TUN.Enabled
}

// probeCandidateNode — проба узла кандидата: подмена из Config (тесты) или временный outbound движка и L2.
func (c *Core) probeCandidateNode(ctx context.Context, n store.NodeRec) prober.Result {
	if c.cfg.DiscoverProbe != nil {
		return c.cfg.DiscoverProbe(ctx, n)
	}
	opts, err := c.probeOpts(n)
	if err != nil {
		return prober.Result{Level: 2, Class: prober.ClassOther}
	}
	var p *engine.Probe
	if p, err = c.eng.NewProbe(opts); err != nil {
		return prober.Result{Level: 2, Class: prober.ClassOther}
	}
	defer p.Close()
	cctx, cancel := context.WithTimeout(ctx, c.cfg.CheckTimeout)
	defer cancel()
	return prober.L2(cctx, p)
}

// nodeFromLink разбирает ссылку выборки в запись узла (только для пробы; в базу не пишется).
func nodeFromLink(l string) (store.NodeRec, bool) {
	o, err := translator.Parse(l)
	if err != nil {
		return store.NodeRec{}, false
	}
	raw, err := json.Marshal(o.Opts)
	if err != nil {
		return store.NodeRec{}, false
	}
	server, port := endpointOf(o)
	if localServerAddr(server) {
		return store.NodeRec{}, false
	}
	return store.NodeRec{ID: store.NodeID(o.Key), Class: translator.Class(o.Opts), Proto: o.Proto, Endpoint: o.Endpoint, Server: server, Port: port,
		Opts: raw, Notes: o.Notes, Insecure: o.Flags.Insecure, NoSecurity: o.Flags.NoSecurity}, true
}

// quarantineOrder: кого проверять первым — тех, у кого выборка самая давняя (ни разу не бравшуюся — раньше всех). Потолок времени этапа
// покрывает лишь часть кандидатов (мёртвые узлы — до 10 с на каждый), и при порядке «по числу ссылок» одни и те же крупные кандидаты
// съедали бы потолок каждый день, а остальные никогда не получили бы статистики. Среди равных порядок по убыванию ссылок.
func quarantineOrder(found []discover.Found, have map[string]store.CandidateRec) []discover.Found {
	out := append([]discover.Found(nil), found...)
	sort.SliceStable(out, func(i, j int) bool { return have[out[i].URL].LastSample.Before(have[out[j].URL].LastSample) })
	return out
}

// quarantine проверяет выборки найденных кандидатов; возвращает число проверенных ссылок, ответивших и признак «проверка выполнялась».
func (c *Core) quarantine(ctx context.Context, found []discover.Found, have map[string]store.CandidateRec, now time.Time) (checked, alive int, probed bool) {
	if !c.quarantineCanProbe() {
		return 0, 0, false
	}
	qctx, cancel := context.WithTimeout(ctx, quarantineCeiling)
	defer cancel()
	today := now.Format("2006-01-02")
	probed = true
	for _, f := range quarantineOrder(found, have) {
		if qctx.Err() != nil {
			break
		}
		if h, ok := have[f.URL]; ok && !h.LastSample.IsZero() && h.LastSample.Format("2006-01-02") == today {
			continue // раз в сутки на кандидата
		}
		var nodes []store.NodeRec
		for _, l := range f.Sample {
			if n, ok := nodeFromLink(l); ok {
				nodes = append(nodes, n)
			}
		}
		var mu sync.Mutex
		var ch, ok int
		sem := make(chan struct{}, quarantineParallel)
		var wg sync.WaitGroup
		for _, n := range nodes {
			if qctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(n store.NodeRec) {
				defer func() { <-sem; wg.Done() }()
				r := c.probeCandidateNode(qctx, n)
				if qctx.Err() != nil || r.Class == prober.ClassCanceled { // прервано нами: провалом не считается
					return
				}
				mu.Lock()
				ch++
				if r.OK {
					ok++
				}
				mu.Unlock()
			}(n)
		}
		wg.Wait()
		if qctx.Err() != nil || ch == 0 { // недопроверенная выборка не засчитывается: статистика только по полным
			continue
		}
		if err := c.st.RecordCandidateSample(f.URL, ch, ok, c.cfg.Now()); err == nil {
			checked += ch
			alive += ok
		}
	}
	return checked, alive, probed
}
