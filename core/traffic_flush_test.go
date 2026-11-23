package core

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// запись суточного трафика не должна стоять в цикле traffic: пока писатель базы занят, цикл продолжает считать отсчёты
// и ядро останавливается без ожидания записи; незаписанное при сбое не теряется.
func TestTrafficFlushDoesNotBlockLoop(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	old := trafficFlushEvery
	trafficFlushEvery = time.Millisecond
	defer func() { trafficFlushEvery = old }()

	release := make(chan struct{})
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	var sum atomic.Int64
	c.trafficAdd = func(day string, up, dn, vu, vd int64) error {
		calls.Add(1)
		<-release
		if fail.Load() {
			return errors.New("busy")
		}
		sum.Add(up + dn)
		return nil
	}

	now := time.Now()
	c.trafficTick(now, 0, 0) // первичный отсчёт: задаёт день
	c.trafficTick(now.Add(time.Second), 100, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.trafficTick(now.Add(2*time.Second), 200, 0) // запись стоит, тик не ждёт
		c.trafficTick(now.Add(3*time.Second), 300, 0)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("trafficTick ждёт запись в базу")
	}
	// запись стартует в своей горутине: ждём её первого вызова, иначе проверка иногда обгоняет запуск
	for end := time.Now().Add(2 * time.Second); calls.Load() == 0 && time.Now().Before(end); {
		time.Sleep(time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("записей одновременно %d, ждали 1", n)
	}
	if got := c.Traffic(10).Samples; len(got) != 3 {
		t.Fatalf("отсчётов %d, ждали 3", len(got))
	}
	close(release)
	// сбой записи возвращает накопленное; следующий сброс отдаёт всё без потерь
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.trafficSamples.mu.Lock()
		busy := c.trafficSamples.flushing
		c.trafficSamples.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	fail.Store(false)
	c.trafficTick(now.Add(4*time.Second), 400, 0)
	deadline = time.Now().Add(2 * time.Second)
	for sum.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sum.Load(); got != 400 {
		t.Fatalf("записано %d байт, ждали 400 (потеря при сбое)", got)
	}
}
