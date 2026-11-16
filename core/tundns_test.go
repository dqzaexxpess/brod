package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTunDNSAddr(t *testing.T) {
	for in, want := range map[string]string{"10.77.77.1/30": "10.77.77.2", "172.19.0.1/24": "172.19.0.2"} {
		got, err := tunDNSAddr(in)
		if err != nil || got != want {
			t.Fatalf("%s: %q %v, ждали %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"10.77.77.1", "fd00::1/64", "10.0.0.1/32", "мусор"} {
		if _, err := tunDNSAddr(bad); err == nil {
			t.Fatalf("%q должен быть отклонён", bad)
		}
	}
}

func TestSetSystemDNS(t *testing.T) {
	var calls []string
	ok := func(_ context.Context, a ...string) error { calls = append(calls, strings.Join(a, " ")); return nil }
	if err := setSystemDNS(context.Background(), ok, "vpnclient0", "10.77.77.2"); err != nil {
		t.Fatal(err)
	}
	want := "dns vpnclient0 10.77.77.2|domain vpnclient0 ~.|default-route vpnclient0 yes"
	if strings.Join(calls, "|") != want {
		t.Fatalf("команды: %v", calls)
	}

	calls = nil
	failSecond := func(_ context.Context, a ...string) error {
		calls = append(calls, a[0])
		if a[0] == "domain" {
			return errors.New("access denied")
		}
		return nil
	}
	if err := setSystemDNS(context.Background(), failSecond, "vpnclient0", "10.77.77.2"); err == nil {
		t.Fatal("ждали ошибку")
	}
	if strings.Join(calls, ",") != "dns,domain,revert" {
		t.Fatalf("после сбоя интерфейс должен быть сброшен: %v", calls)
	}
}
