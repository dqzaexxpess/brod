package supervisor

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"brod/core"
	"brod/killswitch"
)

// fakeKS записывает вызовы в общий журнал вместе с созданием ядер: по нему проверяется порядок.
type fakeKS struct {
	mu       sync.Mutex
	log      *[]string
	failInst error
	failRem  error
	cfgs     []killswitch.Config
}

func (f *fakeKS) add(s string) { f.mu.Lock(); *f.log = append(*f.log, s); f.mu.Unlock() }
func (f *fakeKS) Install(c killswitch.Config) error {
	f.add("install")
	f.mu.Lock()
	f.cfgs = append(f.cfgs, c)
	f.mu.Unlock()
	return f.failInst
}
func (f *fakeKS) Remove() error { f.add("remove"); return f.failRem }

func (f *fakeKS) seq() string { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(*f.log, " ") }
func (f *fakeKS) reset()      { f.mu.Lock(); *f.log = nil; f.mu.Unlock() }

func ksRig(t *testing.T) (*Supervisor, *fakeKS, *rig) {
	t.Helper()
	t.Setenv("BRODD_KILLSWITCH", "")
	r := &rig{dir: t.TempDir()}
	var lg []string
	ks := &fakeKS{log: &lg}
	cfg := r.cfg()
	real := cfg.NewCore
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		ks.add("core:" + map[bool]string{true: "on", false: "off"}[c.TUN.Enabled])
		return real(c)
	}
	cfg.KS = ks
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if runtime.GOOS == "windows" { // на Windows киллсвитч по умолчанию выключен (включается вручную)
		if err := s.Core().SetSetting(core.SetKillswitch, "1", "test"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ks, r
}

// bindKS — киллсвитч, привязанный к экземпляру адаптера (как WFP с LUID на Windows).
type bindKS struct{ *fakeKS }

func (bindKS) BindsInterface() bool { return true }

// Набор, привязанный к адаптеру, ставится заново после каждого запуска ядра с TUN — и при смене режима, когда правила уже стоят
// (адаптер пересоздан, прежний LUID устарел), иначе каждая смена режима отрезала бы сеть.
func TestKillswitchBoundToInterfaceReinstalledAfterStart(t *testing.T) {
	s, ks, _ := ksRig(t)
	s.cfg.KS = bindKS{ks}
	ks.reset()
	on := true
	if st, err := s.Set(&on, core.ModeAll); err != nil || st.Killswitch != "active" {
		t.Fatalf("включение: %+v %v", st, err)
	}
	if got := ks.seq(); got != "install core:on install" {
		t.Fatalf("включение: до ядра (без TUN) и после (с адаптером): %q", got)
	}
	if n := len(ks.cfgs); n != 2 || ks.cfgs[0].RequireInterface || !ks.cfgs[1].RequireInterface {
		t.Fatalf("после запуска ядра адаптер обязателен, до — нет: %+v", ks.cfgs)
	}
	ks.reset()
	if _, err := s.Set(nil, core.ModeAllButRU); err != nil {
		t.Fatal(err)
	}
	if got := ks.seq(); got != "install core:on install" {
		t.Fatalf("смена режима: правила до остановки старого ядра и ещё раз с новым адаптером: %q", got)
	}
}

// Демон перезапустился (падение, служба) при включённом VPN: правила не снимаются, а заменяются до подъёма TUN — окна без защиты нет.
func TestKillswitchKeptAcrossDaemonRestartWithRestoreOn(t *testing.T) {
	s, ks, r := ksRig(t)
	on := true
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.ks = "" // имитация падения: Close (он снял бы правила) не вызывается, процесс просто исчез
	s.mu.Unlock()
	ks.reset()
	cfg := r.cfg()
	cfg.KS, cfg.RestoreOn = ks, true
	real := cfg.NewCore
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		ks.add("core:" + map[bool]string{true: "on", false: "off"}[c.TUN.Enabled])
		return real(c)
	}
	_ = s.cur.Close() // освободить порты ядра «упавшего» демона
	s2, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := ks.seq(); got != "core:on install" || s2.State().Killswitch != "active" {
		t.Fatalf("после перезапуска правила ставятся до TUN и не снимаются: %q %+v", got, s2.State())
	}
}

func TestKillswitchStartupRemovesStaleRules(t *testing.T) {
	s, ks, _ := ksRig(t)
	if got := ks.seq(); got != "core:off remove" {
		t.Fatalf("при старте без VPN остаток правил должен сниматься: %q", got)
	}
	if st := s.State(); st.Killswitch != "" || st.KillswitchError != "" {
		t.Fatalf("%+v", st)
	}
}

func TestKillswitchOrderAcrossRestarts(t *testing.T) {
	skipWithoutKillswitch(t)
	s, ks, _ := ksRig(t)
	ks.reset()
	on := true

	// выключено → «всё»: правила ставятся до подъёма ядра с TUN
	if st, err := s.Set(&on, core.ModeAll); err != nil || st.Killswitch != "active" {
		t.Fatalf("включение: %+v %v", st, err)
	}
	if got := ks.seq(); got != "install core:on" {
		t.Fatalf("включение: правила ставятся до подъёма ядра, один раз: %q", got)
	}
	if c := ks.cfgs[0]; c.Interface != core.DefaultTUNInterface() || !c.AllowLAN {
		t.Fatalf("параметры правил: %+v", c)
	}

	// смена режима на ходу: правила ставятся ДО остановки старого ядра (нет окна утечки), один раз; снятия нет
	ks.reset()
	if st, err := s.Set(nil, core.ModeAllButRU); err != nil || st.Killswitch != "active" {
		t.Fatalf("смена режима: %+v %v", st, err)
	}
	if got := ks.seq(); got != "install core:on" {
		t.Fatalf("при смене режима правила не должны сниматься, а ставиться до ядра: %q", got)
	}

	// «только заблокированное»: киллсвитч не нужен — правила снимаются, но только когда новое ядро уже работает
	ks.reset()
	if st, err := s.Set(nil, core.ModeBlocked); err != nil || st.Killswitch != "" {
		t.Fatalf("режим blocked: %+v %v", st, err)
	}
	if got := ks.seq(); got != "core:on remove" {
		t.Fatalf("blocked: %q", got)
	}

	// обратно в «всё» и выключение VPN: правила снимаются после запуска ядра без VPN
	if _, err := s.Set(nil, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	ks.reset()
	off := false
	if st, err := s.Set(&off, ""); err != nil || st.Killswitch != "" {
		t.Fatalf("выключение: %+v %v", st, err)
	}
	if got := ks.seq(); got != "core:off remove" {
		t.Fatalf("выключение: %q", got)
	}
}

func TestKillswitchInstallFailureIsReportedButVPNStaysOn(t *testing.T) {
	skipWithoutKillswitch(t)
	s, ks, _ := ksRig(t)
	ks.failInst = errors.New("нет права cap_net_admin")
	on := true
	st, err := s.Set(&on, core.ModeAll)
	if err != nil || !st.On {
		t.Fatalf("VPN должен включиться без защиты: %+v %v", st, err)
	}
	if st.Killswitch != "failed" || !strings.Contains(st.KillswitchError, "cap_net_admin") {
		t.Fatalf("сбой должен быть виден в состоянии: %+v", st)
	}
}

func TestReleaseStaleKillswitch(t *testing.T) {
	skipWithoutKillswitch(t)
	s, ks, _ := ksRig(t)
	on := true
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	ks.reset()
	if err := s.ReleaseStaleKillswitch(); !errors.Is(err, core.ErrConflict) || ks.seq() != "" || s.State().Killswitch != "active" {
		t.Fatalf("при включённом VPN правила нужны: %v %q %+v", err, ks.seq(), s.State())
	}
	off := false
	if _, err := s.Set(&off, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	ks.reset()
	if err := s.ReleaseStaleKillswitch(); err != nil || ks.seq() != "remove" {
		t.Fatalf("при выключенном VPN правила снимаются: %v %q", err, ks.seq())
	}
}

func TestKillswitchDisabledBySetting(t *testing.T) {
	skipWithoutKillswitch(t)
	s, ks, _ := ksRig(t)
	on := true
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.Killswitch != "active" {
		t.Fatalf("по умолчанию включён: %+v", st)
	}
	// выключили в настройках при работающем VPN — правила снимаются сразу, без пересоздания ядра
	core1 := s.Core()
	if err := s.Core().SetSetting(core.SetKillswitch, "0", "test"); err != nil {
		t.Fatal(err)
	}
	ks.reset()
	if st := s.RefreshKillswitch(); st.Killswitch != "" || ks.seq() != "remove" || s.Core() != core1 {
		t.Fatalf("после выключения: %+v %q", st, ks.seq())
	}
	// и обратно
	if err := s.Core().SetSetting(core.SetKillswitch, "1", "test"); err != nil {
		t.Fatal(err)
	}
	ks.reset()
	if st := s.RefreshKillswitch(); st.Killswitch != "active" || ks.seq() != "install" {
		t.Fatalf("после включения: %+v %q", st, ks.seq())
	}
	// с выключенной настройкой новое включение правил не ставит
	_ = s.Core().SetSetting(core.SetKillswitch, "0", "test")
	off := false
	_, _ = s.Set(&off, "")
	ks.reset()
	if _, err := s.Set(&on, ""); err != nil || strings.Contains(ks.seq(), "install") {
		t.Fatalf("при выключенной настройке ставить нечего: %q %v", ks.seq(), err)
	}
}

func TestKillswitchEnvOverride(t *testing.T) {
	t.Setenv("BRODD_KILLSWITCH", "off")
	r := &rig{dir: t.TempDir()}
	var lg []string
	ks := &fakeKS{log: &lg}
	cfg := r.cfg()
	cfg.KS = ks
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := true
	if st, err := s.Set(&on, core.ModeAll); err != nil || st.Killswitch != "" || strings.Contains(ks.seq(), "install") {
		t.Fatalf("BRODD_KILLSWITCH=off должен выключать: %+v %v %q", st, err, ks.seq())
	}
}

func TestKillswitchHeldOnFailedEnableRemovedOnOffAndOnClose(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	t.Setenv("BRODD_KILLSWITCH", "")
	var lg []string
	ks := &fakeKS{log: &lg}
	cfg := r.cfg()
	real := cfg.NewCore
	cfg.NewCore = func(c core.Config) (*core.Core, error) {
		if c.TUN.Enabled && c.TUN.Mode == core.ModeAllButRU {
			return nil, os.ErrPermission
		}
		return real(c)
	}
	cfg.KS = ks
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	// пересоздание не удалось (новое ядро не создалось) → демон вернулся без VPN, но правила остаются: иначе весь трафик
	// ушёл бы напрямую именно тогда, когда туннель сломался
	ks.reset()
	st, err := s.Set(nil, core.ModeAllButRU)
	if err == nil || st.On {
		t.Fatalf("ждали отказ: %+v %v", st, err)
	}
	if got := ks.seq(); strings.Contains(got, "remove") {
		t.Fatalf("после неудачного включения правила не должны сниматься: %q", got)
	}
	if st.Killswitch != "active" || st.Error == "" {
		t.Fatalf("состояние должно показывать блокировку и причину: %+v", st)
	}
	// пользователь выключает VPN → правила снимаются
	off := false
	ks.reset()
	if _, err := s.Set(&off, ""); err != nil {
		t.Fatal(err)
	}
	if got := ks.seq(); !strings.HasSuffix(got, "remove") {
		t.Fatalf("выключение VPN должно снимать правила: %q", got)
	}
	// штатное закрытие демона снимает правила
	if _, err := s.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	ks.reset()
	_ = s.Close()
	if got := ks.seq(); got != "remove" {
		t.Fatalf("Close: %q", got)
	}
}

func TestKillswitchNotUsedWithoutImplementation(t *testing.T) {
	t.Setenv("BRODD_KILLSWITCH", "")
	r := &rig{dir: t.TempDir()}
	s, err := New(context.Background(), r.cfg()) // KS == nil
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := true
	if st, err := s.Set(&on, core.ModeAll); err != nil || st.Killswitch != "" {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestStateReportsForeignTunnels(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	var seen string
	cfg.Tunnels = func(ours string) []string { seen = ours; return []string{"throne-tun"} }
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st := s.State()
	if len(st.Conflict) != 1 || st.Conflict[0] != "throne-tun" || seen != core.DefaultTUNInterface() {
		t.Fatalf("конфликт туннелей должен быть виден в состоянии (свой интерфейс исключается): %+v, ours=%q", st, seen)
	}
	cfg2 := r.cfg()
	cfg2.Tunnels = func(string) []string { return nil }
	s2, err := New(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if st := s2.State(); len(st.Conflict) != 0 {
		t.Fatalf("без чужих туннелей конфликта нет: %+v", st)
	}
}

// skipWithoutKillswitch — киллсвитч реализован только на Linux: на других системах ядро его не включает (KillswitchEnabled = false).
func skipWithoutKillswitch(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("киллсвитч реализован только на Linux")
	}
}

// Чужой туннель: включить VPN нельзя (ни явно, ни при запуске демона), выключенное состояние и сохранённый выбор не меняются;
// после исчезновения туннеля включение проходит.
func TestSetRefusesWhileForeignTunnelActive(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	foreign := []string{"throne-tun"}
	cfg.Tunnels = func(string) []string { return foreign }
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := true
	st, err := s.Set(&on, core.ModeAll)
	if !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "throne-tun") {
		t.Fatalf("ожидался отказ из-за чужого туннеля, получено: %v", err)
	}
	if st.On || s.savedOn() {
		t.Fatalf("после отказа VPN не должен быть ни включён, ни помечен «включить при запуске»: %+v", st)
	}
	foreign = nil
	if st, err := s.Set(&on, core.ModeAll); err != nil || !st.On {
		t.Fatalf("без чужого туннеля включение должно пройти: %+v, %v", st, err)
	}
	// выключение при чужом туннеле разрешено
	foreign = []string{"throne-tun"}
	off := false
	if st, err := s.Set(&off, ""); err != nil || st.On {
		t.Fatalf("выключение: %+v, %v", st, err)
	}
}

func TestStartOnSkippedWhileForeignTunnelActive(t *testing.T) {
	r := &rig{dir: t.TempDir()}
	cfg := r.cfg()
	cfg.StartOn = true
	cfg.Tunnels = func(string) []string { return []string{"throne-tun"} }
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st := s.State(); st.On || !strings.Contains(st.Error, "throne-tun") {
		t.Fatalf("при запуске с чужим туннелем VPN не включается, причина в состоянии: %+v", st)
	}
}

// Случай «демон упал и перезапущен»: правила прежнего процесса ещё стоят (признак на месте), а включить VPN не вышло (новое ядро не
// создалось). Правила не снимаются: иначе весь трафик ушёл бы напрямую именно тогда, когда туннель сломан. Явное «выключить» их снимает.
func TestKillswitchHeldWhenRestartedDaemonCannotEnable(t *testing.T) {
	for _, stale := range []bool{true, false} {
		r := &rig{dir: t.TempDir()}
		t.Setenv("BRODD_KILLSWITCH", "")
		var lg []string
		ks := &fakeKS{log: &lg}
		cfg := r.cfg()
		real := cfg.NewCore
		cfg.NewCore = func(c core.Config) (*core.Core, error) {
			if c.TUN.Enabled {
				return nil, os.ErrPermission
			}
			return real(c)
		}
		cfg.KS = ks
		cfg.KSStale = func() bool { return stale }
		cfg.StartOn, cfg.StartMode = true, core.ModeAll
		s, err := New(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		st := s.State()
		if stale {
			if st.On || st.Killswitch != "active" || st.Error == "" {
				t.Fatalf("прежние правила должны остаться и быть видны в состоянии: %+v", st)
			}
			if got := ks.seq(); strings.Contains(got, "remove") {
				t.Fatalf("правила прежнего процесса сняты после неудачного включения: %q", got)
			}
			off := false
			ks.reset()
			if _, err := s.Set(&off, ""); err != nil {
				t.Fatal(err)
			}
			if got := ks.seq(); !strings.HasSuffix(got, "remove") {
				t.Fatalf("явное «выключить» должно снимать правила: %q", got)
			}
		} else if got := ks.seq(); !strings.Contains(got, "remove") {
			// правил прежнего процесса не было: после неудачного включения нечего держать, остаток снимается как раньше
			t.Fatalf("без признака правил ждали штатную очистку: %q", got)
		}
		_ = s.Close()
	}
}
