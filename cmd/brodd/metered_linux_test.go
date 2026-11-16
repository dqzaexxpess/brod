package main

import "testing"

func TestParseMetered(t *testing.T) {
	for out, want := range map[string]bool{"yes\n": true, "yes (guessed)\n": true, "no\n": false, "no (guessed)\n": false, "unknown\n": false, "": false} {
		if got := parseMetered(out); got != want {
			t.Errorf("%q: %v, ожидалось %v", out, got, want)
		}
	}
}
