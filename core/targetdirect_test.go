package core

import (
	"context"
	"testing"

	"brod/store"
)

// приложение со сторонним токеном не может создать цель «все домены», выставить allow_direct без владельца
// и создать неограниченное число целей.
func TestAppTargetCannotCaptureAllDomains(t *testing.T) {
	c := startCore(t, t.TempDir())
	chk := store.TargetCheck{Kind: "http", URL: "https://ok.example/"}
	evil := store.Target{ID: "evil", Title: "x", Rules: store.TargetRules{DomainKeyword: []string{"."}}, Check: chk, AllowDirect: true}
	if err := c.PutTarget(evil, "api:evil"); err == nil {
		t.Fatalf("цель «все домены» с allow_direct принята от стороннего токена")
	}
	evil.AllowDirect = false
	if err := c.PutTarget(evil, "api:evil"); err == nil {
		t.Fatalf("цель с правилом «.» принята")
	}
	ok := store.Target{ID: "ok", Rules: store.TargetRules{DomainSuffix: []string{"ok.example"}}, Check: chk, AllowDirect: true}
	if err := c.PutTarget(ok, "api:evil"); err == nil {
		t.Fatalf("allow_direct принят без подтверждения владельца")
	}
	ok.AllowDirect = false
	if err := c.PutTarget(ok, "api:evil"); err != nil {
		t.Fatalf("обычная цель приложения отклонена: %v", err)
	}
	// владелец разрешил allow_direct: приложение может его сохранять
	ok.Owner, ok.AllowDirect = "api:evil", true
	if err := c.PutTarget(ok, "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.PutTarget(ok, "api:evil"); err != nil {
		t.Fatalf("уже разрешённый владельцем allow_direct отклонён: %v", err)
	}
	if _, err := c.SwitchTarget(context.Background(), "ok", store.SelDirect, "api:evil"); err != nil {
		t.Fatalf("SwitchTarget: %v", err)
	}
	// перекрытие встроенной цели
	yt := store.Target{ID: "yt2", Rules: store.TargetRules{DomainSuffix: []string{"youtube.com"}}, Check: chk}
	if err := c.PutTarget(yt, "api:evil"); err == nil {
		t.Fatalf("правило пересекается со встроенной целью, но принято")
	}
	n := 1 // «ok» уже есть
	for i := 0; i < 300; i++ {
		id := "t" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if err := c.PutTarget(store.Target{ID: id, Rules: store.TargetRules{Domain: []string{id + ".example"}}, Check: chk}, "api:evil"); err != nil {
			break
		}
		n++
	}
	if n > 5 {
		t.Fatalf("сторонний токен создал целей: %d (предел 5)", n)
	}
	// user не ограничен
	for i := 0; i < 8; i++ {
		id := "u" + string(rune('a'+i))
		if err := c.PutTarget(store.Target{ID: id, Rules: store.TargetRules{DomainKeyword: []string{"kw" + id}}, Check: chk, AllowDirect: true}, "user"); err != nil {
			t.Fatalf("user ограничен: %v", err)
		}
	}
}
