//go:build with_xhttp

package rawhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"brod/engine"
	"brod/internal/testport"
	"brod/internal/xraybin"
)

const uuid = "5b0e5c2e-0000-4000-8000-000000000002"

// port — свободный порт, выбранный один раз на пакет (фиксированный 38301 мог оказаться занятым чужим соединением)
var port = testport.Next()

func startXray(t *testing.T) {
	t.Helper()
	xray := xraybin.Path(t, "../..")
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "error"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "tcp", "security": "none",
				"tcpSettings": map[string]any{"header": map[string]any{"type": "http",
					"request": map[string]any{"path": []any{"/x", "/y"}}}}}}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []any{"127.0.0.0/8"}}}}}},
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "srv.json")
	_ = os.WriteFile(path, b, 0o644)
	cmd := exec.Command(xray, "run", "-c", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("xray не поднялся")
}

func get(t *testing.T, transport string, url string) (string, error) {
	t.Helper()
	e, err := engine.New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p, err := e.NewProbe([]byte(fmt.Sprintf(`{"type":"vless","server":"127.0.0.1","server_port":%d,"uuid":%q,"transport":%s}`, port, uuid, transport)))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := p.HTTPClient(10*time.Second, 0).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// Клиентская HTTP-обфускация совместима с настоящим Xray-сервером (`header: http`); без неё соединение не проходит.
func TestHTTPObfsAgainstXray(t *testing.T) {
	startXray(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "через-обфускацию-ок") }))
	defer target.Close()

	body, err := get(t, `{"type":"raw","header_type":"http","host":["a.example","b.example"],"path":["/x","/y"]}`, target.URL)
	if err != nil || body != "через-обфускацию-ок" {
		t.Fatalf("с обфускацией: body=%q err=%v", body, err)
	}
	// несколько подряд (случайные host/path на соединение)
	for i := 0; i < 5; i++ {
		if body, err := get(t, `{"type":"raw","header_type":"http","path":["/x","/y"]}`, target.URL); err != nil || body != "через-обфускацию-ок" {
			t.Fatalf("повтор %d: body=%q err=%v", i, body, err)
		}
	}
	// отрицательный контроль: клиент без обфускации против обфусцирующего сервера обязан не пройти
	if body, err := get(t, `{"type":"raw"}`, target.URL); err == nil && body == "через-обфускацию-ок" {
		t.Fatal("без обфускации соединение прошло — контроль не работает")
	}
}
