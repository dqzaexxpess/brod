package core

import (
	"context"
	"sync"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/store"
)

// ExitFunc собирает сведения о выходе узла через его пробный outbound.
type ExitFunc func(ctx context.Context, node store.NodeRec, p *engine.Probe) prober.ExitInfo

// YouTubeFunc проверяет, открывается ли YouTube через пробный outbound узла.
type YouTubeFunc func(ctx context.Context, node store.NodeRec, p *engine.Probe) prober.Result

// Чистота выхода.
const (
	CleanUnknown = "unknown" // данных нет
	CleanClean   = "clean"   // все проверки прошли
	CleanMixed   = "mixed"   // часть прошла, часть нет
	CleanDirty   = "dirty"   // ни одна не прошла
)

// ExitView — сведения о выходе узла для списка и API.
type ExitView struct {
	IP       string
	Country  string
	Colo     string
	GoogleOK *bool
	SiteOK   *bool
	Clean    string
	Detail   string
	At       time.Time
}

func tri(v int) *bool {
	if v < 0 {
		return nil
	}
	b := v == 1
	return &b
}

// cleanliness оценивает выход по фактам: Google без капчи, строгий сайт без испытания, YouTube (если проверялся успешно).
func cleanliness(google, site *bool, youtube bool) string {
	pass, fail := 0, 0
	for _, v := range []*bool{google, site} {
		if v == nil {
			continue
		}
		if *v {
			pass++
		} else {
			fail++
		}
	}
	if youtube {
		pass++
	}
	switch {
	case pass+fail == 0:
		return CleanUnknown
	case fail == 0:
		return CleanClean
	case pass > 0:
		return CleanMixed
	}
	return CleanDirty
}

func exitView(m map[string]store.ExitInfoRec, id string, youtube bool) *ExitView {
	r, ok := m[id]
	if !ok {
		return nil
	}
	g, s := tri(r.GoogleOK), tri(r.SiteOK)
	return &ExitView{IP: r.IP, Country: r.Country, Colo: r.Colo, GoogleOK: g, SiteOK: s, Clean: cleanliness(g, s, youtube), Detail: r.Detail, At: r.At}
}

// ExitInfoNow собирает сведения о выходах указанных узлов (по три за раз). Возвращает, у скольких получилось определить выход.
func (c *Core) ExitInfoNow(ctx context.Context, ids []string) (int, error) {
	recs, err := c.nodesByIDs(ids)
	if err != nil {
		return 0, err
	}
	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 3)
	)
	for _, n := range recs {
		select {
		case <-ctx.Done():
			wg.Wait()
			return done, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(n store.NodeRec) {
			defer wg.Done()
			defer func() { <-sem }()
			info, ok := c.exitOne(ctx, n)
			if !ok {
				return
			}
			if err := c.st.PutExitInfo(info); err == nil && info.IP != "" {
				mu.Lock()
				done++
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return done, ctx.Err()
}

func (c *Core) exitOne(ctx context.Context, n store.NodeRec) (store.ExitInfoRec, bool) {
	full, err := c.st.Node(n.ID)
	if err != nil {
		return store.ExitInfoRec{}, false
	}
	opts, err := c.probeOpts(full)
	if err != nil {
		return store.ExitInfoRec{}, false
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		return store.ExitInfoRec{}, false
	}
	defer p.Close()
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r := c.cfg.Exit(cctx, full, p)
	if ctx.Err() != nil {
		return store.ExitInfoRec{}, false
	}
	if r.IP == "" && r.Country == "" { // сеть не дошла — ничего не записываем, попробуем в следующий круг
		return store.ExitInfoRec{}, false
	}
	// YouTube — отдельным результатом проверки (цель youtube): от него зависит значок «YouTube» в списке и оценка чистоты
	if yt := c.cfg.YouTube(cctx, full, p); ctx.Err() == nil && yt.Class != prober.ClassCanceled {
		_ = c.st.Record([]store.Row{{Node: n.ID, Net: c.netKey(), Target: "youtube", Class: string(yt.Class), NodeClass: full.Class, Level: 4,
			OK: yt.OK, LatencyMs: yt.LatencyMs, At: c.cfg.Now()}})
	}
	b := func(v *bool) int {
		switch {
		case v == nil:
			return -1
		case *v:
			return 1
		}
		return 0
	}
	return store.ExitInfoRec{Node: n.ID, IP: r.IP, Country: r.Country, Colo: r.Colo, GoogleOK: b(r.GoogleOK), SiteOK: b(r.SiteOK), Detail: r.Detail, At: c.cfg.Now()}, true
}

// exitLoop в фоне собирает сведения о выходах живых узлов: избранные и самые свежие первыми, без сведений или устаревшие.
func (c *Core) exitLoop(ctx context.Context) {
	t := time.NewTimer(c.firstDelay("exit", 20*time.Second, c.cfg.ExitInterval)) // дать сначала пройти первым проверкам; недавний проход помним
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !c.waitActive(ctx) {
			return
		}
		c.exitRound(ctx)
		c.speedRound(ctx)
		if ctx.Err() == nil {
			c.markLoopRun("exit")
		}
		t.Reset(c.cfg.ExitInterval)
	}
}

func (c *Core) exitRound(ctx context.Context) {
	views, err := c.Nodes(NodesQuery{Alive: true, Limit: 400})
	if err != nil {
		return
	}
	var ids []string
	now := c.cfg.Now()
	if id := c.activeNodeID(); id != "" { // активный узел — первым: его страна и чистота видны на главной
		if v, err := c.NodeByID(id); err == nil && (v.Exit == nil || now.Sub(v.Exit.At) > c.cfg.ExitTTL) {
			ids = append(ids, id)
		}
	}
	for _, v := range views {
		if v.ID == c.activeNodeID() {
			continue
		}
		if v.Exit == nil || now.Sub(v.Exit.At) > c.cfg.ExitTTL {
			ids = append(ids, v.ID)
			if len(ids) >= c.cfg.ExitBatch {
				break
			}
		}
	}
	if len(ids) > 0 {
		_, _ = c.ExitInfoNow(ctx, ids)
	}
}

// activeNodeID — активный узел подписки ("" — нет или это WARP/direct).
func (c *Core) activeNodeID() string {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if id == store.SelWarp || id == store.SelDirect {
		return ""
	}
	return id
}
