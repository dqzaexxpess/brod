package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/store"
)

// Параметры замера UDP: 10 запросов с интервалом 200 мс (около 2 с) и ожидание ответов на последние ещё 1,5 с.
const (
	udpProbeSamples = 10
	udpProbeGap     = 200 * time.Millisecond
	udpProbeWait    = 1500 * time.Millisecond
)

// NodeUDP — итог замера UDP через узел.
type NodeUDP struct {
	Node   string `json:"node,omitempty"`
	Proto  string `json:"proto,omitempty"`
	Active bool   `json:"active,omitempty"`
	prober.UDPResult
}

// ProbeUDP замеряет UDP через узел: потери и задержку STUN-запросов. ref — ID или однозначный префикс; пусто или «active» — текущий
// активный выход (тот outbound, по которому идёт трафик). Нужен, чтобы понять, годится ли узел для звонков: голос и видео идут
// по UDP, а обычные проверки (рукопожатие, HTTPS) о нём ничего не говорят.
func (c *Core) ProbeUDP(ctx context.Context, ref string) (NodeUDP, error) {
	var out NodeUDP
	var p *engine.Probe
	if ref == "" || ref == "active" {
		c.mu.Lock()
		raw := c.selected[store.ScopeActive]
		c.mu.Unlock()
		if raw == "" {
			return out, fmt.Errorf("%w: активного узла нет", ErrNoNode)
		}
		ap, err := c.eng.Existing(OutActive)
		if err != nil {
			return out, err
		}
		p, out.Active, out.Node = ap, true, c.activeNodeID()
		if out.Node != "" {
			if rec, err := c.st.Node(out.Node); err == nil {
				out.Proto = rec.Proto
			}
		} else {
			out.Proto = raw // warp или direct: узла в базе нет
		}
	} else {
		id, err := c.ResolveNode(ref)
		if err != nil {
			return out, err
		}
		rec, err := c.st.Node(id)
		if err != nil {
			return out, err
		}
		opts, err := c.probeOpts(rec)
		if err != nil {
			return out, err
		}
		np, err := c.eng.NewProbe(opts)
		if err != nil {
			return out, err
		}
		defer np.Close()
		p, out.Node, out.Proto = np, id, rec.Proto
	}
	out.UDPResult = c.stunVia(ctx, p)
	if out.Node != "" && ctx.Err() == nil {
		c.recordUDP(out.Node, out.UDPResult)
	}
	return out, ctx.Err()
}

func (c *Core) stunVia(ctx context.Context, p *engine.Probe) prober.UDPResult {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return prober.STUN(cctx, p.DialContext, prober.STUNServers, udpProbeSamples, udpProbeGap, udpProbeWait)
}

// recordUDP запоминает замер: по нему в списке узлов показывается метка «для звонков».
func (c *Core) recordUDP(node string, r prober.UDPResult) {
	_ = c.st.PutUDP(store.UDPRec{Node: node, Net: c.netKey(), Grade: r.Grade, LossPct: r.LossPct, MedianMs: r.MedianMs, At: c.cfg.Now()})
}

// udpOne замеряет UDP через пробный outbound узла и запоминает результат.
func (c *Core) udpOne(ctx context.Context, id string) bool {
	rec, err := c.st.Node(id)
	if err != nil {
		return false
	}
	opts, err := c.probeOpts(rec)
	if err != nil {
		return false
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		return false
	}
	defer p.Close()
	r := c.stunVia(ctx, p)
	if ctx.Err() != nil {
		return false
	}
	c.recordUDP(id, r)
	return true
}

// udpFresh: замер UDP узла снят в текущей сети и не устарел.
func (c *Core) udpFresh(r store.UDPRec, now time.Time) bool {
	return r.Node != "" && r.Net == c.netKey() && now.Sub(r.At) <= c.cfg.UDPTTL
}

// udpRound замеряет UDP у нескольких живых узлов без свежего замера (активный и избранные первыми, как у скорости). Два замера
// одновременно: каждый занимает около 3,5 с и держит свой пробный outbound.
func (c *Core) udpRound(ctx context.Context) {
	views, err := c.Nodes(NodesQuery{Alive: true, Limit: 400})
	if err != nil {
		return
	}
	known, _ := c.st.UDPs()
	now := c.cfg.Now()
	var ids []string
	need := func(id string) bool { return !c.udpFresh(known[id], now) }
	if id := c.activeNodeID(); id != "" && need(id) {
		ids = append(ids, id)
	}
	for _, v := range views {
		if len(ids) >= c.cfg.SpeedBatch {
			break
		}
		if v.ID != c.activeNodeID() && need(v.ID) {
			ids = append(ids, v.ID)
		}
	}
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, id := range ids {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			c.udpOne(ctx, id)
		}(id)
	}
	wg.Wait()
}
