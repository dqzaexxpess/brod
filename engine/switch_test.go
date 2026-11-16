//go:build with_wireguard

package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// wgConfig — WireGuard-endpoint с заведомо фиктивными ключами и адресом, к которому никто не подключается:
// нужен лишь затем, чтобы проверить, что switch умеет смотреть на endpoint, а не только на outbound.
const wgConfig = `{"endpoints":[{"type":"wireguard","tag":"wg","address":["10.9.9.2/32"],
 "private_key":"yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
 "peers":[{"address":"192.0.2.1","port":2408,"public_key":"bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=","allowed_ips":["0.0.0.0/0"]}]}]}`

func fetch(t *testing.T, e *Engine, tag, url string) (string, error) {
	t.Helper()
	p, err := e.Existing(tag)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	resp, err := p.HTTPClient(5*time.Second, 0).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestSwitchOnEngine(t *testing.T) {
	e, err := NewFromJSON("error", []byte(wgConfig))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()

	for _, tag := range []string{"d1", "d2"} {
		if _, err := e.add([]byte(fmt.Sprintf(`{"type":"direct","tag":%q}`, tag))); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.AddSwitch("active", "d1", false); err != nil {
		t.Fatal(err)
	}
	if got, err := e.SwitchTarget("active"); err != nil || got != "d1" {
		t.Fatalf("цель %q, ошибка %v", got, err)
	}
	if body, err := fetch(t, e, "active", srv.URL); err != nil || body != "ok" {
		t.Fatalf("через switch → direct: %q %v", body, err)
	}
	if err := e.Remove("d1"); err == nil {
		t.Fatal("выбранный узел удалился")
	}

	// цель — endpoint: переключение принимается, зависимость фиксируется, до фиктивного пира трафик не доходит
	if err := e.SetSwitchTarget("active", "wg"); err != nil {
		t.Fatalf("endpoint как цель: %v", err)
	}
	if got, _ := e.SwitchTarget("active"); got != "wg" {
		t.Fatalf("цель %q", got)
	}
	if err := e.Remove("d1"); err != nil {
		t.Fatalf("после переключения старый узел должен удаляться: %v", err)
	}
	if _, err := fetch(t, e, "active", srv.URL); err == nil {
		t.Fatal("через фиктивный WireGuard-пир запрос не должен проходить — похоже, switch не смотрит на endpoint")
	}

	// возврат на outbound; несуществующая и «не-switch» цели
	if err := e.SetSwitchTarget("active", "d2"); err != nil {
		t.Fatal(err)
	}
	if body, err := fetch(t, e, "active", srv.URL); err != nil || body != "ok" {
		t.Fatalf("после возврата: %q %v", body, err)
	}
	if err := e.SetSwitchTarget("active", "d1"); err == nil {
		t.Fatal("удалённая цель должна отклоняться")
	}
	if err := e.SetSwitchTarget("d2", "d2"); err == nil {
		t.Fatal("не-switch не должен переключаться")
	}
	if _, err := e.SwitchTarget("нет"); err == nil {
		t.Fatal("несуществующий switch")
	}
}

// WireGuard-узел из подписки добавляется как endpoint, на него можно переключиться, пока на нём switch — удалить нельзя,
// после переключения — можно, и тот же тег регистрируется заново (полная очистка).
func TestAddRemoveEndpoint(t *testing.T) {
	e, err := New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	wg := `{"type":"wireguard","tag":"w1","address":["10.9.9.2/32"],"private_key":"yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
 "peers":[{"address":"192.0.2.1","port":2408,"public_key":"bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=","allowed_ips":["0.0.0.0/0"]}]}`
	if tag, err := e.Add([]byte(wg)); err != nil || tag != "w1" {
		t.Fatalf("Add endpoint: %q %v", tag, err)
	}
	if !e.Has("w1") {
		t.Fatal("endpoint не виден как узел")
	}
	if _, err := e.add([]byte(`{"type":"direct","tag":"d"}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.AddSwitch("active", "w1", false); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("w1"); err == nil {
		t.Fatal("endpoint, на который смотрит switch, удалился")
	}
	if err := e.SetSwitchTarget("active", "d"); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("w1"); err != nil {
		t.Fatalf("после переключения endpoint должен удаляться: %v", err)
	}
	if e.Has("w1") {
		t.Fatal("endpoint остался в менеджере")
	}
	if _, err := e.Add([]byte(wg)); err != nil {
		t.Fatalf("повторная регистрация того же тега: %v", err)
	}
	if err := e.Remove("w1"); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("w1"); err == nil {
		t.Fatal("повторное удаление должно отказывать")
	}
}
