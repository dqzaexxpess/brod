package api_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"brod/api"
	"brod/notify"
)

// Уведомления для приложения (Windows): долгий опрос отдаёт показанное, нажатие кнопки доходит до обработчика,
// токен приложения (не владелец) не допускается, без Relay маршрут отвечает 404.
func TestNotificationsRelayRoutes(t *testing.T) {
	c, _ := newEnv(t)
	relay := notify.NewRelay()
	var pressed string
	relay.OnAction(func(id uint32, key string) { pressed = key })
	srv := api.New(c, "t")
	srv.Notices = relay
	sock := filepath.Join(t.TempDir(), "n.sock")
	ln, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Serve(ctx, ln, srv.Handler(true))
	hc := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", sock) }}}
	user := client{t: t, base: "http://unix", hc: hc}

	id, _ := relay.Send(notify.Notification{Summary: "Узел деградирует", Actions: []notify.Action{{Key: "swap", Label: "Сменить"}}})
	out := user.ok("GET", "/v1/notifications?after=0&wait=1", nil)
	ops, _ := out["ops"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["summary"] != "Узел деградирует" || out["next"].(float64) < 1 {
		t.Fatalf("опрос: %v", out)
	}
	user.ok("POST", "/v1/notifications/"+itoa(id)+"/action", map[string]string{"key": "swap"})
	if pressed != "swap" {
		t.Fatalf("кнопка не дошла: %q", pressed)
	}
	relay.Close(id)
	user.expect(404, "POST", "/v1/notifications/"+itoa(id)+"/action", map[string]string{"key": "swap"})
	user.expect(400, "POST", "/v1/notifications/abc/action", map[string]string{"key": "swap"})

	// TCP-вход с токеном приложения — не владелец
	tcp := httptest.NewServer(srv.Handler(false))
	defer tcp.Close()
	tok := user.ok("POST", "/v1/tokens", map[string]string{"name": "app"})["token"].(string)
	app := client{t: t, base: tcp.URL, hc: tcp.Client(), tok: tok}
	app.expect(403, "GET", "/v1/notifications?wait=0", nil)

	// без Relay (Linux/Android) — 404 и владельцу
	sock2 := filepath.Join(t.TempDir(), "p.sock")
	ln2, err := api.ListenUnix(sock2)
	if err != nil {
		t.Fatal(err)
	}
	go api.Serve(ctx, ln2, api.New(c, "t").Handler(true))
	hc2 := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", sock2) }}}
	client{t: t, base: "http://unix", hc: hc2}.expect(404, "GET", "/v1/notifications?wait=0", nil)
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

// интерфейс сообщает ядру, виден ли он; флаг читает Android, чтобы усыплять фоновые проходы.
func TestUIVisibleFlag(t *testing.T) {
	c, _ := newEnv(t)
	srv := api.New(c, "t")
	sock := filepath.Join(t.TempDir(), "u.sock")
	ln, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Serve(ctx, ln, srv.Handler(true))
	hc := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", sock) }}}
	post := func(body string) int {
		resp, err := hc.Post("http://unix/v1/ui", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if post(`{"visible":true}`) != http.StatusNoContent || !api.UIVisible() {
		t.Fatal("visible=true не принят")
	}
	if post(`{"visible":false}`) != http.StatusNoContent || api.UIVisible() {
		t.Fatal("visible=false не принят")
	}
}
