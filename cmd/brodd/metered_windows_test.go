package main

import (
	"encoding/base64"
	"testing"
	"unicode/utf16"
)

func TestParseCost(t *testing.T) {
	for out, want := range map[string]bool{
		"Unrestricted False False\r\n": false,
		"Unknown False False":          false,
		"":                             false,
		"Fixed False False":            true,
		"Variable False False":         true,
		"Unrestricted True False":      true, // роуминг
		"Unrestricted False True":      true, // лимит превышен
	} {
		if got := parseCost(out); got != want {
			t.Errorf("%q: %v, ожидали %v", out, got, want)
		}
	}
}

func TestEncodePS(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(encodePS("'тест' $x"))
	if err != nil || len(b)%2 != 0 {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	if s := string(utf16.Decode(u)); s != "'тест' $x" {
		t.Fatalf("%q", s)
	}
}

// Живой запрос: на этом ПК должен отработать без ошибки за отведённое время (значение зависит от сети).
func TestWinMeteredRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("запуск PowerShell")
	}
	t.Logf("платная сеть: %v", winMetered())
}
