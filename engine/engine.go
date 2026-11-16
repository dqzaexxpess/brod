// Package engine оборачивает sing-box-lx как встроенный движок (P4): один процесс, без входов и TUN.
//
// Пробные outbound'ы создаются напрямую через реестр, а не через OutboundManager: в lx менеджер умеет только
// Create (удаления нет), и тысячи проверок копились бы в нём вечно. Код создания тот же, что у боевых узлов,
// поэтому паритет конфигов (P2) сохраняется: проверяется ровно то, что будет использовано при подключении.
package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter/outbound"

	"brod/ext"
	"brod/ext/dispatch"
	switchout "brod/ext/switch"
	"brod/nodeopts"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

// Engine — запущенный экземпляр движка.
type Engine struct {
	ctx    context.Context
	cancel context.CancelFunc
	outReg *outbound.Registry // реестр outbound'ов с плагинами (создаётся один раз)
	box    *box.Box
	seq    atomic.Uint64

	scopesMu sync.Mutex
	scopes   map[string]*adapter.Scope // scope запуска постоянных outbound'ов (закрывается в Remove)
}

// ContextHook дополняет контекст нового движка (например, регистрирует платформенный интерфейс Android: TUN и защита сокетов даются
// VpnService). nil — ничего не добавлять. Выставляется один раз до запуска ядра.
var ContextHook func(ctx context.Context)

// defaultCertificate: на Windows корни берутся из встроенного набора Mozilla, а не из хранилища системы. На ПК с отключённым обновлением
// корней (политика DisableRootAutoUpdate, «оптимизаторы» Windows — в РФ такие сборки часто) в хранилище нет, например, корня SSL.com,
// которым подписан cloudflare-dns.com, и DNS через туннель целиком падал с «x509: certificate signed by unknown authority».
func defaultCertificate(goos string) *option.CertificateOptions {
	if goos != "windows" {
		return nil
	}
	return &option.CertificateOptions{Store: "mozilla"}
}

// New запускает движок с одним лишь direct-outbound'ом.
func New(logLevel string) (*Engine, error) { return NewFromJSON(logLevel, nil) }

// NewFromJSON запускает движок с полным конфигом sing-box-lx (входы, outbound'ы, маршрут); nil = пустой конфиг.
func NewFromJSON(logLevel string, cfg []byte) (*Engine, error) {
	ctx, cancel := context.WithCancel(ext.Context(context.Background()))
	if ContextHook != nil {
		ContextHook(ctx)
	}
	opts := option.Options{Log: &option.LogOptions{Level: logLevel}}
	if len(cfg) > 0 {
		if err := json.UnmarshalContext(ctx, cfg, &opts); err != nil {
			cancel()
			return nil, fmt.Errorf("decode config: %w", err)
		}
	}
	if opts.Certificate == nil {
		opts.Certificate = defaultCertificate(runtime.GOOS)
	}
	b, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("box.New: %w", err)
	}
	if err := b.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("box.Start: %w", err)
	}
	return &Engine{ctx: ctx, cancel: cancel, box: b, outReg: ext.OutboundRegistry(), scopes: map[string]*adapter.Scope{}}, nil
}

// Close останавливает движок.
func (e *Engine) Close() error {
	err := e.box.Close()
	e.cancel()
	return err
}

// Context возвращает контекст с реестрами движка (нужен для декодирования JSON опций).
func (e *Engine) Context() context.Context { return e.ctx }

// OutboundManager отдаёт менеджер outbound'ов движка (нужен учёту соединений, чтобы показывать цепочку выходов).
func (e *Engine) OutboundManager() adapter.OutboundManager { return e.box.Outbound() }

// AppendTracker подключает наблюдателя за маршрутизируемыми соединениями (например, monitor.Monitor).
func (e *Engine) AppendTracker(t adapter.ConnectionTracker) { e.box.Router().AppendTracker(t) }

// Direct — путь «мимо узла» (outbound direct движка): для целей, где прямой доступ предпочтителен (P7).
func (e *Engine) Direct() adapter.Outbound { return e.box.Outbound().Default() }

// OutboundFlags — что белый список узла разрешает сверх параметров самого узла: тег и цепочку через WARP (detour) ставит ядро
// (core.outboundJSON), одинаково для боевых узлов (Add) и пробных (NewProbe): без Detour у пробы при check.via_warp
// отклонялась бы каждая проверка узла.
var OutboundFlags = nodeopts.Flags{Tag: true, Detour: true}

// Probe — временный outbound для проверки узла. Не регистрируется в менеджере.
type Probe struct {
	scope  *adapter.Scope // для WireGuard-узлов: область жизни endpoint'а, запущенного этой пробой
	Out    adapter.Outbound
	shared bool // outbound принадлежит движку (endpoint из конфига): Close его не закрывает
}

// NewProbe создаёт outbound из JSON (формат sing-box-lx; поле tag необязательно).
func (e *Engine) NewProbe(raw []byte) (*Probe, error) {
	raw, err := nodeopts.Sanitize(raw, OutboundFlags) // белый список «тип → поля»
	if err != nil {
		return nil, err
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err == nil && head.Type == "wireguard" {
		return e.newEndpointProbe(raw) // в sing-box WireGuard — endpoint, а не outbound
	}
	var ob option.Outbound
	if err := json.UnmarshalContext(e.ctx, raw, &ob); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	tag := fmt.Sprintf("probe-%d", e.seq.Add(1))
	logger := e.box.LogFactory().NewLogger(tag)
	out, err := e.outReg.CreateOutbound(e.ctx, e.box.Router(), logger, tag, ob.Type, ob.Options)
	if err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	pr := &Probe{Out: out}
	// часть протоколов (anytls и др.) создаёт рабочие структуры только в Start: без стадий запуска первый же Dial падает на nil
	if lc, ok := out.(adapter.Lifecycle); ok {
		scope := adapter.NewScope(e.ctx, logger)
		for _, stage := range adapter.ListStartStages {
			if err := scope.Start(tag, lc, stage); err != nil {
				_ = scope.Close()
				_ = common.Close(out)
				return nil, fmt.Errorf("start: %w", err)
			}
		}
		pr.scope = scope
	}
	return pr, nil
}

// Close освобождает ресурсы outbound'а (пулы соединений XMUX и т.п.).
func (p *Probe) Close() {
	if p.scope != nil {
		_ = p.scope.Close()
	}
	if !p.shared {
		_ = common.Close(p.Out)
	}
}

// Existing возвращает уже работающий в движке outbound/endpoint по тегу (например, wireguard-endpoint WARP из конфига).
func (e *Engine) Existing(tag string) (*Probe, error) {
	out, ok := e.box.Outbound().Outbound(tag)
	if !ok {
		return nil, fmt.Errorf("outbound/endpoint %q не найден", tag)
	}
	return &Probe{Out: out, shared: true}, nil
}

// DialContext открывает соединение через узел; имя резолвится на стороне узла («DNS через туннель»).
func (p *Probe) DialContext(ctx context.Context, network, addr string) (conn net.Conn, err error) {
	// драйверы чужих протоколов иногда паникуют на экзотических узлах; без этого паника в горутине net/http убивает весь процесс
	defer func() {
		if r := recover(); r != nil {
			conn, err = nil, fmt.Errorf("panic в драйвере %s: %v", p.Out.Type(), r)
		}
	}()
	return p.Out.DialContext(ctx, network, M.ParseSocksaddr(addr))
}

// stallConn ограничивает простой чтения: именно так проявляется заморозка ТСПУ (данные пошли и встали).
type stallConn struct {
	net.Conn
	stall time.Duration
}

func (c *stallConn) Read(b []byte) (int, error) {
	if c.stall > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.stall))
	}
	return c.Conn.Read(b)
}

// HTTPClient возвращает клиент, ходящий через узел. stall — максимальный простой чтения (0 = без ограничения).
func (p *Probe) HTTPClient(total, stall time.Duration) *http.Client {
	return &http.Client{
		Timeout: total,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := p.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &stallConn{Conn: c, stall: stall}, nil
			},
			TLSHandshakeTimeout: total,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Add регистрирует постоянный outbound или endpoint (WireGuard/AWG) в менеджере движка (активный или выбранный под цель узел,
// группа, switch). Тег берётся из JSON. В отличие от Probe, такой узел доступен маршрутизатору и убирается через Remove без
// перезапуска движка (P1). Менеджер lx после старта стадии запуска не гоняет, поэтому проводим их сами: часть протоколов
// (anytls, hysteria2, tuic) создаёт рабочие структуры только в Start, и без этого первый дозвон падает на nil.
func (e *Engine) Add(raw []byte) (string, error) {
	raw, err := nodeopts.Sanitize(raw, OutboundFlags) // белый список «тип → поля»
	if err != nil {
		return "", err
	}
	return e.add(raw)
}

// add — Add без проверки белым списком: для тестов движка с типами direct/selector/switch и для addEndpoint, чей вход уже проверен.
func (e *Engine) add(raw []byte) (string, error) {
	var head struct {
		Type string `json:"type"`
		Tag  string `json:"tag"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if head.Tag == "" {
		return "", fmt.Errorf("у постоянного outbound'а должен быть tag")
	}
	if head.Type == "wireguard" {
		return head.Tag, e.addEndpoint(raw)
	}
	var ob option.Outbound
	if err := json.UnmarshalContext(e.ctx, raw, &ob); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	logger := e.box.LogFactory().NewLogger("node/" + ob.Tag)
	if err := e.box.Outbound().Create(e.ctx, e.box.Router(), logger, ob.Tag, ob.Type, ob.Options); err != nil {
		return "", err
	}
	out, _ := e.box.Outbound().Outbound(ob.Tag)
	if lc, ok := out.(adapter.Lifecycle); ok {
		scope := adapter.NewScope(e.ctx, logger)
		if err := startAll(scope, ob.Tag, lc); err != nil {
			_ = scope.Close()
			_ = e.box.Outbound().Remove(ob.Tag)
			return "", fmt.Errorf("start: %w", err)
		}
		e.scopesMu.Lock()
		e.scopes[ob.Tag] = scope
		e.scopesMu.Unlock()
	}
	return ob.Tag, nil
}

func startAll(scope *adapter.Scope, tag string, lc adapter.Lifecycle) (err error) {
	// драйверы чужих протоколов иногда паникуют на экзотических настройках; в демоне это не должно убивать процесс
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic при запуске %s: %v", tag, r)
		}
	}()
	for _, stage := range adapter.ListStartStages {
		if err := scope.Start(tag, lc, stage); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) addEndpoint(raw []byte) error {
	var ep option.Endpoint
	if err := json.UnmarshalContext(e.ctx, raw, &ep); err != nil {
		return fmt.Errorf("decode endpoint: %w", err)
	}
	logger := e.box.LogFactory().NewLogger("node/" + ep.Tag)
	if err := e.box.Endpoint().Create(e.ctx, e.box.Router(), logger, ep.Tag, ep.Type, ep.Options); err != nil {
		return err
	}
	created, _ := e.box.Endpoint().Get(ep.Tag)
	if err := e.box.Endpoint().StartDynamic(created); err != nil {
		_ = e.box.Endpoint().Remove(ep.Tag)
		return fmt.Errorf("start endpoint: %w", err)
	}
	return nil
}

// Remove убирает узел, добавленный через Add (нужны патчи lx 0001 и 0003). Отказ, если на узел смотрит switch, группа
// или другой outbound (Dependencies); сериализация с SetSwitchTarget — на вызывающем.
func (e *Engine) Remove(tag string) error {
	if _, isEndpoint := e.box.Endpoint().Get(tag); isEndpoint {
		for _, o := range e.box.Outbound().Outbounds() {
			for _, d := range o.Dependencies() {
				if d == tag {
					return fmt.Errorf("cannot remove endpoint %s: used by %s", tag, o.Tag())
				}
			}
		}
		return e.box.Endpoint().Remove(tag)
	}
	if err := e.box.Outbound().Remove(tag); err != nil {
		return err
	}
	e.scopesMu.Lock()
	scope := e.scopes[tag]
	delete(e.scopes, tag)
	e.scopesMu.Unlock()
	if scope != nil {
		_ = scope.Close()
	}
	return nil
}

// AddSwitch создаёт постоянный outbound-переключатель с тегом tag; target — начальная цель ("" — без цели).
func (e *Engine) AddSwitch(tag, target string, interruptExisting bool) error {
	raw, err := json.Marshal(map[string]any{"type": switchout.Type, "tag": tag, "target": target, "interrupt_existing": interruptExisting})
	if err != nil {
		return err
	}
	_, err = e.add(raw)
	return err
}

func (e *Engine) switchOf(tag string) (*switchout.Switch, error) {
	out, ok := e.box.Outbound().Outbound(tag)
	if !ok {
		return nil, fmt.Errorf("switch %q не найден", tag)
	}
	sw, ok := out.(*switchout.Switch)
	if !ok {
		return nil, fmt.Errorf("%q не switch (%s)", tag, out.Type())
	}
	return sw, nil
}

// SetSwitchTarget переключает switch на outbound/endpoint с тегом target. При ошибке цель не меняется.
// Вызывающий сериализует его с Remove (см. комментарий в ext/switch).
func (e *Engine) SetSwitchTarget(switchTag, target string) error {
	sw, err := e.switchOf(switchTag)
	if err != nil {
		return err
	}
	return sw.SetTarget(target)
}

// ClearSwitchTarget снимает цель у switch: дозвоны через него отклоняются, пока цель не назначена снова.
func (e *Engine) ClearSwitchTarget(switchTag string) error {
	sw, err := e.switchOf(switchTag)
	if err != nil {
		return err
	}
	sw.Clear()
	return nil
}

// SwitchTarget возвращает тег текущей цели switch ("" — цели нет).
func (e *Engine) SwitchTarget(switchTag string) (string, error) {
	sw, err := e.switchOf(switchTag)
	if err != nil {
		return "", err
	}
	return sw.Current(), nil
}

// SetDispatchRules заменяет таблицу «домен → цель» у dispatch-outbound'а tag (он описан в базовом конфиге).
func (e *Engine) SetDispatchRules(tag string, rules []dispatch.Rule) error {
	out, ok := e.box.Outbound().Outbound(tag)
	if !ok {
		return fmt.Errorf("dispatch %q не найден", tag)
	}
	d, ok := out.(*dispatch.Dispatch)
	if !ok {
		return fmt.Errorf("%q не dispatch (%s)", tag, out.Type())
	}
	return d.SetRules(rules)
}

// MatchDispatch возвращает тег цели, в которую dispatch tag направит соединение к host ("" — пойдёт в умолчание).
func (e *Engine) MatchDispatch(tag, host string) string {
	if out, ok := e.box.Outbound().Outbound(tag); ok {
		if d, ok := out.(*dispatch.Dispatch); ok {
			return d.Match(host)
		}
	}
	return ""
}

// Has сообщает, зарегистрирован ли outbound/endpoint с таким тегом.
func (e *Engine) Has(tag string) bool {
	_, ok := e.box.Outbound().Outbound(tag)
	return ok
}

// newEndpointProbe создаёт и запускает одиночный endpoint (WireGuard/AmneziaWG) для проверки; Close останавливает его.
func (e *Engine) newEndpointProbe(raw []byte) (*Probe, error) {
	var ep option.Endpoint
	if err := json.UnmarshalContext(e.ctx, raw, &ep); err != nil {
		return nil, fmt.Errorf("decode endpoint: %w", err)
	}
	tag := fmt.Sprintf("probe-ep-%d", e.seq.Add(1))
	logger := e.box.LogFactory().NewLogger(tag)
	endpoint, err := include.EndpointRegistry().Create(e.ctx, e.box.Router(), logger, tag, ep.Type, ep.Options)
	if err != nil {
		return nil, fmt.Errorf("create endpoint: %w", err)
	}
	scope := adapter.NewScope(e.ctx, logger)
	for _, stage := range adapter.ListStartStages {
		if err := scope.Start(tag, endpoint, stage); err != nil {
			_ = scope.Close()
			return nil, fmt.Errorf("start endpoint: %w", err)
		}
	}
	return &Probe{scope: scope, Out: endpoint}, nil
}

// ValidateConfig проверяет полный конфиг sing-box-lx: разбирает опции и собирает движок, но не запускает его (не открывает
// TUN, порты и сокеты). Нужна, чтобы проверять конфиги, для запуска которых нужны привилегии (TUN), без них.
func ValidateConfig(raw []byte) error {
	ctx, cancel := context.WithCancel(ext.Context(context.Background()))
	defer cancel()
	var opts option.Options
	if err := json.UnmarshalContext(ctx, raw, &opts); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	opts.Log = &option.LogOptions{Disabled: true}
	b, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return fmt.Errorf("box.New: %w", err)
	}
	return b.Close()
}
