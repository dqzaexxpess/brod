package prober

import (
	"context"
	"net"
	"testing"
	"time"
)

// serve поднимает TCP-сервер: handler получает принятое соединение.
func serve(t *testing.T, handler func(c net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handler(c)
		}
	}()
	return l.Addr().String()
}

func TestQuickOn(t *testing.T) {
	d := (&net.Dialer{}).DialContext
	run := func(addr string) Result { return quickOn(context.Background(), d, addr) }

	// любой HTTP-ответ — выход пропускает данные (Cloudflare на 1.1.1.1:80 отвечает редиректом)
	ok := serve(t, func(c net.Conn) {
		defer c.Close()
		buf := make([]byte, 256)
		c.Read(buf)
		c.Write([]byte("HTTP/1.1 301 Moved Permanently\r\nLocation: https://1.1.1.1/\r\n\r\n"))
	})
	if r := run(ok); !r.OK || r.Target != "quick" {
		t.Fatalf("HTTP-ответ — успех: %+v", r)
	}

	// не HTTP — провал класса other
	bad := serve(t, func(c net.Conn) { defer c.Close(); c.Write([]byte("SSH-2.0-OpenSSH\r\n")) })
	if r := run(bad); r.OK || r.Class != ClassOther {
		t.Fatalf("не HTTP: %+v", r)
	}

	// соединение закрыто без ответа — провал
	empty := serve(t, func(c net.Conn) { c.Close() })
	if r := run(empty); r.OK {
		t.Fatalf("пустой ответ: %+v", r)
	}

	// порт закрыт — провал
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	if r := run(closed); r.OK {
		t.Fatalf("закрытый порт: %+v", r)
	}

	// молчит — упираемся в таймаут (класс timeout), не висим
	old := QuickTimeout
	QuickTimeout = 300 * time.Millisecond
	defer func() { QuickTimeout = old }()
	hang := serve(t, func(c net.Conn) { time.Sleep(2 * time.Second); c.Close() })
	t0 := time.Now()
	if r := run(hang); r.OK || r.Class != ClassTimeout || time.Since(t0) > time.Second {
		t.Fatalf("зависший ответ: %+v за %v", r, time.Since(t0))
	}
}
