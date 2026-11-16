package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"brod/store"
)

func otaSig(priv ed25519.PrivateKey, doc string) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(doc))))
}

func srcByID(t *testing.T, c *Core) map[string]store.SourceRec {
	t.Helper()
	recs, err := c.st.Sources()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]store.SourceRec{}
	for _, r := range recs {
		m[r.ID] = r
	}
	return m
}

// Список издателя применяется только с верной подписью и новее прежней версии; свои источники пользователя не трогаются,
// отключённый пользователем остаётся отключённым, снятые издателем отключаются.
func TestApplyOTASources(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := c.cfg.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.st.UpsertSource(store.SourceRec{ID: "managed/old", URL: "https://old.example/a", Enabled: true}, now))
	must(c.st.UpsertSource(store.SourceRec{ID: "managed/off", URL: "https://off.example/a", Enabled: true}, now))
	must(c.st.SetSourceEnabled("managed/off", false)) // выключил пользователь
	must(c.st.UpsertSource(store.SourceRec{ID: "mine", URL: "https://mine.example/own", Enabled: true, UserAdded: true}, now))

	doc := `{"version":2,"sources":[
	 {"name":"managed/off","url":"https://off.example/new","priority":2},
	 {"name":"mine","url":"https://evil.example/hijack"},
	 {"name":"managed/new","url":"https://new.example/a","mirror":"https://m.example/a","priority":1}],
	 "retired":["managed/old"]}`

	if _, err := c.applyOTASources([]byte(doc), otaSig(priv, doc+" "), pub, "auto"); err == nil || !strings.Contains(err.Error(), "подпись") {
		t.Fatalf("подпись не сошлась, а список принят: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := c.applyOTASources([]byte(doc), otaSig(priv, doc), other, "auto"); err == nil {
		t.Fatal("подпись чужого ключа принята")
	}
	if len(srcByID(t, c)["managed/new"].ID) != 0 {
		t.Fatal("после отказа источники не должны меняться")
	}

	res, err := c.applyOTASources([]byte(doc), otaSig(priv, doc), pub, "auto")
	if err != nil || !res.Applied || res.Added != 1 || res.Updated != 1 || res.Disabled != 1 || res.Skipped != 1 {
		t.Fatalf("итог %+v, ошибка %v", res, err)
	}
	got := srcByID(t, c)
	if n := got["managed/new"]; n.URL != "https://new.example/a" || n.Mirror != "https://m.example/a" || !n.Enabled || n.UserAdded {
		t.Fatalf("новый источник: %+v", n)
	}
	if o := got["managed/off"]; o.URL != "https://off.example/new" || o.Enabled {
		t.Fatalf("адрес обновляется, а отключённость пользователя сохраняется: %+v", o)
	}
	if m := got["mine"]; m.URL != "https://mine.example/own" || !m.UserAdded {
		t.Fatalf("источник пользователя тронут: %+v", m)
	}
	if got["managed/old"].Enabled {
		t.Fatal("снятый издателем источник должен отключиться")
	}

	// откат на ту же или старую версию не принимается, даже с верной подписью
	old := `{"version":1,"sources":[{"name":"managed/rollback","url":"https://r.example/a"}]}`
	res, err = c.applyOTASources([]byte(old), otaSig(priv, old), pub, "auto")
	if err != nil || res.Applied {
		t.Fatalf("старая версия применена: %+v %v", res, err)
	}
	if _, ok := srcByID(t, c)["managed/rollback"]; ok {
		t.Fatal("источник из старой версии появился")
	}
	res, _ = c.applyOTASources([]byte(doc), otaSig(priv, doc), pub, "auto")
	if res.Applied {
		t.Fatal("та же версия применена повторно")
	}

	// невалидный документ (http-адрес) отвергается целиком, даже с верной подписью
	bad := `{"version":9,"sources":[{"name":"x","url":"http://x.example/a"}]}`
	if _, err := c.applyOTASources([]byte(bad), otaSig(priv, bad), pub, "auto"); err == nil {
		t.Fatal("документ с http-адресом принят")
	}
}

// Живая проверка против опубликованного списка (только вручную: BROD_LIVE=1 go test ./core -run OTALive).
func TestOTALive(t *testing.T) {
	if os.Getenv("BROD_LIVE") != "1" {
		t.Skip("нужна сеть: BROD_LIVE=1")
	}
	c := newCore(t, t.TempDir())
	defer c.Close()
	res, err := c.UpdateOTASources(context.Background(), "user")
	if err != nil || !res.Applied || res.Version < 1 {
		t.Fatalf("итог %+v, ошибка %v", res, err)
	}
	t.Logf("%+v", res)
	again, err := c.UpdateOTASources(context.Background(), "user")
	if err != nil || again.Applied {
		t.Fatalf("повторное применение той же версии: %+v %v", again, err)
	}
}
