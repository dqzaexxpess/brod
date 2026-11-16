package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"brod/aggregator"
	"brod/core"
	"brod/prober"
	"brod/store"
)

func TestCandidatesCommands(t *testing.T) {
	var base string
	var list strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&list, "vless://11111111-2222-3333-4444-%012d@h%d.example.org:443?security=tls#n%d\n", i, i, i)
	}
	files := map[string]string{
		"/o1/r1/main/README.md": "[a](BASE/o1/r1/main/alpha.txt) [b](BASE/o1/r1/main/beta.txt)",
		"/o1/r1/main/alpha.txt": list.String(),
		"/o1/r1/main/beta.txt":  list.String(),
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
		c.DiscoverCanProbe = func() bool { return true }
		c.DiscoverProbe = func(context.Context, store.NodeRec) prober.Result { return prober.Result{Level: 2, OK: true} }
		c.SeedSources = []aggregator.Source{{Name: "seed", URL: base + "o1/r1/main/seed.txt", Priority: 1}}
	})
	if out := e.must(t, "candidates"); !strings.Contains(out, "ИМЯ") || strings.Contains(out, "alpha") {
		t.Fatalf("пустой список: %s", out)
	}
	if out := e.must(t, "candidates", "scan"); !strings.Contains(out, "кандидатов 2 (новых 2)") || !strings.Contains(out, "ответило 40") {
		t.Fatalf("scan: %s", out)
	}
	out := e.must(t, "candidates", "list")
	if !strings.Contains(out, "o1/r1/alpha") || !strings.Contains(out, "vless 30") || !strings.Contains(out, "20 из 20") || strings.Contains(out, "11111111-2222") {
		t.Fatalf("list: %s", out)
	}
	var js []map[string]any
	if err := json.Unmarshal([]byte(e.must(t, "--json", "candidates")), &js); err != nil || len(js) != 2 {
		t.Fatalf("--json: %v", err)
	}
	id0, id1 := js[0]["id"].(string), js[1]["id"].(string)
	if out := e.must(t, "candidates", "add", id0); !strings.Contains(out, "добавлен источник") {
		t.Fatalf("add: %s", out)
	}
	if !strings.Contains(e.must(t, "sources"), "o1/r1/") {
		t.Fatal("источник не появился в списке источников")
	}
	e.must(t, "candidates", "dismiss", id1)
	if out := e.must(t, "candidates"); strings.Contains(out, "o1/r1/") {
		t.Fatalf("после add и dismiss список не пуст: %s", out)
	}
	if code, _, _ := e.run("candidates", "add"); code == 0 {
		t.Error("add без ID принят")
	}
	if code, _, _ := e.run("candidates", "add", "ffffffffffff"); code == 0 {
		t.Error("add неизвестного ID принят")
	}
}
