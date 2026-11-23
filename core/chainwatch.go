package core

import (
	"context"
	"fmt"
	"time"

	"brod/store"
)

// Автоматическая проверка звеньев активной цепочки: пока цепочка не работает, человек видит только «нет связи» и не знает, какое звено умерло.
// Раз в chainWatchEvery звенья проверяются так же, как кнопкой «Проверить звенья»; звено, не ответившее chainWatchFails раза подряд (одиночный сбой
// сети не считается), объявляется событием EvChainHop — один раз, пока оно не оживёт.
const (
	chainWatchEvery = 5 * time.Minute
	chainWatchFails = 2
)

// checkActiveChain проверяет звенья активной цепочки (если активен не цепочка — ничего).
func (c *Core) checkActiveChain(ctx context.Context) {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if id == "" {
		return
	}
	rec, err := c.st.Node(id)
	if err != nil || !isChain(rec) {
		return
	}
	hops, err := c.CheckChain(ctx, id)
	if err != nil || ctx.Err() != nil {
		return
	}
	c.chainWatchMu.Lock()
	if c.chainFails == nil {
		c.chainFails, c.chainWarned = map[string]int{}, map[string]bool{}
	}
	var announce []int
	for i, h := range hops {
		key := id + "|" + h.ID
		switch h.State {
		case "fail":
			c.chainFails[key]++
			if c.chainFails[key] >= chainWatchFails && !c.chainWarned[key] {
				c.chainWarned[key] = true
				announce = append(announce, i)
			}
		case "ok":
			delete(c.chainFails, key)
			delete(c.chainWarned, key)
		}
	}
	c.chainWatchMu.Unlock()
	for _, i := range announce {
		h := hops[i]
		c.bus.Publish(store.Event{Kind: EvChainHop, Node: id, Initiator: "auto",
			Detail: detail(map[string]any{"hop": i + 1, "of": len(hops), "server": fmt.Sprintf("%s:%d", h.Server, h.Port), "proto": h.Proto, "reason": h.Detail})})
	}
}
