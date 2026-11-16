// Package switchout — плагин-outbound "switch": прокси, пересылающий всё на текущую цель и меняющий цель атомарно.
//
// Нужен для P1 («липкий» выбор): активный узел меняется без пересоздания чего-либо, а менеджер движка не даёт удалить
// outbound, на который сейчас смотрит switch (Dependencies возвращает текущую цель). Селектор из lx для этого не годится:
// его состав фиксируется при создании.
//
// Цель может быть любым outbound'ом или endpoint'ом менеджера (узел, WireGuard/AWG WARP, direct, другой switch).
// Цель разрешается лениво, через менеджер, потому что Manager.Create после старта движка стадии запуска не гоняет.
package switchout

import (
	"context"
	"io"
	"net"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// Type — имя типа outbound'а в конфиге.
const Type = "switch"

// maxDepth ограничивает цепочку switch → switch → …, чтобы цикл не завис в SetTarget.
const maxDepth = 16

// Options — опции outbound'а switch.
type Options struct {
	// Target — тег начальной цели (может быть пустым: до первого SetTarget соединения отклоняются).
	Target string `json:"target,omitempty"`
	// InterruptExisting: при смене цели рвать уже открытые соединения (по умолчанию живут до естественного конца).
	InterruptExisting bool `json:"interrupt_existing,omitempty"`
}

// RegisterOutbound добавляет тип в реестр.
func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, Type, New)
}

// Switch — outbound с переключаемой целью.
type Switch struct {
	outbound.Adapter
	ctx       context.Context
	manager   adapter.OutboundManager
	opts      Options
	target    atomic.Pointer[adapter.Outbound]
	interrupt *interrupt.Group
}

var (
	_ adapter.OutboundGroup = (*Switch)(nil)
	_ adapter.Referrer      = (*Switch)(nil)
)

// New создаёт switch. Начальная цель разрешается при создании, если она уже есть в менеджере, иначе при старте движка
// (так бывает, когда switch описан в статическом конфиге раньше своей цели).
func New(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	s := &Switch{
		Adapter:   outbound.NewAdapter(Type, tag, nil, nil),
		ctx:       ctx,
		manager:   service.FromContext[adapter.OutboundManager](ctx),
		opts:      options,
		interrupt: interrupt.NewGroup(),
	}
	if options.Target != "" {
		if options.Target == tag {
			return nil, E.New("switch ", tag, ": цель совпадает с самим switch")
		}
		if s.manager != nil {
			if t, ok := s.manager.Outbound(options.Target); ok {
				s.target.Store(&t)
			}
		}
	}
	return s, nil
}

// Start довершает разрешение начальной цели для статических конфигов.
func (s *Switch) Start(stage adapter.StartStage, _ *adapter.Scope) error {
	if stage != adapter.StartStateStart || s.opts.Target == "" || s.target.Load() != nil {
		return nil
	}
	if s.manager == nil {
		return E.New("switch ", s.Tag(), ": нет менеджера outbound'ов")
	}
	t, ok := s.manager.Outbound(s.opts.Target)
	if !ok {
		return E.New("switch ", s.Tag(), ": цель не найдена: ", s.opts.Target)
	}
	s.target.Store(&t)
	return nil
}

// Dependencies — текущая цель: по ней менеджер отказывает Remove, пока switch на неё смотрит.
func (s *Switch) Dependencies() []string {
	if t := s.target.Load(); t != nil {
		return []string{(*t).Tag()}
	}
	return nil
}

// References нужен lx для графа зависимостей групп.
func (s *Switch) References() []string { return s.Dependencies() }

// Current возвращает тег текущей цели ("" — цели нет).
func (s *Switch) Current() string {
	if t := s.target.Load(); t != nil {
		return (*t).Tag()
	}
	return ""
}

// SetTarget переключает цель на outbound с данным тегом. Атомарно: соединения, открываемые параллельно, идут
// либо на старую, либо на новую цель. При ошибке цель не меняется.
func (s *Switch) SetTarget(tag string) error {
	if s.manager == nil {
		return E.New("switch ", s.Tag(), ": нет менеджера outbound'ов")
	}
	if tag == "" {
		return E.New("switch ", s.Tag(), ": пустая цель")
	}
	next, ok := s.manager.Outbound(tag)
	if !ok {
		return E.New("switch ", s.Tag(), ": цель не найдена: ", tag)
	}
	if err := s.checkLoop(next); err != nil {
		return err
	}
	old := s.target.Swap(&next)
	// Remove может проскочить между поиском и записью: он проверяет зависимости и удаляет под одной блокировкой менеджера,
	// поэтому если цели уже нет, это видно здесь, после записи (иначе Remove увидел бы новую зависимость и отказал).
	if cur, still := s.manager.Outbound(tag); !still || cur != next {
		// Откат на прежнюю цель, если и она ещё в менеджере (за это время её могли удалить, ведь зависимость уже была на новую);
		// иначе switch остаётся без цели, и дозвоны честно падают. Владелец (core) сериализует SetTarget и Remove, так что
		// это страховка, а не рабочий путь.
		if old != nil {
			if prev, ok := s.manager.Outbound((*old).Tag()); ok && prev == *old {
				s.target.Store(old)
				return E.New("switch ", s.Tag(), ": цель удалена во время переключения: ", tag)
			}
		}
		s.target.Store(nil)
		return E.New("switch ", s.Tag(), ": цель удалена во время переключения: ", tag, ", прежней цели тоже нет")
	}
	if old == nil || (*old) != next {
		if s.opts.InterruptExisting {
			s.interrupt.Interrupt(true) // true: рвать и внешние (пользовательские) соединения, как interrupt_exist_connections у selector
		}
	}
	return nil
}

// Clear снимает цель: до следующего SetTarget дозвоны отклоняются («цель не выбрана»), а Remove прежней цели снова разрешён. Нужен правилам
// маршрутизации «через страну», когда подходящего узла нет и сказано блокировать, а не уводить трафик в обход.
func (s *Switch) Clear() {
	old := s.target.Swap(nil)
	if old != nil && s.opts.InterruptExisting {
		s.interrupt.Interrupt(true)
	}
}

// checkLoop отклоняет цель, через цепочку switch'ей ведущую обратно сюда.
func (s *Switch) checkLoop(next adapter.Outbound) error {
	cur := next
	for i := 0; i < maxDepth; i++ {
		sw, ok := cur.(*Switch)
		if !ok {
			return nil
		}
		if sw == s {
			return E.New("switch ", s.Tag(), ": цикл через ", next.Tag())
		}
		t := sw.target.Load()
		if t == nil {
			return nil
		}
		cur = *t
	}
	return E.New("switch ", s.Tag(), ": слишком длинная цепочка switch'ей")
}

// Network — сети текущей цели (до выбора — обе).
func (s *Switch) Network() []string {
	if t := s.target.Load(); t != nil {
		return (*t).Network()
	}
	return []string{N.NetworkTCP, N.NetworkUDP}
}

// All и Selected делают switch группой для lx: учёт соединений, отображение выбранного узла.
func (s *Switch) All() []string { return s.Dependencies() }

func (s *Switch) Selected(string) adapter.Outbound {
	if t := s.target.Load(); t != nil {
		return *t
	}
	return nil
}

func (s *Switch) AttachConnection(closer io.Closer) func() { return s.interrupt.Add(closer, true) }

var (
	errNoTarget = E.New("switch: цель не выбрана")
	errSwitched = E.New("switch: цель сменилась во время установки соединения")
)

func (s *Switch) pick(ctx context.Context) (picked *adapter.Outbound, leaf adapter.Outbound, err error) {
	t := s.target.Load()
	if t == nil {
		return nil, nil, errNoTarget
	}
	leaf, err = adapter.ResolveChainLeaf(ctx, *t)
	return t, leaf, err
}

// switchedDuring: цель сменилась, пока шёл дозвон. Такое соединение принадлежит прежней цели; при interrupt_existing оно
// должно закрыться, иначе проскочит мимо Interrupt (группа его ещё не знала) и повиснет на уже брошенном узле.
func (s *Switch) switchedDuring(picked *adapter.Outbound) bool {
	if !s.opts.InterruptExisting {
		return false
	}
	cur := s.target.Load()
	return cur == nil || picked == nil || *cur != *picked
}

func (s *Switch) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	picked, target, err := s.pick(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := target.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	if s.switchedDuring(picked) {
		_ = conn.Close()
		return nil, errSwitched
	}
	return s.interrupt.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Switch) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	picked, target, err := s.pick(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := target.ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	if s.switchedDuring(picked) {
		_ = conn.Close()
		return nil, errSwitched
	}
	return s.interrupt.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}
