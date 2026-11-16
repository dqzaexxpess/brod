// Package ssr — плагин-outbound ShadowsocksR для sing-box-lx (в самом lx SSR нет).
//
// Подключается без правки ядра: тип "shadowsocksr" регистрируется в реестре outbound'ов (см. ext.OutboundRegistry).
// Реализация протокола — библиотека github.com/v2rayA/shadowsocksR (MIT). Поддерживается только TCP (UDP в SSR-клиенте библиотеки нет).
// SSR считается устаревшим и слабым: узлы с ним помечаются предупреждением, по умолчанию их стоит держать в хвосте очереди.
package ssr

import (
	"context"
	"net"

	shadowsocksr "github.com/v2rayA/shadowsocksR"
	"github.com/v2rayA/shadowsocksR/obfs"
	"github.com/v2rayA/shadowsocksR/protocol"
	"github.com/v2rayA/shadowsocksR/ssr"
	cipher "github.com/v2rayA/shadowsocksR/streamCipher"
	"github.com/v2rayA/shadowsocksR/tools/socks"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// Type — имя типа outbound'а в конфиге.
const Type = "shadowsocksr"

// Options — опции outbound'а shadowsocksr.
type Options struct {
	option.DialerOptions
	option.ServerOptions
	Method        string `json:"method"`
	Password      string `json:"password"`
	Protocol      string `json:"protocol,omitempty"`
	ProtocolParam string `json:"protocol_param,omitempty"`
	Obfs          string `json:"obfs,omitempty"`
	ObfsParam     string `json:"obfs_param,omitempty"`
}

// RegisterOutbound добавляет тип в реестр.
func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, Type, New)
}

// Outbound — клиент SSR.
type Outbound struct {
	outbound.Adapter
	dialer       N.Dialer
	server       M.Socksaddr
	opts         Options
	protocolData any // общие данные протокола между соединениями (счётчики auth_*), как в эталонном клиенте библиотеки
}

var _ adapter.Outbound = (*Outbound)(nil)

// New создаёт outbound и сразу проверяет шифр, obfs и protocol, чтобы ошибка конфигурации была на старте, а не на первом соединении.
func New(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	if _, err := cipher.NewStreamCipher(options.Method, options.Password); err != nil {
		return nil, E.Cause(err, "shadowsocksr: cipher ", options.Method)
	}
	if obfs.NewObfs(options.Obfs) == nil {
		return nil, E.New("shadowsocksr: unsupported obfs: ", options.Obfs)
	}
	if protocol.NewProtocol(options.Protocol) == nil {
		return nil, E.New("shadowsocksr: unsupported protocol: ", options.Protocol)
	}
	d, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
	if err != nil {
		return nil, err
	}
	return &Outbound{
		Adapter:      outbound.NewAdapterWithDialerOptions(Type, tag, []string{N.NetworkTCP}, options.DialerOptions),
		dialer:       d,
		server:       options.ServerOptions.Build(),
		opts:         options,
		protocolData: new(protocol.AuthData),
	}, nil
}

// DialContext открывает TCP-соединение к назначению через SSR-сервер.
func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkTCP {
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	target := socks.ParseAddr(destination.String())
	if target == nil {
		return nil, E.New("shadowsocksr: bad destination ", destination.String())
	}
	sc, err := cipher.NewStreamCipher(o.opts.Method, o.opts.Password)
	if err != nil {
		return nil, err
	}
	raw, err := o.dialer.DialContext(ctx, N.NetworkTCP, o.server)
	if err != nil {
		return nil, err
	}
	c := shadowsocksr.NewSSTCPConn(raw, sc)
	if c.Conn == nil {
		_ = raw.Close()
		return nil, E.New("shadowsocksr: nil connection")
	}
	host, port := o.server.AddrString(), o.server.Port
	c.IObfs = obfs.NewObfs(o.opts.Obfs)
	c.IObfs.SetServerInfo(&ssr.ServerInfo{Host: host, Port: port, TcpMss: 1460, Param: o.opts.ObfsParam})
	c.IProtocol = protocol.NewProtocol(o.opts.Protocol)
	c.IProtocol.SetServerInfo(&ssr.ServerInfo{Host: host, Port: port, TcpMss: 1460, Param: o.opts.ProtocolParam})
	c.IObfs.SetData(c.IObfs.GetData())
	c.IProtocol.SetData(o.protocolData)
	if _, err := c.Write(target); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// ListenPacket: UDP не поддерживается.
func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, E.New("shadowsocksr: UDP is not supported")
}
