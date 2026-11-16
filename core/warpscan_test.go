package core

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/warp"
)

func TestBestWarpPrefersFastButKeepsCurrentWithin25Percent(t *testing.T) {
	cur := WarpProfile{Endpoint: "162.159.192.1", Port: 2408, Mask: "quic"}
	fast := WarpProfile{Endpoint: "188.114.97.9", Port: 987, Mask: "quic"}
	mk := func(p WarpProfile, ok bool, ms int64) WarpScanResult {
		return WarpScanResult{Profile: p, OK: ok, LatencyMs: ms}
	}
	if _, ok := bestWarp([]WarpScanResult{mk(cur, false, 0), mk(fast, false, 0)}, cur); ok {
		t.Fatal("без рабочих профилей выбирать нечего")
	}
	if p, ok := bestWarp([]WarpScanResult{mk(cur, false, 0), mk(fast, true, 400)}, cur); !ok || p != fast {
		t.Fatalf("текущий не работает: ожидался %v, получен %v", fast, p)
	}
	if p, _ := bestWarp([]WarpScanResult{mk(cur, true, 450), mk(fast, true, 400)}, cur); p != cur {
		t.Fatalf("разница меньше 25%%: остаёмся на текущем, получен %v", p)
	}
	if p, _ := bestWarp([]WarpScanResult{mk(cur, true, 900), mk(fast, true, 400)}, cur); p != fast {
		t.Fatalf("текущий заметно медленнее: ожидался %v, получен %v", fast, p)
	}
}

func scanCore(t *testing.T, probe func(WarpProfile) prober.Result) (*Core, *atomic.Int32) {
	t.Helper()
	g := warpVerifyGap
	warpVerifyGap = 0 // повторные проверки без пауз
	t.Cleanup(func() { warpVerifyGap = g })
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	c.warpOn, c.warpAcc = true, &warp.Account{AddrV4: "172.16.0.2"}
	c.cfg.NetID = func() string { return "cell:25060" }
	c.cfg.WarpScanProbe = func(_ context.Context, p WarpProfile) prober.Result { return probe(p) }
	var applied atomic.Int32
	c.cfg.OnWarpProfile = func() { applied.Add(1) }
	return c, &applied
}

func TestWarpScanAppliesBestAndRemembersPerNetwork(t *testing.T) {
	good := WarpProfile{Endpoint: "188.114.97.9", Port: 987, Mask: "quic"}
	c, applied := scanCore(t, func(p WarpProfile) prober.Result {
		if p == good {
			return prober.Result{OK: true, LatencyMs: 406}
		}
		return prober.Result{Class: prober.ClassTimeout}
	})
	cur := c.profileOf() // адрес по умолчанию зависит от платформы: на Windows — IP (config.go)
	res, err := c.WarpScan(context.Background(), WarpScanOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(append([]WarpProfile{c.profileOf()}, warpScanCandidates...)) && len(res) == 0 {
		t.Fatalf("результатов %d", len(res))
	}
	if res[0].Profile.Endpoint != cur.Endpoint || res[0].OK {
		t.Fatalf("первым пробуется действующий профиль: %+v", res[0])
	}
	if rp, ok := c.RememberedWarpProfile(); !ok || rp != good {
		t.Fatalf("профиль сети не запомнен: %v %v", rp, ok)
	}
	if v, _, _ := c.st.Setting(SetWarpEndpoint); v != good.Endpoint {
		t.Fatalf("профиль не записан в настройки: %q", v)
	}
	for i := 0; i < 50 && applied.Load() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if applied.Load() != 1 {
		t.Fatalf("владельцу ядра не сообщили о новом профиле: %d", applied.Load())
	}
	// другая сеть: профиль не запомнен
	c.cfg.NetID = func() string { return "wifi:192.168.0.1" }
	if _, ok := c.RememberedWarpProfile(); ok {
		t.Fatal("профиль сотовой сети не должен показываться в другой сети")
	}
	st := c.WarpState()
	if !st.Enabled || !st.Auto || st.Manual[SetWarpEndpoint] != good.Endpoint || st.Remembered != nil {
		t.Fatalf("состояние: %+v", st)
	}
}

func TestWarpScanWithoutAccountAndWhileBusy(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	if _, err := c.WarpScan(context.Background(), WarpScanOptions{}); err == nil {
		t.Fatal("без аккаунта подбор невозможен")
	}
	release := make(chan struct{})
	c2, _ := scanCore(t, func(WarpProfile) prober.Result { <-release; return prober.Result{} })
	done := make(chan error, 1)
	go func() { _, err := c2.WarpScan(context.Background(), WarpScanOptions{Limit: 2}); done <- err }()
	for i := 0; i < 100 && !c2.WarpState().Scanning; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := c2.WarpScan(context.Background(), WarpScanOptions{}); err != ErrBusy {
		t.Fatalf("второй подбор одновременно: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWarpScanStopsAtGood(t *testing.T) {
	var probed atomic.Int32
	c, _ := scanCore(t, func(WarpProfile) prober.Result { probed.Add(1); return prober.Result{OK: true, LatencyMs: 300} })
	if _, err := c.WarpScan(context.Background(), WarpScanOptions{Parallel: 1, StopAtGood: 3}); err != nil {
		t.Fatal(err)
	}
	if n := probed.Load(); n != 3 {
		t.Fatalf("при StopAtGood=3 и одной пробе за раз ожидалось 3 пробы, было %d", n)
	}
}

func TestSetWarpParamsValidatesBeforeWriting(t *testing.T) {
	c, _ := scanCore(t, func(WarpProfile) prober.Result { return prober.Result{} })
	if err := c.SetWarpParams(map[string]string{SetWarpEndpoint: "188.114.96.74", SetWarpPort: "99999"}); err == nil {
		t.Fatal("неверный порт должен отклонить весь набор")
	}
	if v, _, _ := c.st.Setting(SetWarpEndpoint); v != "" {
		t.Fatalf("часть набора записана несмотря на ошибку: %q", v)
	}
	if err := c.SetWarpParams(map[string]string{"check.via_warp": "0"}); err == nil {
		t.Fatal("чужие ключи через набор WARP недоступны")
	}
	if err := c.SetWarpParams(map[string]string{SetWarpEndpoint: "188.114.96.74", SetWarpPort: "2408", SetWarpMask: "none"}); err != nil {
		t.Fatal(err)
	}
	if got := c.WarpState().Manual; got[SetWarpEndpoint] != "188.114.96.74" || got[SetWarpMask] != "none" {
		t.Fatalf("параметры: %v", got)
	}
}

// Профиль, подобранный для текущей сети, ставится при создании ядра: оно не пересоздаётся через 20 с после старта (холодный WARP каждый раз).
func TestNewCoreUsesRememberedWarpProfile(t *testing.T) {
	dir := t.TempDir()
	good := WarpProfile{Endpoint: "188.114.97.9", Port: 987, Mask: "quic"}
	c := newCore(t, dir)
	c.cfg.NetID = func() string { return "cell:25060" }
	if err := c.rememberWarpProfile(good); err != nil {
		t.Fatal(err)
	}
	c.Close()

	netID := "cell:25060"
	c2, err := New(Config{StateDir: dir, AppPort: nextPort(), LogLevel: "error", Manual: true, NetID: func() string { return netID }})
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.profileOf(); got != good {
		t.Fatalf("профиль сети не применён при создании: %v", got)
	}
	c2.Close()

	netID = "wifi:192.168.0.1" // другая сеть: прежний профиль остаётся в настройках, запомненного для неё нет
	c3, err := New(Config{StateDir: dir, AppPort: nextPort(), LogLevel: "error", Manual: true, NetID: func() string { return netID }})
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if got := c3.profileOf(); got != good {
		t.Fatalf("настройки не должны меняться в сети без профиля: %v", got)
	}
	// авто выключено: профиль сети не подставляется
	_ = c3.st.SetSetting(SetWarpAuto, "0")
	_ = c3.st.SetSetting(SetWarpEndpoint, "162.159.192.1")
	c3.Close()
	netID = "cell:25060"
	c4, err := New(Config{StateDir: dir, AppPort: nextPort(), LogLevel: "error", Manual: true, NetID: func() string { return netID }})
	if err != nil {
		t.Fatal(err)
	}
	defer c4.Close()
	if got := c4.profileOf(); got.Endpoint != "162.159.192.1" {
		t.Fatalf("при warp.auto=0 профиль сети подставлять нельзя: %v", got)
	}
}

// Подбор и автоцикл просят применить один и тот же профиль почти одновременно: перезапуск ядра один.
func TestApplyWarpProfileDedupes(t *testing.T) {
	good := WarpProfile{Endpoint: "188.114.97.9", Port: 987, Mask: "quic"}
	c, applied := scanCore(t, func(WarpProfile) prober.Result { return prober.Result{Class: prober.ClassTimeout} })
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.applyWarpProfile(good) }()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)
	if n := applied.Load(); n != 1 {
		t.Fatalf("перезапусков %d, нужен один", n)
	}
}

// Сразу после старта WARP проверяется чаще обычного: упал он или поднялся, выясняется за секунды.
func TestWarpWatchFastRightAfterStart(t *testing.T) {
	old := warpFastEvery
	warpFastEvery = 40 * time.Millisecond
	defer func() { warpFastEvery = old }()
	var calls atomic.Int32
	c := rawWarpCore(t, newTCheck(), func(cfg *Config) {
		cfg.WarpWatch = time.Hour
		cfg.WarpLiveness = func(context.Context, *engine.Probe) prober.Result {
			calls.Add(1)
			return prober.Result{OK: true}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.warpLoop(ctx) // в тестовом ядре (Manual) циклы сами не запускаются
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() < 4 {
		t.Fatalf("проверок WARP %d: после старта они должны идти часто даже при WarpWatch=1h", calls.Load())
	}
}

// Профиль, ответивший один раз, но не державшийся (на LTE WARP после первой удачной проверки через 20–40 с снова «down»), не должен побеждать
// более медленный, но устойчивый.
func TestWarpScanPrefersStableProfile(t *testing.T) {
	flaky := WarpProfile{Endpoint: "188.114.99.157", Port: 1074, Mask: "quic"}
	steady := WarpProfile{Endpoint: "162.159.195.1", Port: 2408, Mask: "quic"}
	var flakyCalls atomic.Int32
	c, _ := scanCore(t, func(p WarpProfile) prober.Result {
		switch p {
		case flaky:
			if flakyCalls.Add(1) == 1 { // первая проба быстрая, дальше молчит
				return prober.Result{OK: true, LatencyMs: 200}
			}
			return prober.Result{Class: prober.ClassTimeout}
		case steady:
			return prober.Result{OK: true, LatencyMs: 420}
		}
		return prober.Result{Class: prober.ClassTimeout}
	})
	res, err := c.WarpScan(context.Background(), WarpScanOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if rp, ok := c.RememberedWarpProfile(); !ok || rp != steady {
		t.Fatalf("запомнен %v (ожидался устойчивый %v)", rp, steady)
	}
	stable := map[WarpProfile]int{}
	for _, r := range res {
		if r.OK {
			stable[r.Profile] = r.Stable
		}
	}
	if stable[flaky] != 1 || stable[steady] != 1+warpVerifyRounds {
		t.Fatalf("устойчивость: %v", stable)
	}
}

func TestBestWarpPrefersStabilityOverSpeed(t *testing.T) {
	cur := WarpProfile{Endpoint: "162.159.192.1", Port: 2408, Mask: "quic"}
	fast := WarpProfile{Endpoint: "188.114.97.9", Port: 987, Mask: "quic"}
	slow := WarpProfile{Endpoint: "188.114.96.74", Port: 500, Mask: "none"}
	res := []WarpScanResult{{Profile: fast, OK: true, LatencyMs: 200, Stable: 1}, {Profile: slow, OK: true, LatencyMs: 600, Stable: 3}}
	if p, _ := bestWarp(res, cur); p != slow {
		t.Fatalf("устойчивый должен выигрывать у быстрого: %v", p)
	}
}

// Ручные параметры («Применить») должны переживать пересоздание ядра: они становятся запомненным профилем текущей сети,
// иначе их затирал бы запомненный профиль (preapplyWarpProfile).
func TestSetWarpParamsRemembersProfileForNetwork(t *testing.T) {
	c, _ := scanCore(t, func(WarpProfile) prober.Result { return prober.Result{} })
	c.cfg.NetID = func() string { return "wifi:test" }
	if err := c.rememberWarpProfile(WarpProfile{Endpoint: "8.34.146.9", Port: 1074, Mask: "none"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetWarpParams(map[string]string{SetWarpEndpoint: "188.114.96.74", SetWarpPort: "500", SetWarpMask: "quic"}); err != nil {
		t.Fatal(err)
	}
	preapplyWarpProfile(c.st, c.cfg) // как при создании ядра
	if v, _, _ := c.st.Setting(SetWarpEndpoint); v != "188.114.96.74" {
		t.Fatalf("ручной адрес затёрт запомненным профилем: %q", v)
	}
	if p, ok := c.RememberedWarpProfile(); !ok || p.Endpoint != "188.114.96.74" || p.Port != 500 || p.Mask != "quic" {
		t.Fatalf("запомненный профиль: %+v %v", p, ok)
	}
}

// Подбор выбрал профиль, а основной endpoint его не принял: после неудачной первой проверки возвращается прежний (он в подборе отвечал).
func TestWarpTrialRollsBackWhenChosenProfileFails(t *testing.T) {
	c, _ := scanCore(t, func(WarpProfile) prober.Result { return prober.Result{OK: true, LatencyMs: 100} })
	prev := c.profileOf()
	newP := WarpProfile{Endpoint: "8.34.146.9", Port: 1074, Mask: "none"}
	b, _ := json.Marshal(warpTrial{New: newP, Prev: prev})
	_ = c.st.SetSetting(settingWarpTrial, string(b))
	c.cfg.WarpEndpoint, c.cfg.WarpPort, c.cfg.WarpMask = newP.Endpoint, newP.Port, newP.Mask // ядро уже работает на новом профиле

	c.settleWarpTrial(false)
	if p, ok := c.RememberedWarpProfile(); !ok || p != prev {
		t.Fatalf("после провала запомнен %+v, ждали прежний %+v", p, prev)
	}
	if v, _, _ := c.st.Setting(settingWarpTrial); v != "" {
		t.Fatal("опыт должен быть одноразовым")
	}

	// удачная проверка закрепляет профиль и опыт снимает
	_ = c.st.SetSetting(settingWarpTrial, string(b))
	_ = c.rememberWarpProfile(newP)
	c.settleWarpTrial(true)
	if p, _ := c.RememberedWarpProfile(); p != newP {
		t.Fatalf("при удаче профиль должен остаться: %+v", p)
	}
}
