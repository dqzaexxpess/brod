package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"brod/aggregator"
	"brod/core"
)

func TestCandidatesGroupsCommands(t *testing.T) {
	var base string
	var list strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&list, "vless://11111111-2222-3333-4444-%012d@h%d.example.org:443?security=tls#n%d\n", i, i, i)
	}
	files := map[string]string{
		"/o1/r1/main/README.md": "[a](BASE/o1/r1/main/alpha.txt) [b](BASE/o1/r1/main/alpha-copy.txt)",
		"/o1/r1/main/alpha.txt": list.String(),
		// зеркало: те же ссылки, другие комментарии
		"/o1/r1/main/alpha-copy.txt": strings.ReplaceAll(list.String(), "#n", "#m"),
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
	e := setupWith(t, func(c *core.Config) {
		c.DiscoverBase = base
		c.DiscoverCanProbe = func() bool { return false }
		c.SeedSources = []aggregator.Source{{Name: "seed", URL: base + "o1/r1/main/seed.txt", Priority: 1}}
	})
	if out := e.must(t, "candidates", "groups"); !strings.Contains(out, "ПРЕДСТАВИТЕЛЬ") || strings.Contains(out, "alpha") {
		t.Fatalf("пустой список групп: %s", out)
	}
	e.must(t, "candidates", "scan")
	out := e.must(t, "candidates", "groups")
	if !strings.Contains(out, "o1/r1/alpha") || strings.Count(out, "o1/r1/") != 1 || !strings.Contains(out, "не проверялось") {
		t.Fatalf("groups: %s", out)
	}
	var js []map[string]any
	if err := json.Unmarshal([]byte(e.must(t, "--json", "candidates", "groups")), &js); err != nil || len(js) != 1 || js[0]["size"].(float64) != 2 {
		t.Fatalf("--json groups: %v %v", err, js)
	}
	if code, _, _ := e.run("candidates", "dismiss-group"); code == 0 {
		t.Error("dismiss-group без ID принят")
	}
	if code, _, _ := e.run("candidates", "dismiss-group", "ffffffffffff"); code == 0 {
		t.Error("dismiss-group неизвестной группы принят")
	}
	e.must(t, "candidates", "dismiss-group", js[0]["id"].(string))
	if out := e.must(t, "candidates", "groups"); strings.Contains(out, "o1/r1/") {
		t.Fatalf("после dismiss-group: %s", out)
	}
	if out := e.must(t, "candidates"); strings.Contains(out, "o1/r1/") {
		t.Fatalf("после dismiss-group кандидаты остались: %s", out)
	}
}
