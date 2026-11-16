package prober

import (
	"bufio"
	"context"
	"net"
	"strings"
	"time"

	"brod/engine"
)

// QuickTimeout — общий таймаут быстрой проверки.
var QuickTimeout = 4 * time.Second

// Quick — быстрая проверка «через узел идут данные»: TCP к IP-адресу (без DNS) и один HTTP-запрос, любой HTTP-ответ — успех.
// Это два круга до цели вместо пяти у L2 (DNS, TCP, TLS, HTTP через цепочку): подходит, чтобы быстро понять, что выход поднялся
// (подключение, смена узла). Строгую проверку (L2, TLS) она не заменяет — её делают следом, и именно она влияет на деградацию узла.
func Quick(ctx context.Context, p *engine.Probe) Result {
	return quickOn(ctx, p.DialContext, QuickTarget)
}

// QuickTarget — адрес (IP, без DNS), куда идёт быстрая проверка.
var QuickTarget = "1.1.1.1:80"

func quickOn(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), target string) Result {
	start := time.Now()
	r := Result{Level: 2, Target: "quick"}
	ctx, cancel := context.WithTimeout(ctx, QuickTimeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		return fin(r, start, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write([]byte("HEAD / HTTP/1.0\r\nHost: 1.1.1.1\r\nConnection: close\r\n\r\n")); err != nil {
		return fin(r, start, err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return fin(r, start, err)
	}
	if !strings.HasPrefix(line, "HTTP/") {
		r.Class, r.Detail = ClassOther, "ответ не HTTP"
		return fin(r, start, nil).withFail()
	}
	r.OK = true
	return fin(r, start, nil)
}
