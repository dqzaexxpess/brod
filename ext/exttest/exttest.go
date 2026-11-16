// Package exttest — общий стенд для тестов плагинов-outbound'ов: настоящий outbound.Manager с поддельными узлами,
// которые при дозвоне отдают своё имя. Только для тестов.
package exttest

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

// FakeOptions — опции поддельного узла.
type FakeOptions struct {
	Name string `json:"name"`
}

// Fake — поддельный узел: дозвон возвращает соединение, из которого читается имя узла; соединение живёт до закрытия клиентом.
type Fake struct {
	outbound.Adapter
	name   string
	Dials  atomic.Int64
	Closed atomic.Int64 // сколько соединений закрыто клиентской стороной
	Dest   atomic.Value // последнее назначение (M.Socksaddr)
	// Gate: если задан, дозвон ждёт закрытия канала (имитация долгого установления соединения).
	Gate atomic.Pointer[chan struct{}]
	// Started получает сигнал в начале каждого дозвона (если не nil).
	Started atomic.Pointer[chan struct{}]
}

func newFake(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, o FakeOptions) (adapter.Outbound, error) {
	return &Fake{Adapter: outbound.NewAdapter("fake", tag, []string{"tcp"}, nil), name: o.Name}, nil
}

func (f *Fake) DialContext(_ context.Context, _ string, d M.Socksaddr) (net.Conn, error) {
	f.Dials.Add(1)
	f.Dest.Store(d)
	if st := f.Started.Load(); st != nil {
		select {
		case *st <- struct{}{}:
		default:
		}
	}
	if g := f.Gate.Load(); g != nil {
		<-*g
	}
	client, server := net.Pipe()
	go func() {
		_, _ = server.Write([]byte(f.name))
		_, _ = io.Copy(io.Discard, server)
		f.Closed.Add(1)
		_ = server.Close()
	}()
	return client, nil
}

func (f *Fake) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, io.ErrClosedPipe
}

// Rig — менеджер outbound'ов с зарегистрированными типами.
type Rig struct {
	Ctx context.Context
	Mgr *outbound.Manager
	Reg *outbound.Registry
}

var logger = log.NewNOPFactory().NewLogger("")

// New собирает стенд; register подключает тестируемые плагины в реестр. Первым создаётся узел "default"
// (менеджер считает первый outbound умолчательным, и его нельзя удалить).
func New(t *testing.T, register ...func(*outbound.Registry)) *Rig {
	t.Helper()
	reg := outbound.NewRegistry()
	outbound.Register[FakeOptions](reg, "fake", newFake)
	for _, r := range register {
		r(reg)
	}
	mgr := outbound.NewManager(reg, endpoint.NewManager(logger, include.EndpointRegistry()), "")
	r := &Rig{Ctx: service.ContextWith[adapter.OutboundManager](context.Background(), mgr), Mgr: mgr, Reg: reg}
	r.Fake(t, "default")
	return r
}

// Fake создаёт поддельный узел.
func (r *Rig) Fake(t *testing.T, tag string) *Fake {
	t.Helper()
	if err := r.Mgr.Create(r.Ctx, nil, logger, tag, "fake", &FakeOptions{Name: tag}); err != nil {
		t.Fatal(err)
	}
	o, _ := r.Mgr.Outbound(tag)
	return o.(*Fake)
}

// Create создаёт outbound произвольного типа.
func (r *Rig) Create(tag, typ string, opts any) error {
	return r.Mgr.Create(r.Ctx, nil, logger, tag, typ, opts)
}

// DialName дозванивается через o и возвращает имя ответившего узла и открытое соединение.
func DialName(t *testing.T, o adapter.Outbound, dest string) (string, net.Conn) {
	t.Helper()
	c, err := o.DialContext(context.Background(), "tcp", M.ParseSocksaddr(dest))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n]), c
}
