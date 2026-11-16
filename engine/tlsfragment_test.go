package engine

import (
	"crypto/tls"
	"fmt"
	"net"
	"testing"
	"time"
)

// TestTLSFragmentSplitsClientHello: при tls_fragment / tls_record_fragment ClientHello уходит на провод несколькими записями, а не одним куском.
// Проверка нужна потому, что движок принимает параметр и без нарезки (конфиг валиден), а о пользе узнаёшь только по пакетам.
func TestTLSFragmentSplitsClientHello(t *testing.T) {
	for _, opt := range []string{"tls_fragment", "tls_record_fragment"} {
		t.Run(opt, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			target := ln.Addr().(*net.TCPAddr).Port
			got := make(chan []byte, 1)
			segN := make(chan int, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				var all []byte
				segments := 0
				buf := make([]byte, 65535)
				c.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
				for {
					n, err := c.Read(buf)
					all = append(all, buf[:n]...)
					if n > 0 {
						segments++
					}
					if err != nil {
						break
					}
				}
				got <- all
				segN <- segments
			}()
			in := freePort()
			cfg := fmt.Sprintf(`{"log":{"level":"error"},
 "inbounds":[{"type":"mixed","tag":"in","listen":"127.0.0.1","listen_port":%d}],
 "outbounds":[{"type":"direct","tag":"direct"}],
 "route":{"rules":[{"action":"sniff"},{"port":[%d],"outbound":"direct","%s":true}],"final":"direct"}}`, in, target, opt)
			e, err := NewFromJSON("error", []byte(cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			var pc net.Conn
			for i := 0; i < 20; i++ { // вход поднимается асинхронно
				if pc, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", in)); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			pc.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprintf(pc, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", target, target)
			reply := make([]byte, 128)
			if _, err := pc.Read(reply); err != nil {
				t.Fatal(err)
			}
			_ = tls.Client(pc, &tls.Config{ServerName: "blocked.example.com", InsecureSkipVerify: true}).Handshake()
			data, segments := <-got, <-segN
			records := 0
			for b := data; len(b) >= 5 && b[0] == 0x16; {
				n := 5 + int(b[3])<<8 + int(b[4])
				if n > len(b) {
					break
				}
				records++
				b = b[n:]
			}
			// tls_fragment делит пакеты (несколько чтений у приёмника), tls_record_fragment делит записи (несколько записей в данных)
			if (opt == "tls_fragment" && segments < 2) || (opt == "tls_record_fragment" && records < 2) {
				t.Fatalf("%s: ClientHello не разделён: сегментов %d, записей %d", opt, segments, records)
			}
		})
	}
}
