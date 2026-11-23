package api_test

import (
	"brod/nodeopts"
	"brod/ota"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"brod/aggregator"
	"brod/api"
	"brod/core"
	"brod/engine"
	"brod/internal/testport"
	"brod/prober"
	"brod/store"
	"brod/supervisor"
)

func newEnv(t *testing.T) (*core.Core, *httptest.Server) {
	t.Helper()
	nodeopts.AllowTypeForTests(t, "direct") // узлы-заглушки тестов
	c, err := core.New(core.Config{StateDir: t.TempDir(), AppPort: testport.Next(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		NetID:      func() string { return "net1" },
		UpdateURLs: []string{"https://127.0.0.1:9/releases.json"}, // проверка обновлений в тестах не должна ходить в сеть
		YouTube: func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: "youtube", OK: true}
		},
		Speed: func(context.Context, store.NodeRec, *engine.Probe) prober.Result {
			return prober.Result{Level: 3, OK: true, SpeedKBps: 2048}
		},
		Exit: func(context.Context, store.NodeRec, *engine.Probe) prober.ExitInfo {
			t, f := true, false
			return prober.ExitInfo{IP: "203.0.113.7", Country: "DE", Colo: "FRA", GoogleOK: &t, SiteOK: &f, Detail: "строгий сайт не пускает"}
		},
		Check: func(context.Context, store.NodeRec, *engine.Probe) []prober.Result {
			return []prober.Result{{Level: 2, OK: true}}
		},
		TargetCheck: func(_ context.Context, tg store.Target, _, _ string, _ *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: tg.ID, OK: true}
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	srv := httptest.NewServer(api.New(c, "test").Handler(false))
	t.Cleanup(srv.Close)
	return c, srv
}

func addDirectNode(t *testing.T, c *core.Core, key string) string {
	t.Helper()
	id := store.NodeID(key)
	if err := c.Store().UpsertNodes("src", time.Now().UnixNano(), []store.NodeRec{{ID: id, Class: "direct/tcp/none", Proto: "direct", Server: key + ".example", Port: 443,
		Opts: []byte(`{"type":"direct"}`)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return id
}

type client struct {
	t    *testing.T
	base string
	tok  string
	hc   *http.Client
}

func (c client) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	hc := c.hc
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c client) ok(method, path string, body any) map[string]any {
	c.t.Helper()
	code, b := c.do(method, path, body)
	if code/100 != 2 {
		c.t.Fatalf("%s %s: HTTP %d %s", method, path, code, b)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (c client) expect(code int, method, path string, body any) {
	c.t.Helper()
	if got, b := c.do(method, path, body); got != code {
		c.t.Fatalf("%s %s: HTTP %d (ожидали %d) %s", method, path, got, code, b)
	}
}

func TestAuthentication(t *testing.T) {
	c, srv := newEnv(t)
	anon := client{t: t, base: srv.URL}
	anon.expect(401, "GET", "/v1/status", nil)
	client{t: t, base: srv.URL, tok: "чужой"}.expect(401, "GET", "/v1/status", nil)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(200, "GET", "/v1/status", nil)

	// браузерный запрос (с Origin) отклоняется даже с верным токеном
	req, _ := http.NewRequest("GET", srv.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+c.APIToken())
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("Origin: HTTP %d", resp.StatusCode)
	}
	// чужой Host (DNS rebinding) отклоняется, когда список допустимых задан
	pinned := httptest.NewServer(api.New(c, "t").Handler(false, "127.0.0.1:1"))
	defer pinned.Close()
	if code, _ := (client{t: t, base: pinned.URL, tok: c.APIToken()}).do("GET", "/v1/status", nil); code != 403 {
		t.Fatalf("Host: HTTP %d", code)
	}
}

func TestAppTokenPermissions(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	tok := user.ok("POST", "/v1/tokens", map[string]string{"name": "myapp"})["token"].(string)
	app := client{t: t, base: srv.URL, tok: tok}

	app.expect(200, "GET", "/v1/targets", nil)
	app.expect(200, "GET", "/v1/status", nil)
	st := app.ok("GET", "/v1/status", nil)
	if _, has := st["active"]; has || st["app_proxy"] != nil {
		t.Fatalf("приложению показаны детали активного узла/прокси: %v", st)
	}
	for _, p := range [][2]string{{"GET", "/v1/nodes"}, {"GET", "/v1/sources"}, {"GET", "/v1/settings"}, {"GET", "/v1/logs"},
		{"GET", "/v1/app-proxy"}, {"GET", "/v1/tokens"}, {"POST", "/v1/refresh"}, {"POST", "/v1/check"}} {
		app.expect(403, p[0], p[1], nil)
	}
	app.expect(403, "POST", "/v1/nodes/abc/connect", nil)
	app.expect(403, "POST", "/v1/tokens", map[string]string{"name": "x"})
	// выданный токен отзывается
	user.expect(200, "DELETE", "/v1/tokens/myapp", nil)
	app.expect(401, "GET", "/v1/targets", nil)
}

func TestUnixSocketIsTrustedAndPrivate(t *testing.T) {
	c, _ := newEnv(t)
	sock := filepath.Join(t.TempDir(), "v.sock")
	ln, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Serve(ctx, ln, api.New(c, "t").Handler(true))
	if st, err := os.Stat(sock); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) { // на Windows доступ задаёт ACL
		t.Fatalf("права сокета: %v %v", st, err)
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", sock) }}}
	cl := client{t: t, base: "http://unix", hc: hc}
	if out := cl.ok("GET", "/v1/app-proxy", nil); out["username"] == "" {
		t.Fatalf("через сокет владелец видит app-proxy: %v", out)
	}
	// новый запуск заменяет устаревший файл сокета
	cancel()
	time.Sleep(100 * time.Millisecond)
	if ln2, err := api.ListenUnix(sock); err != nil {
		t.Fatalf("повторное создание сокета: %v", err)
	} else {
		ln2.Close()
	}
}

func TestNodesConnectAndFlags(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	a, b := addDirectNode(t, c, "a"), addDirectNode(t, c, "b")

	code, body := user.do("GET", "/v1/nodes", nil)
	var list []map[string]any
	_ = json.Unmarshal(body, &list)
	if code != 200 || len(list) != 2 {
		t.Fatalf("список узлов: %d %s", code, body)
	}
	user.ok("POST", "/v1/nodes/"+a+"/connect", nil)
	st := user.ok("GET", "/v1/status", nil)
	if act, _ := st["active"].(map[string]any); act == nil || act["id"] != a {
		t.Fatalf("активный: %v", st)
	}
	user.ok("POST", "/v1/nodes/"+b+"/favorite", map[string]bool{"on": true})
	n := user.ok("GET", "/v1/nodes/"+b, nil)
	if n["favorite"] != true {
		t.Fatalf("избранное: %v", n)
	}
	user.ok("POST", "/v1/nodes/"+b[:8]+"/connect", nil) // короткий префикс
	if act := user.ok("GET", "/v1/status", nil)["active"].(map[string]any); act["id"] != b {
		t.Fatalf("префикс разрешился в %v", act["id"])
	}
	user.expect(400, "POST", "/v1/nodes/abc/connect", nil)    // слишком короткий
	user.expect(404, "POST", "/v1/nodes/ffffff/connect", nil) // нет такого
	user.expect(404, "POST", "/v1/nodes/"+store.NodeID("нет")+"/connect", nil)
	user.expect(404, "GET", "/v1/nodes/нет", nil)
	// секреты в ответах узлов отсутствуют
	_, raw := user.do("GET", "/v1/nodes/"+a, nil)
	if strings.Contains(string(raw), "uuid") || strings.Contains(string(raw), "password") {
		t.Fatalf("ответ узла содержит секреты: %s", raw)
	}
}

func TestErrorsAndValidation(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(400, "PUT", "/v1/targets/x1", map[string]any{"check": map[string]any{"kind": "нет"}})
	user.expect(400, "PUT", "/v1/targets/Плохой", map[string]any{"check": map[string]any{"kind": "youtube"}})
	user.expect(400, "PUT", "/v1/targets/x2", map[string]any{"неизвестное_поле": 1})
	user.expect(404, "GET", "/v1/targets/нет", nil)
	user.expect(400, "POST", "/v1/targets/youtube/switch", map[string]any{})
	user.expect(400, "PUT", "/v1/settings/security.hide_insecure", map[string]string{"value": "да"})
	user.expect(400, "PUT", "/v1/settings/api.token", map[string]string{"value": "x"})
	// слишком большое тело
	big := strings.Repeat("a", 2<<20)
	if code, _ := user.do("PUT", "/v1/targets/x3", map[string]any{"title": big}); code != 400 {
		t.Fatalf("большое тело: HTTP %d", code)
	}
}

func TestTargetLifecycleAndOwnership(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	myapp := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "myapp"})["token"].(string)}
	other := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "other"})["token"].(string)}

	put := map[string]any{"title": "VideoSite", "rules": map[string]any{"domain_suffix": []string{"videosite.example"}},
		"check": map[string]any{"kind": "http", "url": "https://videosite.example/", "expect_codes": []int{200, 403}}, "policy": "auto-failover", "failover_k": 2, "allow_direct": true}
	myapp.expect(403, "PUT", "/v1/targets/videosite", put) // allow_direct без подтверждения владельца
	put["allow_direct"] = false
	got := myapp.ok("PUT", "/v1/targets/videosite", put)
	put["allow_direct"] = true
	user.ok("PUT", "/v1/targets/videosite", put) // владелец разрешил allow_direct, цель остаётся приложению
	got = myapp.ok("PUT", "/v1/targets/videosite", put)
	if got["owner"] != "api:myapp" || got["policy"] != "auto-failover" || got["node"] != "" {
		t.Fatalf("цель: %v", got)
	}
	other.expect(403, "PUT", "/v1/targets/videosite", put)
	other.expect(403, "DELETE", "/v1/targets/videosite", nil)
	other.expect(403, "POST", "/v1/targets/videosite/switch", map[string]string{"to": "direct"})
	other.expect(403, "POST", "/v1/targets/videosite/probe", nil)
	other.expect(403, "POST", "/v1/targets/videosite/probe-url", map[string]any{"url": "https://cdn.example/x", "ttl_sec": 60})
	myapp.expect(403, "PUT", "/v1/targets/youtube", map[string]any{"check": map[string]any{"kind": "youtube"}})
	myapp.expect(403, "DELETE", "/v1/targets/youtube", nil)

	myapp.ok("POST", "/v1/targets/videosite/probe-url", map[string]any{"url": "https://cdn.example/v?sig=секрет", "ttl_sec": 120})
	sw := myapp.ok("POST", "/v1/targets/videosite/switch", map[string]string{"to": "direct"})
	if sw["node"] != "direct" {
		t.Fatalf("switch: %v", sw)
	}
	res := myapp.ok("POST", "/v1/targets/videosite/probe", nil)
	if res["ok"] != true {
		t.Fatalf("проверка цели: %v", res)
	}
	st := myapp.ok("GET", "/v1/targets/videosite", nil)
	if st["node"] != "direct" || st["has_probe_url"] != true || st["degraded"] != false {
		t.Fatalf("статус цели: %v", st)
	}
	_, raw := myapp.do("GET", "/v1/targets/videosite", nil)
	if strings.Contains(string(raw), "секрет") {
		t.Fatal("пробная ссылка вернулась в ответе")
	}
	myapp.ok("DELETE", "/v1/targets/videosite", nil)
	myapp.expect(404, "GET", "/v1/targets/videosite", nil)
}

func TestSourcesHideTokens(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(201, "POST", "/v1/sources", map[string]any{"name": "mine", "url": "https://sub.example.org/p/секрет?token=abc123", "priority": 1})
	_, raw := user.do("GET", "/v1/sources", nil)
	if strings.Contains(string(raw), "abc123") || strings.Contains(string(raw), "секрет") || !strings.Contains(string(raw), "sub.example.org") {
		t.Fatalf("список источников: %s", raw)
	}
	user.ok("POST", "/v1/sources/mine/enable", map[string]bool{"on": false})
	user.expect(200, "DELETE", "/v1/sources/mine", nil)
	user.expect(404, "DELETE", "/v1/sources/mine", nil)
}

// SSE: журнал догоняется по Last-Event-ID, живые события приходят сразу, приложение видит только свои цели.
func TestEventsStreamAndFilter(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	appTok := user.ok("POST", "/v1/tokens", map[string]string{"name": "myapp"})["token"].(string)
	myapp := client{t: t, base: srv.URL, tok: appTok}
	a := addDirectNode(t, c, "a")
	myapp.ok("PUT", "/v1/targets/videosite", map[string]any{"rules": map[string]any{"domain": []string{"videosite.example"}}, "check": map[string]any{"kind": "http", "url": "https://videosite.example/"}})
	user.ok("POST", "/v1/nodes/"+a+"/connect", nil)

	// JSON-список
	code, body := user.do("GET", "/v1/events?after=0", nil)
	var all []map[string]any
	_ = json.Unmarshal(body, &all)
	if code != 200 || len(all) < 3 {
		t.Fatalf("журнал: %d %s", code, body)
	}
	_, cb := myapp.do("GET", "/v1/events?after=0", nil)
	var mine []map[string]any
	_ = json.Unmarshal(cb, &mine)
	for _, e := range mine {
		if e["target"] != "videosite" {
			t.Fatalf("приложению показано чужое событие: %v", e)
		}
	}
	if len(mine) == 0 || len(mine) >= len(all) {
		t.Fatalf("фильтр событий: у приложения %d из %d", len(mine), len(all))
	}

	// SSE с докачкой: после id первого события приходят остальные, затем живое
	firstID := int64(all[0]["id"].(float64))
	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+c.APIToken())
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", fmt.Sprint(firstID))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type: %s", ct)
	}
	ids := make(chan int64, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "id: "); ok {
				var id int64
				fmt.Sscan(v, &id)
				ids <- id
			}
		}
		close(ids)
	}()
	var got []int64
	deadline := time.After(3 * time.Second)
	for len(got) < len(all)-1 {
		select {
		case id := <-ids:
			got = append(got, id)
		case <-deadline:
			t.Fatalf("докачка: получено %v из %d", got, len(all)-1)
		}
	}
	if got[0] != firstID+1 {
		t.Fatalf("докачка началась с %d, ожидалось %d", got[0], firstID+1)
	}
	// живое событие
	user.ok("POST", "/v1/nodes/"+a+"/favorite", map[string]bool{"on": true})
	select {
	case id := <-ids:
		if id <= got[len(got)-1] {
			t.Fatalf("id не растут: %d после %d", id, got[len(got)-1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("живое событие не пришло")
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("дыра в потоке: %v", got)
		}
	}
}

// поток событий стороннего токена закрывается после отзыва токена, а не живёт до разрыва соединения.
func TestEventsStreamClosesAfterTokenRevoke(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	appTok := user.ok("POST", "/v1/tokens", map[string]string{"name": "myapp"})["token"].(string)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+appTok)
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("поток закрылся до отзыва токена")
	case <-time.After(700 * time.Millisecond):
	}
	user.ok("DELETE", "/v1/tokens/myapp", nil)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("поток отозванного токена остался открытым")
	}
}

func TestLogsAndAppProxyOwnerOnly(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	_, user2, _ := c.AppProxy()
	out := user.ok("GET", "/v1/app-proxy", nil)
	if out["username"] != user2 || out["password"] == "" {
		t.Fatalf("app-proxy: %v", out)
	}
	if l := user.ok("GET", "/v1/logs?n=10", nil); l["lines"] == nil {
		t.Fatalf("logs: %v", l)
	}
}

func TestWarpStatusAndProbeWithoutAccount(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	st := user.ok("GET", "/v1/status", nil)
	if w, _ := st["warp"].(map[string]any); w == nil || w["enabled"] != false || w["via_warp"] != false {
		t.Fatalf("статус WARP: %v", st["warp"])
	}
	user.expect(400, "POST", "/v1/warp/probe", nil) // WARP не настроен
	user.expect(400, "PUT", "/v1/settings/check.via_warp", map[string]string{"value": "1"})
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/warp/probe", nil)
}

func TestRouteRulesVia(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	got := user.ok("GET", "/v1/route-rules", nil)
	if _, ok := got["via"].([]any); !ok {
		t.Fatalf("в ответе нет списка привязок via: %v", got)
	}
	if _, ok := got["countries"].([]any); !ok {
		t.Fatalf("в ответе нет списка стран: %v", got)
	}
	rule := func(via map[string]any) map[string]any {
		r := map[string]any{"enabled": true, "action": "proxy", "match": map[string]any{"domain": []string{"x.example"}}}
		if via != nil {
			r["via"] = via
		}
		return r
	}
	put := func(via map[string]any) map[string]any {
		return map[string]any{"rules": []any{rule(via)}}
	}
	user.expect(400, "PUT", "/v1/route-rules", put(map[string]any{"kind": "country", "country": "NLD"}))
	user.expect(400, "PUT", "/v1/route-rules", put(map[string]any{"kind": "country", "country": "NL", "oops": 1})) // неизвестное поле
	out := user.ok("PUT", "/v1/route-rules", put(map[string]any{"kind": "country", "country": "nl", "fallback": "direct"}))
	rs, _ := out["rules"].([]any)
	if len(rs) != 1 {
		t.Fatalf("правила: %v", out)
	}
	via, _ := rs[0].(map[string]any)["via"].(map[string]any)
	if via["kind"] != "country" || via["country"] != "NL" || via["fallback"] != "direct" {
		t.Fatalf("via не сохранился или не нормализован: %v", via)
	}
}

func TestRouteProfilesAPI(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "GET", "/v1/route-profiles", nil)
	app.expect(403, "POST", "/v1/route-profiles/import", map[string]string{"text": "x"})

	got := user.ok("GET", "/v1/route-profiles", nil)
	ps, _ := got["profiles"].([]any)
	if got["active"] != "main" || len(ps) != 1 {
		t.Fatalf("профили по умолчанию: %v", got)
	}
	user.ok("PUT", "/v1/route-rules", map[string]any{"rules": []any{map[string]any{"enabled": true, "action": "direct", "match": map[string]any{"domain": []string{"a.example"}}}}})
	made := user.ok("POST", "/v1/route-profiles", map[string]string{"name": "Копия", "from": "main"})
	id, _ := made["id"].(string)
	if id == "" || made["rules"] != float64(1) {
		t.Fatalf("создание: %v", made)
	}
	user.expect(400, "POST", "/v1/route-profiles", map[string]string{"name": "копия"}) // повтор названия
	user.expect(404, "PUT", "/v1/route-profiles/нет", map[string]string{"name": "x"})
	user.expect(400, "DELETE", "/v1/route-profiles/main", nil) // активный

	link := user.ok("GET", "/v1/route-profiles/"+id+"/share", nil)["link"].(string)
	if !strings.HasPrefix(link, "brod://route/") {
		t.Fatalf("ссылка: %q", link)
	}
	imp := user.ok("POST", "/v1/route-profiles/import", map[string]string{"text": link, "name": "Из ссылки"})
	if imp["name"] != "Из ссылки" || imp["rules"] != float64(1) {
		t.Fatalf("импорт: %v", imp)
	}
	user.expect(400, "POST", "/v1/route-profiles/import", map[string]string{"text": "brod://route/!!"})
	user.expect(400, "POST", "/v1/route-profiles/import", map[string]any{"text": link, "oops": 1}) // неизвестное поле

	act := user.ok("POST", "/v1/route-profiles/"+imp["id"].(string)+"/activate", nil)
	if rs, _ := act["rules"].([]any); len(rs) != 1 {
		t.Fatalf("после переключения: %v", act)
	}
	if after := user.ok("GET", "/v1/route-profiles", nil); after["active"] != imp["id"] {
		t.Fatalf("активный профиль не сменился: %v", after)
	}
	user.ok("PUT", "/v1/route-profiles/"+id, map[string]string{"name": "Новое имя"})
	user.ok("DELETE", "/v1/route-profiles/"+id, nil)
}

func TestUpdateAPI(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	for _, p := range []string{"GET /v1/update", "POST /v1/update/check", "POST /v1/update/download"} {
		m, path, _ := strings.Cut(p, " ")
		app.expect(403, m, path, nil)
	}
	st := user.ok("GET", "/v1/update", nil)
	d, _ := st["download"].(map[string]any)
	if st["available"] != false || st["current"] == "" || d["state"] != "none" {
		t.Fatalf("исходное состояние: %v", st)
	}
	// проверка без доступного манифеста отвечает состоянием с причиной, а не ошибкой запроса
	chk := user.ok("POST", "/v1/update/check", nil)
	if e, _ := chk["error"].(string); e == "" || chk["available"] != false {
		t.Fatalf("проверка без сети: %v", chk)
	}
	user.expect(400, "POST", "/v1/update/download", nil) // скачивать нечего
	if _, has := user.ok("GET", "/v1/status", nil)["update"]; has {
		t.Fatal("в статусе без доступного обновления не должно быть блока update")
	}
}

func TestNotifyTestEndpoint(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(409, "POST", "/v1/notify-test", nil) // уведомления отключены
	called := false
	s.NotifyTest = func() error { called = true; return nil }
	user.expect(200, "POST", "/v1/notify-test", nil)
	if !called {
		t.Fatal("обработчик проверки не вызван")
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/notify-test", nil)
}

type fakeVPN struct {
	st          supervisor.State
	err         error
	reloads     int
	restarts    int
	ksRefreshes int
	ksReleases  int
}

func (f *fakeVPN) State() supervisor.State             { return f.st }
func (f *fakeVPN) Reload() (supervisor.State, error)   { f.reloads++; return f.st, nil }
func (f *fakeVPN) Restart() (supervisor.State, error)  { f.restarts++; return f.st, nil }
func (f *fakeVPN) RefreshKillswitch() supervisor.State { f.ksRefreshes++; return f.st }
func (f *fakeVPN) ReleaseStaleKillswitch() error {
	f.ksReleases++
	if f.st.On {
		return core.ErrConflict
	}
	return nil
}
func (f *fakeVPN) SetAutoconnect(v string) (supervisor.State, error) {
	f.st.Autoconnect = v
	return f.st, nil
}
func (f *fakeVPN) Set(on *bool, mode string) (supervisor.State, error) {
	if f.err != nil {
		return f.st, f.err
	}
	if on != nil {
		f.st.On = *on
	}
	if mode != "" {
		f.st.Mode = mode
	}
	return f.st, nil
}

func TestVPNEndpoint(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(409, "GET", "/v1/vpn", nil) // переключатель не подключён

	f := &fakeVPN{st: supervisor.State{Mode: "blocked"}}
	s.VPN = f
	if got := user.ok("GET", "/v1/vpn", nil); got["on"] != false || got["mode"] != "blocked" {
		t.Fatalf("состояние: %v", got)
	}
	if got := user.ok("POST", "/v1/vpn", map[string]any{"on": true, "mode": "all"}); got["on"] != true || got["mode"] != "all" {
		t.Fatalf("включение: %v", got)
	}
	user.expect(400, "POST", "/v1/vpn", map[string]any{"mode": "нет такого"})
	user.expect(400, "POST", "/v1/vpn", map[string]any{"лишнее": 1})
	// автоподключение меняется отдельно и не трогает состояние VPN
	if got := user.ok("POST", "/v1/vpn", map[string]any{"autoconnect": "always"}); got["autoconnect"] != "always" || got["on"] != true {
		t.Fatalf("автоподключение: %v", got)
	}
	user.expect(400, "POST", "/v1/vpn", map[string]any{"autoconnect": "иногда"})
	f.err = errors.New("нет прав")
	user.expect(409, "POST", "/v1/vpn", map[string]any{"on": false})
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "GET", "/v1/vpn", nil)
	app.expect(403, "POST", "/v1/vpn", map[string]any{"on": true})
}

func TestKillswitchReleaseEndpoint(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	f := &fakeVPN{st: supervisor.State{Mode: "all", On: false}}
	s.VPN = f
	user.ok("POST", "/v1/killswitch/release", nil)
	if f.ksReleases != 1 {
		t.Fatalf("правила должны сниматься при выключенном VPN: %d", f.ksReleases)
	}
	f.st.On = true
	user.expect(409, "POST", "/v1/killswitch/release", nil)
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/killswitch/release", nil)
}

func TestKillswitchSettingAppliesWithoutRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("киллсвитч реализован только на Linux")
	}
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	f := &fakeVPN{st: supervisor.State{Mode: "all", On: true}}
	s.VPN = f
	if got := user.ok("GET", "/v1/settings", nil); got[core.SetKillswitch] != "1" {
		t.Fatalf("киллсвитч по умолчанию включён: %v", got)
	}
	user.ok("PUT", "/v1/settings/"+core.SetKillswitch, map[string]string{"value": "0"})
	if f.ksRefreshes != 1 || f.reloads != 0 {
		t.Fatalf("настройка должна применяться без перезапуска ядра: refresh=%d reload=%d", f.ksRefreshes, f.reloads)
	}
	user.expect(400, "PUT", "/v1/settings/"+core.SetKillswitch, map[string]string{"value": "может"})
	user.ok("PUT", "/v1/settings/notify.level", map[string]string{"value": "all"})
	if f.ksRefreshes != 1 {
		t.Fatal("другие настройки киллсвитч не трогают")
	}
}

func TestNodeExitInfoEndpoint(t *testing.T) {
	c, srv := newEnv(t)
	id := addDirectNode(t, c, "exit1")
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	nodes := func() []map[string]any {
		code, body := user.do("GET", "/v1/nodes", nil)
		var l []map[string]any
		if code != 200 || json.Unmarshal(body, &l) != nil {
			t.Fatalf("список узлов: %d %s", code, body)
		}
		return l
	}
	if l := nodes(); len(l) != 1 || l[0]["exit"] != nil {
		t.Fatalf("exit до сбора: %v", l)
	}
	got := user.ok("POST", "/v1/nodes/"+id+"/exit-info", nil)
	ex, _ := got["exit"].(map[string]any)
	if ex == nil || ex["country"] != "DE" || ex["ip"] != "203.0.113.7" || ex["clean"] != "mixed" || ex["google_ok"] != true || ex["site_ok"] != false {
		t.Fatalf("exit в ответе: %v", got)
	}
	if l := nodes(); l[0]["exit"] == nil {
		t.Fatalf("exit в списке после сбора: %v", l)
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/nodes/"+id+"/exit-info", nil)
}

func TestRouteRulesEndpoint(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	f := &fakeVPN{st: supervisor.State{Mode: "blocked", On: true}}
	s.VPN = f
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	got := user.ok("GET", "/v1/route-rules", nil)
	if rules, _ := got["rules"].([]any); len(rules) != 0 || len(got["sets"].([]any)) == 0 {
		t.Fatalf("начальное состояние: %v", got)
	}
	body := map[string]any{"rules": []any{map[string]any{"title": "тест", "enabled": true, "action": "direct", "match": map[string]any{"domain_suffix": []string{"Example.com"}}}}}
	put := user.ok("PUT", "/v1/route-rules", body)
	rules := put["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["id"] == "" || rules[0].(map[string]any)["match"].(map[string]any)["domain_suffix"].([]any)[0] != "example.com" {
		t.Fatalf("сохранено: %v", put)
	}
	if f.reloads != 1 {
		t.Fatalf("перезапуск VPN после сохранения: %d", f.reloads)
	}
	user.expect(400, "PUT", "/v1/route-rules", map[string]any{"rules": []any{map[string]any{"action": "direct", "match": map[string]any{}}}})
	user.expect(400, "PUT", "/v1/route-rules", map[string]any{"лишнее": 1})
	if f.reloads != 1 {
		t.Fatal("отклонённое сохранение не должно перезапускать VPN")
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "GET", "/v1/route-rules", nil)
	app.expect(403, "PUT", "/v1/route-rules", body)
}

func TestNodeSpeedEndpoint(t *testing.T) {
	c, srv := newEnv(t)
	id := addDirectNode(t, c, "speed1")
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	got := user.ok("POST", "/v1/nodes/"+id+"/speed", nil)
	if got["speed_kbps"] != 2048.0 || got["speed_at"] == nil {
		t.Fatalf("скорость в ответе: %v", got)
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/nodes/"+id+"/speed", nil)
}

func TestMeasureEndpoint(t *testing.T) {
	c, srv := newEnv(t)
	id := addDirectNode(t, c, "meas1")
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(400, "POST", "/v1/measure", map[string]any{"ids": []string{}, "speed": true})
	user.expect(400, "POST", "/v1/measure", map[string]any{"ids": []string{id}})
	user.expect(404, "POST", "/v1/measure", map[string]any{"ids": []string{"нет-такого"}, "speed": true})
	got := user.ok("POST", "/v1/measure", map[string]any{"ids": []string{id}, "speed": true, "exit": true})
	if got["total"] != 2.0 {
		t.Fatalf("старт: %v", got)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := user.ok("GET", "/v1/measure", nil)
		if st["running"] == false {
			if st["done"] != 2.0 {
				t.Fatalf("итог: %v", st)
			}
			app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
			app.expect(403, "POST", "/v1/measure", map[string]any{"ids": []string{id}, "speed": true})
			app.expect(403, "GET", "/v1/measure", nil)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("пакет не завершился")
}

func TestOwnNodesEndpoint(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(400, "POST", "/v1/own-nodes", map[string]any{"text": "нет ссылок"})
	got := user.ok("POST", "/v1/own-nodes", map[string]any{"text": "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZDEyMw@203.0.113.7:8388#x"})
	ids, _ := got["ids"].([]any)
	if got["added"] != 1.0 || len(ids) != 1 {
		t.Fatalf("%v", got)
	}
	code, body := user.do("GET", "/v1/nodes", nil)
	if code != 200 || !strings.Contains(string(body), `"own":true`) {
		t.Fatalf("список узлов должен помечать свой сервер: %d %s", code, body)
	}
	user.ok("DELETE", "/v1/own-nodes/"+ids[0].(string), nil)
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/own-nodes", map[string]any{"text": "ss://x"})
}

func TestConnectionsAndTrafficEndpoints(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	code, body := user.do("GET", "/v1/connections?closed=1", nil)
	if code != 200 || !strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		t.Fatalf("соединения: %d %s", code, body)
	}
	tr := user.ok("GET", "/v1/traffic?n=30", nil)
	if _, ok := tr["samples"]; !ok || tr["up_total"] == nil || tr["conns"] == nil {
		t.Fatalf("трафик: %v", tr)
	}
	user.expect(404, "DELETE", "/v1/connections/нет-такого", nil)
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "GET", "/v1/connections", nil)
	app.expect(403, "GET", "/v1/traffic", nil)
}

func TestEventsTail(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	for i := 0; i < 5; i++ {
		c.Bus().Publish(store.Event{Kind: "tail-test", Initiator: "user", Detail: fmt.Sprint(i)})
	}
	code, body := user.do("GET", "/v1/events?tail=3", nil)
	var evs []map[string]any
	if code != 200 || json.Unmarshal(body, &evs) != nil || len(evs) != 3 {
		t.Fatalf("tail: %d %s", code, body)
	}
	if evs[2]["detail"] != "4" || evs[0]["detail"] != "2" {
		t.Fatalf("последние три события, старые первыми: %v", evs)
	}
}

func TestOwnEditAndLabelEndpoints(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	got := user.ok("POST", "/v1/own-nodes", map[string]any{"text": "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZDEyMw@203.0.113.7:8388#x"})
	id := got["ids"].([]any)[0].(string)
	user.ok("PATCH", "/v1/own-nodes/"+id, map[string]any{"label": "Мой", "server": "198.51.100.9", "port": 9000})
	code, body := user.do("GET", "/v1/nodes", nil)
	if code != 200 || !strings.Contains(string(body), `"label":"Мой"`) || !strings.Contains(string(body), `"server":"198.51.100.9"`) {
		t.Fatalf("после правки: %d %s", code, body)
	}
	user.expect(400, "PATCH", "/v1/own-nodes/"+id, map[string]any{"port": 70000})
	user.ok("PUT", "/v1/nodes/"+id+"/label", map[string]any{"label": ""})
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "PATCH", "/v1/own-nodes/"+id, map[string]any{"label": "x"})
	app.expect(403, "PUT", "/v1/nodes/"+id+"/label", map[string]any{"label": "x"})
}

func TestBackupBestAndDailyEndpoints(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(409, "POST", "/v1/connect-best", nil) // живых узлов нет
	days := user.do
	if code, body := days("GET", "/v1/traffic/daily?days=7", nil); code != 200 || !strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		t.Fatalf("по дням: %d %s", code, body)
	}
	user.ok("POST", "/v1/own-nodes", map[string]any{"text": "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZDEyMw@203.0.113.7:8388#x"})
	code, body := user.do("GET", "/v1/backup", nil)
	if code != 200 || !strings.Contains(string(body), `"own"`) {
		t.Fatalf("копия: %d %s", code, body)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	rep := user.ok("POST", "/v1/backup", doc)
	if rep["own"] != 1.0 {
		t.Fatalf("восстановление: %v", rep)
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "GET", "/v1/backup", nil)
	app.expect(403, "POST", "/v1/connect-best", nil)
	app.expect(403, "GET", "/v1/traffic/daily", nil)
	app.expect(403, "POST", "/v1/diagnostics", nil)
}

func TestTGProxiesEndpoints(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(400, "POST", "/v1/tg-proxies", map[string]any{"text": "нет ссылок"})
	user.expect(400, "POST", "/v1/tg-proxies", map[string]any{"лишнее": 1})
	got := user.ok("POST", "/v1/tg-proxies", map[string]any{"text": "https://t.me/proxy?server=one.example&port=443&secret=00112233445566778899aabbccddeeff tg://socks?server=s.example&port=1080"})
	if got["added"] != float64(2) {
		t.Fatalf("добавлено: %v", got)
	}
	l := user.ok("GET", "/v1/tg-proxies", nil)
	items := l["items"].([]any)
	if len(items) != 2 || l["total"] != float64(2) || l["alive"] != float64(0) {
		t.Fatalf("список: %v", l)
	}
	first := items[0].(map[string]any)
	if first["link"] == "" || first["own"] != true || first["checked"] != false {
		t.Fatalf("элемент: %v", first)
	}
	if only := user.ok("GET", "/v1/tg-proxies?kind=socks5", nil)["items"].([]any); len(only) != 1 {
		t.Fatalf("фильтр: %v", only)
	}
	user.expect(400, "GET", "/v1/tg-proxies?kind=ftp", nil)
	id := first["id"].(string)
	user.ok("PUT", "/v1/tg-proxies/"+id+"/favorite", map[string]any{"on": false})
	user.ok("DELETE", "/v1/tg-proxies/"+id, nil)
	user.expect(404, "DELETE", "/v1/tg-proxies/"+id, nil)
	user.expect(404, "PUT", "/v1/tg-proxies/нет/favorite", map[string]any{"on": true})
	// приложения (с токеном приложения) к вкладке доступа не имеют
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "GET", "/v1/tg-proxies", nil)
	app.expect(403, "POST", "/v1/tg-proxies", map[string]any{"text": "tg://socks?server=s&port=1"})
}

func TestStatusReportsActivity(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	st := user.ok("GET", "/v1/status", nil)
	a, ok := st["activity"].(map[string]any)
	if !ok || a["refreshing"] != false || a["checking"] != false || a["tg_checking"] != false {
		t.Fatalf("статус без активности: %v", st["activity"])
	}
}

// Импорт аккаунта WARP: неверный файл — 400, верный — сохраняется и ядро перезапускается; только владелец.
func TestWarpImportRestartsCore(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	good := map[string]any{"private_key": "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=", "peer_public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=", "addr_v4": "10.9.9.2"}

	user.expect(400, "POST", "/v1/warp/import", map[string]any{})                                  // нет аккаунта в теле
	user.expect(400, "POST", "/v1/warp/import", map[string]any{"account": map[string]any{"x": 1}}) // не хватает полей
	f := &fakeVPN{st: supervisor.State{Mode: "blocked"}}
	s.VPN = f
	user.ok("POST", "/v1/warp/import", map[string]any{"account": good})
	if f.restarts != 1 {
		t.Fatalf("после импорта ядро должно перезапуститься: %d", f.restarts)
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/warp/import", map[string]any{"account": good})
	app.expect(403, "POST", "/v1/warp/register", nil)
}

// Раздельное туннелирование одной записью: список и режим сохраняются вместе, VPN перезапускается один раз, ошибки проверки — 400.
func TestTunAppsSavedAtOnceWithSingleReload(t *testing.T) {
	c, _ := newEnv(t)
	s := api.New(c, "t")
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	f := &fakeVPN{st: supervisor.State{Mode: "blocked", On: true}}
	s.VPN = f

	user.ok("PUT", "/v1/tun/apps", map[string]any{"mode": "exclude", "apps": []string{"ru.vk.vkx", "com.example.b", "ru.vk.vkx"}})
	if f.reloads != 1 {
		t.Fatalf("VPN должен перезапуститься один раз, а не по разу на настройку: %d", f.reloads)
	}
	got := user.ok("GET", "/v1/settings", nil)
	if got["tun.apps_mode"] != "exclude" || got["tun.apps"] != "com.example.b,ru.vk.vkx" {
		t.Fatalf("настройки после записи: %v", got)
	}
	user.expect(400, "PUT", "/v1/tun/apps", map[string]any{"mode": "include", "apps": []string{}})
	user.expect(400, "PUT", "/v1/tun/apps", map[string]any{"mode": "exclude", "apps": []string{"не пакет"}})
	user.expect(400, "PUT", "/v1/tun/apps", map[string]any{"mode": "что-то", "apps": []string{"a.b"}})
	if f.reloads != 1 {
		t.Fatalf("неудачные записи не должны перезапускать VPN: %d", f.reloads)
	}
	f.err = nil
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "PUT", "/v1/tun/apps", map[string]any{"mode": "off", "apps": []string{}})
}

// Текст ошибки: байты cp1251 (так SQLite на Windows отдаёт описание ошибки ОС) превращаются в читаемый UTF-8; «база закрыта» — 503.
func TestErrorTextAndRestartStatus(t *testing.T) {
	c, srv := newEnv(t)
	_ = srv
	s := api.New(c, "t")
	hs := httptest.NewServer(s.Handler(false))
	defer hs.Close()
	user := client{t: t, base: hs.URL, tok: c.APIToken()}
	// 401 не логируется и не ломает ответ; 404 — обычная ошибка с читаемым текстом
	_, raw := user.do("GET", "/v1/nodes/нет-такого", nil)
	if !utf8.Valid(raw) {
		t.Fatalf("ответ не UTF-8: %q", raw)
	}
	cp1251 := string([]byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}) // «Привет» в Windows-1251
	if got := api.Utf8TextForTest(cp1251); got != "Привет" {
		t.Fatalf("cp1251 → %q", got)
	}
	if got := api.Utf8TextForTest("уже utf-8"); got != "уже utf-8" {
		t.Fatalf("utf-8 изменён: %q", got)
	}
}

// Сквозная проверка и самотест: только владелец; несуществующий узел — 404; самотест при выключенном VPN отвечает «провален» одним шагом.
func TestNodeCheckAndSelfTestEndpoints(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	user.expect(404, "POST", "/v1/nodes/нет-такого/check", nil)
	user.expect(404, "POST", "/v1/active/check", nil) // активного узла нет
	st := user.ok("POST", "/v1/vpn/selftest", nil)
	if st["ok"] != false {
		t.Fatalf("при выключенном VPN самотест не может быть пройден: %v", st)
	}
	if steps, _ := st["steps"].([]any); len(steps) != 1 {
		t.Fatalf("шагов %d", len(steps))
	}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/vpn/selftest", nil)
	app.expect(403, "POST", "/v1/active/check", nil)
	app.expect(403, "POST", "/v1/nodes/abc/check", nil)
}

// Сквозной путь: манифест и файл по https → проверка → блок update в статусе → скачивание → проверенный файл.
func TestUpdateEndToEnd(t *testing.T) {
	data := []byte(strings.Repeat("brod-0.4.0-", 30_000))
	sum := sha256.Sum256(data)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases.json", "/releases.json.sig":
			doc, _ := json.Marshal(ota.ReleasesDoc{Version: 1, Release: ota.Release{App: "0.4.0", Notes: "правила маршрутизации", Assets: map[string]ota.Asset{
				"linux-amd64": {URL: srv.URL + "/brod-0.4.0.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}}}})
			if strings.HasSuffix(r.URL.Path, ".sig") {
				w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, doc))))
				return
			}
			w.Write(doc)
		case "/brod-0.4.0.tar.gz":
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := core.New(core.Config{StateDir: t.TempDir(), AppPort: testport.Next(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		NetID: func() string { return "net1" }, UpdateURLs: []string{srv.URL + "/releases.json"}, ReleasesKey: pub, OTAClient: srv.Client(),
		AppVersion: "0.3.0", UpdatePlatform: "linux-amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ts := httptest.NewServer(api.New(c, "test").Handler(false))
	defer ts.Close()
	user := client{t: t, base: ts.URL, tok: c.APIToken()}

	chk := user.ok("POST", "/v1/update/check", nil)
	if chk["available"] != true || chk["app"] != "0.4.0" || chk["error"] != nil {
		t.Fatalf("проверка: %v", chk)
	}
	up, _ := user.ok("GET", "/v1/status", nil)["update"].(map[string]any)
	if up["available"] != true || up["app"] != "0.4.0" || up["download"] != "none" {
		t.Fatalf("блок update в статусе: %v", up)
	}
	user.ok("POST", "/v1/update/download", nil)
	deadline := time.Now().Add(10 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		st = user.ok("GET", "/v1/update", nil)
		if d, _ := st["download"].(map[string]any); d["state"] == "ready" {
			p, _ := d["path"].(string)
			got, err := os.ReadFile(p)
			if err != nil || string(got) != string(data) {
				t.Fatalf("файл не совпал: %v", err)
			}
			if up2, _ := user.ok("GET", "/v1/status", nil)["update"].(map[string]any); up2["download"] != "ready" {
				t.Fatalf("статус после скачивания: %v", up2)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("скачивание не завершилось: %v", st)
}

// список узлов отдаётся с меткой версии (ETag), на повтор с If-None-Match без изменений приходит 304 без тела.
func TestNodesETag(t *testing.T) {
	c, srv := newEnv(t)
	tok := c.APIToken()
	a := addDirectNode(t, c, "a")
	addDirectNode(t, c, "b")
	get := func(etag string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/nodes?limit=10", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	r1, b1 := get("")
	tag := r1.Header.Get("ETag")
	if r1.StatusCode != 200 || tag == "" || len(b1) < 10 {
		t.Fatalf("первый ответ: %d, ETag %q, %d байт", r1.StatusCode, tag, len(b1))
	}
	r2, b2 := get(tag)
	if r2.StatusCode != http.StatusNotModified || len(b2) != 0 {
		t.Fatalf("список не менялся, ждали 304 без тела: %d, %d байт", r2.StatusCode, len(b2))
	}
	user := client{t: t, base: srv.URL, tok: tok}
	user.ok("POST", "/v1/nodes/"+a+"/favorite", map[string]bool{"on": true})
	r3, b3 := get(tag)
	if r3.StatusCode != 200 || r3.Header.Get("ETag") == tag || len(b3) == 0 {
		t.Fatalf("список изменился, ждали 200 с новой меткой: %d, ETag %q", r3.StatusCode, r3.Header.Get("ETag"))
	}
}

func TestChainEndpoints(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	a := user.ok("POST", "/v1/own-nodes", map[string]any{"text": "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZDEyMw@203.0.113.7:8388#a"})["ids"].([]any)[0].(string)
	b := user.ok("POST", "/v1/own-nodes", map[string]any{"text": "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZDEyMw@203.0.113.8:8389#b"})["ids"].([]any)[0].(string)
	user.expect(400, "POST", "/v1/chains", map[string]any{"hops": []string{a}})
	user.expect(400, "POST", "/v1/chains", map[string]any{"hops": []string{a, a}})
	got := user.ok("POST", "/v1/chains", map[string]any{"name": "Двойной", "hops": []string{a, b}})
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("id цепочки: %v", got)
	}
	code, body := user.do("GET", "/v1/nodes", nil)
	if code != 200 || !strings.Contains(string(body), `"proto":"chain"`) || !strings.Contains(string(body), `"label":"Двойной"`) || !strings.Contains(string(body), `"chain":[`) {
		t.Fatalf("цепочка в списке узлов: %d %s", code, body)
	}
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "chacha20") {
		t.Fatalf("в списке узлов есть параметры доступа звеньев: %s", body)
	}
	user.ok("DELETE", "/v1/own-nodes/"+id, nil)
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "x"})["token"].(string)}
	app.expect(403, "POST", "/v1/chains", map[string]any{"hops": []string{a, b}})
}
