// Package supervisor — переключатель VPN (TUN) поверх ядра.
//
// Ядро строит движок один раз при создании, а TUN-вход в него входит или не входит. Поэтому «включить/выключить VPN» и «сменить режим»
// — это пересоздание ядра целиком (как перезапуск у Throne): состояние (узлы, выбор, цели, события) лежит в базе и подхватывается заново,
// активные соединения рвутся на несколько секунд. Режим запоминается в vpn.json в каталоге состояния; включённость — нет:
// демон стартует с выключенным VPN, если явно не просили иного (флаг --tun), чтобы маршруты не менялись без команды.
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brod/core"
	"brod/killswitch"
	"brod/store"
)

// EvVPN — событие переключения VPN.
const EvVPN = "vpn"

// State — состояние переключателя.
type State struct {
	On        bool   `json:"on"`
	Mode      string `json:"mode"`                // blocked | all | all-but-ru — выбранный режим (запоминается)
	Interface string `json:"interface,omitempty"` // имя TUN-интерфейса, когда VPN включён
	Error     string `json:"error,omitempty"`     // почему последняя попытка включить не удалась
	// Killswitch: "active" — правила файрвола стоят (при падении демона трафик заблокируется, а не утечёт); "failed" — нужны, но
	// поставить/снять не вышло (KillswitchError), VPN при этом работает без защиты от падения демона; пусто — не нужен (VPN выключен,
	// режим «только заблокированное» или выключен в настройках).
	Killswitch      string `json:"killswitch,omitempty"`
	KillswitchError string `json:"killswitch_error,omitempty"`
	// Conflict — активные чужие туннели (например, throne-tun от параллельно запущенного Throne): с ними Brod, как правило, не подключается.
	Conflict []string `json:"conflict,omitempty"`
	// Autoconnect — включать ли VPN при старте демона (после перезагрузки, падения, перезапуска службы): last | always | off.
	// Пусто — демон этим не управляет (Android: VPN поднимает система).
	Autoconnect string `json:"autoconnect,omitempty"`
}

// Значения автоподключения.
const (
	AutoconnectLast   = "last"   // как было: VPN, включённый пользователем, включается снова (по умолчанию)
	AutoconnectAlways = "always" // всегда включать
	AutoconnectOff    = "off"    // не включать
)

// ValidAutoconnect сообщает, что значение автоподключения известно.
func ValidAutoconnect(v string) bool {
	return v == AutoconnectLast || v == AutoconnectAlways || v == AutoconnectOff
}

// Killswitch — файрвольная защита (пакет killswitch); в тестах подменяется. nil — выключена совсем.
type Killswitch interface {
	Install(killswitch.Config) error
	Remove() error
}

// interfaceBinder — киллсвитч, чьи правила ссылаются на конкретный экземпляр TUN-адаптера, а не на имя (WFP: LUID). Такой набор ставится
// ещё раз после запуска ядра, когда адаптер уже есть.
type interfaceBinder interface{ BindsInterface() bool }

func ksBindsInterface(k Killswitch) bool {
	b, ok := k.(interfaceBinder)
	return ok && b.BindsInterface()
}

// ksApplies: киллсвитч имеет смысл, когда весь трафик должен идти через туннель. В режиме «только заблокированное» большая часть
// трафика намеренно идёт напрямую, и блокировка всего при падении демона хуже утечки.
func ksApplies(on bool, mode string) bool {
	return on && (mode == core.ModeAll || mode == core.ModeAllButRU)
}

// Config — настройки супервизора.
type Config struct {
	Base core.Config    // всё, кроме TUN; Base.StateDir обязателен
	TUN  core.TUNConfig // шаблон: Interface, AllowIPv6, NoStrict, NoAutoRoute… Enabled и Mode задаёт супервизор
	// StartOn — запустить сразу с включённым VPN (флаг --tun). StartMode — режим из командной строки ("" — взять сохранённый).
	StartOn bool
	// RestoreOn — при старте включить VPN, если он был включён пользователем (сохраняется в vpn.json): после падения, перезапуска службы
	// или перезагрузки туннель поднимается сам, и киллсвитч не снимает правила, пока VPN по-прежнему нужен. Выключение VPN тоже
	// запоминается. На Android VPN поднимает система (VpnService) — там false.
	RestoreOn bool
	StartMode string
	// OnCore вызывается для каждого нового ядра; ctx отменяется, когда ядро заменяют или демон останавливается (презентер уведомлений и т. п.).
	OnCore func(ctx context.Context, c *core.Core)
	// NewCore подменяется в тестах.
	NewCore func(core.Config) (*core.Core, error)
	// KS — киллсвитч (nil — не используется).
	KS Killswitch
	// KSStale сообщает, что правила киллсвитча уже стоят (поставил прежний процесс: демон упал, правила остались). Нужен для этого случая:
	// если при перезапуске включить VPN не вышло, эти правила остаются, а не снимаются. nil — признака нет.
	KSStale func() bool
	// Tunnels подменяется в тестах: список чужих туннельных интерфейсов (по умолчанию — чтение /sys/class/net).
	Tunnels func(ours string) []string
}

type saved struct {
	Mode string `json:"mode"`
	// On — последнее явное «включить/выключить VPN» пользователя: с ним демон поднимается после перезапуска (Config.RestoreOn).
	On bool `json:"on,omitempty"`
	// Autoconnect — last | always | off; пусто = last.
	Autoconnect string `json:"autoconnect,omitempty"`
}

// Supervisor владеет текущим ядром.
type Supervisor struct {
	cfg Config
	ctx context.Context

	setMu  sync.Mutex // одно переключение за раз
	mu     sync.RWMutex
	cur    *core.Core
	cancel context.CancelFunc
	on     bool
	wantOn bool   // последнее явное включение/выключение (сохраняется)
	auto   string // автоподключение (сохраняется): last | always | off
	mode   string
	err    string
	ks     string // "" | active | failed
	keepKS bool   // только под setMu: откат на start(false) после неудачного включения не снимает правила киллсвитча
	ksErr  string
	chg    chan struct{} // закрывается и заменяется при каждой смене ядра/состояния (см. Changed)
}

// New читает сохранённый режим и поднимает ядро.
func New(ctx context.Context, cfg Config) (*Supervisor, error) {
	if cfg.NewCore == nil {
		cfg.NewCore = core.New
	}
	s := &Supervisor{cfg: cfg, ctx: ctx, mode: core.ModeBlocked}
	sv := s.load()
	if sv.Mode != "" && core.ValidMode(sv.Mode) {
		s.mode = sv.Mode
	}
	s.wantOn = sv.On
	s.auto = AutoconnectLast
	if ValidAutoconnect(sv.Autoconnect) {
		s.auto = sv.Autoconnect
	}
	startOn := cfg.StartOn
	if cfg.RestoreOn && !startOn {
		switch {
		case s.auto == AutoconnectAlways:
			startOn = true
			log.Printf("автоподключение «всегда»: включаю VPN (режим %s)", s.mode)
		case s.auto == AutoconnectLast && sv.On:
			startOn = true
			log.Printf("VPN был включён до остановки демона — включаю снова (режим %s)", s.mode)
		}
	}
	if startOn {
		if err := s.errForeign(); err != nil { // при запуске демона тоже: чужой туннель не трогаем, причина видна в состоянии
			startOn = false
			s.err = err.Error()
			log.Printf("VPN не включён: %v", err)
		}
	}
	if cfg.StartMode != "" {
		if !core.ValidMode(cfg.StartMode) {
			return nil, fmt.Errorf("неизвестный режим TUN %q (blocked | all | all-but-ru)", cfg.StartMode)
		}
		s.mode = cfg.StartMode
		s.save()
	}
	if err := s.start(startOn); err != nil {
		if !startOn {
			s.ksRemove() // демон не поднялся: оставленные правила заперли бы сеть
			return nil, err
		}
		// включить не вышло (нет прав и т. п.) — демон всё равно поднимаем, без VPN, причину видно в состоянии и в журнале
		s.err = err.Error()
		s.keepKS = s.ksHeldAfterFailedEnable() || s.adoptStaleKS(startOn)
		err2 := s.start(false)
		s.keepKS = false
		if err2 != nil {
			s.ksRemove()
			return nil, err2
		}
		s.publish(failedEnableMessage(err, s.State().Killswitch == "active"))
	}
	return s, nil
}

func (s *Supervisor) path() string { return filepath.Join(s.cfg.Base.StateDir, "vpn.json") }

func (s *Supervisor) load() saved {
	var v saved
	if b, err := os.ReadFile(s.path()); err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v
}

func (s *Supervisor) save() {
	s.mu.RLock()
	v := saved{Mode: s.mode, On: s.wantOn}
	if s.auto != AutoconnectLast {
		v.Autoconnect = s.auto
	}
	s.mu.RUnlock()
	b, _ := json.Marshal(v)
	tmp := s.path() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path())
	}
}

// start создаёт и запускает ядро с текущим режимом; on — с TUN. Вызывать под setMu (или из New).
func (s *Supervisor) start(on bool) error {
	cc := s.cfg.Base
	cc.TUN = s.cfg.TUN
	cc.TUN.Enabled = on
	cc.TUN.Mode = s.mode
	if cc.OnWarpProfile == nil {
		cc.OnWarpProfile = func() { _, _ = s.Restart() } // подбор нашёл профиль WARP под сеть: параметры читаются при создании ядра
	}
	c, err := s.cfg.NewCore(cc)
	if err != nil {
		return err
	}
	// правила ставятся до подъёма TUN (и остаются на месте при пересоздании ядра: окна утечки нет); снимаются только когда новое
	// ядро уже работает без нужды в них. При неудачном запуске правила остаются — вызывающий откатится на start(false), и тот снимет.
	wantKS := s.ksWanted(on, c)
	var ksFail error
	if wantKS {
		s.mu.RLock()
		installed := s.ks == "active" // при перезапуске правила уже поставлены до остановки старого ядра
		s.mu.RUnlock()
		if !installed {
			ksFail = s.ksInstall()
		}
	}
	cctx, cancel := context.WithCancel(s.ctx)
	if err := c.Start(cctx); err != nil {
		cancel()
		_ = c.Close()
		return err
	}
	if wantKS && ksFail == nil && ksBindsInterface(s.cfg.KS) {
		// набор привязан к экземпляру адаптера (Windows: LUID), а TUN появился только сейчас — ставим заново всегда, в том числе когда
		// правила уже стояли до перезапуска ядра (адаптер пересоздан, прежний LUID мог устареть)
		ksFail = s.ksInstallWith(true)
	}
	s.mu.Lock()
	s.cur, s.cancel, s.on = c, cancel, on
	s.notifyLocked()
	s.mu.Unlock()
	if s.cfg.OnCore != nil {
		s.cfg.OnCore(cctx, c)
	}
	if !wantKS {
		if !s.keepKS { // включить не вышло при включённом киллсвитче — правила остаются, трафик не уходит напрямую
			s.ksRemove()
		}
	} else if ksFail != nil {
		s.publish("киллсвитч не включился: " + ksFail.Error())
	}
	return nil
}

func (s *Supervisor) ksIface() string {
	if s.cfg.TUN.Interface != "" {
		return s.cfg.TUN.Interface
	}
	return core.DefaultTUNInterface()
}

// ksHeldAfterFailedEnable: правила киллсвитча уже стоят (их ставят до запуска ядра, только если он включён в настройках и нужен
// в этом режиме), а включить VPN не вышло. Снимать их нельзя: демон остался без туннеля, и весь трафик ушёл бы напрямую именно
// тогда, когда защита нужна. Пользователь выключает VPN или нажимает «Снять блокировку» (ReleaseStaleKillswitch).
func (s *Supervisor) ksHeldAfterFailedEnable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.KS != nil && s.ks == "active" && ksApplies(true, s.mode)
}

// adoptStaleKS (демон упал и перезапущен): правила поставил прежний процесс и они ещё стоят, а включить VPN не вышло. Новый процесс
// о них не знает (ksInstall вызывается только после создания ядра), и откат на start(false) снял бы их: трафик ушёл бы напрямую именно
// тогда, когда туннель сломан. Принимаем правила как свои: состояние «активно», снять их можно выключением VPN или «Снять блокировку».
func (s *Supervisor) adoptStaleKS(wantedOn bool) bool {
	if !wantedOn || s.cfg.KS == nil || s.cfg.KSStale == nil || !s.cfg.KSStale() || !ksApplies(true, s.mode) {
		return false
	}
	s.mu.Lock()
	s.ks, s.ksErr = "active", ""
	s.mu.Unlock()
	log.Printf("киллсвитч: правила прежнего процесса остаются, VPN не включён")
	return true
}

func (s *Supervisor) ksHeld() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ks == "active"
}

func failedEnableMessage(err error, held bool) string {
	if held {
		return "не удалось включить VPN: " + err.Error() + "; киллсвитч держит сеть заблокированной (выключите VPN или нажмите «Снять блокировку»)"
	}
	return "не удалось включить VPN: " + err.Error()
}

// ksWanted — нужны ли правила при таком состоянии (c — ядро, из настроек которого читается переключатель).
func (s *Supervisor) ksWanted(on bool, c *core.Core) bool {
	s.mu.RLock()
	mode := s.mode
	s.mu.RUnlock()
	return s.cfg.KS != nil && c != nil && ksApplies(on, mode) && c.KillswitchEnabled()
}

// ksInstall ставит (заменяет) правила и запоминает результат в состоянии.
func (s *Supervisor) ksInstall() error { return s.ksInstallWith(false) }

// ksInstallWith: requireIface — набор после запуска ядра, интерфейс TUN уже должен быть.
func (s *Supervisor) ksInstallWith(requireIface bool) error {
	err := s.cfg.KS.Install(killswitch.Config{Interface: s.ksIface(), AllowLAN: true, RequireInterface: requireIface})
	s.mu.Lock()
	if err != nil {
		s.ks, s.ksErr = "failed", err.Error()
	} else {
		s.ks, s.ksErr = "active", ""
	}
	s.mu.Unlock()
	if err != nil {
		log.Printf("киллсвитч: %v", err)
	}
	return err
}

// ksRemove снимает правила (если их не было — ничего не происходит).
func (s *Supervisor) ksRemove() {
	if s.cfg.KS == nil {
		return
	}
	err := s.cfg.KS.Remove()
	s.mu.Lock()
	if err != nil {
		s.ks, s.ksErr = "failed", err.Error()
	} else {
		s.ks, s.ksErr = "", ""
	}
	s.mu.Unlock()
	if err != nil {
		log.Printf("киллсвитч: %v", err)
	}
}

// RefreshKillswitch применяет изменение настройки киллсвитча без пересоздания ядра: включённый VPN получает правила или теряет их.
func (s *Supervisor) RefreshKillswitch() State {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.mu.RLock()
	on, c := s.on, s.cur
	s.mu.RUnlock()
	if s.ksWanted(on, c) {
		if err := s.ksInstall(); err != nil {
			s.publish("киллсвитч не включился: " + err.Error())
		}
	} else {
		s.ksRemove()
	}
	return s.State()
}

// ReleaseStaleKillswitch снимает правила, оставшиеся после упавшего демона, по просьбе пользователя (кнопка «Снять блокировку» без прав
// администратора: на Windows демон — служба с правом на WFP, приложение — нет). Пока VPN включён, правила нужны, и снятие отклоняется.
func (s *Supervisor) ReleaseStaleKillswitch() error {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.mu.RLock()
	on := s.on
	s.mu.RUnlock()
	if on {
		return fmt.Errorf("%w: VPN включён, правила киллсвитча нужны; выключите VPN или киллсвитч в настройках", core.ErrConflict)
	}
	s.ksRemove()
	if st := s.State(); st.KillswitchError != "" {
		return fmt.Errorf("не удалось снять правила: %s", st.KillswitchError)
	}
	return nil
}

// ReleaseKillswitch снимает правила принудительно (выход демона, когда остановка затянулась).
func (s *Supervisor) ReleaseKillswitch() { s.ksRemove() }

func (s *Supervisor) publish(detail string) {
	log.Printf("VPN: %s", detail) // смены состояния попадают в журнал демона всегда (на Windows у демона нет консоли)
	if c := s.Core(); c != nil {
		c.Bus().Publish(store.Event{Kind: EvVPN, Initiator: "user", Detail: detail})
	}
}

// Changed возвращает канал, который закроется при следующей смене ядра или состояния VPN (после закрытия снова вызвать Changed).
// Замена опроса On() по таймеру: потребитель узнаёт о выключении сразу, а не через период опроса.
func (s *Supervisor) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chg == nil {
		s.chg = make(chan struct{})
	}
	return s.chg
}

// notifyLocked будит всех, кто ждёт на Changed. Вызывать под s.mu.
func (s *Supervisor) notifyLocked() {
	if s.chg != nil {
		close(s.chg)
		s.chg = nil
	}
}

// Core — текущее ядро (после остановки — последнее закрытое).
func (s *Supervisor) Core() *core.Core {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// On сообщает, включён ли VPN: дешёвый опрос без чтения списка интерфейсов (State сканирует /sys/class/net в поисках чужих туннелей).
func (s *Supervisor) On() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.on
}

// Mode возвращает выбранный режим.
func (s *Supervisor) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// foreign возвращает активные чужие туннели (Throne и подобные): с ними Brod не включается.
func (s *Supervisor) foreign() []string {
	tunnels := s.cfg.Tunnels
	if tunnels == nil {
		tunnels = foreignTunnels
	}
	return tunnels(s.ksIface())
}

// errForeign — текст отказа при чужом туннеле; nil, если чужих туннелей нет.
func (s *Supervisor) errForeign() error {
	c := s.foreign()
	if len(c) == 0 {
		return nil
	}
	return fmt.Errorf("%w (%s): выключите его и включите Brod снова, вместе они не работают", core.ErrConflict, strings.Join(c, ", "))
}

// State — текущее состояние.
func (s *Supervisor) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := State{On: s.on, Mode: s.mode, Error: s.err, Killswitch: s.ks, KillswitchError: s.ksErr}
	if s.cfg.RestoreOn {
		st.Autoconnect = s.auto
	}
	st.Conflict = s.foreign()
	if s.on {
		st.Interface = s.cfg.TUN.Interface
		if st.Interface == "" {
			st.Interface = core.DefaultTUNInterface()
		}
	}
	return st
}

// stopCurrent останавливает текущее ядро и его побочные задания.
func (s *Supervisor) stopCurrent() error {
	s.mu.RLock()
	c, cancel := s.cur, s.cancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		return c.Close()
	}
	return nil
}

// Set меняет состояние: on == nil — не менять включённость; mode == "" — не менять режим. Режим запоминается, кроме случая, когда включить с ним не вышло.
// Если VPN включён и что-то поменялось, ядро пересоздаётся. Если включить не удалось, остаётся выключенный VPN с работающим демоном.
func (s *Supervisor) Set(on *bool, mode string) (State, error) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	if mode != "" && !core.ValidMode(mode) {
		return s.State(), fmt.Errorf("неизвестный режим %q (blocked | all | all-but-ru)", mode)
	}
	s.mu.RLock()
	wasOn, wasMode := s.on, s.mode
	s.mu.RUnlock()
	wantOn, wantMode := wasOn, wasMode
	if on != nil {
		wantOn = *on
	}
	if mode != "" {
		wantMode = mode
	}
	if wantOn && !wasOn { // чужой туннель: не включаем (ни явно, ни как «включить при следующем запуске»)
		if err := s.errForeign(); err != nil {
			return s.State(), err
		}
	}
	if wantMode != wasMode || (on != nil && *on != s.savedOn()) {
		s.mu.Lock()
		s.mode = wantMode
		if on != nil {
			s.wantOn = *on
		}
		s.mu.Unlock()
		s.save()
	}
	if on != nil && !*on && !wasOn && s.ksHeld() { // явное «выключить» в состоянии «заблокировано, VPN не поднялся» снимает правила
		s.ksRemove()
	}
	if wantOn == wasOn && (!wantOn || wantMode == wasMode) {
		return s.State(), nil // ничего не изменилось (режим при выключенном VPN только запомнен)
	}
	st, err := s.restartLocked(wantOn)
	if err != nil && wantOn && wantMode != wasMode {
		// включить с новым режимом не вышло: прежний режим остаётся сохранённым, а не тот, с которым туннель так и не поднялся
		s.mu.Lock()
		s.mode = wasMode
		s.mu.Unlock()
		s.save()
		st = s.State()
	}
	return st, err
}

func (s *Supervisor) savedOn() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wantOn
}

// SetAutoconnect меняет автоподключение (действует со следующего старта демона; текущее состояние VPN не меняется).
func (s *Supervisor) SetAutoconnect(v string) (State, error) {
	if !ValidAutoconnect(v) {
		return s.State(), fmt.Errorf("неизвестное автоподключение %q (last | always | off)", v)
	}
	if !s.cfg.RestoreOn {
		return s.State(), fmt.Errorf("на этой платформе VPN при запуске включает система, а не демон")
	}
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.mu.Lock()
	s.auto = v
	s.mu.Unlock()
	s.save()
	return s.State(), nil
}

// restartLocked пересоздаёт ядро с VPN on/off. Вызывать под setMu. Не получилось включить — демон остаётся без VPN, причина в состоянии.
func (s *Supervisor) restartLocked(wantOn bool) (State, error) {
	t0 := time.Now()
	if s.ksWanted(wantOn, s.Core()) { // до остановки старого ядра: пока TUN пересоздаётся, трафик не уходит напрямую
		_ = s.ksInstall()
	}
	tKS := time.Since(t0)
	t1 := time.Now()
	_ = s.stopCurrent() // ошибка закрытия не мешает поднять новое ядро: порты и интерфейс всё равно освобождены
	tStop := time.Since(t1)
	s.mu.Lock()
	s.err = ""
	wantMode := s.mode
	s.mu.Unlock()
	t2 := time.Now()
	err := s.start(wantOn)
	// этапы переключения всегда пишутся в журнал: на жалобу «долго включается и выключается» нужна разбивка, а не общее впечатление
	defer func() {
		log.Printf("переключение VPN (включить=%v): правила %s, остановка старого ядра %s, запуск нового %s, всего %s",
			wantOn, tKS.Round(time.Millisecond), tStop.Round(time.Millisecond), time.Since(t2).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))
	}()
	if err != nil {
		s.mu.Lock()
		s.err = err.Error()
		s.mu.Unlock()
		s.keepKS = wantOn && s.ksHeldAfterFailedEnable()
		err2 := s.start(false)
		s.keepKS = false
		if err2 != nil {
			s.ksRemove()
			return s.State(), fmt.Errorf("%v; не удалось вернуть демон без VPN: %w", err, err2)
		}
		s.publish(failedEnableMessage(err, s.State().Killswitch == "active"))
		return s.State(), err
	}
	if wantOn {
		s.publish("VPN включён, режим " + wantMode)
	} else {
		s.publish("VPN выключен")
	}
	return s.State(), nil
}

// Reload применяет изменившиеся настройки движка (правила маршрутизации): если VPN включён — пересоздаёт ядро, иначе ничего не делает
// (правила читаются при следующем включении).
func (s *Supervisor) Reload() (State, error) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.mu.RLock()
	on := s.on
	s.mu.RUnlock()
	if !on {
		return s.State(), nil
	}
	return s.restartLocked(true)
}

// Restart пересоздаёт ядро с прежними VPN и режимом, даже когда VPN выключен: нужно, когда меняется то, что ядро читает при создании
// (аккаунт WARP). При включённом VPN это короткий перезапуск туннеля.
func (s *Supervisor) Restart() (State, error) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.mu.RLock()
	on := s.on
	s.mu.RUnlock()
	return s.restartLocked(on)
}

// Close останавливает ядро и (при штатной остановке) снимает правила киллсвитча: закрыл приложение — сеть на месте. При падении демона
// эта функция не выполняется, и правила остаются — в этом и смысл.
func (s *Supervisor) Close() error {
	t0 := time.Now()
	s.setMu.Lock()
	defer s.setMu.Unlock()
	tWait := time.Since(t0)
	t1 := time.Now()
	err := s.stopCurrent()
	tStop := time.Since(t1)
	t2 := time.Now()
	s.ksRemove()
	log.Printf("остановка супервизора: ожидание блокировки %s, ядро %s, правила киллсвитча %s, всего %s",
		tWait.Round(time.Millisecond), tStop.Round(time.Millisecond), time.Since(t2).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))
	return err
}
