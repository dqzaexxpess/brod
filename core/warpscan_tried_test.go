package core

import (
	"testing"
	"time"
)

// «не чаще раза в 6 часов на сеть» переживает пересоздание ядра: время последнего подбора лежит в настройках.
func TestWarpTriedPersists(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	if !c.warpTriedAt("net-a").IsZero() {
		t.Fatal("для новой сети подбор не должен числиться пробованным")
	}
	c.markWarpTried("net-a")
	if time.Since(c.warpTriedAt("net-a")) > time.Minute {
		t.Fatalf("время подбора не запомнилось: %v", c.warpTriedAt("net-a"))
	}
	if !c.warpTriedAt("net-b").IsZero() {
		t.Fatal("отметка одной сети не должна касаться другой")
	}
	c.Close()

	c2 := newCore(t, dir) // ядро пересоздано на той же базе
	defer c2.Close()
	if time.Since(c2.warpTriedAt("net-a")) > time.Minute {
		t.Fatalf("после пересоздания ядра подбор забыт: %v", c2.warpTriedAt("net-a"))
	}
	if v, _, _ := c2.st.Setting(warpTriedKey("net-a")); v == "" || warpTriedKey("net-a") == "warp.tried.net-a" {
		t.Fatalf("идентификатор сети не должен лежать в базе открытым текстом (ключ %q, значение %q)", warpTriedKey("net-a"), v)
	}
}
