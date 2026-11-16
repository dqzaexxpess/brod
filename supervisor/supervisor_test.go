package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"brod/aggregator"
	"brod/core"
	"brod/internal/testport"
)

type rig struct {
	dir   string
	mu    sync.Mutex
	calls []core.TUNConfig
}

// newCore подменяет core.New: TUN в тестах не поднимаем (нужны права), но запоминаем, что просили.
func (r *rig) newCore(cfg core.Config) (*core.Core, error) {
	r.mu.Lock()
	r.calls = append(r.calls, cfg.TUN)
	r.mu.Unlock()
	cfg.TUN = core.TUNConfig{}
	return core.New(cfg)
}

func (r *rig) cfg() Config {
	return Config{
		Base:    core.Config{StateDir: r.dir, AppPort: testport.Next(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}},
		NewCore: r.newCore,
	}
}

func TestSetRestartsAndRemembersMode(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	s, err := New(context.Background(), r.cfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st := s.State(); st.On || st.Mode != core.ModeBlocked {
		t.Fatalf("старт: %+v", st)
	}
	first := s.Core()

	// режим при выключенном VPN только запоминается, ядро не пересоздаётся
	if st, err := s.Set(nil, core.ModeAll); err != nil || st.On || st.Mode != core.ModeAll {
		t.Fatalf("смена режима: %+v %v", st, err)
	}
	if s.Core() != first || len(r.calls) != 1 {
		t.Fatal("ядро не должно пересоздаваться при смене режима выключенного VPN")
	}

	on := true
	st, err := s.Set(&on, "")
	if err != nil || !st.On || st.Mode != core.ModeAll || st.Interface != core.DefaultTUNInterface() {
		t.Fatalf("включение: %+v %v", st, err)
	}
	if s.Core() == first || len(r.calls) != 2 {
		t.Fatal("ядро должно быть пересоздано")
	}
	if c := r.calls[1]; !c.Enabled || c.Mode != core.ModeAll {
		t.Fatalf("запрошено %+v", c)
	}

	// повторное включение тем же режимом — без пересоздания
	second := s.Core()
	if _, err := s.Set(&on, core.ModeAll); err != nil || s.Core() != second || len(r.calls) != 2 {
		t.Fatalf("повтор: %v, вызовов %d", err, len(r.calls))
	}

	// смена режима при включённом VPN — пересоздание
	if st, err := s.Set(nil, core.ModeAllButRU); err != nil || !st.On || st.Mode != core.ModeAllButRU || len(r.calls) != 3 {
		t.Fatalf("смена режима на ходу: %+v %v (%d)", st, err, len(r.calls))
	}

	off := false
	if st, err := s.Set(&off, ""); err != nil || st.On || st.Interface != "" || r.calls[3].Enabled {
		t.Fatalf("выключение: %+v %v %+v", st, err, r.calls)
	}
	if _, err := s.Set(nil, "nope"); err == nil {
		t.Fatal("неизвестный режим должен отклоняться")
	}
	s.Close()

	// режим запомнен, VPN после перезапуска выключен
	b, _ := os.ReadFile(filepath.Join(r.dir, "vpn.json"))
	if string(b) != `{"mode":"all-but-ru"}` {
		t.Fatalf("vpn.json: %s", b)
	}
	s2, err := New(context.Background(), r.cfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if st := s2.State(); st.On || st.Mode != core.ModeAllButRU {
		t.Fatalf("после перезапуска: %+v", st)
	}
}

func TestEnableFailureKeepsDaemonAlive(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	real := cfg.NewCore
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		if c.TUN.Enabled {
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
	st, err := s.Set(&on, "")
	if err == nil || st.On || st.Error == "" {
		t.Fatalf("ждали отказ, состояние %+v, ошибка %v", st, err)
	}
	if s.Core() == nil {
		t.Fatal("демон должен остаться с работающим ядром без VPN")
	}
	if _, err := s.Core().Status(); err != nil {
		t.Fatalf("ядро не отвечает: %v", err)
	}
}

// Неудачное включение с новым режимом не меняет сохранённый режим.
func TestEnableFailureKeepsPreviousMode(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	real := cfg.NewCore
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		if c.TUN.Enabled {
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
	if _, err := s.Set(&on, core.ModeAll); err == nil {
		t.Fatal("ждали отказ")
	}
	if m := s.State().Mode; m != core.ModeBlocked {
		t.Fatalf("режим после неудачного включения = %q, ждали blocked", m)
	}
	if got := s.load(); got.Mode != core.ModeBlocked {
		t.Fatalf("сохранён режим %q", got.Mode)
	}
}

// Включённый пользователем VPN поднимается сам при следующем старте демона (RestoreOn), выключенный — нет; без RestoreOn (Android)
// сохранённое состояние не включает VPN.
// Автоподключение: «всегда» включает VPN при старте, даже если перед остановкой его выключили; «не включать» не включает, даже если
// он был включён; «как было» (по умолчанию) — по последнему явному выбору. Значение переживает перезапуск.
func TestAutoconnect(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	start := func(restore bool) *Supervisor {
		t.Helper()
		cfg := r.cfg()
		cfg.RestoreOn = restore
		s, err := New(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := start(true)
	if st := s.State(); st.Autoconnect != AutoconnectLast {
		t.Fatalf("по умолчанию «как было»: %+v", st)
	}
	if _, err := s.SetAutoconnect("иногда"); err == nil {
		t.Fatal("неизвестное значение должно отклоняться")
	}
	if _, err := s.SetAutoconnect(AutoconnectAlways); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = start(true)
	if st := s.State(); !st.On || st.Autoconnect != AutoconnectAlways {
		t.Fatalf("«всегда»: VPN включается при старте, хотя его не включали: %+v", st)
	}
	on := true
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAutoconnect(AutoconnectOff); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = start(true)
	if st := s.State(); st.On || st.Autoconnect != AutoconnectOff {
		t.Fatalf("«не включать»: VPN не включается, хотя был включён: %+v", st)
	}
	_ = s.Close()
	s = start(false) // Android: VPN поднимает система — поля нет, менять нельзя
	defer s.Close()
	if st := s.State(); st.Autoconnect != "" || st.On {
		t.Fatalf("без RestoreOn автоподключением демон не управляет: %+v", st)
	}
	if _, err := s.SetAutoconnect(AutoconnectAlways); err == nil {
		t.Fatal("без RestoreOn смена автоподключения должна отклоняться")
	}
}

func TestRestoreOnAfterRestart(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	start := func(restore bool) *Supervisor {
		t.Helper()
		cfg := r.cfg()
		cfg.RestoreOn = restore
		s, err := New(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := start(true)
	if s.State().On {
		t.Fatal("первый запуск: VPN выключен")
	}
	on := true
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // штатная остановка (служба, перезагрузка) намерение не меняет
	s = start(true)
	if st := s.State(); !st.On || st.Mode != core.ModeAll {
		t.Fatalf("после перезапуска VPN должен подняться сам: %+v", st)
	}
	_ = s.Close()
	s = start(false)
	if s.State().On {
		t.Fatal("без RestoreOn сохранённое состояние VPN не включает")
	}
	_ = s.Close()
	s = start(true)
	off := false
	if _, err := s.Set(&off, ""); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = start(true)
	defer s.Close()
	if st := s.State(); st.On || st.Mode != core.ModeAll {
		t.Fatalf("выключение запоминается, режим — тоже: %+v", st)
	}
}

func TestStartOnFailureFallsBack(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	real := cfg.NewCore
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		if c.TUN.Enabled {
			return nil, os.ErrPermission
		}
		return real(c)
	}
	cfg.StartOn, cfg.StartMode = true, core.ModeAll
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st := s.State(); st.On || st.Error == "" || st.Mode != core.ModeAll {
		t.Fatalf("%+v", st)
	}
}

func TestReloadOnlyWhenOn(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	s, err := New(context.Background(), r.cfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := s.Core()
	if _, err := s.Reload(); err != nil || s.Core() != first || len(r.calls) != 1 {
		t.Fatalf("при выключенном VPN перезапуска быть не должно: %v, вызовов %d", err, len(r.calls))
	}
	on := true
	if _, err := s.Set(&on, ""); err != nil {
		t.Fatal(err)
	}
	second := s.Core()
	st, err := s.Reload()
	if err != nil || !st.On || s.Core() == second || len(r.calls) != 3 || !r.calls[2].Enabled {
		t.Fatalf("при включённом VPN ядро пересоздаётся: %+v %v, вызовов %d", st, err, len(r.calls))
	}
}

// смена состояния сообщается сразу (канал Changed), без опроса по таймеру.
func TestChangedFiresOnSet(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	s, err := New(context.Background(), r.cfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch := s.Changed()
	select {
	case <-ch:
		t.Fatal("канал закрылся без смены состояния")
	default:
	}
	on := true
	if _, err := s.Set(&on, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("после Set канал Changed не закрылся")
	}
	if !s.On() {
		t.Fatal("VPN должен быть включён")
	}
}
