package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// отмена посреди записи большого источника прерывает запись между порциями, а не после последней:
// иначе остановка ядра (и выключение VPN) ждали до 9 с на телефоне.
func TestRefreshCancelStopsBetweenChunks(t *testing.T) {
	old := ingestChunk
	ingestChunk = 50
	t.Cleanup(func() { ingestChunk = old })
	const total = 6000
	links := make([]string, total)
	for i := range links {
		links[i] = vlessLink(i)
	}
	f := newFeed()
	f.set("A", links...)
	c := refreshCore(t, f, "A")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // отмена, как только запись началась
		for ctx.Err() == nil {
			if n, _, _ := c.st.CountNodes(); n > 0 {
				cancel()
				return
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	t0 := time.Now()
	_, err := c.Refresh(ctx, "auto")
	took := time.Since(t0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка прерванного обновления: %v", err)
	}
	n, _, _ := c.st.CountNodes()
	if n >= total {
		t.Fatalf("запись не прервана: записано %d из %d (обновление шло %v)", n, total, took)
	}
	if _, ok, _ := c.st.Setting(settingIngestRev); ok {
		t.Fatal("прерванное обновление отметило версию разбора")
	}
	rep, err := c.Refresh(context.Background(), "user") // недописанный источник доезжает следующим обновлением
	if err != nil || rep.Total != total {
		t.Fatalf("следующее обновление: total=%d, %v", rep.Total, err)
	}
}
