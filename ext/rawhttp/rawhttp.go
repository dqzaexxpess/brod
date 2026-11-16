// Package rawhttp — плагин для sing-box-lx: клиентский транспорт "raw" с HTTP-обфускацией заголовка (`headerType: http` в Xray/V2Ray).
//
// Подключается без правки ядра: init() регистрирует конструктор в публичном реестре transport/v2ray; в ядре нужен лишь тип опций
// (patches/lx/0002-raw-transport-options.patch). Достаточно импортировать пакет: import _ "brod/ext/rawhttp".
//
// Формат на проводе (совместим с Xray): клиент перед первыми данными пишет фальшивый HTTP-запрос
// «METHOD path HTTP/1.1» + заголовки + пустая строка; сервер перед первыми данными отвечает фальшивым HTTP-ответом,
// который клиент отбрасывает до пустой строки. Дальше поток идёт как обычно. Это маскировка под HTTP, не шифрование.
package rawhttp

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2ray"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func init() {
	v2ray.RegisterClient(C.V2RayTransportTypeRaw, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		return NewClient(dialer, serverAddr, options.RawOptions, tlsConfig)
	})
}

// maxResponseHeader ограничивает буферизацию заголовка ответа: сервер без пустой строки не должен съедать память.
const maxResponseHeader = 16 << 10

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/53.0.2785.143 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 10_0_2 like Mac OS X) AppleWebKit/601.1 (KHTML, like Gecko) CriOS/53.0.2785.109 Mobile/14A456 Safari/601.1.46",
}

// Client — клиентский транспорт raw.
type Client struct {
	dialer      N.Dialer
	serverAddr  M.Socksaddr
	opts        option.V2RayRawOptions
	hostDefault string
}

var _ adapter.V2RayClientTransport = (*Client)(nil)

// NewClient создаёт транспорт. Для TLS/Reality (tlsConfig != nil) шифрование поднимается до обфускации: заголовок идёт внутри TLS.
func NewClient(dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayRawOptions, tlsConfig tls.Config) (*Client, error) {
	switch strings.ToLower(options.HeaderType) {
	case "", "none", "http":
	default:
		return nil, errors.New("raw: неизвестный header_type " + options.HeaderType)
	}
	hostDefault := serverAddr.String()
	if tlsConfig != nil {
		if sn := tlsConfig.ServerName(); sn != "" {
			hostDefault = sn
		}
		dialer = tls.NewDialer(dialer, tlsConfig)
	}
	return &Client{dialer: dialer, serverAddr: serverAddr, opts: options, hostDefault: hostDefault}, nil
}

// DialContext соединяется с сервером и (при header_type=http) оборачивает соединение в обфускацию.
func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	conn, err := c.dialer.DialContext(ctx, N.NetworkTCP, c.serverAddr)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(c.opts.HeaderType) != "http" {
		return conn, nil
	}
	return &obfsConn{Conn: conn, header: c.requestHeader()}, nil
}

// Close ничего не держит: соединения закрывает вызывающая сторона.
func (c *Client) Close() error { return nil }

func pick(list []string, def string) string {
	var vals []string
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return def
	}
	return vals[rand.IntN(len(vals))]
}

func (c *Client) requestHeader() []byte {
	method := c.opts.Method
	if method == "" {
		method = "GET"
	}
	path := pick(c.opts.Path, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := pick(c.opts.Host, c.hostDefault)
	var b bytes.Buffer
	b.WriteString(method + " " + path + " HTTP/1.1\r\n")
	b.WriteString("Host: " + host + "\r\n")
	have := map[string]bool{"host": true}
	for k, vs := range c.opts.Headers {
		if len(vs) == 0 || strings.EqualFold(k, "host") {
			continue
		}
		have[strings.ToLower(k)] = true
		b.WriteString(k + ": " + pick(vs, "") + "\r\n")
	}
	defaults := [][2]string{
		{"User-Agent", userAgents[rand.IntN(len(userAgents))]},
		{"Accept-Encoding", "gzip, deflate"},
		{"Connection", "keep-alive"},
		{"Pragma", "no-cache"},
	}
	for _, d := range defaults {
		if !have[strings.ToLower(d[0])] {
			b.WriteString(d[0] + ": " + d[1] + "\r\n")
		}
	}
	b.WriteString("\r\n")
	return b.Bytes()
}

// obfsConn: заголовок запроса дописывается перед первой записью, заголовок ответа срезается с первого чтения.
type obfsConn struct {
	net.Conn
	header []byte

	wmu      sync.Mutex
	wrote    bool
	rmu      sync.Mutex
	readDone bool
	pending  []byte // данные после заголовка ответа, полученные в том же чтении
}

func (o *obfsConn) Write(p []byte) (int, error) {
	o.wmu.Lock()
	if !o.wrote {
		o.wrote = true
		out := make([]byte, 0, len(o.header)+len(p))
		out = append(append(out, o.header...), p...)
		o.wmu.Unlock()
		if _, err := o.Conn.Write(out); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	o.wmu.Unlock()
	return o.Conn.Write(p)
}

func (o *obfsConn) Read(p []byte) (int, error) {
	o.rmu.Lock()
	defer o.rmu.Unlock()
	if !o.readDone {
		var acc []byte
		tmp := make([]byte, 4096)
		for {
			n, err := o.Conn.Read(tmp)
			acc = append(acc, tmp[:n]...)
			if i := bytes.Index(acc, []byte("\r\n\r\n")); i >= 0 {
				o.pending = append([]byte(nil), acc[i+4:]...)
				o.readDone = true
				break
			}
			if err != nil {
				return 0, err
			}
			if len(acc) > maxResponseHeader {
				return 0, errors.New("raw: заголовок ответа не завершён (нет пустой строки)")
			}
		}
	}
	if len(o.pending) > 0 {
		n := copy(p, o.pending)
		o.pending = o.pending[n:]
		return n, nil
	}
	return o.Conn.Read(p)
}
