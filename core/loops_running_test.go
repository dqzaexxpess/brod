package core

import (
	"reflect"
	"testing"
)

func TestRunningLoopsTracksNamesUntilExit(t *testing.T) {
	c := &Core{}
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	for _, name := range []string{"refresh", "check", "check"} {
		c.goLoop(name, func() { started <- struct{}{}; <-release })
	}
	for i := 0; i < 3; i++ {
		<-started
	}
	if got, want := c.runningLoops(), []string{"check×2", "refresh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("работающие задания: %v, ожидалось %v", got, want)
	}
	close(release)
	c.wg.Wait()
	if got := c.runningLoops(); len(got) != 0 {
		t.Fatalf("после выхода остались задания: %v", got)
	}
}
