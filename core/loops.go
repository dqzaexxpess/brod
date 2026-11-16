package core

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"brod/store"
)

// go запускает фоновое задание с защитой от паники: ошибка в одном задании не должна ронять демон.
func (c *Core) goLoop(name string, f func()) {
	c.wg.Add(1)
	c.loopEnter(name)
	go func() {
		defer c.wg.Done()
		defer c.loopExit(name)
		defer func() {
			if r := recover(); r != nil {
				c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: fmt.Sprintf("паника в задании %s: %v\n%s", name, r, debug.Stack())})
			}
		}()
		f()
	}()
}

func (c *Core) loopEnter(name string) {
	c.loopsMu.Lock()
	if c.loops == nil {
		c.loops = map[string]int{}
	}
	c.loops[name]++
	c.loopsMu.Unlock()
}

func (c *Core) loopExit(name string) {
	c.loopsMu.Lock()
	if c.loops[name]--; c.loops[name] <= 0 {
		delete(c.loops, name)
	}
	c.loopsMu.Unlock()
}

// runningLoops — имена ещё работающих фоновых заданий (с числом, если одноимённых несколько), по алфавиту.
func (c *Core) runningLoops() []string {
	c.loopsMu.Lock()
	defer c.loopsMu.Unlock()
	out := make([]string, 0, len(c.loops))
	for name, n := range c.loops {
		if n > 1 {
			name = fmt.Sprintf("%s×%d", name, n)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// waitLoops ждёт завершения фоновых заданий; если через after они ещё работают, пишет в журнал, какие именно.
func (c *Core) waitLoops(after time.Duration) {
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	// медленная остановка разбирается по именам: кто ещё работал через 250 мс (жалоба «долго выключается»: разбивка по заданиям, а не общее время)
	select {
	case <-done:
		return
	case <-time.After(250 * time.Millisecond):
		if names := c.runningLoops(); len(names) > 0 {
			log.Printf("остановка ядра: через 250 мс ещё работают задания: %s", strings.Join(names, ", "))
		}
	}
	select {
	case <-done:
		return
	case <-time.After(max(after-250*time.Millisecond, 0)):
	}
	if names := c.runningLoops(); len(names) > 0 {
		log.Printf("остановка ядра: через %s ещё работают задания: %s", after, strings.Join(names, ", "))
	}
	<-done
}

// loopLastKey — ключ настройки со временем (unix, с) последнего завершённого прохода периодического задания.
func loopLastKey(name string) string { return "loop." + name + ".last" }

// firstDelay — через сколько после пересоздания ядра запускать первый проход задания name с периодом interval: soon, если прошлого
// прохода не было или он старше периода, иначе остаток периода (не меньше soon). Ядро пересоздаётся при каждом включении, выключении,
// смене режима и сети; без памяти задания (проверка прокси Telegram, списки маршрутизации, список источников) шли заново каждый раз.
func (c *Core) firstDelay(name string, soon, interval time.Duration) time.Duration {
	v, ok, _ := c.st.Setting(loopLastKey(name))
	if !ok {
		return soon
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return soon
	}
	age := c.cfg.Now().Sub(time.Unix(sec, 0))
	if age < 0 || age >= interval {
		return soon
	}
	return max(interval-age, soon)
}

// markLoopRun запоминает время завершённого прохода задания name.
func (c *Core) markLoopRun(name string) {
	_ = c.st.SetSetting(loopLastKey(name), strconv.FormatInt(c.cfg.Now().Unix(), 10))
}

// startLoops запускает периодические задания. Первое обновление подписок не блокирует старт (P3: кэш виден сразу).
func (c *Core) startLoops(ctx context.Context) {
	if c.cfg.Manual {
		return
	}
	c.kick = make(chan struct{}, 1)
	c.goLoop("refresh", func() { c.refreshLoop(ctx) })
	c.goLoop("ota", func() { c.otaLoop(ctx) })
	c.goLoop("discover", func() { c.discoverLoop(ctx) })
	c.goLoop("update", func() { c.updateLoop(ctx) })
	c.goLoop("check", func() { c.checkLoop(ctx) })
	c.goLoop("health", func() { c.healthLoop(ctx) })
	c.goLoop("standby", func() { c.standbyLoop(ctx) })
	c.goLoop("netmode", func() { c.netModeLoop(ctx) })
	if c.warpOn {
		c.goLoop("warp", func() { c.warpLoop(ctx) })
		c.goLoop("warp-auto", func() { c.warpAutoLoop(ctx) })
	}
	c.goLoop("exit", func() { c.exitLoop(ctx) })
	c.goLoop("targets", func() { c.targetLoop(ctx) })
	c.tgKick = make(chan struct{}, 1)
	c.goLoop("tgproxy", func() { c.tgLoop(ctx) })
	if c.cfg.TUN.Enabled {
		c.goLoop("rulesets", func() { c.ruleSetLoop(ctx) })
		c.goLoop("route-via", func() { c.routeViaLoop(ctx) })
	}
}

// idlePoll — как часто спящее без дела задание проверяет, не пора ли проснуться.
const idlePoll = 30 * time.Second

// waitActive задерживает проход периодического задания, пока ядру нечего обслуживать: VPN выключен и интерфейс приложения не на экране. Android убивает фоновое приложение за процессор (EXCESSIVE CPU USAGE, 13 нояб.), а обновление источников стоит там 90–110 с
// процессора. Работа идёт сразу, как включат VPN (ядро тогда создаётся заново) или откроют приложение. false — контекст отменён.
func (c *Core) waitActive(ctx context.Context) bool {
	for c.cfg.Idle != nil && !c.cfg.TUN.Enabled && c.cfg.Idle() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(idlePoll):
		}
	}
	return ctx.Err() == nil
}

// KickCheck просит планировщик провести круг проверок как можно скорее (после обновления подписок, смены сети).
func (c *Core) KickCheck() {
	if c.kick == nil {
		return
	}
	select {
	case c.kick <- struct{}{}:
	default: // запрос уже стоит в очереди
	}
}

func (c *Core) refreshLoop(ctx context.Context) {
	timer := time.NewTimer(c.firstRefreshDelay())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if !c.waitActive(ctx) {
			return
		}
		c.waitForDefaultRoute(ctx, 2*time.Second, 2*time.Minute)
		rep, err := c.Refresh(ctx, "auto")
		if err != nil && ctx.Err() == nil && err != ErrBusy {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "обновление подписок: " + err.Error()})
		}
		c.KickCheck()
		next := c.cfg.RefreshInterval
		if mostlyFailed(rep) {
			// большинство источников не загрузилось (сеть ещё не готова после загрузки системы, DNS отвечает REFUSED):
			// повтор через минуту, а не через полный период
			next = min(next, time.Minute)
		}
		timer.Reset(next)
	}
}

// waitForDefaultRoute ждёт, пока у системы появится маршрут по умолчанию (служба Windows стартует раньше сети), но не дольше limit.
func (c *Core) waitForDefaultRoute(ctx context.Context, poll, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for strings.HasPrefix(c.cfg.NetID(), "no-default-route") && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// mostlyFailed — не загрузилось больше половины источников обновления.
func mostlyFailed(rep RefreshReport) bool {
	failed := 0
	for _, s := range rep.Sources {
		if s.Err != "" {
			failed++
		}
	}
	return len(rep.Sources) > 0 && failed*2 > len(rep.Sources)
}

func (c *Core) checkLoop(ctx context.Context) {
	t := time.NewTicker(c.cfg.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
		if !c.waitActive(ctx) {
			return
		}
		if _, err := c.CheckNow(ctx, CheckOptions{}, "auto"); err != nil && ctx.Err() == nil && err != ErrBusy {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "проверки: " + err.Error()})
		}
	}
}
