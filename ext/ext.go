// Package ext собирает плагины движка: протоколы и транспорты, которых нет в sing-box-lx, подключаются из нашего модуля,
// без правки ядра (в ядре остаются только минимальные патчи опций, см. patches/lx).
package ext

import (
	"context"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/include"

	"brod/ext/dispatch"
	_ "brod/ext/rawhttp" // транспорт raw с HTTP-обфускацией (регистрируется в init)
	"brod/ext/ssr"
	switchout "brod/ext/switch"
)

// OutboundRegistry — реестр outbound'ов lx плюс наши плагины (shadowsocksr, switch, dispatch).
func OutboundRegistry() *outbound.Registry {
	r := include.OutboundRegistry()
	ssr.RegisterOutbound(r)
	switchout.RegisterOutbound(r)
	dispatch.RegisterOutbound(r)
	return r
}

// Context — аналог include.Context, но с расширенным реестром outbound'ов.
func Context(ctx context.Context) context.Context {
	return box.Context(ctx, include.InboundRegistry(), OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(),
		include.ServiceRegistry(), include.CertificateProviderRegistry())
}
