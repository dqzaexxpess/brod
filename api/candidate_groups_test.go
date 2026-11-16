package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"brod/aggregator"
	"brod/api"
	"brod/core"
	"brod/internal/testport"
)

// Группы кандидатов-зеркал: поля group/group_size, GET /v1/source-candidate-groups, скрытие группы, наследование «скрыто», 403 для токена приложения.
func TestSourceCandidateGroupsAPI(t *testing.T) {
	var base string
	var mu sync.Mutex
	mirror := candList(100, "mir")
	files := map[string]string{
		"/o1/r1/main/README.md": "[a](BASE/o1/r1/main/mir-a.txt) [b](BASE/o1/r1/main/mir-b.txt) [z](BASE/o1/r1/main/zzz.txt)",
		"/o1/r1/main/mir-a.txt": mirror,
		"/o1/r1/main/mir-b.txt": mirror + candList(10, "bx"),
		"/o1/r1/main/zzz.txt":   candList(100, "zzz"),
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body, ok := files[r.URL.Path]
		mu.Unlock()
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
		DiscoverBase: base, DiscoverCanProbe: func() bool { return false }})
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

	app.expect(403, "GET", "/v1/source-candidate-groups", nil)
	app.expect(403, "POST", "/v1/source-candidate-groups/abc/dismiss", nil)

	_, raw := user.do("GET", "/v1/source-candidate-groups", nil)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("до прохода групп нет, ждали []: %s", raw)
	}
	user.ok("POST", "/v1/source-candidates/scan", nil)

	type cand struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Links     int    `json:"links"`
		Group     string `json:"group"`
		GroupSize int    `json:"group_size"`
	}
	type group struct {
		ID             string `json:"id"`
		Representative cand   `json:"representative"`
		Members        []cand `json:"members"`
		Size           int    `json:"size"`
		TotalLinksMax  int    `json:"total_links_max"`
	}
	getGroups := func() []group {
		code, raw := user.do("GET", "/v1/source-candidate-groups", nil)
		if code != 200 {
			t.Fatalf("группы: %d %s", code, raw)
		}
		if strings.Contains(string(raw), "11111111-2222") {
			t.Error("в ответе ссылка узла")
		}
		var g []group
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	groups := getGroups()
	if len(groups) != 2 {
		t.Fatalf("групп %d", len(groups))
	}
	var mir group
	for _, g := range groups {
		if g.Size == 2 {
			mir = g
		}
	}
	if mir.ID == "" || !strings.HasSuffix(mir.Representative.Name, "/mir-b") || len(mir.Members) != 1 || !strings.HasSuffix(mir.Members[0].Name, "/mir-a") ||
		mir.TotalLinksMax != 110 || mir.Representative.Group != mir.ID || mir.Representative.GroupSize != 2 {
		t.Fatalf("группа зеркал: %+v", mir)
	}
	_, raw = user.do("GET", "/v1/source-candidates", nil)
	var list []cand
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 3 {
		t.Fatalf("список: %v %s", err, raw)
	}
	for _, v := range list {
		if strings.Contains(v.Name, "/mir-") && (v.Group != mir.ID || v.GroupSize != 2) {
			t.Errorf("%+v вне группы зеркал", v)
		}
	}

	// добавление представителя не трогает остальных членов
	user.ok("POST", "/v1/source-candidates/"+mir.Representative.ID+"/add", nil)
	if g := getGroups(); len(g) != 2 {
		t.Errorf("после добавления представителя групп %d", len(g))
	}

	user.expect(404, "POST", "/v1/source-candidate-groups/ffffffffffff/dismiss", nil)
	var zzz group
	for _, g := range getGroups() {
		if g.Size == 1 && strings.HasSuffix(g.Representative.Name, "/zzz") {
			zzz = g
		}
	}
	res := user.ok("POST", "/v1/source-candidate-groups/"+zzz.ID+"/dismiss", nil)
	if res["dismissed"].(float64) != 1 {
		t.Errorf("dismiss: %v", res)
	}
	user.expect(404, "POST", "/v1/source-candidate-groups/"+zzz.ID+"/dismiss", nil) // уже скрыта

	// скрываем оставшуюся (mir-a) как группу; новое зеркало потом не предлагается
	left := getGroups()
	if len(left) != 1 {
		t.Fatalf("осталось групп %d", len(left))
	}
	user.ok("POST", "/v1/source-candidate-groups/"+left[0].ID+"/dismiss", nil)
	mu.Lock()
	files["/o1/r1/main/README.md"] += " [c](BASE/o1/r1/main/mir-c.txt)"
	files["/o1/r1/main/mir-c.txt"] = mirror + candList(3, "cx")
	mu.Unlock()
	user.ok("POST", "/v1/source-candidates/scan", nil)
	if g := getGroups(); len(g) != 0 {
		t.Errorf("новое зеркало скрытой группы не унаследовало «скрыто»: %+v", g)
	}
	_, raw = user.do("GET", "/v1/source-candidates", nil)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("список кандидатов не пуст: %s", raw)
	}
}
