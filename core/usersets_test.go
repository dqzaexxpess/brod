package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"brod/engine"
	"brod/rulesets"
)

const srcSet = `{"version":3,"rules":[{"domain_suffix":["example.org","example.net"]}]}`

func userSetRig(t *testing.T, body *atomic.Value) (*Core, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body.Load().(string))
	}))
	t.Cleanup(srv.Close)
	c := cfgCore(t, func(cfg *Config) { cfg.RuleSetClient = srv.Client() })
	return c, srv
}

func TestUserSetLifecycle(t *testing.T) {
	var body atomic.Value
	body.Store(srcSet)
	c, srv := userSetRig(t, &body)
	ctx := t.Context()

	// отказы: не https, мусор, 404 — ничего не добавляется
	for _, u := range []string{"http://example.org/a.json", "ftp://x/a", "https://u:p@example.org/a.json", "not a url"} {
		if _, err := c.AddUserSet(ctx, u, "", "test"); err == nil {
			t.Errorf("адрес %q принят", u)
		}
	}
	if _, err := c.AddUserSet(ctx, srv.URL+"/missing", "", "test"); err == nil {
		t.Error("404 принят")
	}
	body.Store("это не список")
	if _, err := c.AddUserSet(ctx, srv.URL+"/bad", "", "test"); err == nil {
		t.Error("мусор принят")
	}
	if len(c.UserSets()) != 0 {
		t.Fatalf("после отказов в списке: %+v", c.UserSets())
	}

	// исходный JSON компилируется в srs
	body.Store(srcSet)
	set, err := c.AddUserSet(ctx, srv.URL+"/list.json", "  Мой список  ", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !userSetRe.MatchString(set.ID) || set.Title != "Мой список" || set.Size == 0 {
		t.Fatalf("список: %+v", set)
	}
	b, err := os.ReadFile(c.userSetPath(set.ID))
	if err != nil || rulesets.Validate(b) != nil {
		t.Fatalf("файл списка: %v %v", err, rulesets.Validate(b))
	}
	if _, err := c.AddUserSet(ctx, srv.URL+"/list.json", "", "test"); err == nil {
		t.Error("повторное добавление принято")
	}

	// правило ссылается на список: удалить нельзя
	rule := RouteRule{ID: "r1", Title: "через список", Enabled: true, Action: ActionDirect, Match: RouteMatch{RuleSet: []string{set.ID}}}
	if err := rule.validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.PutRouteRules([]RouteRule{rule}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveUserSet(set.ID, "test"); err == nil || !strings.Contains(err.Error(), "через список") {
		t.Fatalf("удаление списка с правилом: %v", err)
	}

	// обновление: тот же файл — без изменений; меньше четверти прежнего — отвергается, файл прежний
	if ch, err := c.RefreshUserSets(ctx, "test"); err != nil || len(ch) != 0 {
		t.Fatalf("тот же файл: %v %v", ch, err)
	}
	var doms []string
	for i := range 3000 {
		doms = append(doms, fmt.Sprintf(`"host%d-%d.example.org"`, i, i*7919))
	}
	big := `{"version":3,"rules":[{"domain_suffix":[` + strings.Join(doms, ",") + `]}]}`
	body.Store(big)
	if ch, err := c.RefreshUserSets(ctx, "test"); err != nil || len(ch) != 1 {
		t.Fatalf("больший файл принят: %v %v", ch, err)
	}
	grown, _ := os.ReadFile(c.userSetPath(set.ID))
	body.Store(srcSet) // стал во много раз меньше
	if ch, err := c.RefreshUserSets(ctx, "test"); err != nil || len(ch) != 0 {
		t.Fatalf("обрезанный ответ принят: %v %v", ch, err)
	}
	after, _ := os.ReadFile(c.userSetPath(set.ID))
	if string(after) != string(grown) {
		t.Fatal("файл заменён обрезанным ответом")
	}
	if us := c.UserSets(); len(us) != 1 || us[0].Error == "" {
		t.Fatalf("причина отказа не записана: %+v", us)
	}

	// правило убрали — список удаляется вместе с файлом
	if err := c.PutRouteRules(nil, "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveUserSet(set.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.userSetPath(set.ID)); err == nil || len(c.UserSets()) != 0 {
		t.Fatal("список не удалён")
	}
}

// В конфиг движка попадают определения скачанных списков; правило на несуществующий свой список пропускается целиком (иначе без условия оно
// совпало бы со всем трафиком).
func TestUserSetInEngineConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := rulesets.Install(dir); err != nil {
		t.Fatal(err)
	}
	var body atomic.Value
	body.Store(srcSet)
	c, srv := userSetRig(t, &body)
	set, err := c.AddUserSet(t.Context(), srv.URL+"/l.json", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	have := RouteRule{ID: "have", Enabled: true, Action: ActionBlock, Match: RouteMatch{RuleSet: []string{set.ID}}}
	lost := RouteRule{ID: "lost", Enabled: true, Action: ActionBlock, Match: RouteMatch{RuleSet: []string{"ext-deadbeef"}}}
	for _, r := range []*RouteRule{&have, &lost} {
		if err := r.validate(); err != nil {
			t.Fatal(err)
		}
	}
	m, raw := tunCfg(t, TUNConfig{Mode: ModeAll, RuleSetDir: dir, UserSets: map[string]string{set.ID: c.userSetPath(set.ID)}, UserRules: []RouteRule{have, lost}})
	if err := engine.ValidateConfig([]byte(raw)); err != nil {
		t.Fatalf("движок не принял конфиг: %v", err)
	}
	if !strings.Contains(raw, `"tag":"`+set.ID+`"`) && !strings.Contains(raw, `"tag": "`+set.ID+`"`) {
		t.Fatalf("определение списка не попало в конфиг: %s", raw)
	}
	if strings.Contains(raw, "ext-deadbeef") {
		t.Fatal("правило на несуществующий список попало в конфиг")
	}
	_ = m
}

// Профиль по https-адресу: скачивается ядром и разбирается тем же строгим разбором; чужой мусор и не-https отвергаются.
func TestImportProfileFromURL(t *testing.T) {
	var body atomic.Value
	body.Store(`{"v":1,"name":"Из сети","rules":[{"title":"t","enabled":true,"match":{"domain_suffix":["example.org"]},"action":"direct"}]}`)
	c, srv := userSetRig(t, &body)
	text, err := c.FetchProfileText(t.Context(), srv.URL+"/p.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.ImportRouteProfile(text, "", "test")
	if err != nil || p.Rules != 1 {
		t.Fatalf("импорт: %+v %v", p, err)
	}
	if _, err := c.FetchProfileText(t.Context(), "http://example.org/p.json"); err == nil {
		t.Error("http принят")
	}
	if _, err := c.FetchProfileText(t.Context(), srv.URL+"/missing"); err == nil {
		t.Error("404 принят")
	}
	body.Store(strings.Repeat("x", maxShareSize+10))
	if _, err := c.FetchProfileText(t.Context(), srv.URL+"/big"); err == nil {
		t.Error("слишком большой профиль принят")
	}
	body.Store("мусор")
	text, _ = c.FetchProfileText(t.Context(), srv.URL+"/junk")
	if _, err := c.ImportRouteProfile(text, "", "test"); err == nil {
		t.Error("мусор принят как профиль")
	}
}
