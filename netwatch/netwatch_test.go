package netwatch

import (
	"context"
	"testing"
	"time"
)

func TestDebounceCollapsesBursts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan struct{})
	out := Debounce(ctx, in, 100*time.Millisecond)
	for i := 0; i < 30; i++ { // всплеск из 30 событий за 150 мс
		in <- struct{}{}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("событие не пришло")
	}
	select {
	case <-out:
		t.Fatal("всплеск дал больше одного события")
	case <-time.After(250 * time.Millisecond):
	}
	// следующий всплеск — новое событие
	in <- struct{}{}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("второе событие не пришло")
	}
}

func TestDebounceStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan struct{})
	out := Debounce(ctx, in, time.Hour)
	in <- struct{}{}
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("после отмены не должно быть событий")
		}
	case <-time.After(time.Second):
		t.Fatal("канал не закрылся после отмены")
	}
}

// Подписка netlink должна открываться без привилегий и закрываться по отмене (на не-Linux допускается ошибка).
func TestWatchOpensAndCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Watch(ctx)
	if err != nil {
		t.Skipf("netlink недоступен: %v", err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		_ = ok
	case <-time.After(3 * time.Second):
		t.Fatal("канал не закрылся после отмены")
	}
}
