package core

import (
	"strings"
	"testing"
	"time"
)

func TestSubStages(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name            string
		used, total     int64
		expire          time.Time
		wantExp, wantTr string
	}{
		{"всё хорошо", 100, 1000, now.Add(30 * day), "", ""},
		{"срок через два дня", 0, 0, now.Add(2 * day), "soon", ""},
		{"срок истёк", 0, 0, now.Add(-time.Hour), "expired", ""},
		{"осталось меньше десятой", 950, 1000, time.Time{}, "", "low"},
		{"ровно десятая — ещё не предупреждаем", 900, 1000, time.Time{}, "", ""},
		{"трафик кончился", 1000, 1000, time.Time{}, "", "out"},
		{"перерасход", 1200, 1000, time.Time{}, "", "out"},
		{"лимита нет", 5000, 0, time.Time{}, "", ""},
	} {
		e, tr := subStages(now, tc.used, tc.total, tc.expire)
		if e != tc.wantExp || tr != tc.wantTr {
			t.Errorf("%s: срок %q, трафик %q; ожидалось %q и %q", tc.name, e, tr, tc.wantExp, tc.wantTr)
		}
	}
}

func TestNoteSubscriptionWarnsOncePerStage(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	sub := c.bus.Subscribe(16)
	defer sub.Close()
	ch := sub.C
	count := func() (n int) {
		for {
			select {
			case e := <-ch:
				if e.Kind == EvSubscription {
					n++
				}
			default:
				return
			}
		}
	}
	now := c.cfg.Now()
	exp := now.Add(48 * time.Hour)
	c.noteSubscription("s1", "Моя подписка", 0, 0, exp)
	c.noteSubscription("s1", "Моя подписка", 0, 0, exp) // повтор: тишина
	if n := count(); n != 1 {
		t.Fatalf("предупреждений о сроке: %d, ожидалось 1", n)
	}
	c.noteSubscription("s1", "Моя подписка", 0, 0, now.Add(60*24*time.Hour)) // продлили: память сброшена, предупреждения нет
	if n := count(); n != 0 {
		t.Fatalf("после продления предупреждений быть не должно: %d", n)
	}
	c.noteSubscription("s1", "Моя подписка", 0, 0, exp) // снова скоро кончится: новое предупреждение
	if n := count(); n != 1 {
		t.Fatalf("новое предупреждение после продления и нового срока: %d", n)
	}
	c.noteSubscription("s1", "", 960, 1000, time.Time{})  // трафик на исходе
	c.noteSubscription("s1", "", 1000, 1000, time.Time{}) // трафик кончился: другая стадия
	if n := count(); n != 2 {
		t.Fatalf("предупреждений о трафике: %d, ожидалось 2 (low, out)", n)
	}
}

func TestSubscriptionText(t *testing.T) {
	s, b := SubscriptionText("expire", "soon", "Моя", 2, 0)
	if !strings.Contains(s, "скоро") || !strings.Contains(b, "через 2 дня") {
		t.Fatalf("%q / %q", s, b)
	}
	if _, b := SubscriptionText("expire", "soon", "Моя", 1, 0); !strings.Contains(b, "завтра") {
		t.Fatalf("%q", b)
	}
	if _, b := SubscriptionText("expire", "soon", "Моя", 5, 0); !strings.Contains(b, "через 5 дней") {
		t.Fatalf("%q", b)
	}
	if _, b := SubscriptionText("traffic", "low", "Моя", 0, 7); !strings.Contains(b, "7%") {
		t.Fatalf("%q", b)
	}
	if s, _ := SubscriptionText("traffic", "out", "Моя", 0, 0); !strings.Contains(s, "закончился") {
		t.Fatalf("%q", s)
	}
}
