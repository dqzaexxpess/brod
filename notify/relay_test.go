package notify

import (
	"context"
	"testing"
	"time"
)

func TestRelayDeliversShowCloseAndActions(t *testing.T) {
	r := NewRelay()
	var got []string
	r.OnAction(func(id uint32, key string) { got = append(got, key) })
	ctx := context.Background()

	id, _ := r.Send(Notification{Summary: "узел деградирует", Body: "b", Actions: []Action{{"swap", "Сменить"}}, Timeout: -time.Second})
	next, ops := r.Poll(ctx, 0, time.Second)
	if len(ops) != 1 || ops[0].Op != "show" || ops[0].ID != id || ops[0].Actions[0].Key != "swap" || ops[0].TimeoutMs != -1000 {
		t.Fatalf("первый опрос: %+v", ops)
	}
	// замена того же уведомления и закрытие приходят по порядку
	r.Send(Notification{Summary: "заменено", ReplaceID: id})
	r.Close(id)
	next2, ops := r.Poll(ctx, next, time.Second)
	if len(ops) != 2 || ops[0].Op != "show" || ops[0].Summary != "заменено" || ops[1].Op != "close" || next2 != next+2 {
		t.Fatalf("второй опрос: %+v next=%d", ops, next2)
	}
	if r.Action(id, "swap") {
		t.Fatal("кнопка закрытого уведомления сработала")
	}
	id2, _ := r.Send(Notification{Summary: "x", Actions: []Action{{"ok", "Ок"}}})
	if !r.Action(id2, "ok") || len(got) != 1 || got[0] != "ok" {
		t.Fatalf("кнопка: %v", got)
	}
}

func TestRelayPollWaitsAndWakes(t *testing.T) {
	r := NewRelay()
	ctx := context.Background()
	t0 := time.Now()
	if _, ops := r.Poll(ctx, 0, 30*time.Millisecond); len(ops) != 0 || time.Since(t0) < 25*time.Millisecond {
		t.Fatalf("пустой опрос не ждал: %v", ops)
	}
	go func() { time.Sleep(20 * time.Millisecond); r.Send(Notification{Summary: "новое"}) }()
	if _, ops := r.Poll(ctx, 0, 2*time.Second); len(ops) != 1 || ops[0].Summary != "новое" {
		t.Fatalf("ожидание не разбудилось: %+v", ops)
	}
}

func TestRelaySnapshotAfterGapOrRestart(t *testing.T) {
	r := NewRelay()
	ctx := context.Background()
	keep, _ := r.Send(Notification{Summary: "открытое"})
	for i := 0; i < relayKeep+10; i++ {
		id, _ := r.Send(Notification{Summary: "шум"})
		r.Close(id)
	}
	// клиент отстал дальше кольца: снимок открытых
	next, ops := r.Poll(ctx, 1, time.Second)
	if len(ops) != 1 || ops[0].ID != keep || next != r.seq {
		t.Fatalf("снимок после отставания: %+v", ops)
	}
	// демон перезапустился (счётчик меньше, чем у клиента), открытых нет: ответ сразу, с новым счётчиком
	r2 := NewRelay()
	t0 := time.Now()
	next, ops = r2.Poll(ctx, 500, time.Second)
	if len(ops) != 0 || next != 0 || time.Since(t0) > 500*time.Millisecond {
		t.Fatalf("после перезапуска: %+v next=%d", ops, next)
	}
}
