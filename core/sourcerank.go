package core

import (
	"log"
	"time"

	"brod/store"
)

// Рейтинг источников. Источников около сотни, и каждый круг обновления скачивает и разбирает их все, в том числе те, из которых за неделю не ожил
// ни один узел: это трафик, процессор и батарея (на телефоне в базе ~110 тыс. узлов). Источник, у которого проверено не меньше
// deadMinChecked узлов и ни один не ответил за неделю, считается «мёртвым грузом» и обновляется автоматически не чаще раза в deadRetry; ручное
// обновление его не пропускает. Источники пользователя и недавно добавленные (моложе deadGrace) не трогаются. Это не отключение: источник
// оживёт — его узлы пройдут проверку, и он вернётся в обычный круг.
const (
	deadMinChecked = 50
	deadRetry      = 24 * time.Hour
	deadGrace      = 3 * 24 * time.Hour
	yieldWindow    = 7 * 24 * time.Hour
	yieldTTL       = time.Hour
)

// sourceYields — отдача источников, пересчитывается не чаще раза в yieldTTL.
func (c *Core) sourceYields(now time.Time) map[string]store.SourceYield {
	c.yieldMu.Lock()
	defer c.yieldMu.Unlock()
	if c.yieldAt.IsZero() || now.Sub(c.yieldAt) >= yieldTTL || now.Before(c.yieldAt) {
		y, err := c.st.SourceYields(now.Add(-yieldWindow))
		if err != nil {
			log.Printf("рейтинг источников: %v", err)
			return nil
		}
		c.yield, c.yieldAt = y, now
	}
	return c.yield
}

// sourceIsDead: за неделю ни один из достаточно большой проверенной выборки узлов источника не ответил (признак «обновляется редко» в интерфейсе).
func sourceIsDead(r store.SourceRec, y store.SourceYield, now time.Time) bool {
	if r.UserAdded || r.AddedAt.IsZero() || now.Sub(r.AddedAt) < deadGrace {
		return false
	}
	return y.Checked >= deadMinChecked && y.Alive == 0
}

// sourceIsDeadWeight: источник можно не скачивать в этом круге автоматического обновления.
func sourceIsDeadWeight(r store.SourceRec, y store.SourceYield, now time.Time) bool {
	return sourceIsDead(r, y, now) && !r.LastOK.IsZero() && now.Sub(r.LastOK) < deadRetry
}
