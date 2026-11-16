//go:build with_wireguard

package engine

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"brod/ext/dispatch"
)

// appPort — порт входа приложений в тесте. Свободный порт выбирается заранее (а не зашит): иначе тест падает, пока на этом порту
// работает настоящий демон (у него 29080/29081).
var appPort = freePort()

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 29081
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func appCfg() string {
	return fmt.Sprintf(`{"inbounds":[{"type":"mixed","tag":"app-in","listen":"127.0.0.1","listen_port":%d,
  "users":[{"username":"app","password":"secret"}]}],
 "outbounds":[{"type":"direct","tag":"direct"},{"type":"switch","tag":"active","target":"direct"},
  {"type":"dispatch","tag":"app","default":"active"}],
 "route":{"final":"app"}}`, appPort)
}

func viaProxy(t *testing.T, user, pass, target string) (string, error) {
	t.Helper()
	pu, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", appPort))
	if user != "" {
		pu.User = url.UserPassword(user, pass)
	}
	c := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
	resp, err := c.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}

// App-вход с логином/паролем → dispatch по домену → цель-switch. Правило реально применяется (цель без узла рвёт соединение),
// переключение цели видно сразу, остальной трафик идёт через active.
func TestAppInboundDispatch(t *testing.T) {
	e, err := NewFromJSON("error", []byte(appCfg()))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	byName := "http://localhost:" + port + "/" // домен, а не IP: только он попадает под доменные правила

	if body, err := viaProxy(t, "app", "secret", byName); err != nil || body != "ok" {
		t.Fatalf("через умолчание (active → direct): %q %v", body, err)
	}
	if _, err := viaProxy(t, "app", "wrong", byName); err == nil {
		t.Fatal("с неверным паролем вход должен отказывать")
	}
	if _, err := viaProxy(t, "", "", byName); err == nil {
		t.Fatal("без логина вход должен отказывать")
	}

	if err := e.AddSwitch("t-local", "", false); err != nil { // цель без узла
		t.Fatal(err)
	}
	rules := []dispatch.Rule{{Target: "t-local", DomainKeyword: []string{"localhost"}}}
	if err := e.SetDispatchRules("app", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := viaProxy(t, "app", "secret", byName); err == nil {
		t.Fatal("правило по домену не применилось: цель без узла должна рвать соединение")
	}
	if body, err := viaProxy(t, "app", "secret", srv.URL+"/"); err != nil || body != "ok" {
		t.Fatalf("IP-назначение идёт мимо доменного правила, через умолчание: %q %v", body, err)
	}
	if err := e.SetSwitchTarget("t-local", "direct"); err != nil {
		t.Fatal(err)
	}
	if body, err := viaProxy(t, "app", "secret", byName); err != nil || body != "ok" {
		t.Fatalf("после выбора узла цели: %q %v", body, err)
	}
	if err := e.Remove("t-local"); err == nil {
		t.Fatal("цель в правилах dispatch удалилась")
	}
	if err := e.SetDispatchRules("app", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("t-local"); err != nil {
		t.Fatalf("после снятия правил цель удаляется: %v", err)
	}
	if err := e.SetDispatchRules("active", nil); err == nil {
		t.Fatal("active не dispatch")
	}
}
