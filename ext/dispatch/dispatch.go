// Package dispatch — плагин-outbound "dispatch": направляет соединение в один из outbound'ов по домену назначения.
//
// Нужен для целей P7 (внешние приложения регистрируют домены во время работы): правила маршрута sing-box статичны,
// а здесь список домен → цель меняется атомарно, без перезапуска и перезагрузки rule-set. Приложение подключается к mixed-входу
// демона через CONNECT/SOCKS5 с именем хоста, поэтому домен назначения известен без сниффинга.
//
// Если назначение — IP (режим TUN), домен берётся из метаданных соединения (сниффинг SNI/Host).
// Остальное (домен не совпал или не определён) уходит в outbound по умолчанию (switch активного узла).
// Dispatch намеренно не является группой lx: роутер разворачивает группы до «листа» и дозванивается мимо них,
// а нам нужно решение по назначению именно в DialContext.
package dispatch

import (
	"context"
	"net"
	"strings"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// Type — имя типа outbound'а в конфиге.
const Type = "dispatch"

// Rule — правило: если назначение подходит под любое из условий, соединение идёт в Target.
type Rule struct {
	Target        string   `json:"target"`
	Domain        []string `json:"domain,omitempty"`         // точное совпадение
	DomainSuffix  []string `json:"domain_suffix,omitempty"`  // сам домен и все поддомены
	DomainKeyword []string `json:"domain_keyword,omitempty"` // подстрока
}

// Options — опции outbound'а.
type Options struct {
	Default string `json:"default"` // тег outbound'а по умолчанию
	Rules   []Rule `json:"rules,omitempty"`
}

// RegisterOutbound добавляет тип в реестр.
func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, Type, New)
}

type compiled struct {
	rules []Rule
	outs  []adapter.Outbound // outs[i] — цель rules[i]
	deps  []string
}

// Dispatch — outbound с таблицей домен → цель.
type Dispatch struct {
	outbound.Adapter
	manager adapter.OutboundManager
	defTag  string
	def     atomic.Pointer[adapter.Outbound]
	table   atomic.Pointer[compiled]
	initial []Rule
}

// New создаёт dispatch. Цели и умолчание разрешаются при создании, если они уже есть в менеджере, иначе на стадии Start.
func New(ctx context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	if options.Default == "" {
		return nil, E.New("dispatch ", tag, ": не задан default")
	}
	if options.Default == tag {
		return nil, E.New("dispatch ", tag, ": default совпадает с самим dispatch")
	}
	d := &Dispatch{
		Adapter: outbound.NewAdapter(Type, tag, nil, nil),
		manager: service.FromContext[adapter.OutboundManager](ctx),
		defTag:  options.Default,
		initial: options.Rules,
	}
	d.table.Store(&compiled{})
	if d.manager != nil {
		d.tryResolveInitial()
	}
	return d, nil
}

func (d *Dispatch) tryResolveInitial() {
	if o, ok := d.manager.Outbound(d.defTag); ok {
		d.def.Store(&o)
	}
	if len(d.initial) > 0 {
		if c, err := d.compile(d.initial); err == nil {
			d.table.Store(c)
		}
	}
}

// Start довершает разрешение для статических конфигов.
func (d *Dispatch) Start(stage adapter.StartStage, _ *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if d.def.Load() == nil {
		o, ok := d.manager.Outbound(d.defTag)
		if !ok {
			return E.New("dispatch ", d.Tag(), ": default не найден: ", d.defTag)
		}
		d.def.Store(&o)
	}
	if len(d.initial) > 0 && len(d.table.Load().rules) == 0 {
		c, err := d.compile(d.initial)
		if err != nil {
			return err
		}
		d.table.Store(c)
	}
	return nil
}

// Dependencies — умолчание и все цели правил: менеджер не даст удалить ни одну из них.
func (d *Dispatch) Dependencies() []string {
	deps := []string{d.defTag}
	return append(deps, d.table.Load().deps...)
}

func (d *Dispatch) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

func norm(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }

func (d *Dispatch) compile(rules []Rule) (*compiled, error) {
	if d.manager == nil {
		return nil, E.New("dispatch ", d.Tag(), ": нет менеджера outbound'ов")
	}
	c := &compiled{rules: make([]Rule, len(rules)), outs: make([]adapter.Outbound, len(rules))}
	seen := map[string]bool{}
	for i, r := range rules {
		if r.Target == "" || r.Target == d.Tag() {
			return nil, E.New("dispatch ", d.Tag(), ": правило ", i, ": недопустимая цель ", r.Target)
		}
		if len(r.Domain)+len(r.DomainSuffix)+len(r.DomainKeyword) == 0 {
			return nil, E.New("dispatch ", d.Tag(), ": правило ", i, " без условий")
		}
		o, ok := d.manager.Outbound(r.Target)
		if !ok {
			return nil, E.New("dispatch ", d.Tag(), ": цель не найдена: ", r.Target)
		}
		nr := Rule{Target: r.Target}
		for _, s := range r.Domain {
			nr.Domain = append(nr.Domain, norm(s))
		}
		for _, s := range r.DomainSuffix {
			nr.DomainSuffix = append(nr.DomainSuffix, strings.TrimPrefix(norm(s), "."))
		}
		for _, s := range r.DomainKeyword {
			nr.DomainKeyword = append(nr.DomainKeyword, strings.ToLower(s))
		}
		c.rules[i], c.outs[i] = nr, o
		if !seen[r.Target] {
			seen[r.Target] = true
			c.deps = append(c.deps, r.Target)
		}
	}
	return c, nil
}

// SetRules атомарно заменяет таблицу правил. Порядок значим: побеждает первое совпавшее правило.
// При ошибке прежняя таблица остаётся. Сериализация с удалением целей — на вызывающем (внутри страховочная проверка).
func (d *Dispatch) SetRules(rules []Rule) error {
	c, err := d.compile(rules)
	if err != nil {
		return err
	}
	old := d.table.Swap(c)
	for i, o := range c.outs { // цель могла быть удалена между поиском и записью (Remove смотрит зависимости под своей блокировкой)
		if cur, ok := d.manager.Outbound(c.rules[i].Target); !ok || cur != o {
			if oc, err := d.compile(old.rules); err == nil {
				d.table.Store(oc)
			} else {
				d.table.Store(&compiled{})
			}
			return E.New("dispatch ", d.Tag(), ": цель удалена во время обновления: ", c.rules[i].Target)
		}
	}
	return nil
}

// Rules возвращает текущую таблицу (копию).
func (d *Dispatch) Rules() []Rule { return append([]Rule(nil), d.table.Load().rules...) }

// Match возвращает тег цели для домена ("" — правила не подошли и пойдёт default).
func (d *Dispatch) Match(host string) string {
	if o := d.pick(host); o != nil && o != d.defaultOut() {
		return o.Tag()
	}
	return ""
}

func (d *Dispatch) defaultOut() adapter.Outbound {
	if p := d.def.Load(); p != nil {
		return *p
	}
	return nil
}

func (d *Dispatch) pick(host string) adapter.Outbound {
	host = norm(host)
	if host != "" {
		c := d.table.Load()
		for i, r := range c.rules {
			if matches(r, host) {
				return c.outs[i]
			}
		}
	}
	return d.defaultOut()
}

func matches(r Rule, host string) bool {
	for _, s := range r.Domain {
		if host == s {
			return true
		}
	}
	for _, s := range r.DomainSuffix {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	for _, s := range r.DomainKeyword {
		if strings.Contains(host, s) {
			return true
		}
	}
	return false
}

func (d *Dispatch) route(ctx context.Context, destination M.Socksaddr) (adapter.Outbound, error) {
	host := ""
	if destination.IsFqdn() {
		host = destination.Fqdn
	} else if md := adapter.ContextFrom(ctx); md != nil {
		host = md.Domain // назначение — IP (TUN): берём домен, определённый по SNI/Host/QUIC (sniff)
	}
	o := d.pick(host)
	if o == nil {
		return nil, E.New("dispatch ", d.Tag(), ": нет outbound'а по умолчанию")
	}
	return o, nil
}

func (d *Dispatch) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	o, err := d.route(ctx, destination)
	if err != nil {
		return nil, err
	}
	return o.DialContext(ctx, network, destination)
}

func (d *Dispatch) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	o, err := d.route(ctx, destination)
	if err != nil {
		return nil, err
	}
	return o.ListenPacket(ctx, destination)
}
