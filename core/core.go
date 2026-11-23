package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"brod/aggregator"
	"brod/engine"
	"brod/monitor"
	"brod/ota"
	"brod/store"
	"brod/updater"
	"brod/warp"
)

// Core — ядро демона. Все операции, меняющие набор зарегистрированных в движке узлов или цели switch'ей, идут под c.mu:
// этого требует контракт ext/switch и ext/dispatch (иначе Remove может проскочить между поиском цели и записью).
type Core struct {
	cfg Config
	st  *store.Store
	eng *engine.Engine
	bus *Bus

	traffic        *trafficcontrol.Manager                            // учёт соединений и байт движка
	trafficAdd     func(day string, up, dn, vpnUp, vpnDn int64) error // запись суточного трафика; nil — база (подмена в тестах)
	trafficSamples trafficRing
	vpnClosedMu    sync.Mutex // учёт «через VPN» среди закрытых соединений
	vpnClosedSeen  map[string]struct{}
	vpnClosedUp    int64
	vpnClosedDn    int64
	measure        measureJob // пакетный замер выбранных узлов (пинг, страна и чистота, скорость)
	diagEnv        diagEnv    // внешние зависимости диагностики (пустые поля = боевые)

	swapMu sync.Mutex // не чаще одного автоперехода в минуту
	swapAt time.Time

	pingMu sync.Mutex // запись «пинга» активного узла не чаще раза в минуту
	pingAt time.Time

	warpFirstLogged      bool         // первая проверка WARP после старта ядра уже записана в журнал (только из warpLoop)
	usMu                 sync.Mutex   // свои списки маршрутизации (usersets.go)
	nm                   netModeState // режим мобильной сети (netmode.go)
	updMu                sync.Mutex   // состояние обновления приложения (update.go)
	upd                  UpdateInfo
	updAsset             ota.Asset
	updInstalling        bool // установка запущена (update_install.go)
	updLast              *updater.Result
	viaMu                sync.Mutex // привязки выходов правил маршрутизации (routevia.go)
	viaState             map[string]ViaState
	viaFails             map[string]int       // подряд проваленных проверок узла правила
	viaPingAt            map[string]time.Time // когда в историю последний раз записан успех узла правила
	cntMu                sync.Mutex           // кэш числа узлов: подсчёт по 100 тысячам строк занимает ~0,3 с и блокирует базу, а статус опрашивается каждые пару секунд
	cntAt                time.Time
	cntTotal, cntPresent int

	dnsSet bool // DNS системы направлен в TUN (нужно сбросить при остановке)

	act             activityState // что демон делает сейчас (индикация в приложении)
	refreshMu       sync.Mutex    // один Refresh за раз
	checkMu         sync.Mutex    // один круг проверок за раз
	kick            chan struct{}
	tgKick          chan struct{} // просьба провести круг проверки прокси Telegram
	standby         standbyState  // горячий резерв (standby.go)
	nodeCheckActive atomic.Bool   // идёт круг проверки узлов (прокси Telegram на это время не проверяются)
	tgProxyMu       sync.Mutex    // один круг проверки прокси Telegram за раз

	// подменяемые в тестах участки загрузки подписок (nil = настоящие)
	directFetch Fetcher
	viaFetch    func(ctx context.Context, srcs []aggregator.Source, perSource time.Duration, cacheDir string, client *http.Client) <-chan aggregator.Result

	confirmMu  sync.Mutex
	confirming map[string]bool // области, где уже идёт подтверждающая проверка

	health     healthState
	mon        *monitor.Monitor
	baseCtx    context.Context
	netMu      sync.Mutex
	lastNet    string // ключ истории (сеть + режим цепочки)
	lastRawNet string // идентификатор сети без режима

	tgMu        sync.RWMutex
	targetByTag map[string]string       // тег switch'а цели → идентификатор цели
	tstate      map[string]*targetState // оперативное состояние целей

	mu       sync.Mutex
	nodeSrv  *nodeServers      // домены серверов узлов для DNS «local»; nil — без TUN
	reg      map[string]string // NodeID → тег в движке: узлы, зарегистрированные сейчас
	selected map[string]string // область (store.ScopeActive, store.TargetScope(id)) → NodeID/«direct»/«warp»

	startedAt     time.Time
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	loopsMu       sync.Mutex
	loops         map[string]int // работающие фоновые задания по имени (для журнала долгой остановки)
	appUser       string
	warpOn        bool       // аккаунт WARP загружен, endpoint поднят
	chainMu       sync.Mutex // вспомогательные звенья цепочек в движке (chain.go)
	chainHelpers  map[string]bool
	restored      *restoredActive          // итог restoreActive до первого Start
	auth          atomic.Pointer[authSnap] // снимок токенов для Authenticate/CallerActive (authcache.go)
	viaWarpMem    atomic.Int32             // кэш настройки check.via_warp (viaWarpUnknown/On/Off); сбрасывается при её записи
	warpAcc       *warp.Account            // аккаунт для подбора профилей (warpscan.go); nil — WARP не настроен
	lastOnlineRaw string                   // последний идентификатор сети, отличный от «offline» (под netMu)
	reattachAt    atomic.Int64             // unix-нс возврата в ту же сотовую сеть после потери связи: провалы до этого момента не мешают проверкам
	warpChecked   atomic.Bool              // была хотя бы одна проверка WARP (warpCheck)
	warpLastOK    atomic.Bool              // последняя проверка WARP успешна
	wsMu          sync.Mutex               // идёт подбор профиля WARP (warpscan.go)
	wsRunning     bool
	discoverMu    sync.Mutex // идёт проход автопоиска источников (discover.go)
	yieldMu       sync.Mutex // рейтинг источников (sourcerank.go)
	yield         map[string]store.SourceYield
	yieldAt       time.Time
	warpApplyMu   sync.Mutex  // applyWarpProfile: один перезапуск на один профиль
	warpApplied   WarpProfile // профиль, по которому перезапуск уже запрошен
	warpDown      atomic.Bool // WARP не отвечает: цепочка через него временно отключена (настройка check.via_warp не меняется)
	warpFails     int         // подряд проваленных проверок WARP (только цикл warpLoop)
	warpOKs       int         // подряд успешных проверок, пока WARP считается упавшим
	appPass       string
}

// New открывает базу, поднимает движок с app-входом и пустым switch'ем active. Фоновые задания запускает Start.
func New(cfg Config) (*Core, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(cfg.StateDir, 0o700)
	st, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		return nil, err
	}
	user, pass, err := ensureSecrets(st)
	if err != nil {
		st.Close()
		return nil, err
	}
	preapplyWarpProfile(st, cfg)
	applyWarpSettings(&cfg, func(k string) string { v, _, _ := st.Setting(k); return v })
	acc, err := loadWarpAccount(&cfg)
	if err != nil {
		st.Close()
		return nil, err
	}
	if v, _, _ := st.Setting(SetProxyLAN); v == "1" && cfg.AppListen == "" {
		cfg.AppListen = "0.0.0.0" // раздача в локальную сеть
	}
	if cfg.TUN.Enabled && !cfg.TUN.FakeIP {
		if v, _, _ := st.Setting(SetFakeIP); v == "1" {
			cfg.TUN.FakeIP = true
		}
	}
	if cfg.TUN.Enabled && cfg.TUN.UserRules == nil {
		rs, err := loadRouteRules(st)
		if err != nil {
			st.Close()
			return nil, err
		}
		cfg.TUN.UserRules = rs
	}
	if cfg.TUN.Enabled && cfg.TUN.UserSets == nil {
		dir := cfg.TUN.RuleSetDir
		if dir == "" {
			dir = filepath.Join(cfg.StateDir, "rulesets")
		}
		cfg.TUN.UserSets = userSetDefs(st, dir)
	}
	if cfg.TUN.Enabled && cfg.TUN.IncludePackage == nil && cfg.TUN.ExcludePackage == nil {
		mode, _, _ := st.Setting(SetAppsMode)
		apps, _, _ := st.Setting(SetApps)
		switch list := splitApps(apps); mode {
		case "include":
			cfg.TUN.IncludePackage = list
		case "exclude":
			cfg.TUN.ExcludePackage = list
		}
	}
	if !ValidMode(cfg.TUN.Mode) {
		st.Close()
		return nil, fmt.Errorf("неизвестный режим TUN %q (blocked | all | all-but-ru)", cfg.TUN.Mode)
	}
	if err := prepareTUN(&cfg); err != nil {
		st.Close()
		return nil, fmt.Errorf("TUN: %w", err)
	}
	var nodeSrv *nodeServers
	if cfg.TUN.Enabled {
		nodeSrv = newNodeServers(cfg.TUN.NodeServers)
		noteSavedActive(st, nodeSrv)
	}
	raw, err := engineConfig(&cfg, user, pass, acc)
	if err != nil {
		st.Close()
		return nil, err
	}
	eng, err := engine.NewFromJSON(cfg.LogLevel, raw)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("движок: %w", err)
	}
	c := &Core{
		cfg: cfg, st: st, eng: eng, bus: newBus(st), nodeSrv: nodeSrv,
		reg: map[string]string{}, selected: map[string]string{},
		appUser: user, appPass: pass, warpOn: acc != nil, warpAcc: acc,
	}
	c.targetByTag = map[string]string{}
	c.viaState = map[string]ViaState{}
	c.viaFails = map[string]int{}
	c.viaPingAt = map[string]time.Time{}
	c.tstate = map[string]*targetState{}
	c.confirming = map[string]bool{}
	c.mon = monitor.New(cfg.Monitor, c.onMonitor)
	c.mon.Scope = c.scopeOf
	eng.AppendTracker(c.mon)
	c.vpnClosedSeen = map[string]struct{}{}
	c.traffic = trafficcontrol.NewManager()
	c.traffic.SetOutboundManagerLx(eng.OutboundManager())
	eng.AppendTracker(c.traffic)
	// движок уже запущен (при TUN — вместе с туннелем), а цель переключателя «active» пуста и соединения отклоняются. Сохранённый
	// активный узел выставляем сразу, до засева источников/целей (записи в базу), запуска заданий и настройки DNS системы; события — в Start.
	c.restoreActive()
	if err := c.seedSources(); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.seedTargets(); err != nil {
		c.Close()
		return nil, err
	}
	if r, ok := updater.ReadResult(updater.ResultFile(cfg.StateDir)); ok { // итог установки обновления, сделанной перед этим запуском
		c.updLast = &r
	}
	return c, nil
}

// Store и Bus нужны API и тестам.
func (c *Core) Store() *store.Store { return c.st }
func (c *Core) Bus() *Bus           { return c.bus }

// NodesTag — метка версии ответа Nodes(q), считается ДО сборки списка: те же данные и тот же запрос дают ту же метку.
// Входы: версии хранилища (ViewGen покрывает узлы, пробы, скорость, выход, настройки; NodesGen и эпоха — для ясности и после
// перезапуска), ключ сети (от него зависит история проб), параметры запроса и 5-минутный отсчёт времени (проба выходит из семидневного
// окна без записи в базу). ok=false — метку заранее посчитать нельзя: фильтр «живые» зависит от текущего времени с точностью до секунды,
// для него вызывающий считает метку по телу ответа.
func (c *Core) NodesTag(q NodesQuery) (tag string, ok bool) {
	if q.Alive {
		return "", false
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%x|%x|%x|%s|%+v|%d", c.st.Epoch(), c.st.ViewGen(), c.st.NodesGen(), c.netKey(), q, c.cfg.Now().Unix()/300)))
	return `"n` + hex.EncodeToString(h[:8]) + `"`, true
}

// Engine нужен тестам приёмки (замораживание узлов и т. п.); боевой код ядра обращается к движку сам.
func (c *Core) Engine() *engine.Engine { return c.eng }

// AppProxy возвращает адрес и учётные данные app-входа (секрет: показывать только по явной команде владельца).
func (c *Core) AppProxy() (addr, user, pass string) {
	return fmt.Sprintf("%s:%d", c.cfg.AppHost, c.cfg.AppPort), c.appUser, c.appPass
}

// ProxyShare — сведения для подключения других устройств к app-входу: включена ли раздача, адреса этого компьютера в локальных сетях, порт и учётные данные.
type ProxyShare struct {
	Enabled bool
	Addrs   []string // IPv4-адреса интерфейсов, на которые можно подключиться
	Port    int
	User    string
	Pass    string
}

// ProxyShare возвращает данные раздачи прокси (секрет: показывать только владельцу).
func (c *Core) ProxyShare() ProxyShare {
	p := ProxyShare{Enabled: c.cfg.AppListen != "" && c.cfg.AppListen != c.cfg.AppHost, Port: c.cfg.AppPort, User: c.appUser, Pass: c.appPass}
	if p.Enabled {
		p.Addrs = lanAddrs()
	}
	return p
}

// lanAddrs — частные IPv4-адреса работающих интерфейсов (кроме loopback и туннелей): по ним устройства сети находят этот компьютер.
func lanAddrs() []string {
	var out []string
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || strings.HasPrefix(ifc.Name, "brod") || strings.HasPrefix(ifc.Name, "tun") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip := ipn.IP.To4(); ip != nil && ip.IsPrivate() {
					out = append(out, ip.String())
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// Start восстанавливает сохранённый выбор и запускает фоновые задания. Не блокируется на сети (P3: старт ≤1 с).
func (c *Core) Start(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	c.baseCtx = ctx
	c.startedAt = c.cfg.Now()
	c.netMu.Lock()
	c.lastNet, c.lastRawNet = c.netKey(), c.cfg.NetID()
	c.netMu.Unlock()
	c.goLoop("monitor", func() { c.mon.Run(ctx) })
	c.goLoop("traffic", c.trafficLoop)
	c.setupSystemDNS(ctx)
	c.restore()
	c.bus.Publish(store.Event{Kind: EvStarted, Initiator: "auto"})
	c.startLoops(ctx)
	return nil
}

// Close останавливает фоновые задания, движок и базу. Этапы дольше двух секунд пишутся в журнал: остановка Windows укладывалась не во все 10 с,
// и по журналу нельзя было сказать, что именно держит (задания, закрытие движка с wintun, база).
func (c *Core) Close() error {
	start := time.Now()
	t0 := start
	var stages []string
	stage := func(name string) {
		d := time.Since(t0)
		stages = append(stages, fmt.Sprintf("%s %s", name, d.Round(time.Millisecond)))
		if d > 2*time.Second {
			log.Printf("остановка ядра: этап «%s» занял %s", name, d.Round(100*time.Millisecond))
		}
		t0 = time.Now()
	}
	defer func() {
		log.Printf("остановка ядра за %s: %s", time.Since(start).Round(time.Millisecond), strings.Join(stages, ", "))
	}()
	// сторож медленной остановки: какой этап идёт сейчас, знает только Close (stopdump.go)
	var current atomic.Pointer[string]
	enter := func(name string) { current.Store(&name) }
	enter("фоновые задания")
	defer c.watchSlowStop(start, func() string { return *current.Load() })()
	if c.cancel != nil {
		c.cancel()
	}
	c.waitLoops(time.Second)
	stage("фоновые задания")
	enter("трафик и DNS системы")
	c.flushTraffic()
	c.revertSystemDNS()
	stage("трафик и DNS системы")
	c.bus.Publish(store.Event{Kind: EvStopped, Initiator: "auto"})
	enter("движок")
	err := c.eng.Close()
	stage("движок")
	enter("база")
	if e := c.st.Close(); err == nil {
		err = e
	}
	stage("база")
	return err
}

// ---- узлы в движке ----

// NodeTag — тег узла в движке.
func NodeTag(id string) string {
	if len(id) > 16 {
		id = id[:16]
	}
	return "n-" + id
}

// outboundJSON собирает JSON постоянного outbound'а/endpoint'а из сохранённых опций узла.
func outboundJSON(rec store.NodeRec, tag string, viaWarp bool) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(rec.Opts, &m); err != nil {
		return nil, fmt.Errorf("опции узла: %w", err)
	}
	m["tag"] = tag
	if viaWarp && !rec.Endpoint && m["type"] != "wireguard" {
		m["detour"] = OutWarp // цепочка WARP → узел
	}
	return json.Marshal(m)
}

// ensureRegistered регистрирует узел в движке, если его там ещё нет. Вызывать под c.mu.
func (c *Core) ensureRegistered(id string) (string, error) {
	if tag, ok := c.reg[id]; ok {
		return tag, nil
	}
	rec, err := c.st.Node(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = ErrNoNode
		}
		return "", fmt.Errorf("узел %s: %w", short(id), err)
	}
	tag := NodeTag(id)
	raw, err := c.nodeOutboundJSON(rec, tag, c.viaWarp())
	if err != nil {
		return "", err
	}
	fresh := false
	for _, srv := range nodeServerNames(rec) { // у цепочки серверы всех звеньев
		if c.nodeSrv.note(srv) { // домен сервера — в набор правил DNS «local» до первого дозвона
			fresh = true
		}
	}
	if fresh {
		time.Sleep(nodeServersSettle)
	}
	if _, err := c.eng.Add(raw); err != nil {
		return "", err
	}
	c.reg[id] = tag
	return tag, nil
}

// ShortID — сокращённый идентификатор узла для сообщений.
func ShortID(id string) string { return short(id) }

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// needed — узлы, на которые сейчас смотрят switch'и. Под c.mu.
func (c *Core) needed() map[string]bool {
	n := map[string]bool{}
	for _, id := range c.selected {
		n[id] = true
	}
	return n
}

// gc убирает из движка зарегистрированные узлы, на которые ничто не смотрит. Под c.mu.
func (c *Core) gc() {
	need := c.needed()
	for id, tag := range c.reg {
		if need[id] {
			continue
		}
		if err := c.eng.Remove(tag); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Node: id, Detail: "gc: " + err.Error()})
			continue
		}
		delete(c.reg, id)
	}
}

// ---- выбор активного узла (P1) ----

// ErrNoNode — узла нет в базе.
var ErrNoNode = errors.New("узла нет в базе")

// Connect делает узел активным. Это единственный путь смены активного узла помимо явных API-команд пользователя:
// ни обновление подписок, ни проверки его не вызывают (P1). initiator: user | api:<приложение> | auto.
// При ошибке прежний активный узел остаётся.
func (c *Core) Connect(nodeID, initiator string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked(nodeID, initiator, true)
}

func (c *Core) connectLocked(nodeID, initiator string, announce bool) error {
	old := c.selected[store.ScopeActive]
	if old == nodeID {
		if _, ok := c.reg[nodeID]; ok {
			return nil // уже активен
		}
	}
	var tag string
	var err error
	switch nodeID {
	case store.SelWarp: // WARP как активный выход: стабильный путь, когда бесплатных узлов нет
		tag, err = c.tagFor(store.SelWarp)
	case store.SelDirect:
		err = fmt.Errorf("%w: direct активным выходом не бывает (это обход туннеля); выберите узел или warp", ErrInvalid)
	default:
		tag, err = c.ensureRegistered(nodeID)
	}
	if err != nil {
		c.bus.Publish(store.Event{Kind: EvConnectFailed, Initiator: initiator, Node: nodeID, Detail: err.Error()})
		return err
	}
	if err := c.eng.SetSwitchTarget(OutActive, tag); err != nil {
		c.gc() // свежезарегистрированный узел никому не нужен
		c.bus.Publish(store.Event{Kind: EvConnectFailed, Initiator: initiator, Node: nodeID, Detail: err.Error()})
		return err
	}
	c.selected[store.ScopeActive] = nodeID
	if old != nodeID {
		c.resetHealth() // новый узел — новый отсчёт деградации
	}
	if announce {
		sel := store.Selection{Scope: store.ScopeActive, Node: nodeID, By: initiator, SetAt: c.cfg.Now()}
		if prev, err := c.st.Selection(store.ScopeActive); err == nil {
			sel.Fallbacks = prev.Fallbacks
		}
		if err := c.st.SetSelection(sel); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Node: nodeID, Detail: "выбор не сохранён: " + err.Error()})
		}
		c.bus.Publish(store.Event{Kind: EvSwitch, Initiator: initiator, Node: nodeID, Detail: "с " + short(old)})
	}
	c.gc()
	return nil
}

// restore поднимает сохранённый выбор после перезапуска. Если узел не поднялся — событие, active остаётся без цели:
// автовыбора нет, P1 запрещает смену без пользователя.
func (c *Core) restore() {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.restoreTargets() // после активного узла: цели могут разделять с ним зарегистрированные узлы
	r := c.restored
	c.restored = nil
	if r == nil {
		return
	}
	if r.err != nil {
		c.bus.Publish(store.Event{Kind: EvRestoreFailed, Initiator: "auto", Node: r.node, Detail: r.err.Error()})
		return
	}
	c.bus.Publish(store.Event{Kind: EvRestored, Initiator: "auto", Node: r.node, Detail: "выбор от " + r.by})
}

// noteSavedActive заранее кладёт домен сервера сохранённого активного узла в набор правил node-servers: файл читается при запуске движка,
// и restoreActive → ensureRegistered не ждёт nodeServersSettle (250 мс) под c.mu. Для IP-адреса и «warp» ничего не делает.
func noteSavedActive(st *store.Store, ns *nodeServers) {
	sel, err := st.Selection(store.ScopeActive)
	if err != nil || sel.Node == "" {
		return
	}
	if rec, err := st.Node(sel.Node); err == nil {
		for _, srv := range nodeServerNames(rec) {
			ns.note(srv)
		}
	}
}

// restoredActive — итог restoreActive, о котором Start сообщает событием.
type restoredActive struct {
	node, by string
	err      error
}

// restoreActive выставляет цель переключателя «active» из сохранённого выбора (вызывается из New, до Start).
func (c *Core) restoreActive() {
	c.mu.Lock()
	defer c.mu.Unlock()
	sel, err := c.st.Selection(store.ScopeActive)
	if err != nil || sel.Node == "" {
		return
	}
	c.restored = &restoredActive{node: sel.Node, by: sel.By, err: c.connectLocked(sel.Node, "restore", false)}
}

// ---- состояние ----

// NodeInfo — краткие сведения об узле (без секретов).
type NodeInfo struct {
	ID       string
	Proto    string
	Class    string
	Server   string
	Port     int
	Favorite bool
	Pinned   bool
	Insecure bool
	Present  bool // false: источники перестали отдавать узел (на активный узел это не влияет)
}

func nodeInfo(r store.NodeRec) NodeInfo {
	return NodeInfo{ID: r.ID, Proto: r.Proto, Class: r.Class, Server: r.Server, Port: r.Port, Favorite: r.Favorite, Pinned: r.Pinned, Insecure: r.Insecure, Present: r.Present}
}

// Status — сводка состояния.
type Status struct {
	Active      *NodeInfo // nil — активного узла нет
	ActiveSince time.Time
	ActiveBy    string
	NetID       string
	Nodes       int
	Present     int
	StartedAt   time.Time
	WarpEnabled bool   // аккаунт WARP настроен
	ViaWarp     bool   // проверки и новые подключения идут цепочкой через WARP
	WarpDown    bool   // WARP не отвечает, цепочка временно отключена (ViaWarp при этом false)
	WarpDownMin int    // сколько минут WARP признан упавшим (0 — не упал): долгий простой при рабочем TCP значит, что сеть не пропускает UDP
	NetMode     string // режим мобильной сети: open, whitelist, offline; пусто — не определён (NetMode*)
}

// Status возвращает сводку.
func (c *Core) Status() (Status, error) {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	s := Status{NetID: c.cfg.NetID(), StartedAt: c.startedAt, WarpEnabled: c.warpOn, ViaWarp: c.viaWarp(), WarpDown: c.warpDown.Load(), NetMode: c.NetMode()}
	s.WarpDownMin = c.warpDownMinutes()
	var err error
	if s.Nodes, s.Present, err = c.counts(); err != nil {
		return s, err
	}
	if id == store.SelWarp {
		s.Active = &NodeInfo{ID: store.SelWarp, Proto: "warp", Class: "wireguard/warp", Server: "Cloudflare WARP", Present: true}
	} else if id != "" {
		if rec, err := c.st.Node(id); err == nil {
			ni := nodeInfo(rec)
			s.Active = &ni
		}
	}
	if id != "" {
		if sel, err := c.st.Selection(store.ScopeActive); err == nil {
			s.ActiveSince, s.ActiveBy = sel.SetAt, sel.By
		}
	}
	return s, nil
}

// EventsAfter отдаёт события из журнала после afterID.
func (c *Core) EventsAfter(afterID int64, limit int) ([]store.Event, error) {
	return c.st.Events(afterID, limit)
}

// LastEvents возвращает последние n событий журнала (старые первыми).
func (c *Core) LastEvents(n int) ([]store.Event, error) { return c.st.LastEvents(n) }

// counts возвращает число узлов (всего и присутствующих в источниках) из кэша не старше 20 с.
func (c *Core) counts() (total, present int, err error) {
	c.cntMu.Lock()
	defer c.cntMu.Unlock()
	if !c.cntAt.IsZero() && c.cfg.Now().Sub(c.cntAt) < 20*time.Second {
		return c.cntTotal, c.cntPresent, nil
	}
	if total, present, err = c.st.CountNodes(); err != nil {
		return 0, 0, err
	}
	c.cntTotal, c.cntPresent, c.cntAt = total, present, c.cfg.Now()
	return total, present, nil
}

// invalidateCounts сбрасывает кэш (после обновления подписок).
func (c *Core) invalidateCounts() {
	c.cntMu.Lock()
	c.cntAt = time.Time{}
	c.cntMu.Unlock()
}
