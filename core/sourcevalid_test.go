package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"brod/store"
)

// имя и адрес пользовательского источника проверяются (AddSource закрывает API и восстановление из копии).
func TestAddSourceValidatesNameAndURL(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	bad := []struct{ name, url, mirror string }{
		{`..\..\..\x`, "https://a.example/x", ""},
		{"../x", "https://a.example/x", ""},
		{"a:b", "https://a.example/x", ""},
		{"ok", "http://a.example/x", ""},
		{"ok", "file:///etc/passwd", ""},
		{"ok", "https://a.example/x", "http://m.example/x"},
	}
	for _, b := range bad {
		if err := c.AddSource(b.name, b.url, b.mirror, 1, "user"); err == nil {
			t.Errorf("принято %+v", b)
		}
	}
	if len(srcByID(t, c)) != 0 {
		t.Errorf("отвергнутые источники попали в базу: %v", srcByID(t, c))
	}
	if err := c.AddSource("моя/подписка-1", "https://a.example/x?token=1", "https://m.example/x", 1, "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddSource("local", "http://127.0.0.1:9/x", "", 1, "user"); err != nil {
		t.Fatalf("loopback по http должен приниматься: %v", err)
	}
}

// Резервная копия с источником «..\..\x» не создаёт его, остальное восстанавливается.
func TestRestoreSkipsBadSourceName(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	b := Backup{Version: BackupVersion, Sources: []BackupSource{
		{Name: `..\..\x`, URL: "https://a.example/x", Enabled: true},
		{Name: "good", URL: "https://a.example/y", Enabled: true},
	}}
	rep, err := c.ImportBackup(b, "user")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sources != 1 || len(rep.Skipped) != 1 {
		t.Fatalf("отчёт: %+v", rep)
	}
	if got := srcByID(t, c); len(got) != 1 || got["good"].ID == "" {
		t.Fatalf("источники: %v", got)
	}
}

// Подписанный список с негодным именем источника отвергается целиком.
func TestOTASourcesRejectBadName(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	doc := `{"version":3,"sources":[{"name":"..\\..\\x","url":"https://a.example/x"}]}`
	if _, err := c.applyOTASources([]byte(doc), otaSig(priv, doc), pub, "auto"); err == nil {
		t.Fatal("список с именем «..\\..\\x» принят")
	}
	var _ store.SourceRec
	if len(srcByID(t, c)) != 0 {
		t.Fatal("источник попал в базу")
	}
}
