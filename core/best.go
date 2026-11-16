package core

import (
	"fmt"
	"sort"
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

// BestNodes возвращает живые узлы в порядке «лучший первым».
func (c *Core) BestNodes(limit int) ([]NodeView, error) {
	views, err := c.Nodes(NodesQuery{Alive: true, Limit: 400})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(views, func(i, j int) bool { return bestScore(views[i]) > bestScore(views[j]) })
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
	for _, v := range best {
		if err := c.Connect(v.ID, initiator); err == nil {
			return v, nil
		} else {
			last = err
		}
	}
	return NodeView{}, last
}
