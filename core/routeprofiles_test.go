package core

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func simpleRule(domain string, action string) RouteRule {
	return RouteRule{Enabled: true, Action: action, Match: RouteMatch{Domain: []string{domain}}}
}

func putRules(t *testing.T, c *Core, rs ...RouteRule) {
	t.Helper()
	if err := c.PutRouteRules(rs, "user"); err != nil {
		t.Fatal(err)
	}
}

func profileNames(t *testing.T, c *Core) []string {
	t.Helper()
	ps, err := c.RouteProfiles()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		n := p.Name
		if p.Active {
			n += "*"
		}
		out = append(out, n)
	}
	return out
}

func TestRouteProfilesLifecycle(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	if got := strings.Join(profileNames(t, c), ","); got != "Основной*" {
		t.Fatalf("профили по умолчанию: %s", got)
	}
	putRules(t, c, simpleRule("a.example", ActionDirect), simpleRule("b.example", ActionBlock))
	work, err := c.CreateRouteProfile("Работа", "", "user") // пустой
	if err != nil || work.Rules != 0 {
		t.Fatalf("создание: %+v, %v", work, err)
	}
	copyP, err := c.CreateRouteProfile("Копия", "main", "user") // копия активного
	if err != nil || copyP.Rules != 2 {
		t.Fatalf("копия активного: %+v, %v", copyP, err)
	}
	if _, err := c.CreateRouteProfile("работа", "", "user"); err == nil {
		t.Fatal("повторяющееся название (без учёта регистра) должно отклоняться")
	}
	if _, err := c.CreateRouteProfile("  ", "", "user"); err == nil {
		t.Fatal("пустое название должно отклоняться")
	}
	if _, err := c.CreateRouteProfile("Из несуществующего", "нет-такого", "user"); err == nil {
		t.Fatal("копия несуществующего профиля должна отклоняться")
	}
	// переключение на «Работа»: правила основного сохраняются в нём, route.rules пуст
	if err := c.ActivateRouteProfile(work.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if rs, _ := c.RouteRules(); len(rs) != 0 {
		t.Fatalf("после переключения на пустой профиль правил %d", len(rs))
	}
	putRules(t, c, simpleRule("work.example", ActionProxy))
	if err := c.ActivateRouteProfile("main", "user"); err != nil {
		t.Fatal(err)
	}
	rs, _ := c.RouteRules()
	if len(rs) != 2 || rs[0].Match.Domain[0] != "a.example" {
		t.Fatalf("основной профиль не вернул свои правила: %+v", rs)
	}
	ps, _ := c.RouteProfiles()
	for _, p := range ps {
		if p.Name == "Работа" && p.Rules != 1 {
			t.Fatalf("правила «Работа» потерялись: %+v", p)
		}
	}
	// удаление: активный нельзя, неактивный можно
	if err := c.DeleteRouteProfile("main", "user"); err == nil {
		t.Fatal("активный профиль удалять нельзя")
	}
	if err := c.DeleteRouteProfile(work.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameRouteProfile(copyP.ID, "Копия 2", "user"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(profileNames(t, c), ","); got != "Основной*,Копия 2" {
		t.Fatalf("итог: %s", got)
	}
	if err := c.ActivateRouteProfile("нет", "user"); err == nil {
		t.Fatal("переключение на несуществующий профиль должно отклоняться")
	}
}

func TestActivateRollsBackOnInvalidStoredRules(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	putRules(t, c, simpleRule("a.example", ActionDirect))
	p, err := c.CreateRouteProfile("Повреждённый", "", "user")
	if err != nil {
		t.Fatal(err)
	}
	// портим сохранённые правила неактивного профиля (как после ручной правки базы или старой версии формата)
	profMu.Lock()
	d, _ := c.loadProfiles()
	d.List[d.find(p.ID)].Rules = []RouteRule{{Enabled: true, Action: "route", Match: RouteMatch{Domain: []string{"x.example"}}}}
	if err := c.saveProfiles(d); err != nil {
		t.Fatal(err)
	}
	profMu.Unlock()
	if err := c.ActivateRouteProfile(p.ID, "user"); err == nil {
		t.Fatal("негодные правила профиля должны отклонять переключение")
	}
	if got := strings.Join(profileNames(t, c), ","); got != "Основной*,Повреждённый" {
		t.Fatalf("после неудачи активный профиль должен остаться прежним: %s", got)
	}
	if rs, _ := c.RouteRules(); len(rs) != 1 || rs[0].Match.Domain[0] != "a.example" {
		t.Fatalf("правила изменились после неудачного переключения: %+v", rs)
	}
}

func TestShareEmptyProfileIsRefused(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	p, err := c.CreateRouteProfile("Пустой", "", "user")
	if err != nil {
		t.Fatal(err)
	}
	if link, err := c.ShareRouteProfile(p.ID); !errors.Is(err, ErrInvalid) || link != "" {
		t.Fatalf("ссылка на пустой профиль не должна выдаваться (импорт её всё равно отклонит): %q, %v", link, err)
	}
}

func TestShareAndImportProfile(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	putRules(t, c, simpleRule("a.example", ActionDirect), RouteRule{Enabled: true, Action: ActionProxy, Via: &RouteVia{Kind: ViaCountry, Country: "NL"}, Match: RouteMatch{DomainSuffix: []string{"nl.example"}}})
	link, err := c.ShareRouteProfile("main")
	if err != nil || !strings.HasPrefix(link, shareLinkPrefix) {
		t.Fatalf("ссылка: %q, %v", link, err)
	}
	p, err := c.ImportRouteProfile(link, "", "user")
	if err != nil || p.Rules != 2 || p.Name != "Основной (2)" {
		t.Fatalf("импорт своей ссылки: %+v, %v", p, err)
	}
	if err := c.ActivateRouteProfile(p.ID, "user"); err != nil {
		t.Fatal(err)
	}
	rs, _ := c.RouteRules()
	if len(rs) != 2 || rs[1].Via == nil || rs[1].Via.Country != "NL" || rs[0].ID == "" || rs[0].ID == rs[1].ID {
		t.Fatalf("правила после импорта: %+v", rs)
	}
	// ссылка не содержит id правил
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, shareLinkPrefix))
	if strings.Contains(string(raw), `"id":"`) && !strings.Contains(string(raw), `"id":""`) {
		t.Fatalf("в ссылку попали id правил: %s", raw)
	}
	// голый JSON-список и явное имя
	p2, err := c.ImportRouteProfile(`[{"enabled":true,"action":"block","match":{"domain":["ads.example"]}}]`, "Реклама", "user")
	if err != nil || p2.Name != "Реклама" || p2.Rules != 1 {
		t.Fatalf("импорт списка: %+v, %v", p2, err)
	}
	// недоверенный ввод
	enc := func(s string) string { return shareLinkPrefix + base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, in := range map[string]string{
		"мусор":               "привет",
		"битая ссылка":        shareLinkPrefix + "!!!",
		"не json":             enc("не json"),
		"версия":              enc(`{"v":2,"name":"x","rules":[{"enabled":true,"action":"block","match":{"domain":["a.example"]}}]}`),
		"пустые правила":      enc(`{"v":1,"name":"x","rules":[]}`),
		"неизвестное поле":    enc(`{"v":1,"name":"x","rules":[{"enabled":true,"action":"block","match":{"domain":["a.example"]},"evil":1}]}`),
		"негодное правило":    enc(`{"v":1,"name":"x","rules":[{"enabled":true,"action":"teleport","match":{"domain":["a.example"]}}]}`),
		"негодная страна":     enc(`{"v":1,"name":"x","rules":[{"enabled":true,"action":"proxy","via":{"kind":"country","country":"???"},"match":{"domain":["a.example"]}}]}`),
		"слишком большой":     enc(strings.Repeat("x", maxShareSize)),
		"имя из пробелов нет": "",
	} {
		if _, err := c.ImportRouteProfile(in, "", "user"); err == nil {
			t.Errorf("%s: импорт должен отклоняться", name)
		}
	}
	// после всех отказов профилей ровно три: основной, импорт ссылки, «Реклама»
	if got := len(profileNames(t, c)); got != 3 {
		t.Fatalf("профилей %d, ожидалось 3 (отклонённые не должны создаваться)", got)
	}
}

func TestBackupCarriesProfiles(t *testing.T) {
	c := newCore(t, t.TempDir())
	putRules(t, c, simpleRule("a.example", ActionDirect))
	if _, err := c.ImportRouteProfile(`[{"enabled":true,"action":"block","match":{"domain":["ads.example"]}}]`, "Реклама", "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameRouteProfile("main", "Дом", "user"); err != nil {
		t.Fatal(err)
	}
	b, err := c.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if b.ActiveProfile != "Дом" || len(b.Profiles) != 1 || b.Profiles[0].Name != "Реклама" || len(b.Profiles[0].Rules) != 1 {
		t.Fatalf("копия: активный %q, профили %+v", b.ActiveProfile, b.Profiles)
	}
	c.Close()
	// чистое ядро: восстановление
	c2 := newCore(t, t.TempDir())
	defer c2.Close()
	rep, err := c2.ImportBackup(b, "user")
	if err != nil || len(rep.Skipped) != 0 {
		t.Fatalf("восстановление: %+v, %v", rep, err)
	}
	if got := strings.Join(profileNames(t, c2), ","); got != "Дом*,Реклама" {
		t.Fatalf("профили после восстановления: %s", got)
	}
	if rs, _ := c2.RouteRules(); len(rs) != 1 || rs[0].Match.Domain[0] != "a.example" {
		t.Fatalf("правила активного профиля: %+v", rs)
	}
	// негодный профиль в копии пропускается с пояснением, остальное применяется
	b.Profiles = append(b.Profiles, BackupProfile{Name: "Плохой", Rules: []RouteRule{{Enabled: true, Action: "teleport", Match: RouteMatch{Domain: []string{"x.example"}}}}})
	rep, err = c2.ImportBackup(b, "user")
	if err != nil || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "Плохой") {
		t.Fatalf("негодный профиль: %+v, %v", rep, err)
	}
}
