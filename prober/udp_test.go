package prober

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSTUN отвечает на Binding Request; decide по номеру запроса задаёт задержку ответа или отказ ответить (ok=false).
func fakeSTUN(t *testing.T, decide func(n int) (delay time.Duration, ok bool)) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	var n int32
	go func() {
		buf := make([]byte, 1500)
		for {
			k, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if k < 20 {
				continue
			}
			delay, ok := decide(int(atomic.AddInt32(&n, 1)) - 1)
			if !ok {
				continue
			}
			reply := make([]byte, 20)
			copy(reply, buf[:20])
			reply[0], reply[1] = 0x01, 0x01 // Binding Success Response
			go func(reply []byte, from net.Addr) {
				time.Sleep(delay)
				pc.WriteTo(reply, from)
			}(reply, from)
		}
	}()
	return pc.LocalAddr().String()
}

func localDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func TestSTUNGoodChannel(t *testing.T) {
	srv := fakeSTUN(t, func(int) (time.Duration, bool) { return 5 * time.Millisecond, true })
	r := STUN(context.Background(), localDial, []string{srv}, 8, 10*time.Millisecond, 300*time.Millisecond)
	if r.Grade != UDPGood || r.Received != 8 || r.LossPct != 0 {
		t.Fatalf("хороший канал оценён как %+v", r)
	}
	if r.MedianMs < 4 || r.MedianMs > 100 {
		t.Fatalf("медиана задержки вне ожидаемого: %+v", r)
	}
}

func TestSTUNLossMakesUnstable(t *testing.T) {
	srv := fakeSTUN(t, func(n int) (time.Duration, bool) { return time.Millisecond, n%2 == 0 }) // каждый второй запрос без ответа
	r := STUN(context.Background(), localDial, []string{srv}, 10, 10*time.Millisecond, 300*time.Millisecond)
	if r.Grade != UDPUnstable || r.Received != 5 || r.LossPct != 50 {
		t.Fatalf("канал с потерями 50%% оценён как %+v", r)
	}
}

func TestSTUNSpreadMakesUnstable(t *testing.T) {
	srv := fakeSTUN(t, func(n int) (time.Duration, bool) {
		if n%2 == 0 {
			return time.Millisecond, true
		}
		return 450 * time.Millisecond, true // каждый второй ответ приходит поздно: разброс больше порога
	})
	r := STUN(context.Background(), localDial, []string{srv}, 6, 10*time.Millisecond, 700*time.Millisecond)
	if r.Grade != UDPUnstable || r.Received != 6 || r.MaxMs-r.MinMs <= udpGoodMaxSpread {
		t.Fatalf("канал с большим разбросом оценён как %+v", r)
	}
}

func TestSTUNSilenceIsNone(t *testing.T) {
	srv := fakeSTUN(t, func(int) (time.Duration, bool) { return 0, false })
	r := STUN(context.Background(), localDial, []string{srv}, 4, 5*time.Millisecond, 100*time.Millisecond)
	if r.Grade != UDPNone || r.Received != 0 || r.LossPct != 100 || r.Detail == "" {
		t.Fatalf("молчащий канал оценён как %+v", r)
	}
}

func TestSTUNFallsBackToSecondServer(t *testing.T) {
	dead := fakeSTUN(t, func(int) (time.Duration, bool) { return 0, false })
	live := fakeSTUN(t, func(int) (time.Duration, bool) { return time.Millisecond, true })
	r := STUN(context.Background(), localDial, []string{dead, live}, 4, 5*time.Millisecond, 100*time.Millisecond)
	if r.Server != live || r.Received != 4 {
		t.Fatalf("второй сервер не использован: %+v", r)
	}
}

func TestSTUNDialErrorIsNone(t *testing.T) {
	bad := func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }
	r := STUN(context.Background(), bad, []string{"x:1"}, 3, time.Millisecond, 10*time.Millisecond)
	if r.Grade != UDPNone || r.Detail == "" {
		t.Fatalf("ошибка дозвона: %+v", r)
	}
}

func TestSTUNIgnoresForeignPackets(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() { // отвечает мусором и ответом с чужим идентификатором транзакции
		buf := make([]byte, 1500)
		for {
			_, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo([]byte("not stun at all, just garbage bytes"), from)
			bad := make([]byte, 20)
			bad[0], bad[1] = 0x01, 0x01
			bad[4], bad[5], bad[6], bad[7] = 0x21, 0x12, 0xA4, 0x42
			pc.WriteTo(bad, from)
		}
	}()
	r := STUN(context.Background(), localDial, []string{pc.LocalAddr().String()}, 3, 5*time.Millisecond, 100*time.Millisecond)
	if r.Received != 0 || r.Grade != UDPNone {
		t.Fatalf("чужие пакеты засчитаны как ответы: %+v", r)
	}
}
