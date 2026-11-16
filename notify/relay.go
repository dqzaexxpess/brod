package notify

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Relay — Sender, который не показывает уведомления сам, а отдаёт их приложению (GET /v1/notifications, долгий опрос).
// Нужен на Windows: демон работает службой в сеансе 0 и не может показывать уведомления рабочего стола; их показывает
// приложение Brod в сеансе пользователя, а нажатия кнопок возвращает (POST /v1/notifications/{id}/action).
// Логика — когда и что показывать — остаётся в Presenter, как на Linux и Android.
type Relay struct {
	mu     sync.Mutex
	seq    uint64
	nextID uint32
	ops    []Op          // последние операции (кольцо до relayKeep)
	open   map[uint32]Op // показанные и не закрытые: новому клиенту отдаются сразу
	wake   chan struct{} // закрывается при новой операции
	cb     func(id uint32, key string)
}

// relayKeep — сколько последних операций помнит Relay; клиент, отставший сильнее, получает снимок открытых.
const relayKeep = 64

// Op — операция для приложения: show (показать или заменить уведомление id) или close (убрать).
type Op struct {
	Seq       uint64     `json:"seq"`
	Op        string     `json:"op"`
	ID        uint32     `json:"id"`
	Summary   string     `json:"summary,omitempty"`
	Body      string     `json:"body,omitempty"`
	Actions   []OpAction `json:"actions,omitempty"`
	Urgency   Urgency    `json:"urgency,omitempty"`
	TimeoutMs int64      `json:"timeout_ms,omitempty"` // 0 — по умолчанию системы; <0 — не скрывать
}

// OpAction — кнопка уведомления.
type OpAction struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// NewRelay создаёт пустой ретранслятор.
func NewRelay() *Relay {
	return &Relay{open: map[uint32]Op{}, wake: make(chan struct{})}
}

// Send реализует Sender.
func (r *Relay) Send(n Notification) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := n.ReplaceID
	if id == 0 {
		r.nextID++
		id = r.nextID
	}
	op := Op{Op: "show", ID: id, Summary: n.Summary, Body: n.Body, Urgency: n.Urgency, TimeoutMs: n.Timeout.Milliseconds()}
	for _, a := range n.Actions {
		op.Actions = append(op.Actions, OpAction{Key: a.Key, Label: a.Label})
	}
	r.push(&op)
	r.open[id] = op
	return id, nil
}

// Close реализует Sender.
func (r *Relay) Close(id uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.open[id]; !ok {
		return
	}
	delete(r.open, id)
	op := Op{Op: "close", ID: id}
	r.push(&op)
}

// OnAction реализует Sender.
func (r *Relay) OnAction(f func(id uint32, key string)) {
	r.mu.Lock()
	r.cb = f
	r.mu.Unlock()
}

// Action передаёт нажатие кнопки key уведомления id презентеру; false — такого уведомления уже нет.
func (r *Relay) Action(id uint32, key string) bool {
	r.mu.Lock()
	_, ok := r.open[id]
	cb := r.cb
	r.mu.Unlock()
	if !ok || cb == nil {
		return false
	}
	cb(id, key)
	return true
}

func (r *Relay) push(op *Op) {
	r.seq++
	op.Seq = r.seq
	r.ops = append(r.ops, *op)
	if len(r.ops) > relayKeep {
		r.ops = r.ops[len(r.ops)-relayKeep:]
	}
	close(r.wake)
	r.wake = make(chan struct{})
}

// Poll возвращает операции после after; если их нет — ждёт до wait (или отмены ctx). Клиент без истории (after == 0) или
// отставший больше, чем помнит Relay (в том числе после перезапуска демона), получает снимок открытых уведомлений.
// next — что передать в следующий раз.
func (r *Relay) Poll(ctx context.Context, after uint64, wait time.Duration) (next uint64, ops []Op) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		next, ops, reset := r.collect(after)
		wake := r.wake
		r.mu.Unlock()
		if len(ops) > 0 || (reset && after != next) {
			return next, ops
		}
		select {
		case <-wake:
		case <-deadline.C:
			return next, ops
		case <-ctx.Done():
			return next, ops
		}
	}
}

// collect — под r.mu. reset — вместо операций отдан снимок открытых уведомлений.
func (r *Relay) collect(after uint64) (next uint64, ops []Op, reset bool) {
	oldest := r.seq + 1
	if len(r.ops) > 0 {
		oldest = r.ops[0].Seq
	}
	if after == 0 || after+1 < oldest || after > r.seq {
		for _, op := range r.open {
			op.Seq = r.seq
			ops = append(ops, op)
		}
		sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })
		return r.seq, ops, true
	}
	for _, op := range r.ops {
		if op.Seq > after {
			ops = append(ops, op)
		}
	}
	return r.seq, ops, false
}
