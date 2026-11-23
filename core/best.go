package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"brod/store"
)

// bestScore оценивает узел для выбора «лучшего»: чистота и YouTube важнее скорости, скорость важнее пинга; свои серверы и избранные — с бонусом.
func bestScore(v NodeView) float64 {
	sc := 0.0
	if v.Own {
		sc += 100
	}
	if v.Favorite {
		sc += 20
	}
	if v.Exit != nil {
		switch v.Exit.Clean {
		case CleanClean:
			sc += 30
		case CleanMixed:
			sc += 10
		case CleanDirty:
			sc -= 20
		}
	}
	if v.YouTube {
		sc += 20
	}
	if v.GoogleAPI {
		sc += 10
	}
	if v.SpeedKBps > 0 {
		sc += min(v.SpeedKBps*8/1024, 30) // до 30 Мбит/с
	}
	if v.LatencyMs > 0 {
		sc -= float64(v.LatencyMs) / 50 // 500 мс = −10
	}
	return sc
}

// stabilityBonus — поправка к оценке за поведение узла на проверках за сутки: доля успехов (от +15 при 100% до −35 при 0%; меньше трёх проверок —
// данных мало, поправки нет), «мигание» (успех↔провал) и свежие провалы за последние 15 минут. Живой по последней проверке узел с плохой историей
// уступает чуть менее быстрому, но стабильному.
func stabilityBonus(st store.Stability) float64 {
	if st.OK+st.Fail < 3 {
		return 0
	}
	r, _ := st.Ratio()
	b := (r - 0.7) * 50
	b -= float64(min(st.Flips, 6)) * 3
	b -= float64(min(st.Recent, 3)) * 10
	return b
}

// BestNodes возвращает живые узлы в порядке «лучший первым».
func (c *Core) BestNodes(limit int) ([]NodeView, error) {
	views, err := c.Nodes(NodesQuery{Alive: true, Limit: 400})
	if err != nil {
		return nil, err
	}
	kept := views[:0]
	for _, v := range views {
		if v.Proto != chainProto { // цепочки автоподбор не выбирает
			kept = append(kept, v)
		}
	}
	views = kept
	score := make(map[string]float64, len(views))
	ids := make([]string, len(views))
	for i, v := range views {
		ids[i] = v.ID
	}
	now := c.cfg.Now()
	stab, _ := c.st.NodeStability(c.netKey(), ids, now.Add(-24*time.Hour), now.Add(-15*time.Minute)) // нет истории — оценка без поправки
	for _, v := range views {
		score[v.ID] = bestScore(v) + stabilityBonus(stab[v.ID])
	}
	sort.SliceStable(views, func(i, j int) bool { return score[views[i].ID] > score[views[j].ID] })
	if limit > 0 && len(views) > limit {
		views = views[:limit]
	}
	return views, nil
}

// ConnectBest делает активным лучший живой узел и возвращает его. Если живых нет — ErrNoCandidates.
func (c *Core) ConnectBest(initiator string) (NodeView, error) {
	best, err := c.BestNodes(10)
	if err != nil {
		return NodeView{}, err
	}
	if len(best) == 0 {
		return NodeView{}, fmt.Errorf("%w: живых узлов пока нет — обновите подписки и подождите круг проверок", ErrNoCandidates)
	}
	var last error
	var first *NodeView
	tried := 0
	for _, v := range best {
		if err := c.Connect(v.ID, initiator); err != nil {
			last = err
			continue
		}
		if first == nil {
			f := v
			first = &f
		}
		tried++
		// узел «жив» по прошлым проверкам, но сейчас может не пропускать трафик: подтверждаем реальным запросом через путь, как он будет работать
		if c.verifyActive() {
			return v, nil
		}
		if tried >= bestVerifyTries {
			break
		}
	}
	if first == nil {
		return NodeView{}, last
	}
	// ни один из первых узлов не прошёл проверку: остаётся лучший по оценке (проверка могла быть неудачной из-за самой сети)
	if tried > 1 {
		if err := c.Connect(first.ID, initiator); err != nil {
			return NodeView{}, err
		}
	}
	return *first, nil
}

// bestVerifyTries — сколько лучших узлов проверяется реальным запросом при выборе «лучшего».
const bestVerifyTries = 3

// verifyActive — быстрая проверка выхода активного узла; true и тогда, когда проверить нельзя (ядро не запущено): отказ только по явному провалу.
func (c *Core) verifyActive() bool {
	r, err := c.ProbeActiveQuick(context.Background())
	if err != nil {
		return true
	}
	return r.OK
}
