package tgproxy

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DC2 — датацентр Telegram, с которым проверяется обмен (через SOCKS5 — адрес для CONNECT).
const DC2 = "149.154.167.51:443"

// Result — итог проверки.
type Result struct {
	OK        bool
	LatencyMs int64
	Class     string // "" при успехе; connect | timeout | handshake | tls | socks | tg-error | other
	Detail    string
}

// DialFunc — как открывать TCP-соединения (в тестах подменяется).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Probe проверяет прокси настоящим обменом с датацентром Telegram: obfuscated2-рукопожатие (для fake-TLS — с маскировкой под TLS)
// и запрос req_pq_multi, ожидается resPQ с тем же nonce. Просто открытый порт «живым» не считается.
func Probe(ctx context.Context, p Proxy, dial DialFunc, timeout time.Duration) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if p.Kind == KindWeb {
		return probeWeb(ctx, p, dial, start)
	}
	fail := func(class string, err error) Result {
		// тайм-аут проверки закрывает соединение (context.AfterFunc ниже), и ошибка чтения тогда «use of closed network connection»: причина всё равно тайм-аут
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "timeout"
		}
		return Result{Class: class, Detail: short(err), LatencyMs: time.Since(start).Milliseconds()}
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(p.Server, strconv.Itoa(p.Port)))
	if err != nil {
		return fail("connect", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// отмена (остановка ядра, выход из приложения) обрывает рукопожатие сразу: без этого чтение ждало бы весь тайм-аут (8 с), и остановка стояла на этом проверке
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var rw io.ReadWriter = conn
	var secret []byte
	tag := tagPadded
	switch p.Kind {
	case KindSocks5:
		if err := socks5Connect(conn, p.User, p.Pass, DC2); err != nil {
			return fail("socks", err)
		}
	case KindMTProto:
		key, domain, err := p.keyAndDomain()
		if err != nil {
			return fail("handshake", err)
		}
		secret = key
		switch p.Mode() {
		case ModeFakeTLS:
			ts, err := fakeTLSHandshake(conn, key, domain)
			if err != nil {
				return fail("tls", err)
			}
			rw = ts
		case ModePlain:
			tag = tagAbridged
		}
	default:
		return fail("other", fmt.Errorf("неизвестный вид %q", p.Kind))
	}
	if err := reqPQ(rw, secret, tag); err != nil {
		var te tgErr
		if errors.As(err, &te) {
			return fail("tg-error", err)
		}
		return fail("handshake", err)
	}
	return Result{OK: true, LatencyMs: time.Since(start).Milliseconds()}
}

type tgErr struct{ code int32 }

func (e tgErr) Error() string {
	return fmt.Sprintf("ответ-ошибка транспорта Telegram %d", e.code)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func short(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// ---- obfuscated2 ----

var (
	tagAbridged = [4]byte{0xef, 0xef, 0xef, 0xef}
	tagPadded   = [4]byte{0xdd, 0xdd, 0xdd, 0xdd}
)

func ctr(key, iv []byte) cipher.Stream {
	b, _ := aes.NewCipher(key)
	return cipher.NewCTR(b, iv)
}

func rev(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

// newObf строит 64-байтный заголовок obfuscated2 и потоки шифрования (secret — 16 байт секрета прокси, nil — без секрета).
func newObf(secret []byte, tag [4]byte) (header []byte, enc, dec cipher.Stream) {
	var init [64]byte
	for {
		_, _ = rand.Read(init[:])
		if init[0] == 0xef {
			continue
		}
		bad := false
		for _, s := range []string{"HEAD", "POST", "GET ", "\xee\xee\xee\xee", "\xdd\xdd\xdd\xdd", "\x16\x03\x01\x02"} {
			if string(init[:4]) == s {
				bad = true
			}
		}
		if bad || binary.LittleEndian.Uint32(init[4:8]) == 0 {
			continue
		}
		break
	}
	copy(init[56:60], tag[:])
	binary.LittleEndian.PutUint16(init[60:62], 2) // DC2
	encKey, encIV := append([]byte(nil), init[8:40]...), init[40:56]
	r := rev(init[8:56])
	decKey, decIV := append([]byte(nil), r[:32]...), r[32:48]
	if len(secret) > 0 {
		h := sha256.Sum256(append(append([]byte(nil), encKey...), secret...))
		encKey = h[:]
		h2 := sha256.Sum256(append(append([]byte(nil), decKey...), secret...))
		decKey = h2[:]
	}
	enc, dec = ctr(encKey, encIV), ctr(decKey, decIV)
	encInit := make([]byte, 64)
	enc.XORKeyStream(encInit, init[:])
	header = append(append([]byte(nil), init[:56]...), encInit[56:]...)
	return header, enc, dec
}

// reqPQ отправляет req_pq_multi и ждёт resPQ.
func reqPQ(rw io.ReadWriter, secret []byte, tag [4]byte) error {
	header, enc, dec := newObf(secret, tag)
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	msg := make([]byte, 0, 40)
	msg = append(msg, make([]byte, 8)...) // auth_key_id = 0
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	id := uint64(time.Now().Unix())<<32 + uint64(binary.LittleEndian.Uint32(rnd[:])&0x0fffffff)*4
	msg = binary.LittleEndian.AppendUint64(msg, id)
	msg = binary.LittleEndian.AppendUint32(msg, 20)
	msg = binary.LittleEndian.AppendUint32(msg, 0xbe7e8ef1) // req_pq_multi
	msg = append(msg, nonce...)

	var frame []byte
	if tag == tagAbridged {
		frame = append([]byte{byte(len(msg) / 4)}, msg...)
	} else {
		frame = binary.LittleEndian.AppendUint32(nil, uint32(len(msg)))
		frame = append(frame, msg...)
	}
	enc.XORKeyStream(frame, frame)
	if _, err := rw.Write(append(header, frame...)); err != nil {
		return err
	}
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		if _, err := io.ReadFull(rw, b); err != nil {
			return nil, err
		}
		dec.XORKeyStream(b, b)
		return b, nil
	}
	var n int
	if tag == tagAbridged {
		b, err := read(1)
		if err != nil {
			return err
		}
		n = int(b[0])
		if n == 0x7f {
			l3, err := read(3)
			if err != nil {
				return err
			}
			n = int(l3[0]) | int(l3[1])<<8 | int(l3[2])<<16
		}
		n *= 4
	} else {
		b, err := read(4)
		if err != nil {
			return err
		}
		n = int(binary.LittleEndian.Uint32(b))
	}
	if n < 0 || n > 1<<16 {
		return fmt.Errorf("длина ответа %d", n)
	}
	body, err := read(n)
	if err != nil {
		return err
	}
	if len(body) == 4 { // ошибка транспорта: int32 код (например, -404)
		return tgErr{int32(binary.LittleEndian.Uint32(body))}
	}
	if len(body) < 40 || binary.LittleEndian.Uint32(body[20:24]) != 0x05162463 || !bytes.Equal(body[24:40], nonce) {
		return fmt.Errorf("в ответе нет resPQ с нашим nonce (%d байт)", len(body))
	}
	return nil
}

// ---- SOCKS5 ----

func socks5Connect(c net.Conn, user, pass, target string) error {
	methods := []byte{0x00}
	if user != "" {
		methods = []byte{0x00, 0x02}
	}
	if _, err := c.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	r := make([]byte, 2)
	if _, err := io.ReadFull(c, r); err != nil {
		return err
	}
	if r[0] != 0x05 {
		return fmt.Errorf("не SOCKS5")
	}
	switch r[1] {
	case 0x00:
	case 0x02:
		if len(user) > 255 || len(pass) > 255 {
			return fmt.Errorf("слишком длинные учётные данные")
		}
		req := append(append([]byte{0x01, byte(len(user))}, user...), byte(len(pass)))
		if _, err := c.Write(append(req, pass...)); err != nil {
			return err
		}
		a := make([]byte, 2)
		if _, err := io.ReadFull(c, a); err != nil {
			return err
		}
		if a[1] != 0x00 {
			return fmt.Errorf("логин/пароль не приняты")
		}
	default:
		return fmt.Errorf("SOCKS5: метод %#x не поддерживается", r[1])
	}
	host, portS, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portS)
	ip := net.ParseIP(host).To4()
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		return err
	}
	h := make([]byte, 4)
	if _, err := io.ReadFull(c, h); err != nil {
		return err
	}
	if h[1] != 0x00 {
		return fmt.Errorf("SOCKS5: CONNECT отклонён (код %d)", h[1])
	}
	var skip int
	switch h[3] {
	case 0x01:
		skip = 4 + 2
	case 0x04:
		skip = 16 + 2
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return err
		}
		skip = int(l[0]) + 2
	default:
		return fmt.Errorf("SOCKS5: странный ответ")
	}
	_, err := io.ReadFull(c, make([]byte, skip))
	return err
}

// ---- fake-TLS ----

// fakeTLSHandshake выполняет рукопожатие «ee»-прокси: ClientHello с дайджестом по секрету, проверка дайджеста сервера, ChangeCipherSpec.
// Возвращает поток, оборачивающий данные в записи TLS.
func fakeTLSHandshake(c net.Conn, secret []byte, domain string) (io.ReadWriter, error) {
	if domain == "" {
		return nil, fmt.Errorf("в секрете ee нет домена")
	}
	hello, clientRandom := buildClientHello(secret, domain)
	if _, err := c.Write(hello); err != nil {
		return nil, err
	}
	var resp []byte
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(c, b)
		return b, err
	}
	// ServerHello, ChangeCipherSpec, ApplicationData
	for i, want := range []byte{0x16, 0x14, 0x17} {
		h, err := read(5)
		if err != nil {
			return nil, err
		}
		if h[0] != want || h[1] != 0x03 || h[2] != 0x03 {
			return nil, fmt.Errorf("ответ не похож на ответ прокси (запись %d: тип %#x)", i+1, h[0])
		}
		body, err := read(int(binary.BigEndian.Uint16(h[3:5])))
		if err != nil {
			return nil, err
		}
		resp = append(append(resp, h...), body...)
	}
	if len(resp) < 43 {
		return nil, fmt.Errorf("слишком короткий ответ")
	}
	got := append([]byte(nil), resp[11:43]...)
	zeroed := append([]byte(nil), resp...)
	for i := 11; i < 43; i++ {
		zeroed[i] = 0
	}
	m := hmac.New(sha256.New, secret)
	m.Write(clientRandom)
	m.Write(zeroed)
	if !hmac.Equal(m.Sum(nil), got) {
		return nil, fmt.Errorf("дайджест сервера не сошёлся (это не прокси с нашим секретом)")
	}
	if _, err := c.Write([]byte{0x14, 0x03, 0x03, 0x00, 0x01, 0x01}); err != nil {
		return nil, err
	}
	return &tlsStream{c: c}, nil
}

func buildClientHello(secret []byte, domain string) (hello, clientRandom []byte) {
	be16 := func(v int) []byte { return []byte{byte(v >> 8), byte(v)} }
	ext := func(t int, data []byte) []byte { return append(append(be16(t), be16(len(data))...), data...) }
	var exts []byte
	sni := append([]byte{0x00}, append(be16(len(domain)), domain...)...)
	exts = append(exts, ext(0x0000, append(be16(len(sni)), sni...))...)
	exts = append(exts, ext(0x0017, nil)...)
	exts = append(exts, ext(0xff01, []byte{0x00})...)
	exts = append(exts, ext(0x000a, []byte{0x00, 0x06, 0x00, 0x1d, 0x00, 0x17, 0x00, 0x18})...)
	exts = append(exts, ext(0x000b, []byte{0x01, 0x00})...)
	exts = append(exts, ext(0x0023, nil)...)
	exts = append(exts, ext(0x0010, []byte{0x00, 0x0c, 0x02, 'h', '2', 0x08, 'h', 't', 't', 'p', '/', '1', '.', '1'})...)
	exts = append(exts, ext(0x0005, []byte{0x01, 0x00, 0x00, 0x00, 0x00})...)
	exts = append(exts, ext(0x000d, []byte{0x00, 0x10, 0x04, 0x03, 0x08, 0x04, 0x04, 0x01, 0x05, 0x03, 0x08, 0x05, 0x05, 0x01, 0x08, 0x06, 0x06, 0x01})...)
	exts = append(exts, ext(0x002b, []byte{0x04, 0x03, 0x04, 0x03, 0x03})...)
	exts = append(exts, ext(0x002d, []byte{0x01, 0x01})...)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	exts = append(exts, ext(0x0033, append([]byte{0x00, 0x24, 0x00, 0x1d, 0x00, 0x20}, key...))...)

	suites := []byte{0x13, 0x01, 0x13, 0x02, 0x13, 0x03, 0xc0, 0x2b, 0xc0, 0x2f, 0xc0, 0x2c, 0xc0, 0x30, 0xcc, 0xa9, 0xcc, 0xa8, 0xc0, 0x13, 0xc0, 0x14, 0x00, 0x9c, 0x00, 0x9d, 0x00, 0x2f, 0x00, 0x35}
	sid := make([]byte, 32)
	_, _ = rand.Read(sid)
	var hs []byte
	hs = append(hs, 0x03, 0x03)
	hs = append(hs, make([]byte, 32)...) // random: пока нули, затем дайджест
	hs = append(hs, 0x20)
	hs = append(hs, sid...)
	hs = append(hs, be16(len(suites))...)
	hs = append(hs, suites...)
	hs = append(hs, 0x01, 0x00)
	hs = append(hs, be16(len(exts))...)
	hs = append(hs, exts...)
	h := append([]byte{0x01, byte(len(hs) >> 16), byte(len(hs) >> 8), byte(len(hs))}, hs...)
	rec := append([]byte{0x16, 0x03, 0x01}, be16(len(h))...)
	rec = append(rec, h...)

	m := hmac.New(sha256.New, secret)
	m.Write(rec)
	digest := m.Sum(nil)
	ts := uint32(time.Now().Unix())
	var tsb [4]byte
	binary.LittleEndian.PutUint32(tsb[:], ts)
	for i := 0; i < 4; i++ {
		digest[28+i] ^= tsb[i]
	}
	copy(rec[11:43], digest)
	return rec, digest
}

// tlsStream оборачивает данные в записи TLS ApplicationData и разворачивает их обратно (записи ChangeCipherSpec пропускаются).
type tlsStream struct {
	c   net.Conn
	buf []byte
}

func (t *tlsStream) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 16000 {
			chunk = chunk[:16000]
		}
		rec := append([]byte{0x17, 0x03, 0x03, byte(len(chunk) >> 8), byte(len(chunk))}, chunk...)
		if _, err := t.c.Write(rec); err != nil {
			return n, err
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}

func (t *tlsStream) Read(p []byte) (int, error) {
	for len(t.buf) == 0 {
		h := make([]byte, 5)
		if _, err := io.ReadFull(t.c, h); err != nil {
			return 0, err
		}
		body := make([]byte, int(binary.BigEndian.Uint16(h[3:5])))
		if _, err := io.ReadFull(t.c, body); err != nil {
			return 0, err
		}
		if h[0] == 0x17 {
			t.buf = body
		} else if h[0] != 0x14 {
			return 0, fmt.Errorf("неожиданная запись TLS %#x", h[0])
		}
	}
	n := copy(p, t.buf)
	t.buf = t.buf[n:]
	return n, nil
}

// ---- WEB-прокси ----

// webTLSConfig — настройки TLS проверки WEB-прокси (в тестах подменяется, чтобы доверять самоподписанному сертификату).
var webTLSConfig *tls.Config

// BridgeCapability — «возможность» моста WEB-прокси: base64url(HMAC-SHA256(ключ=секрет, "tdesktop-web-proxy-bridge-v1\n"+хост)).
// Секрет — байты как есть, у режима dd первый байт сохраняется (контракт PROTOCOL.md tproxy-server).
func BridgeCapability(host string, secret []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("tdesktop-web-proxy-bridge-v1\n" + host))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// probeWeb проверяет, что по адресу действительно работает WEB-прокси с нашим секретом: запрос /?bridge=<возможность> возвращает страницу
// моста (у неё свой CSP: script-src 'nonce-…' и connect-src … wss://хост), а обычный сайт на том же имени отвечает иначе. Это проверка
// моста (сервер узнал секрет и ретранслятор поднят); полный путь до датацентра Telegram через WEB-прокси она не проверяет.
func probeWeb(ctx context.Context, p Proxy, dial DialFunc, start time.Time) Result {
	fail := func(class string, err error) Result {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			class = "timeout"
		}
		return Result{Class: class, Detail: short(err), LatencyMs: time.Since(start).Milliseconds()}
	}
	secret, err := hex.DecodeString(p.Secret)
	if err != nil {
		return fail("handshake", err)
	}
	addr := net.JoinHostPort(p.Server, strconv.Itoa(p.Port))
	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       func(ctx context.Context, network, _ string) (net.Conn, error) { return dial(ctx, network, addr) },
			TLSClientConfig:   webTLSConfig,
		},
	}
	get := func(path string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+p.Server+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
		return cl.Do(req)
	}
	resp, err := get("/?bridge=" + BridgeCapability(p.Server, secret))
	if err != nil {
		class := "connect"
		var ce *tls.CertificateVerificationError
		if errors.As(err, &ce) || strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509") {
			class = "tls"
		}
		return fail(class, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != http.StatusOK || !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(csp, "wss://") {
		return Result{Class: "handshake", Detail: fmt.Sprintf("сайт отвечает (HTTP %d), но это не мост WEB-прокси с нашим секретом", resp.StatusCode), LatencyMs: time.Since(start).Milliseconds()}
	}
	return Result{OK: true, LatencyMs: time.Since(start).Milliseconds()}
}
