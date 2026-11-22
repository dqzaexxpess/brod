package dnsfallback

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeDNS отвечает на UDP-запросы функцией h (nil — молчит).
func fakeDNS(t *testing.T, h func(q dnsmessage.Message) []byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("нет loopback UDP:", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil {
				continue
			}
			if resp := h(q); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func answer(q dnsmessage.Message, rcode dnsmessage.RCode, ip [4]byte) []byte {
	m := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: q.ID, Response: true, RCode: rcode, RecursionAvailable: true},
		Questions: q.Questions,
	}
	if rcode == dnsmessage.RCodeSuccess && q.Questions[0].Type == dnsmessage.TypeA {
		m.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: ip},
		}}
	}
	b, _ := m.Pack()
	return b
}

func fakeDoH(t *testing.T, hits *atomic.Int32, ip [4]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		var q dnsmessage.Message
		if q.Unpack(body) != nil {
			http.Error(w, "bad", 400)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(answer(q, dnsmessage.RCodeSuccess, ip))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func lookup(t *testing.T, r *net.Resolver, name string) ([]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return r.LookupHost(ctx, name)
}

func TestPrimaryAnswers(t *testing.T) {
	addr := fakeDNS(t, func(q dnsmessage.Message) []byte { return answer(q, dnsmessage.RCodeSuccess, [4]byte{10, 0, 0, 1}) })
	var hits atomic.Int32
	doh := fakeDoH(t, &hits, [4]byte{10, 0, 0, 2})
	r := Resolver(func() string { return addr }, []string{doh})
	got, err := lookup(t, r, "example.org")
	if err != nil || len(got) == 0 || got[0] != "10.0.0.1" {
		t.Fatalf("получено %v, %v", got, err)
	}
	if hits.Load() != 0 {
		t.Fatal("DoH вызван при рабочем DNS сети")
	}
}

func TestNXDomainGoesToDoH(t *testing.T) {
	addr := fakeDNS(t, func(q dnsmessage.Message) []byte { return answer(q, dnsmessage.RCodeNameError, [4]byte{}) })
	var hits atomic.Int32
	doh := fakeDoH(t, &hits, [4]byte{10, 0, 0, 2})
	r := Resolver(func() string { return addr }, []string{doh})
	got, err := lookup(t, r, "cdn.example.org")
	if err != nil || len(got) == 0 || got[0] != "10.0.0.2" {
		t.Fatalf("получено %v, %v", got, err)
	}
}

func TestSilentPrimaryGoesToDoH(t *testing.T) {
	addr := fakeDNS(t, func(dnsmessage.Message) []byte { return nil })
	var hits atomic.Int32
	doh := fakeDoH(t, &hits, [4]byte{10, 0, 0, 2})
	r := Resolver(func() string { return addr }, []string{doh})
	got, err := lookup(t, r, "cdn.example.org")
	if err != nil || len(got) == 0 || got[0] != "10.0.0.2" {
		t.Fatalf("получено %v, %v", got, err)
	}
}

func TestNXDomainKeptWithoutDoH(t *testing.T) {
	addr := fakeDNS(t, func(q dnsmessage.Message) []byte { return answer(q, dnsmessage.RCodeNameError, [4]byte{}) })
	r := Resolver(func() string { return addr }, nil)
	_, err := lookup(t, r, "nonexistent.example.org")
	var de *net.DNSError
	if !errors.As(err, &de) || !de.IsNotFound {
		t.Fatalf("ожидалось «не найдено», получено %v", err)
	}
}

func TestFallbackToDoH(t *testing.T) {
	var hits atomic.Int32
	doh := fakeDoH(t, &hits, [4]byte{10, 0, 0, 2})
	// DNS сети не задан: сразу DoH
	r := Resolver(func() string { return "" }, []string{doh})
	got, err := lookup(t, r, "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "10.0.0.2" {
		t.Fatalf("получено %v", got)
	}
	if hits.Load() == 0 {
		t.Fatal("DoH не вызывался")
	}
}

func TestNegativeKeptWhenNoFallback(t *testing.T) {
	r := Resolver(func() string { return "" }, nil)
	if _, err := lookup(t, r, "example.org"); err == nil {
		t.Fatal("ожидалась ошибка без единого ответа")
	}
}

func TestAAAAEmptyIsAcceptable(t *testing.T) {
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 9}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.org."), Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}}}
	qb, _ := q.Pack()
	if !acceptable(qb, answer(q, dnsmessage.RCodeSuccess, [4]byte{})) {
		t.Fatal("пустой AAAA с NOERROR должен приниматься без запасного пути")
	}
}

// Локальный сервер отвечает ответом сети, а на пустой ответ и молчание сети — ответом DoH.
func TestServeAnswersViaPrimaryThenDoH(t *testing.T) {
	var hits atomic.Int32
	doh := fakeDoH(t, &hits, [4]byte{9, 9, 9, 9})
	query := func(srv string, name string) dnsmessage.Message {
		t.Helper()
		qn := dnsmessage.MustNewName(name)
		m := dnsmessage.Message{Header: dnsmessage.Header{ID: 0x4242, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: qn, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
		b, _ := m.Pack()
		c, err := net.Dial("udp", srv)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(8 * time.Second))
		if _, err := c.Write(b); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("нет ответа: %v", err)
		}
		var r dnsmessage.Message
		if err := r.Unpack(buf[:n]); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// сеть отвечает нормально: DoH не нужен
	good := fakeDNS(t, func(q dnsmessage.Message) []byte { return answer(q, dnsmessage.RCodeSuccess, [4]byte{1, 2, 3, 4}) })
	srv, stop, err := Serve(func() string { return good }, []string{doh})
	if err != nil {
		t.Skip("нет loopback UDP:", err)
	}
	defer stop()
	r := query(srv, "ok.example.")
	if len(r.Answers) != 1 || r.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{1, 2, 3, 4} || hits.Load() != 0 {
		t.Fatalf("ответ сети: %+v, DoH вызван %d раз", r.Answers, hits.Load())
	}
	// сеть отдаёт пустой NOERROR: ответ берётся у DoH
	emptyAns := fakeDNS(t, func(q dnsmessage.Message) []byte {
		m := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true}, Questions: q.Questions}
		b, _ := m.Pack()
		return b
	})
	srv2, stop2, err := Serve(func() string { return emptyAns }, []string{doh})
	if err != nil {
		t.Fatal(err)
	}
	defer stop2()
	r = query(srv2, "node.example.")
	if len(r.Answers) != 1 || r.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{9, 9, 9, 9} {
		t.Fatalf("пустой ответ сети не заменён DoH: %+v", r.Answers)
	}
	// сеть молчит
	silent := fakeDNS(t, func(dnsmessage.Message) []byte { return nil })
	srv3, stop3, err := Serve(func() string { return silent }, []string{doh})
	if err != nil {
		t.Fatal(err)
	}
	defer stop3()
	r = query(srv3, "quiet.example.")
	if len(r.Answers) != 1 || r.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{9, 9, 9, 9} {
		t.Fatalf("молчание сети не заменено DoH: %+v", r.Answers)
	}
}

// Локальный сервер переживает потерянный сокет: после закрытия сокета снаружи (как при ошибке чтения) он пересоздаётся на том же порту.
func TestServeRecreatesSocketAfterReadFailure(t *testing.T) {
	good := fakeDNS(t, func(q dnsmessage.Message) []byte { return answer(q, dnsmessage.RCodeSuccess, [4]byte{1, 2, 3, 4}) })
	s, err := serve(func() string { return good }, nil)
	if err != nil {
		t.Skip("нет loopback UDP:", err)
	}
	defer s.stop()
	ask := func() error {
		qn := dnsmessage.MustNewName("x.example.")
		m := dnsmessage.Message{Header: dnsmessage.Header{ID: 7, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: qn, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
		b, _ := m.Pack()
		c, err := net.Dial("udp", s.addr)
		if err != nil {
			return err
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(4 * time.Second))
		if _, err := c.Write(b); err != nil {
			return err
		}
		buf := make([]byte, 4096)
		_, err = c.Read(buf)
		return err
	}
	if err := ask(); err != nil {
		t.Fatalf("до сбоя: %v", err)
	}
	_ = s.conn().Close() // сокет пропал
	time.Sleep(600 * time.Millisecond)
	if err := ask(); err != nil {
		t.Fatalf("после потери сокета ответа нет: %v", err)
	}
}
