package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// пока интерфейс скрыт и VPN выключен, проход периодического задания ждёт; включили VPN или показали интерфейс — идёт сразу.
func TestWaitActive(t *testing.T) {
	var idle atomic.Bool
	c := &Core{cfg: Config{Idle: idle.Load}}

	if !c.waitActive(context.Background()) {
		t.Fatal("интерфейс на экране: задание не должно ждать")
	}

	idle.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if c.waitActive(ctx) {
		t.Fatal("интерфейс скрыт, VPN выключен: задание должно спать до отмены")
	}

	c.cfg.TUN.Enabled = true
	if !c.waitActive(context.Background()) {
		t.Fatal("VPN включён: задание не должно ждать")
	}

	c.cfg.TUN.Enabled = false
	c.cfg.Idle = nil
	if !c.waitActive(context.Background()) {
		t.Fatal("гейта нет (ПК): задание не должно ждать")
	}
}
