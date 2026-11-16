package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brod/store"
)

// домен сервера сохранённого активного узла попадает в набор node-servers до запуска движка, поэтому при восстановлении
// ensureRegistered не спит nodeServersSettle под c.mu: note() второй раз возвращает false.
func TestSavedActiveServerNotedBeforeEngineStart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id := store.NodeID("dom-node")
	if err := st.UpsertNodes("src", 1, []store.NodeRec{{ID: id, Class: "vless", Proto: "vless", Server: "node.example.org", Port: 443, Opts: []byte(`{"type":"direct"}`)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node-servers.json")
	ns := newNodeServers(path)

	noteSavedActive(st, ns) // выбора нет: ничего не меняется
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "node.example.org") {
		t.Fatal("домен записан без сохранённого выбора")
	}

	if err := st.SetSelection(store.Selection{Scope: store.ScopeActive, Node: id, By: "user", SetAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	noteSavedActive(st, ns)
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "node.example.org") {
		t.Fatalf("домен активного узла не в наборе: %s", b)
	}
	if ns.note("node.example.org") {
		t.Fatal("note вернул true: ensureRegistered заснёт на nodeServersSettle")
	}
}
