package supervisor

import (
	"context"
	"os"
	"testing"

	"brod/core"
)

// T-8: Restart пересоздаёт ядро с прежними VPN и режимом, в том числе при выключенном VPN (аккаунт WARP читается при создании ядра).
func TestRestartKeepsStateAndRecreatesCore(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	s, err := New(context.Background(), r.cfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Set(nil, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	first := s.Core()
	st, err := s.Restart() // VPN выключен: ядро всё равно пересоздаётся, VPN остаётся выключенным
	if err != nil || st.On || st.Mode != core.ModeAll {
		t.Fatalf("Restart при выключенном VPN: %+v %v", st, err)
	}
	if s.Core() == first || len(r.calls) != 2 || r.calls[1].Enabled {
		t.Fatalf("ядро не пересоздано без VPN: вызовов %d, %+v", len(r.calls), r.calls)
	}
	if _, err := s.Core().Status(); err != nil {
		t.Fatalf("новое ядро не отвечает: %v", err)
	}

	on := true
	if _, err := s.Set(&on, ""); err != nil {
		t.Fatal(err)
	}
	second := s.Core()
	n := len(r.calls)
	st, err = s.Restart() // VPN включён: прежние включённость и режим
	if err != nil || !st.On || st.Mode != core.ModeAll {
		t.Fatalf("Restart при включённом VPN: %+v %v", st, err)
	}
	if s.Core() == second || len(r.calls) != n+1 || !r.calls[n].Enabled || r.calls[n].Mode != core.ModeAll {
		t.Fatalf("ядро не пересоздано с VPN: %+v", r.calls)
	}
}

// Не получилось поднять ядро с VPN при Restart: демон остаётся с ядром без VPN, причина в состоянии и в ошибке.
func TestRestartFailureFallsBackToNoVPN(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	real := cfg.NewCore
	fail := false
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		if fail && c.TUN.Enabled {
			return nil, os.ErrPermission
		}
		return real(c)
	}
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := true
	if _, err := s.Set(&on, ""); err != nil {
		t.Fatal(err)
	}
	fail = true
	st, err := s.Restart()
	if err == nil || st.On || st.Error == "" {
		t.Fatalf("ждали отказ: %+v %v", st, err)
	}
	if s.Core() == nil {
		t.Fatal("демон должен остаться с ядром без VPN")
	}
	if _, err := s.Core().Status(); err != nil {
		t.Fatalf("ядро не отвечает: %v", err)
	}
}
