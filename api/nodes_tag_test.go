package api_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"brod/store"
)

// метка списка узлов считается от версии хранилища до сборки списка и обязана меняться при любом изменении, видимом в ответе.
// Без правок метка стабильна и приходит 304.
func TestNodesETagFollowsEveryVisibleChange(t *testing.T) {
	c, srv := newEnv(t)
	tok := c.APIToken()
	a := addDirectNode(t, c, "a")
	addDirectNode(t, c, "b")
	user := client{t: t, base: srv.URL, tok: tok}
	get := func(path, etag string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
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
		return resp.StatusCode, resp.Header.Get("ETag"), string(b)
	}
	const path = "/v1/nodes?limit=10"
	code, tag, body := get(path, "")
	if code != 200 || tag == "" {
		t.Fatalf("первый ответ: %d %q", code, tag)
	}
	for i := 0; i < 3; i++ {
		if code, tag2, _ := get(path, tag); code != http.StatusNotModified || tag2 != tag {
			t.Fatalf("список не менялся: %d, метка %q (была %q)", code, tag2, tag)
		}
	}
	st := c.Store()
	now := time.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	steps := []struct {
		name string
		do   func()
	}{
		{"избранное", func() { user.ok("POST", "/v1/nodes/"+a+"/favorite", map[string]bool{"on": true}) }},
		{"закрепление", func() { must(c.SetPinned(a, true, "test")) }},
		{"результат проверки", func() {
			must(st.Record([]store.Row{{Node: a, Net: "net1", Level: 2, OK: true, LatencyMs: 42, At: now}}))
		}},
		{"задержка (новая проба)", func() {
			must(st.Record([]store.Row{{Node: a, Net: "net1", Level: 2, OK: true, LatencyMs: 77, At: now.Add(time.Second)}}))
		}},
		{"скорость", func() { must(st.PutSpeed(store.SpeedRec{Node: a, KBps: 1234, At: now})) }},
		{"выход", func() { must(st.PutExitInfo(store.ExitInfoRec{Node: a, Country: "DE", Colo: "FRA", At: now})) }},
		{"подпись", func() { must(st.SetNodeLabel(a, "мой")) }},
		{"новый узел", func() { addDirectNode(t, c, "c") }},
	}
	for _, s := range steps {
		s.do()
		code, tag2, body2 := get(path, tag)
		if code != 200 || tag2 == tag || body2 == body {
			t.Fatalf("%s: ждали 200, новую метку и новое тело: код %d, метка %q (была %q), тело изменилось: %v", s.name, code, tag2, tag, body2 != body)
		}
		tag, body = tag2, body2
	}
	// метка чужого запроса не даёт 304 на запрос с другими параметрами
	if code, _, _ := get("/v1/nodes?limit=1", tag); code != 200 {
		t.Fatalf("метка другого запроса дала %d", code)
	}
}
