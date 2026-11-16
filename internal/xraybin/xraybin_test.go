package xraybin

import "testing"

func TestCIStrict(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true} {
		t.Setenv("CI", v)
		if got := CIStrict(); got != want {
			t.Errorf("CI=%q: %v, ожидалось %v", v, got, want)
		}
	}
}
