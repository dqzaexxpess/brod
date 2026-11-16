package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"brod/aggregator"
	"brod/api"
	"brod/core"
	"brod/internal/testport"
	"brod/prober"
	"brod/store"
)

func candList(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "vless://11111111-2222-3333-4444-%012d@%s%d.example.org:443?security=tls#%s%d\n", i, tag, i, tag, i)
	}
	return b.String()
}

// Маршруты автопоиска источников: список, внеочередной проход, добавление, скрытие; токен приложения не пускается.
func TestSourceCandidatesAPI(t *testing.T) {
	var base string
	files := map[string]string{
		"/o1/r1/main/README.md": "[a](BASE/o1/r1/main/alpha.txt) [b](BASE/o1/r1/main/beta.txt)",
		"/o1/r1/main/alpha.txt": candList(30, "alpha"),
		"/o1/r1/main/beta.txt":  candList(25, "beta"),
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(body, "BASE", strings.TrimSuffix(base, "/")))
	}))
	defer fake.Close()
	base = fake.URL + "/"
	c, err := core.New(core.Config{StateDir: t.TempDir(), AppPort: testport.Next(), LogLevel: "error", Manual: true,
		SeedSources:  []aggregator.Source{{Name: "seed", URL: base + "o1/r1/main/seed.txt", Priority: 1}},
		DiscoverBase: base, DiscoverCanProbe: func() bool { return true },
		DiscoverProbe: func(context.Context, store.NodeRec) prober.Result { return prober.Result{Level: 2, OK: true} }})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	srv := httptest.NewServer(api.New(c, "test").Handler(false))
	t.Cleanup(srv.Close)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	app := client{t: t, base: srv.URL, tok: user.ok("POST", "/v1/tokens", map[string]string{"name": "myapp"})["token"].(string)}

	for _, rt := range [][2]string{{"GET", "/v1/source-candidates"}, {"POST", "/v1/source-candidates/scan"}, {"POST", "/v1/source-candidates/abc/add"}, {"POST", "/v1/source-candidates/abc/dismiss"}} {
		app.expect(403, rt[0], rt[1], nil)
	}

	scan := user.ok("POST", "/v1/source-candidates/scan", nil)
	if scan["found"].(float64) != 2 || scan["new"].(float64) != 2 || scan["probed"] != true || scan["sampled"].(float64) != 40 {
		t.Fatalf("scan: %v", scan)
	}
	code, raw := user.do("GET", "/v1/source-candidates", nil)
	if code != 200 {
		t.Fatalf("список: %d %s", code, raw)
	}
	var list []struct {
		ID       string         `json:"id"`
		Name     string         `json:"name"`
		Links    int            `json:"links"`
		Schemes  map[string]int `json:"schemes"`
		DaysSeen int            `json:"days_seen"`
		Sampled  int            `json:"sampled"`
		SampleOK int            `json:"sample_ok"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 2 {
		t.Fatalf("список: %v %s", err, raw)
	}
	if strings.Contains(string(raw), "11111111-2222") {
		t.Error("в ответе ссылка узла")
	}
	if list[0].Links != 30 || list[0].Schemes["vless"] != 30 || list[0].Sampled != 20 || list[0].SampleOK != 20 || list[0].DaysSeen != 1 {
		t.Errorf("первый кандидат: %+v", list[0])
	}
	// в источники и узлы кандидаты не попали
	_, srcRaw := user.do("GET", "/v1/sources", nil)
	if strings.Contains(string(srcRaw), "alpha") {
		t.Errorf("кандидат попал в источники: %s", srcRaw)
	}
	// повторный скан: новых нет (ничего не предлагается заново)
	if again := user.ok("POST", "/v1/source-candidates/scan", map[string]any{}); again["new"].(float64) != 0 {
		t.Errorf("повторный проход: %v", again)
	}

	add := user.ok("POST", "/v1/source-candidates/"+list[0].ID+"/add", nil)
	if add["name"] != list[0].Name {
		t.Fatalf("add: %v", add)
	}
	_, srcRaw = user.do("GET", "/v1/sources", nil)
	if !strings.Contains(string(srcRaw), list[0].Name) {
		t.Errorf("источник не заведён: %s", srcRaw)
	}
	user.expect(400, "POST", "/v1/source-candidates/"+list[0].ID+"/add", nil) // повторно
	user.expect(200, "POST", "/v1/source-candidates/"+list[1].ID+"/dismiss", nil)
	user.expect(404, "POST", "/v1/source-candidates/"+list[1].ID+"x/dismiss", nil)
	user.expect(404, "POST", "/v1/source-candidates/ffffffffffff/add", nil)
	_, raw = user.do("GET", "/v1/source-candidates", nil)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("после добавления и скрытия список не пуст: %s", raw)
	}
	user.expect(400, "POST", "/v1/source-candidates/scan", map[string]any{"лишнее": 1})
}
