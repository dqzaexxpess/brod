// Package dnsfallback — резолвер Go с запасным путём через DoH на IP-адрес.
//
// DNS сети (особенно мобильной) отвечает на часть имён отказом или молчит; без запасного пути прямой HTTPS ядра (проверка обновлений,
// списки маршрутизации, импорт профиля) падает с «no such host». Резолвер Go (PreferGo) ходит на DNS сети по UDP; если ответа нет
// или он отрицательный, тот же запрос уходит в DoH (RFC 8484) на адреса, не требующие DNS: имя хоста в URL — IP.
package dnsfallback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DefaultDoH — запасные DoH-серверы (сертификаты выданы на IP-адреса).
var DefaultDoH = []string{
	"https://1.1.1.1/dns-query",
	"https://8.8.8.8/dns-query",
	"https://9.9.9.9/dns-query",
}

const (
	primaryTimeout = 2500 * time.Millisecond
	dohTimeout     = 4 * time.Second
	maxDNSMessage  = 4096
)

// Resolver возвращает резолвер: основной DNS даёт primary() (адрес без порта; пусто — сразу DoH), запасной — серверы doh.
func Resolver(primary func() string, doh []string) *net.Resolver {
	f := &fallback{primary: primary, doh: doh, hc: &http.Client{
		Timeout:   dohTimeout,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: dohTimeout},
	}}
	return &net.Resolver{PreferGo: true, Dial: f.dial}
}

type fallback struct {
	primary func() string
	doh     []string
	hc      *http.Client
}

func (f *fallback) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	return &conn{f: f, ctx: ctx, replies: make(chan []byte, 1)}, nil
}

// conn изображает потоковое соединение с DNS-сервером (резолвер Go в этом случае шлёт и ждёт сообщения с префиксом длины).
type conn struct {
	f       *fallback
	ctx     context.Context
	replies chan []byte
	mu      sync.Mutex
	buf     bytes.Buffer // принятое от резолвера, ещё не разобранное
	rd      bytes.Buffer // ответ, ещё не прочитанный резолвером
	closed  bool
	dl      time.Time
}

func (c *conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	c.buf.Write(p)
	for c.buf.Len() >= 2 {
		n := int(c.buf.Bytes()[0])<<8 | int(c.buf.Bytes()[1])
		if c.buf.Len() < 2+n {
			break
		}
		q := append([]byte(nil), c.buf.Bytes()[2:2+n]...)
		c.buf.Next(2 + n)
		go func() {
			resp := c.f.exchange(c.ctx, q)
			select {
			case c.replies <- resp:
			default:
			}
		}()
	}
	return len(p), nil
}

func (c *conn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.rd.Len() > 0 {
		defer c.mu.Unlock()
		return c.rd.Read(p)
	}
	dl := c.dl
	c.mu.Unlock()
	var timeout <-chan time.Time
	if !dl.IsZero() {
		t := time.NewTimer(time.Until(dl))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case resp := <-c.replies:
		if resp == nil {
			return 0, io.EOF
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.rd.Write([]byte{byte(len(resp) >> 8), byte(len(resp))})
		c.rd.Write(resp)
		return c.rd.Read(p)
	case <-timeout:
		return 0, timeoutErr{}
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	}
}

func (c *conn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *conn) SetDeadline(t time.Time) error     { return c.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(time.Time) error  { return nil }
func (c *conn) LocalAddr() net.Addr               { return &net.TCPAddr{} }
func (c *conn) RemoteAddr() net.Addr              { return &net.TCPAddr{} }
func (c *conn) SetReadDeadline(t time.Time) error { c.mu.Lock(); c.dl = t; c.mu.Unlock(); return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// exchange отвечает на один запрос: сначала DNS сети, при неудаче DoH. Если запасной путь тоже ничего не дал, возвращается исходный
// отрицательный ответ сети (резолвер Go сообщит «no such host», а не обрыв). nil — ответа нет вовсе.
func (f *fallback) exchange(ctx context.Context, q []byte) []byte {
	var first []byte
	if addr := f.primary(); addr != "" {
		if resp, err := udpExchange(ctx, withPort(addr), q); err == nil {
			if acceptable(q, resp) {
				return resp
			}
			first = resp
		}
	}
	for _, u := range f.doh {
		if resp, err := f.dohExchange(ctx, u, q); err == nil && acceptable(q, resp) {
			return resp
		}
	}
	return first
}

// withPort добавляет порт 53 к адресу без порта (с портом оставляет как есть: удобно в тестах).
func withPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, "53")
}

func udpExchange(ctx context.Context, addr string, q []byte) ([]byte, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(ctx, primaryTimeout)
	defer cancel()
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	dl, _ := ctx.Deadline()
	_ = c.SetDeadline(dl)
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, maxDNSMessage)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= 2 && buf[0] == q[0] && buf[1] == q[1] { // тот же идентификатор запроса
			return append([]byte(nil), buf[:n]...), nil
		}
	}
}

func (f *fallback) dohExchange(ctx context.Context, url string, q []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(q))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("DoH: " + resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDNSMessage))
}

// acceptable — ответ годится, если он на этот запрос, без ошибки сервера и (для A) не пустой: пустой A у настоящего имени почти не бывает,
// а пустой AAAA — норма (нет IPv6), его запасным путём не перепроверяем.
func acceptable(q, resp []byte) bool {
	if len(resp) < 12 || len(q) < 12 || resp[0] != q[0] || resp[1] != q[1] {
		return false
	}
	var m dnsmessage.Message
	if m.Unpack(resp) != nil || m.RCode != dnsmessage.RCodeSuccess {
		return false
	}
	var qm dnsmessage.Message
	if qm.Unpack(q) != nil || len(qm.Questions) == 0 {
		return false
	}
	if qm.Questions[0].Type == dnsmessage.TypeA && len(m.Answers) == 0 {
		return false
	}
	return true
}
