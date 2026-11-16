package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Пакетные замеры по выбранным узлам: пинг (проверка L2), страна и чистота выхода, скорость. Один пакет за раз, в фоне; ход виден через
// MeasureState, остановить можно CancelMeasure.

// MeasureOptions — что и для кого замерять.
type MeasureOptions struct {
	IDs   []string
	Ping  bool
	Exit  bool
	Speed bool
}

// MeasureState — ход пакетного замера.
type MeasureState struct {
	Running  bool
	Total    int // шагов всего (узлов × видов замера)
	Done     int
	Stage    string // ping | exit | speed
	Started  time.Time
	Finished time.Time
	OK       int // узлов, у которых замер удался (по последнему виду)
	Canceled bool
}

const maxMeasureIDs = 500

type measureJob struct {
	mu     sync.Mutex
	st     MeasureState
	cancel context.CancelFunc
}

// StartMeasure запускает пакетный замер в фоне. ErrBusy, если предыдущий ещё идёт.
func (c *Core) StartMeasure(opt MeasureOptions) error {
	if len(opt.IDs) == 0 {
		return fmt.Errorf("%w: не выбрано ни одного узла", ErrInvalid)
	}
	if len(opt.IDs) > maxMeasureIDs {
		return fmt.Errorf("%w: за раз не больше %d узлов", ErrInvalid, maxMeasureIDs)
	}
	kinds := 0
	for _, k := range []bool{opt.Ping, opt.Exit, opt.Speed} {
		if k {
			kinds++
		}
	}
	if kinds == 0 {
		return fmt.Errorf("%w: не выбран вид замера", ErrInvalid)
	}
	j := &c.measure
	j.mu.Lock()
	if j.st.Running {
		j.mu.Unlock()
		return ErrBusy
	}
	base := c.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	j.cancel = cancel
	j.st = MeasureState{Running: true, Total: len(opt.IDs) * kinds, Started: c.cfg.Now()}
	j.mu.Unlock()
	c.goLoop("measure", func() { c.runMeasure(ctx, opt) })
	return nil
}

func (c *Core) runMeasure(ctx context.Context, opt MeasureOptions) {
	j := &c.measure
	set := func(f func(*MeasureState)) {
		j.mu.Lock()
		f(&j.st)
		j.mu.Unlock()
	}
	chunks := func(step int, f func(ids []string) int) {
		for i := 0; i < len(opt.IDs) && ctx.Err() == nil; i += step {
			part := opt.IDs[i:min(i+step, len(opt.IDs))]
			ok := f(part)
			set(func(s *MeasureState) { s.Done += len(part); s.OK = ok })
		}
	}
	if opt.Ping {
		set(func(s *MeasureState) { s.Stage, s.OK = "ping", 0 })
		chunks(20, func(ids []string) int {
			rep, _ := c.CheckNow(ctx, CheckOptions{IDs: ids, Force: true}, "user")
			return rep.OK
		})
	}
	if opt.Exit {
		set(func(s *MeasureState) { s.Stage, s.OK = "exit", 0 })
		chunks(6, func(ids []string) int {
			n, _ := c.ExitInfoNow(ctx, ids)
			return n
		})
	}
	if opt.Speed {
		set(func(s *MeasureState) { s.Stage, s.OK = "speed", 0 })
		chunks(4, func(ids []string) int {
			n, _ := c.SpeedNow(ctx, ids)
			return n
		})
	}
	set(func(s *MeasureState) {
		s.Running, s.Finished, s.Canceled = false, c.cfg.Now(), ctx.Err() != nil
	})
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	j.mu.Unlock()
}

// MeasureState возвращает ход последнего пакетного замера.
func (c *Core) MeasureState() MeasureState {
	c.measure.mu.Lock()
	defer c.measure.mu.Unlock()
	return c.measure.st
}

// CancelMeasure останавливает идущий пакетный замер.
func (c *Core) CancelMeasure() {
	c.measure.mu.Lock()
	cancel := c.measure.cancel
	c.measure.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
