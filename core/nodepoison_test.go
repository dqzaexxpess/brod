package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"brod/store"
)

// ссылка из другого источника с тем же ключом дедупликации заменяет параметры узла (в том числе своего сервера и активного узла).
func TestOwnNodeNotOverwrittenByOtherSource(t *testing.T) {
	good := "vless://5b0e5c2e-0000-4000-8000-000000000001@cdn.example.org:443?security=tls&sni=cdn.example.org&type=ws&path=%2Fws&host=good.example.org&fp=chrome#good"
	evil := "vless://5b0e5c2e-0000-4000-8000-000000000001@cdn.example.org:443?security=tls&sni=cdn.example.org&type=ws&path=%2Fws&host=evil.example.net&allowInsecure=1#evil"
	f := newFeed()
	f.set("A", good)
	c := refreshCore(t, f, "A", "B")
	if _, err := c.AddOwn(good, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Refresh(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	nodes, _ := c.st.Nodes(store.NodeFilter{WithOpts: true, IncludeNoSecurity: true})
	if len(nodes) != 1 {
		t.Fatalf("узлов %d", len(nodes))
	}
	id := nodes[0].ID
	own, _ := c.st.OwnIDs()
	t.Logf("до: свой=%v insecure=%v opts=%s", own[id], nodes[0].Insecure, nodes[0].Opts)
	f.set("B", evil)
	f.mu.Lock()
	f.notmod["A"] = true // источник A не менялся (обычный случай: ответ 304)
	f.mu.Unlock()
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("отчёт: %+v", rep.Sources)
	n, _ := c.st.Node(id)
	var m map[string]any
	_ = json.Unmarshal(n.Opts, &m)
	t.Logf("после источника B: свой=%v insecure=%v opts=%s", own[id], n.Insecure, n.Opts)
	if strings.Contains(string(n.Opts), "evil.example.net") || n.Insecure {
		t.Fatalf("чужой источник изменил параметры собственного сервера: insecure=%v opts=%s", n.Insecure, n.Opts)
	}
}
