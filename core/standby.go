package core

import (
	"context"
	"log"
	"sync"
	"time"

	"brod/store"
)

// Горячий резерв. Пока активный узел здоров, ядро раз в StandbyInterval подбирает и проверяет несколько запасных узлов и помнит их
// (они же записываются как запасные активного выбора). Когда узел деградирует, не нужно искать замену с нуля (от «деградирует» до
// автозамены раньше проходило от 8 до 150 с): достаточно убедиться, что резерв жив сейчас (одна параллельная проверка), и сразу
// предложить или (degrade.action=auto) выполнить переход. Резерв не меняет выбор сам: решения по P1 принимает прежняя логика.

type standbyState struct {
	mu   sync.Mutex
	from string // активный узел, для которого собран резерв
	ids  []string
	at   time.Time
	busy bool // обновление резерва идёт
}

func (c *Core) standbyLoop(ctx context.Context) {
	timer := time.NewTimer(c.firstDelay("standby", c.cfg.StandbyFirstDelay, c.cfg.StandbyInterval)) // недавний проход помним
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if !c.waitActive(ctx) {
			return
		}
		if c.refreshStandby(ctx) {
			timer.Reset(c.cfg.StandbyInterval)
		} else {
			timer.Reset(standbyBusyRetry) // круг проверки узлов занимал сеть: не ждать целый период, на телефоне круги идут почти непрерывно
		}
	}
}

// refreshStandby обновляет резерв, если сейчас нет деградации и не идёт круг проверки узлов. Поиск альтернатив при деградации с ним не
// конфликтует (отдельный флаг), поэтому долгое обновление резерва не задерживает замену.
// Возвращает false, если обновление отложено из-за круга проверки узлов (повторить скоро); в остальных случаях true.
func (c *Core) refreshStandby(ctx context.Context) bool {
	c.mu.Lock()
	active := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if active == "" || c.Degraded() {
		return true
	}
	if c.nodeCheckActive.Load() {
		return false
	}
	c.standby.mu.Lock()
	if c.standby.busy {
		c.standby.mu.Unlock()
		return true
	}
	c.standby.busy = true
	c.standby.mu.Unlock()
	defer func() {
		c.standby.mu.Lock()
		c.standby.busy = false
		c.standby.mu.Unlock()
	}()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	alts, err := c.Alternatives(cctx, c.cfg.AltCount) // сам записывает запасные активного выбора
	if err != nil || len(alts) == 0 || ctx.Err() != nil {
		log.Printf("горячий резерв: не собран (запасных %d, ошибка %v)", len(alts), err)
		return true
	}
	ids := make([]string, len(alts))
	for i, a := range alts {
		ids[i] = a.Node.ID
	}
	c.standby.mu.Lock()
	c.standby.from, c.standby.ids, c.standby.at = active, ids, c.cfg.Now()
	c.standby.mu.Unlock()
	log.Printf("горячий резерв: собрано %d запасных", len(ids))
	c.markLoopRun("standby")
	return true
}

// standbyBusyRetry — через сколько повторить обновление резерва, если его отложил круг проверки узлов.
const standbyBusyRetry = 20 * time.Second

// liveStandby возвращает резерв узла from, который отвечает прямо сейчас (проверка параллельная, до AltCount штук в порядке резерва).
// Пусто — резерва нет, он устарел, относится к другому узлу или никто не ответил: тогда работает обычный поиск альтернатив.
func (c *Core) liveStandby(ctx context.Context, from string) []Candidate {
	c.standby.mu.Lock()
	ids, at, owner := append([]string(nil), c.standby.ids...), c.standby.at, c.standby.from
	c.standby.mu.Unlock()
	if len(ids) == 0 || owner != from {
		// после перезапуска ядра (на Android это обычное дело) резерва в памяти нет, но запасные активного выбора лежат в базе: они проверяются
		// живой проверкой ниже, так что устаревший список не опасен
		sel, err := c.st.Selection(store.ScopeActive)
		if err != nil || sel.Node != from || len(sel.Fallbacks) == 0 {
			log.Printf("горячий резерв: запасных нет (ни в памяти, ни в базе), поиск с нуля")
			return nil
		}
		ids = sel.Fallbacks
	} else if c.cfg.Now().Sub(at) > c.cfg.StandbyFresh {
		log.Printf("горячий резерв: устарел, поиск с нуля")
		return nil
	}
	recs, err := c.st.Nodes(store.NodeFilter{IDs: ids, OnlyPresent: true, HideInsecure: c.hideInsecure()})
	if err != nil {
		return nil
	}
	byID := make(map[string]store.NodeRec, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	var cands []store.NodeRec
	for _, id := range ids {
		if r, ok := byID[id]; ok && id != from {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	out, _ := c.verify(ctx, c.netKey(), cands, c.cfg.AltCount, len(cands))
	log.Printf("горячий резерв: запасных %d, ответило %d", len(cands), len(out))
	return out
}
