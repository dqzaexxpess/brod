package core

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"brod/rulesets"
	"brod/store"
)

// EvRuleSets — обновление списков маршрутизации TUN.
const EvRuleSets = "rulesets"

func (c *Core) ruleSetDir() string {
	if c.cfg.TUN.RuleSetDir != "" {
		return c.cfg.TUN.RuleSetDir
	}
	return filepath.Join(c.cfg.StateDir, "rulesets")
}

// UpdateRuleSets обновляет встроенные списки: сначала напрямую (основной адрес, затем зеркало), не получившиеся — через активный узел.
// Файлы заменяются только после проверки формата и размера; движок подхватывает изменение сам (rule-set отслеживается по файлу).
func (c *Core) UpdateRuleSets(ctx context.Context, initiator string) (rulesets.Result, error) {
	dir := c.ruleSetDir()
	client := c.cfg.RuleSetClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	res, err := rulesets.Update(ctx, client, dir)
	if err != nil {
		return res, err
	}
	c.mu.Lock()
	hasActive := c.selected[store.ScopeActive] != ""
	c.mu.Unlock()
	if len(res.Failed) > 0 && hasActive && c.cfg.RuleSetClient == nil && ctx.Err() == nil {
		if via, err := rulesets.Update(ctx, c.proxyClient(90*time.Second), dir); err == nil {
			res.Updated = append(res.Updated, via.Updated...)
			res.Unchanged += via.Unchanged
			res.Failed = via.Failed
		}
	}
	c.bus.Publish(store.Event{Kind: EvRuleSets, Initiator: initiator,
		Detail: detail(map[string]any{"updated": res.Updated, "unchanged": res.Unchanged, "failed": len(res.Failed)})})
	if _, err := c.RefreshUserSets(ctx, initiator); err != nil && ctx.Err() == nil { // свои списки обновляются вместе со встроенными
		c.bus.Publish(store.Event{Kind: EvError, Initiator: initiator, Detail: "свои списки: " + err.Error()})
	}
	return res, nil
}

// Периоды ruleSetLoop (переменные, чтобы тест мог их подменить).
var (
	ruleSetSoon  = 2 * time.Minute
	ruleSetEvery = 12 * time.Hour
)

// ruleSetLoop раз в 12 часов обновляет списки (первый раз через две минуты после старта, когда подписки уже загружены).
func (c *Core) ruleSetLoop(ctx context.Context) {
	timer := time.NewTimer(c.firstDelay("rulesets", ruleSetSoon, ruleSetEvery))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := c.UpdateRuleSets(ctx, "auto"); err != nil && ctx.Err() == nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "обновление списков: " + err.Error()})
		} else if err == nil {
			c.markLoopRun("rulesets")
		}
		timer.Reset(ruleSetEvery)
	}
}
