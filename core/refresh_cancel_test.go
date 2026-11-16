package core

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"brod/aggregator"
	"brod/tgproxy"
)

// Прерванное обновление (остановка демона) не пишет разобранные источники в базу и не отмечает версию разбора:
// иначе остановка службы ждала бы записи сотен тысяч узлов, а быстрый путь «не изменилось» доверял бы недописанному.
func TestRefreshCancelledSkipsIngestAndIngestRev(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1), vlessLink(2))
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, Fetch: f.fetch,
		SeedSources: []aggregator.Source{{Name: "A", URL: "http://A.invalid/", Priority: 1}},
		TGProbe:     func(context.Context, tgproxy.Proxy) tgproxy.Result { return tgproxy.Result{Class: "connect"} }})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Refresh(ctx, "auto"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка прерванного обновления: %v", err)
	}
	if n, _, _ := c.st.CountNodes(); n != 0 {
		t.Fatalf("прерванное обновление записало узлов: %d", n)
	}
	if _, ok, _ := c.st.Setting(settingIngestRev); ok {
		t.Fatal("прерванное обновление отметило версию разбора")
	}

	rep, err := c.Refresh(context.Background(), "user")
	if err != nil || rep.Total != 2 {
		t.Fatalf("следующее обновление: %+v, %v", rep, err)
	}
}

// Перезапуск ядра сразу после обновления не запускает полное обновление заново: первое ждёт окончания периода.
func TestFirstRefreshDelayRespectsLastRefresh(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	now := time.Unix(1_800_000_000, 0)
	c.cfg.Now = func() time.Time { return now }
	c.cfg.RefreshInterval = 30 * time.Minute
	if d := c.firstRefreshDelay(); d != 500*time.Millisecond {
		t.Fatalf("без прошлого обновления: %v", d)
	}
	_ = c.st.SetSetting(settingRefreshLast, strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10))
	if d := c.firstRefreshDelay(); d != 20*time.Minute {
		t.Fatalf("прошлое 10 мин назад: %v, ожидалось 20 мин", d)
	}
	_ = c.st.SetSetting(settingRefreshLast, strconv.FormatInt(now.Add(-time.Hour).Unix(), 10))
	if d := c.firstRefreshDelay(); d != 500*time.Millisecond {
		t.Fatalf("прошлое час назад: %v", d)
	}
	_ = c.st.SetSetting(settingRefreshLast, "мусор")
	if d := c.firstRefreshDelay(); d != 500*time.Millisecond {
		t.Fatalf("испорченное значение: %v", d)
	}
}

// Завершённое обновление запоминает время начала, прерванное — нет.
func TestRefreshRecordsLastOnlyWhenCompleted(t *testing.T) {
	f := newFeed()
	f.set("A", vlessLink(1))
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true, Fetch: f.fetch,
		SeedSources: []aggregator.Source{{Name: "A", URL: "http://A.invalid/", Priority: 1}},
		TGProbe:     func(context.Context, tgproxy.Proxy) tgproxy.Result { return tgproxy.Result{Class: "connect"} }})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = c.Refresh(ctx, "auto")
	if _, ok, _ := c.st.Setting(settingRefreshLast); ok {
		t.Fatal("прерванное обновление записало время")
	}
	if _, err := c.Refresh(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.st.Setting(settingRefreshLast); !ok {
		t.Fatal("завершённое обновление не записало время")
	}
}

func TestParseLinksCtxStopsOnCancel(t *testing.T) {
	links := make([]string, 1000)
	for i := range links {
		links[i] = vlessLink(i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := parseLinksCtx(ctx, links); got != nil {
		t.Fatalf("отменённый разбор вернул %d узлов", len(got))
	}
	if got := parseLinks(links); len(got) != 1000 {
		t.Fatalf("обычный разбор: %d", len(got))
	}
}

func TestMostlyFailed(t *testing.T) {
	r := func(errs ...string) RefreshReport {
		var rep RefreshReport
		for _, e := range errs {
			rep.Sources = append(rep.Sources, SourceReport{Err: e})
		}
		return rep
	}
	for _, tc := range []struct {
		rep  RefreshReport
		want bool
	}{
		{r(), false},
		{r("", "", "x"), false},
		{r("x", "x", ""), true},
		{r("x", ""), false},
	} {
		if got := mostlyFailed(tc.rep); got != tc.want {
			t.Fatalf("%+v: %v", tc.rep, got)
		}
	}
}

func TestWaitForDefaultRouteWaitsUntilRouteOrDeadline(t *testing.T) {
	c := &Core{}
	var calls int
	c.cfg.NetID = func() string {
		calls++
		if calls < 3 {
			return "no-default-route"
		}
		return "Ethernet/192.168.1.1"
	}
	c.waitForDefaultRoute(context.Background(), time.Millisecond, time.Second)
	if calls != 3 {
		t.Fatalf("опросов до появления маршрута: %d", calls)
	}
	c.cfg.NetID = func() string { return "no-default-route" }
	t0 := time.Now()
	c.waitForDefaultRoute(context.Background(), time.Millisecond, 30*time.Millisecond)
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("ожидание не ограничено сроком: %v", d)
	}
}
