package core

import (
	"context"
	"sync"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/store"
)

// SpeedFunc замеряет скорость скачивания через пробный outbound узла.
type SpeedFunc func(ctx context.Context, node store.NodeRec, p *engine.Probe) prober.Result

// SpeedNow замеряет скорость указанных узлов (по два за раз: замер тяжёлый и мешает друг другу). Возвращает, у скольких получилось.
func (c *Core) SpeedNow(ctx context.Context, ids []string) (int, error) {
	recs, err := c.nodesByIDs(ids)
	if err != nil {
		return 0, err
	}
	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 2)
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
			kb, ok := c.speedOne(ctx, n)
			if !ok {
				return
			}
			if err := c.st.PutSpeed(store.SpeedRec{Node: n.ID, KBps: kb, At: c.cfg.Now()}); err == nil {
				mu.Lock()
				done++
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return done, ctx.Err()
}

func (c *Core) speedOne(ctx context.Context, n store.NodeRec) (float64, bool) {
	full, err := c.st.Node(n.ID)
	if err != nil {
		return 0, false
	}
	opts, err := c.probeOpts(full)
	if err != nil {
		return 0, false
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		return 0, false
	}
	defer p.Close()
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	r := c.cfg.Speed(cctx, full, p)
	if ctx.Err() != nil || !r.OK || r.SpeedKBps <= 0 {
		return 0, false
	}
	return r.SpeedKBps, true
}

// speedRound замеряет скорость нескольких живых узлов без замера или с устаревшим: избранные и свежие первыми.
func (c *Core) speedRound(ctx context.Context) {
	views, err := c.Nodes(NodesQuery{Alive: true, Limit: 400})
	if err != nil {
		return
	}
	var ids []string
	now := c.cfg.Now()
	if id := c.activeNodeID(); id != "" { // активный узел — первым
		if v, err := c.NodeByID(id); err == nil && (v.SpeedAt.IsZero() || now.Sub(v.SpeedAt) > c.cfg.SpeedTTL) {
			ids = append(ids, id)
		}
	}
	for _, v := range views {
		if v.ID == c.activeNodeID() {
			continue
		}
		if v.SpeedAt.IsZero() || now.Sub(v.SpeedAt) > c.cfg.SpeedTTL {
			ids = append(ids, v.ID)
			if len(ids) >= c.cfg.SpeedBatch {
				break
			}
		}
	}
	if len(ids) > 0 {
		_, _ = c.SpeedNow(ctx, ids)
	}
}
