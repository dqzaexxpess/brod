package core

import (
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Локальный прокси открывается один раз, держит адрес и отвечает по DNS сети, читая его при каждом запросе.
func TestLocalDNSProxyAnswersFromCurrentSystemDNS(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("нет loopback UDP:", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil {
				continue
			}
			m := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true}, Questions: q.Questions,
				Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body: &dnsmessage.AResource{A: [4]byte{7, 7, 7, 7}}}}}
			b, _ := m.Pack()
			_, _ = pc.WriteTo(b, from)
		}
	}()
	old := SystemDNS
	SystemDNS = func() string { return pc.LocalAddr().String() }
	t.Cleanup(func() { SystemDNS = old })

	host, port, ok := localDNSProxy()
	if !ok {
		t.Skip("прокси не открылся")
	}
	if h2, p2, _ := localDNSProxy(); h2 != host || p2 != port {
		t.Fatal("адрес прокси меняется между вызовами")
	}
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 9, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("a.example."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	b, _ := q.Pack()
	c, err := net.Dial("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var r dnsmessage.Message
	if err := r.Unpack(buf[:n]); err != nil || len(r.Answers) != 1 || r.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{7, 7, 7, 7} {
		t.Fatalf("ответ: %+v %v", r.Answers, err)
	}
}
