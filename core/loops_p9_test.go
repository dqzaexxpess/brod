package core

import (
	"strconv"
	"testing"
	"time"
)

// периодическое задание после пересоздания ядра стартует не сразу, а когда истечёт остаток периода от прошлого прохода.
func TestFirstDelayRemembersLastRun(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	const soon, interval = 20 * time.Second, 10 * time.Minute
	if d := c.firstDelay("x", soon, interval); d != soon {
		t.Fatalf("без прошлого прохода ждали %s, получили %s", soon, d)
	}
	c.markLoopRun("x")
	if d := c.firstDelay("x", soon, interval); d < interval-time.Minute || d > interval {
		t.Fatalf("после прохода «только что» ждали около %s, получили %s", interval, d)
	}
	old := c.cfg.Now().Add(-interval - time.Minute).Unix()
	if err := c.st.SetSetting(loopLastKey("x"), strconv.FormatInt(old, 10)); err != nil {
		t.Fatal(err)
	}
	if d := c.firstDelay("x", soon, interval); d != soon {
		t.Fatalf("проход старше периода: ждали %s, получили %s", soon, d)
	}
	_ = c.st.SetSetting(loopLastKey("x"), "мусор")
	if d := c.firstDelay("x", soon, interval); d != soon {
		t.Fatalf("мусор в настройке: ждали %s, получили %s", soon, d)
	}
}
