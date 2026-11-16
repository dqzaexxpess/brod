package tgproxy

import (
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSecretsAndLinks(t *testing.T) {
	raw16 := "00112233445566778899aabbccddeeff"
	cases := []struct {
		link, server, mode, secret string
		port                       int
	}{
		{"https://t.me/proxy?server=Host.Example.&port=443&secret=" + raw16, "host.example", ModePlain, raw16, 443}, // точка на конце, регистр
		{"tg://proxy?server=h&port=1&secret=dd" + raw16, "h", ModeSecure, "dd" + raw16, 1},
		{"http://t.me/proxy?server=h&port=2&secret=ee" + raw16 + hex.EncodeToString([]byte("example.com")), "h", ModeFakeTLS, "ee" + raw16 + hex.EncodeToString([]byte("example.com")), 2},
		{"https://t.me/proxy?server=h&port=3&secret=ABEiM0RVZneImaq7zN3u_w", "h", ModePlain, raw16, 3},    // base64url без добивки
		{"https://t.me/proxy?server=h&port=4&secret=ABEiM0RVZneImaq7zN3u/w==", "h", ModePlain, raw16, 4},  // обычный base64 с добивкой
		{"https://t.me/proxy?server=h&amp;port=5&amp;secret=" + raw16, "h", ModePlain, raw16, 5},          // HTML-экранирование
		{"https://t.me/proxy?server=h&port=6&secret=3d" + raw16[2:], "h", ModePlain, "3d" + raw16[2:], 6}, // обычный hex, первый байт не dd/ee
	}
	for _, c := range cases {
		p, err := Parse(c.link)
		if err != nil || p.Server != c.server || p.Mode() != c.mode || p.Secret != c.secret || p.Port != c.port || p.Kind != KindMTProto {
			t.Errorf("%s -> %+v %v", c.link, p, err)
		}
	}
	s, err := Parse("tg://socks?server=s.example&port=1080&user=u&pass=p")
	if err != nil || s.Kind != KindSocks5 || s.User != "u" || s.Pass != "p" || s.Mode() != "" {
		t.Fatalf("socks: %+v %v", s, err)
	}
	for _, bad := range []string{"vless://x@y:1", "tg://proxy?server=&port=1&secret=" + raw16, "tg://proxy?server=h&port=0&secret=" + raw16,
		"tg://proxy?server=h&port=70000&secret=" + raw16, "tg://proxy?server=h&port=1", "tg://proxy?server=h&port=1&secret=zz", "tg://proxy?server=h&port=1&secret=0011"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("принято %q", bad)
		}
	}
	// ссылка для открытия разбирается обратно в тот же прокси, ID стабилен
	a, _ := Parse("https://t.me/proxy?server=Host.Example.&port=443&secret=" + raw16)
	b, err := Parse(a.Link())
	if err != nil || a.ID() != b.ID() || !strings.HasPrefix(a.Link(), "tg://proxy?") {
		t.Fatalf("круг: %+v %+v %v", a, b, err)
	}
	if s.Link() != "tg://socks?pass=p&port=1080&server=s.example&user=u" {
		t.Fatalf("ссылка socks: %s", s.Link())
	}
}

func TestExtractLinks(t *testing.T) {
	body := `<a href="https://t.me/proxy?server=a.example&amp;port=443&amp;secret=00112233445566778899aabbccddeeff">x</a>
текст tg://proxy?server=b.example&port=1&secret=dd00112233445566778899aabbccddeeff, ещё (https://t.me/socks?server=c.example&port=2).
повтор https://t.me/proxy?server=a.example&port=443&secret=00112233445566778899aabbccddeeff vless://aaa@b:1`
	got := ExtractLinks([]byte(body))
	if len(got) != 3 {
		t.Fatalf("%d ссылок: %v", len(got), got)
	}
	for _, l := range got {
		if _, err := Parse(l); err != nil {
			t.Errorf("не разбирается %q: %v", l, err)
		}
	}
}

// ---- локальные серверы, повторяющие протокол ----

type obfServer struct{ enc, dec cipher.Stream }

// acceptObf читает 64-байтный заголовок obfuscated2 и строит потоки сервера (зеркало клиентских).
func acceptObf(c io.Reader, secret []byte) (*obfServer, [4]byte, error) {
	var h [64]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return nil, [4]byte{}, err
	}
	key, iv := append([]byte(nil), h[8:40]...), h[40:56]
	r := rev(h[8:56])
	skey, siv := append([]byte(nil), r[:32]...), r[32:48]
	if len(secret) > 0 {
		k := sha256.Sum256(append(append([]byte(nil), key...), secret...))
		key = k[:]
		k2 := sha256.Sum256(append(append([]byte(nil), skey...), secret...))
		skey = k2[:]
	}
	dec := ctr(key, iv) // клиент→сервер
	plain := make([]byte, 64)
	dec.XORKeyStream(plain, h[:]) // последние 8 байт расшифрованы, первые 56 «испорчены» — берём только тег
	var tag [4]byte
	copy(tag[:], plain[56:60])
	return &obfServer{enc: ctr(skey, siv), dec: dec}, tag, nil
}

// answerPQ читает кадр с req_pq_multi и отвечает resPQ (или кодом ошибки транспорта, если errCode != 0).
func answerPQ(rw io.ReadWriter, o *obfServer, tag [4]byte, errCode int32) error {
	readN := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(rw, b); err != nil {
			return nil
		}
		o.dec.XORKeyStream(b, b)
		return b
	}
	var n int
	if tag == tagAbridged {
		b := readN(1)
		if b == nil {
			return io.ErrUnexpectedEOF
		}
		n = int(b[0]) * 4
	} else {
		b := readN(4)
		if b == nil {
			return io.ErrUnexpectedEOF
		}
		n = int(binary.LittleEndian.Uint32(b))
	}
	body := readN(n)
	if body == nil || len(body) != 40 || binary.LittleEndian.Uint32(body[20:24]) != 0xbe7e8ef1 {
		return io.ErrUnexpectedEOF
	}
	var payload []byte
	if errCode != 0 {
		payload = binary.LittleEndian.AppendUint32(nil, uint32(errCode))
	} else {
		payload = append(payload, make([]byte, 8)...)
		payload = binary.LittleEndian.AppendUint64(payload, 1<<32)
		payload = binary.LittleEndian.AppendUint32(payload, 20)
		payload = binary.LittleEndian.AppendUint32(payload, 0x05162463)
		payload = append(payload, body[24:40]...)
	}
	var frame []byte
	if tag == tagAbridged {
		frame = append([]byte{byte(len(payload) / 4)}, payload...)
	} else {
		frame = binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))
		frame = append(frame, payload...)
	}
	o.enc.XORKeyStream(frame, frame)
	_, err := rw.Write(frame)
	return err
}

func listen(t *testing.T, handle func(c net.Conn)) (host string, port int) {
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
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	a := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func mtServer(t *testing.T, secret []byte, errCode int32) (string, int) {
	return listen(t, func(c net.Conn) {
		o, tag, err := acceptObf(c, secret)
		if err != nil {
			return
		}
		_ = answerPQ(c, o, tag, errCode)
	})
}

var direct = (&net.Dialer{}).DialContext

func probe(p Proxy) Result { return Probe(context.Background(), p, direct, 3*time.Second) }

func TestProbePlainAndSecure(t *testing.T) {
	secret, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	host, port := mtServer(t, secret, 0)
	plain := Proxy{Kind: KindMTProto, Server: host, Port: port, Secret: hex.EncodeToString(secret)}
	if r := probe(plain); !r.OK || r.LatencyMs < 0 {
		t.Fatalf("простой: %+v", r)
	}
	secure := Proxy{Kind: KindMTProto, Server: host, Port: port, Secret: "dd" + hex.EncodeToString(secret)}
	if r := probe(secure); !r.OK {
		t.Fatalf("secure (padded): %+v", r)
	}
	// неверный секрет: сервер расшифровывает мусор и молчит/закрывает — не «жив»
	wrong := plain
	wrong.Secret = "ffeeddccbbaa99887766554433221100"
	if r := Probe(context.Background(), wrong, direct, 500*time.Millisecond); r.OK || (r.Class != "handshake" && r.Class != "timeout") {
		t.Fatalf("неверный секрет: %+v", r)
	}
	// сервер отвечает кодом ошибки транспорта (например, -404, когда датацентр недоступен)
	h2, p2 := mtServer(t, secret, -404)
	if r := probe(Proxy{Kind: KindMTProto, Server: h2, Port: p2, Secret: hex.EncodeToString(secret)}); r.OK || r.Class != "tg-error" {
		t.Fatalf("код ошибки: %+v", r)
	}
}

func TestProbeConnectTimeoutAndGarbage(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if r := probe(Proxy{Kind: KindMTProto, Server: "127.0.0.1", Port: closed, Secret: "00112233445566778899aabbccddeeff"}); r.OK || r.Class != "connect" {
		t.Fatalf("закрытый порт: %+v", r)
	}
	// молчащий сервер — таймаут, а не зависание
	host, port := listen(t, func(c net.Conn) { time.Sleep(3 * time.Second) })
	t0 := time.Now()
	r := Probe(context.Background(), Proxy{Kind: KindMTProto, Server: host, Port: port, Secret: "00112233445566778899aabbccddeeff"}, direct, 400*time.Millisecond)
	if r.OK || r.Class != "timeout" || time.Since(t0) > 2*time.Second {
		t.Fatalf("молчащий сервер: %+v за %v", r, time.Since(t0))
	}
	// ответ не по протоколу
	h2, p2 := listen(t, func(c net.Conn) { io.CopyN(io.Discard, c, 64); c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n")) })
	if r := probe(Proxy{Kind: KindMTProto, Server: h2, Port: p2, Secret: "00112233445566778899aabbccddeeff"}); r.OK {
		t.Fatalf("чужой протокол: %+v", r)
	}
}

func TestProbeSocks5(t *testing.T) {
	// SOCKS5 с логином и паролем; после CONNECT «датацентр» — тот же сервер obfuscated2 без секрета
	handle := func(wantUser, wantPass string) func(c net.Conn) {
		return func(c net.Conn) {
			b := make([]byte, 2)
			io.ReadFull(c, b)
			methods := make([]byte, b[1])
			io.ReadFull(c, methods)
			if wantUser != "" {
				c.Write([]byte{5, 2})
				h := make([]byte, 2)
				io.ReadFull(c, h)
				u := make([]byte, h[1])
				io.ReadFull(c, u)
				pl := make([]byte, 1)
				io.ReadFull(c, pl)
				pw := make([]byte, pl[0])
				io.ReadFull(c, pw)
				if string(u) != wantUser || string(pw) != wantPass {
					c.Write([]byte{1, 1})
					return
				}
				c.Write([]byte{1, 0})
			} else {
				c.Write([]byte{5, 0})
			}
			req := make([]byte, 10)
			io.ReadFull(c, req)
			if net.IP(req[4:8]).String() != "149.154.167.51" || binary.BigEndian.Uint16(req[8:10]) != 443 {
				c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
				return
			}
			c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
			o, tag, err := acceptObf(c, nil)
			if err == nil {
				_ = answerPQ(c, o, tag, 0)
			}
		}
	}
	host, port := listen(t, handle("user", "pw"))
	if r := probe(Proxy{Kind: KindSocks5, Server: host, Port: port, User: "user", Pass: "pw"}); !r.OK {
		t.Fatalf("socks5 с логином: %+v", r)
	}
	if r := probe(Proxy{Kind: KindSocks5, Server: host, Port: port, User: "user", Pass: "bad"}); r.OK || r.Class != "socks" {
		t.Fatalf("неверный пароль: %+v", r)
	}
	h2, p2 := listen(t, handle("", ""))
	if r := probe(Proxy{Kind: KindSocks5, Server: h2, Port: p2}); !r.OK {
		t.Fatalf("socks5 без логина: %+v", r)
	}
}

// fake-TLS: сервер проверяет дайджест ClientHello, отвечает записями с собственным дайджестом, затем читает ChangeCipherSpec и записи данных.
func TestProbeFakeTLS(t *testing.T) {
	secret, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	domain := "example.com"
	serve := func(good bool) (string, int) {
		return listen(t, func(c net.Conn) {
			h := make([]byte, 5)
			if _, err := io.ReadFull(c, h); err != nil {
				return
			}
			hello := make([]byte, int(binary.BigEndian.Uint16(h[3:5])))
			io.ReadFull(c, hello)
			rec := append(append([]byte(nil), h...), hello...)
			clientRandom := append([]byte(nil), rec[11:43]...)
			zero := append([]byte(nil), rec...)
			for i := 11; i < 43; i++ {
				zero[i] = 0
			}
			m := hmac.New(sha256.New, secret)
			m.Write(zero)
			exp := m.Sum(nil)
			ts := binary.LittleEndian.Uint32(exp[28:32]) ^ binary.LittleEndian.Uint32(clientRandom[28:32])
			for i := 0; i < 28; i++ {
				if exp[i] != clientRandom[i] {
					return // не наш клиент: дайджест не сошёлся
				}
			}
			if d := int64(ts) - time.Now().Unix(); d < -60 || d > 60 {
				return
			}
			// ответ: ServerHello, ChangeCipherSpec, ApplicationData
			sh := append([]byte{0x16, 0x03, 0x03, 0x00, 0x7a, 0x02, 0x00, 0x00, 0x76, 0x03, 0x03}, make([]byte, 32)...)
			sh = append(sh, make([]byte, 5+0x7a-1-3-2-32)...)
			sh[3], sh[4] = 0, byte(len(sh)-5)
			resp := append(sh, 0x14, 0x03, 0x03, 0x00, 0x01, 0x01, 0x17, 0x03, 0x03, 0x00, 0x04, 1, 2, 3, 4)
			mm := hmac.New(sha256.New, secret)
			mm.Write(clientRandom)
			mm.Write(resp)
			dg := mm.Sum(nil)
			if !good {
				dg[0] ^= 0xff
			}
			copy(resp[11:43], dg)
			c.Write(resp)
			ccs := make([]byte, 6)
			io.ReadFull(c, ccs)
			// дальше — записи ApplicationData с obfuscated2 внутри
			ts2 := &tlsStream{c: c}
			o, tag, err := acceptObf(ts2, secret)
			if err != nil {
				return
			}
			_ = answerPQ(ts2, o, tag, 0)
		})
	}
	sec := "ee" + hex.EncodeToString(secret) + hex.EncodeToString([]byte(domain))
	host, port := serve(true)
	if r := probe(Proxy{Kind: KindMTProto, Server: host, Port: port, Secret: sec}); !r.OK {
		t.Fatalf("fake-TLS: %+v", r)
	}
	// сервер с неверным дайджестом ответа — это не прокси с нашим секретом (например, настоящий сайт)
	h2, p2 := serve(false)
	if r := probe(Proxy{Kind: KindMTProto, Server: h2, Port: p2, Secret: sec}); r.OK || r.Class != "tls" {
		t.Fatalf("неверный дайджест сервера: %+v", r)
	}
	// неверный секрет клиента — сервер молча закрывает
	h3, p3 := serve(true)
	wrong := "ee" + "ffeeddccbbaa99887766554433221100" + hex.EncodeToString([]byte(domain))
	if r := Probe(context.Background(), Proxy{Kind: KindMTProto, Server: h3, Port: p3, Secret: wrong}, direct, 500*time.Millisecond); r.OK {
		t.Fatalf("неверный секрет: %+v", r)
	}
	// секрет ee без домена
	if r := probe(Proxy{Kind: KindMTProto, Server: host, Port: port, Secret: "ee" + hex.EncodeToString(secret)}); r.OK {
		t.Fatalf("ee без домена: %+v", r)
	}
}

func TestBridgeCapabilityOfficialVectors(t *testing.T) {
	// векторы из PROTOCOL.md tproxy-server
	s1, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	if got := BridgeCapability("proxy.example.com", s1); got != "MHLEY5PmW1GWqJkSrlmJpvJUiLhBH_QKy6yKg8a0JPk" {
		t.Fatalf("простой секрет: %s", got)
	}
	s2, _ := hex.DecodeString("dd000102030405060708090a0b0c0d0e0f")
	if got := BridgeCapability("proxy.example.com", s2); got != "IpJrt3e7sKtzPyoXy6w-Zj6GGEvsvclN66JzQEfPYLA" {
		t.Fatalf("режим dd (ведущий байт сохраняется): %s", got)
	}
}

func TestParseWebProxy(t *testing.T) {
	sec := "000102030405060708090a0b0c0d0e0f"
	for _, l := range []string{"https://t.me/webproxy?server=Proxy.Example.com.&secret=" + sec, "tg://webproxy?server=proxy.example.com&secret=" + sec} {
		p, err := Parse(l)
		if err != nil || p.Kind != KindWeb || p.Server != "proxy.example.com" || p.Port != 443 || p.Secret != sec || p.Mode() != "" {
			t.Fatalf("%s -> %+v %v", l, p, err)
		}
		if p.Link() != "tg://webproxy?secret="+sec+"&server=proxy.example.com" {
			t.Fatalf("ссылка: %s", p.Link())
		}
	}
	if d, err := Parse("tg://webproxy?server=x.example&secret=dd" + sec); err != nil || d.Secret != "dd"+sec {
		t.Fatalf("dd: %+v %v", d, err)
	}
	for _, bad := range []string{"tg://webproxy?server=localhost&secret=" + sec, "tg://webproxy?server=1.2.3.4:443&secret=" + sec, "tg://webproxy?server=x.example",
		"tg://webproxy?server=x.example&secret=ee" + sec + "6162"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("принято %q", bad)
		}
	}
	if got := ExtractLinks([]byte("текст https://t.me/webproxy?server=a.example&secret=" + sec + " и tg://proxy?server=b&port=1&secret=" + sec)); len(got) != 2 {
		t.Fatalf("извлечение: %v", got)
	}
}

func TestProbeWebProxy(t *testing.T) {
	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	host := "proxy.example.com"
	want := BridgeCapability(host, secret)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.URL.Query().Get("bridge") == want {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self' wss://"+host+"; script-src 'nonce-abc'; sandbox allow-same-origin allow-scripts")
			w.Header().Set("Cache-Control", "no-store")
			w.Write([]byte("<html>bridge</html>"))
			return
		}
		w.Write([]byte("<html>обычный сайт</html>")) // публичный сайт на том же имени
	}))
	defer srv.Close()
	old := webTLSConfig
	webTLSConfig = &tls.Config{InsecureSkipVerify: true}
	defer func() { webTLSConfig = old }()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	dialSrv := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	good := Proxy{Kind: KindWeb, Server: host, Port: port, Secret: hex.EncodeToString(secret)}
	if r := Probe(context.Background(), good, dialSrv, 3*time.Second); !r.OK {
		t.Fatalf("настоящий мост: %+v", r)
	}
	// неверный секрет: сервер отдаёт публичный сайт — не WEB-прокси с нашим секретом
	bad := good
	bad.Secret = "ffeeddccbbaa99887766554433221100"
	if r := Probe(context.Background(), bad, dialSrv, 3*time.Second); r.OK || r.Class != "handshake" {
		t.Fatalf("неверный секрет: %+v", r)
	}
	// порт закрыт — отказ соединения
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	dialClosed := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, closed)
	}
	if r := Probe(context.Background(), good, dialClosed, 2*time.Second); r.OK || r.Class != "connect" {
		t.Fatalf("порт закрыт: %+v", r)
	}
	// сертификат не доверен — класс tls (без подмены webTLSConfig)
	webTLSConfig = nil
	if r := Probe(context.Background(), good, dialSrv, 3*time.Second); r.OK || r.Class != "tls" {
		t.Fatalf("недоверенный сертификат: %+v", r)
	}
}

func TestExtractTextBlocks(t *testing.T) {
	body := []byte(`<div>Server: lolo65.quillmarsh.xyz<br/>Port: 8443<br/>Secret: eeae5ab45be1bccbba85eec21085b2fd20706574726f766963682e7275<br/>@ProxyMTProto</div>
<div>🖥  Server :  russia.knox-proxy.world<br/>🔌  Port :  4515<br/>🔑  Secret :  eee9a4f23b1d768c04a8d7f39120ca5b6e626973636f7474692e79656b74616e65742e636f6d</div>
<div>💳 Server: Unknown<br/>👁 Port: 443<br/>🔑 Secret: eef767ebee5708dadc985d58ad6e04560664726976652e676f6f676c652e636f6d</div>
<div>Server: zarnia.***<br/>Secret: ddfd367c42b90b0d8ec99796e343454424</div>
<a href="https://t.me/proxy?server=1.2.3.4&amp;port=443&amp;secret=dd91f6dd74b79cc72f81f1bf17a96b91fb">connect</a>`)
	links := ExtractLinks(body)
	if len(links) != 3 {
		t.Fatalf("ожидалось 3 ссылки (две текстовые и одна ссылкой), получено %d: %v", len(links), links)
	}
	for _, l := range links {
		if _, err := Parse(l); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
}

// Отмена контекста обрывает проверку, застрявшую на рукопожатии, сразу, а не по тайм-ауту: от этого зависит скорость остановки ядра и выхода.
func TestProbeReturnsPromptlyOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // принимает и молчит
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	start := time.Now()
	d := (&net.Dialer{}).DialContext
	res := Probe(ctx, Proxy{Kind: KindMTProto, Server: "127.0.0.1", Port: port, Secret: "dd00112233445566778899aabbccddeeff"}, d, 8*time.Second)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("проверка после отмены ждала %s (результат %+v)", took, res)
	}
}
