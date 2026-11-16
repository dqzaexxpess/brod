package prober

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"sort"
	"sync"
	"time"
)

// Замер UDP через узел: серия STUN-запросов (Binding Request, RFC 5389) на публичный сервер и учёт ответов. Голос и видео
// (Discord, звонки в мессенджерах) идут по UDP и чувствительны к потерям и разбросу задержки; узел, который возит UDP внутри
// TCP-соединения, при потере пакета останавливает весь поток, и задержка прыгает на секунды. Веб-страницы этого не замечают,
// поэтому обычные проверки (рукопожатие, HTTPS) о пригодности узла для звонков ничего не говорят.

// Оценка UDP-качества узла.
const (
	UDPGood     = "good"     // потерь немного, задержка и разброс небольшие: звонки пойдут
	UDPUnstable = "unstable" // ответы есть, но потери или разброс велики: звонки будут рваться
	UDPNone     = "none"     // ни одного ответа: UDP через узел не проходит
)

// Пороги оценки: потери не больше 10%, медиана не больше 300 мс, разница между самым медленным и самым быстрым ответом не больше 300 мс.
const (
	udpGoodMaxLossPct = 10.0
	udpGoodMaxMedian  = 300
	udpGoodMaxSpread  = 300
)

// STUNServers — серверы по умолчанию: второй берётся, если от первого не пришло ни одного ответа (часть сетей режет конкретный адрес).
var STUNServers = []string{"stun.l.google.com:19302", "stun.cloudflare.com:3478"}

// DialFunc открывает соединение через проверяемый узел (engine.Probe.DialContext).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// UDPResult — итог замера UDP.
type UDPResult struct {
	Server   string  `json:"server"`
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	LossPct  float64 `json:"loss_pct"`
	MinMs    int64   `json:"min_ms"`
	MedianMs int64   `json:"median_ms"`
	MaxMs    int64   `json:"max_ms"`
	Grade    string  `json:"grade"`
	Detail   string  `json:"detail,omitempty"`
}

// stunRequest — Binding Request без атрибутов: тип 0x0001, длина 0, magic cookie, 12 случайных байт идентификатора транзакции.
func stunRequest() (req []byte, txid [12]byte, err error) {
	if _, err = rand.Read(txid[:]); err != nil {
		return nil, txid, err
	}
	req = make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001)
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	copy(req[8:], txid[:])
	return req, txid, nil
}

// stunReplyTxid возвращает идентификатор транзакции, если пакет — успешный ответ на Binding Request (тип 0x0101).
func stunReplyTxid(b []byte) ([12]byte, bool) {
	var id [12]byte
	if len(b) < 20 || binary.BigEndian.Uint16(b[0:]) != 0x0101 || binary.BigEndian.Uint32(b[4:]) != 0x2112A442 {
		return id, false
	}
	copy(id[:], b[8:20])
	return id, true
}

// STUN замеряет UDP через dial: samples запросов с интервалом gap, ответы ждутся ещё wait после последнего. Если от первого сервера
// из servers не пришло ни одного ответа, пробуется следующий; возвращается результат последнего опробованного.
func STUN(ctx context.Context, dial DialFunc, servers []string, samples int, gap, wait time.Duration) UDPResult {
	var res UDPResult
	for _, srv := range servers {
		res = stunOnce(ctx, dial, srv, samples, gap, wait)
		if res.Received > 0 || ctx.Err() != nil {
			break
		}
	}
	return res
}

func stunOnce(ctx context.Context, dial DialFunc, server string, samples int, gap, wait time.Duration) UDPResult {
	res := UDPResult{Server: server, Sent: samples, Grade: UDPNone}
	conn, err := dial(ctx, "udp", server)
	if err != nil {
		res.Detail = Sanitize(err.Error())
		return res
	}
	defer conn.Close()

	var (
		mu   sync.Mutex
		sent = map[[12]byte]time.Time{}
		rtts []int64
	)
	done := make(chan struct{})
	go func() { // читатель: сопоставляет ответы с отправленными запросами
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			id, ok := stunReplyTxid(buf[:n])
			if !ok {
				continue
			}
			mu.Lock()
			if t0, found := sent[id]; found {
				delete(sent, id)
				rtts = append(rtts, time.Since(t0).Milliseconds())
			}
			mu.Unlock()
		}
	}()

	for i := 0; i < samples && ctx.Err() == nil; i++ {
		req, id, err := stunRequest()
		if err != nil {
			res.Detail = err.Error()
			break
		}
		mu.Lock()
		sent[id] = time.Now()
		mu.Unlock()
		if _, err := conn.Write(req); err != nil {
			res.Detail = Sanitize(err.Error())
			break
		}
		if i < samples-1 {
			select {
			case <-time.After(gap):
			case <-ctx.Done():
			}
		}
	}
	select { // ответы на последние запросы
	case <-time.After(wait):
	case <-ctx.Done():
	}
	conn.Close() // разбудить читателя
	<-done

	mu.Lock()
	defer mu.Unlock()
	res.Received = len(rtts)
	if res.Sent > 0 {
		res.LossPct = float64(res.Sent-res.Received) * 100 / float64(res.Sent)
	}
	if res.Received == 0 {
		if res.Detail == "" {
			res.Detail = "ни одного ответа"
		}
		return res
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	res.MinMs, res.MaxMs, res.MedianMs = rtts[0], rtts[len(rtts)-1], rtts[len(rtts)/2]
	res.Grade = UDPUnstable
	if res.LossPct <= udpGoodMaxLossPct && res.MedianMs <= udpGoodMaxMedian && res.MaxMs-res.MinMs <= udpGoodMaxSpread {
		res.Grade = UDPGood
	}
	return res
}
