package core

import (
	"context"
	"fmt"
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
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out.UDPResult = prober.STUN(cctx, p.DialContext, prober.STUNServers, udpProbeSamples, udpProbeGap, udpProbeWait)
	return out, ctx.Err()
}
