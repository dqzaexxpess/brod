package core

import (
	"context"
	"testing"
	"time"
)

// резерв (период 3 мин) тоже помнит время прошлого прохода: удачное обновление его отмечает, неудачное (запасных нет) нет.
func TestStandbyRefreshMarksLoopRun(t *testing.T) {
	lv, fc := &liveness{ok: true}, newFakeCheck()
	c := healthCore(t, lv, fc)
	c.cfg.StandbyInterval = 10 * time.Minute
	active := addNode(t, c, "active")
	_ = c.Connect(active, "user")

	c.refreshStandby(context.Background()) // запасных нет
	if _, ok, _ := c.st.Setting(loopLastKey("standby")); ok {
		t.Fatal("проход без результата отмечен как удачный")
	}
	alt := addNode(t, c, "alt")
	fc.ok[alt] = true
	seedFresh(t, c, alt)
	c.refreshStandby(context.Background())
	if d := c.firstDelay("standby", c.cfg.StandbyFirstDelay, c.cfg.StandbyInterval); d < c.cfg.StandbyInterval-time.Minute {
		t.Fatalf("после удачного прохода ждали около периода, получили %s", d)
	}
}
