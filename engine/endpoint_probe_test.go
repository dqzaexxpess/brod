//go:build with_wireguard

package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/warp"
)

// WireGuard-узел (endpoint) проверяется через NewProbe тем же L2, что и обычные outbound'ы.
// Нужны сеть и зарегистрированный WARP-аккаунт (work/warp/account.json); иначе тест пропускается.
func TestWireGuardEndpointProbe(t *testing.T) {
	if os.Getenv("BROD_LIVE") != "1" {
		t.Skip("нужна сеть и аккаунт WARP: BROD_LIVE=1")
	}
	b, err := os.ReadFile("../work/warp/account.json")
	if err != nil {
		t.Skip("нет work/warp/account.json")
	}
	var acc warp.Account
	if err := json.Unmarshal(b, &acc); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(acc.Endpoint(warp.Options{Tag: "x"}))
	e, err := engine.New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p, err := e.NewProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	prober.L2Timeout = 25 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := prober.L2(ctx, p)
	if r.OK {
		return
	}
	// дать завершиться дозвонам, начатым проверкой, до закрытия пробы (иначе -race видит гонку Close и DialContext внутри движка)
	cancel()
	time.Sleep(time.Second)
	if r.Class == prober.ClassTimeout {
		t.Skipf("WARP-эндпоинт не отвечает из этой сети (таймаут L2): %+v", r)
	}
	t.Fatalf("L2 через WireGuard-endpoint: %+v", r)
}
