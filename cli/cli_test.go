package cli_test

import (
	"brod/nodeopts"
	"brod/ota"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brod/aggregator"
	"brod/api"
	"brod/cli"
	"brod/core"
	"brod/engine"
	"brod/internal/testport"
	"brod/prober"
	"brod/store"
)

type env struct {
	c    *core.Core
	sock string
	srv  *httptest.Server
}

func setup(t *testing.T) *env { return setupWith(t, nil) }

// setupWith — то же, но конфиг ядра можно дополнить (тесты обновления подставляют свой манифест).
func setupWith(t *testing.T, mutate func(*core.Config)) *env {
	t.Helper()
	nodeopts.AllowTypeForTests(t, "direct") // узлы-заглушки тестов
	cfg := core.Config{StateDir: t.TempDir(), AppPort: testport.Next(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{},
		NetID: func() string { return "net1" },
		Check: func(context.Context, store.NodeRec, *engine.Probe) []prober.Result {
			return []prober.Result{{Level: 2, OK: true}}
		},
		TargetCheck: func(_ context.Context, tg store.Target, _, _ string, _ *engine.Probe) prober.Result {
			return prober.Result{Level: 4, Target: tg.ID, OK: true, LatencyMs: 12}
		}}
	cfg.UpdateURLs = []string{"https://127.0.0.1:9/releases.json"} // обновления в тестах не ходят в сеть
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := core.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go api.Serve(ctx, ln, api.New(c, "test").Handler(true))
	srv := httptest.NewServer(api.New(c, "test").Handler(false))
	t.Cleanup(srv.Close)
	return &env{c: c, sock: sock, srv: srv}
}

func (e *env) node(t *testing.T, key string) string {
	id := store.NodeID(key)
	if err := e.c.Store().UpsertNodes("src", time.Now().UnixNano(), []store.NodeRec{{ID: id, Class: "direct/tcp/none", Proto: "direct", Server: key + ".example", Port: 443,
		Opts: []byte(`{"type":"direct"}`)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cli.Run(append([]string{"--sock", e.sock}, args...), cli.Env{Out: &out, Err: &errb, Getenv: func(string) string { return "" }})
	return code, out.String(), errb.String()
}

func (e *env) must(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errs := e.run(args...)
	if code != 0 {
		t.Fatalf("brodctl %v: код %d\nout: %s\nerr: %s", args, code, out, errs)
	}
	return out
}

func TestStatusConnectListFlags(t *testing.T) {
	e := setup(t)
	a, b := e.node(t, "alpha"), e.node(t, "beta")
	if out := e.must(t, "status"); !strings.Contains(out, "не выбран") {
		t.Fatalf("status без узла: %s", out)
	}
	e.must(t, "connect", a[:8])
	out := e.must(t, "status")
	if !strings.Contains(out, a[:10]) || !strings.Contains(out, "alpha.example:443") || !strings.Contains(out, "выбрал: user") {
		t.Fatalf("status: %s", out)
	}
	e.must(t, "fav", b[:8])
	e.must(t, "pin", b[:8])
	list := e.must(t, "list")
	if !strings.Contains(list, "★") || !strings.Contains(list, "закреплён") || !strings.Contains(list, b[:10]) {
		t.Fatalf("list: %s", list)
	}
	e.must(t, "fav", b[:8], "--off") // флаг после аргумента
	if strings.Contains(e.must(t, "list"), "★") {
		t.Fatal("--off не снял флаг")
	}
	if code, _, errs := e.run("connect", "ffffff"); code != 1 || !strings.Contains(errs, "ошибка") {
		t.Fatalf("несуществующий узел: %d %s", code, errs)
	}
	if code, _, _ := e.run("connect"); code != 2 {
		t.Fatalf("без аргумента код %d", code)
	}
	out = e.must(t, "test", "--force")
	if !strings.Contains(out, "проверено 1") { // активный не проверяется планировщиком, остальные — да
		t.Fatalf("test: %s", out)
	}
	if out := e.must(t, "alternatives"); !strings.Contains(out, b[:10]) {
		t.Fatalf("alternatives: %s", out)
	}
	if out := e.must(t, "probe"); !strings.Contains(out, "активный узел") {
		t.Fatalf("probe: %s", out)
	}
}

func TestJSONOutputIsValid(t *testing.T) {
	e := setup(t)
	e.node(t, "alpha")
	for _, args := range [][]string{{"status"}, {"list"}, {"sources"}, {"targets"}, {"events"}, {"settings"}} {
		out := e.must(t, append([]string{"--json"}, args...)...)
		if !json.Valid([]byte(out)) {
			t.Errorf("%v: не JSON: %s", args, out)
		}
	}
}

func TestSourcesAndTokensHideSecrets(t *testing.T) {
	e := setup(t)
	e.must(t, "sources", "add", "mine", "https://sub.example.org/p?token=abc123", "--prio", "2")
	out := e.must(t, "sources")
	if !strings.Contains(out, "mine") || !strings.Contains(out, "sub.example.org") || strings.Contains(out, "abc123") {
		t.Fatalf("sources: %s", out)
	}
	e.must(t, "sources", "disable", "mine")
	if !strings.Contains(e.must(t, "sources"), "нет") {
		t.Fatal("источник не выключен")
	}
	e.must(t, "sources", "rm", "mine")

	tok := e.must(t, "token", "create", "myapp")
	lines := strings.Split(strings.TrimSpace(tok), "\n")
	secret := lines[len(lines)-1]
	if !strings.Contains(e.must(t, "token", "list"), "myapp") {
		t.Fatal("токен не в списке")
	}
	// по TCP с токеном приложения: цели доступны, узлы — нет
	var out2, err2 bytes.Buffer
	run := func(args ...string) int {
		out2.Reset()
		err2.Reset()
		return cli.Run(append([]string{"--addr", e.srv.URL, "--token", secret}, args...), cli.Env{Out: &out2, Err: &err2, Getenv: func(string) string { return "" }})
	}
	if code := run("targets"); code != 0 || !strings.Contains(out2.String(), "youtube") {
		t.Fatalf("targets по токену приложения: %d %s %s", code, out2.String(), err2.String())
	}
	if code := run("list"); code != 1 || !strings.Contains(err2.String(), "403") {
		t.Fatalf("list по токену приложения: %d %s", code, err2.String())
	}
	e.must(t, "token", "revoke", "myapp")
	if code := run("targets"); code != 1 || !strings.Contains(err2.String(), "401") {
		t.Fatalf("после отзыва: %d %s", code, err2.String())
	}
	if out := e.must(t, "app-proxy"); !strings.Contains(out, "логин:") || !strings.Contains(out, "пароль:") {
		t.Fatalf("app-proxy: %s", out)
	}
}

func TestTargetsCommands(t *testing.T) {
	e := setup(t)
	e.must(t, "targets", "add", "videosite", "--keyword", "videosite", "--suffix", "hdvideosite.ac", "--check", "http", "--url", "https://videosite.example/",
		"--expect", "200,403", "--policy", "auto-failover", "--k", "2")
	list := e.must(t, "targets")
	if !strings.Contains(list, "videosite") || !strings.Contains(list, "auto-failover") || !strings.Contains(list, "узел не выбран") {
		t.Fatalf("targets: %s", list)
	}
	if out := e.must(t, "targets", "switch", "videosite", "direct"); !strings.Contains(out, "direct") {
		t.Fatalf("switch: %s", out)
	}
	if out := e.must(t, "targets", "probe", "videosite"); !strings.Contains(out, "работает") {
		t.Fatalf("probe: %s", out)
	}
	e.must(t, "targets", "probe-url", "videosite", "https://cdn.example/v?sig=1", "--ttl", "60")
	show := e.must(t, "targets", "show", "videosite")
	var m map[string]any
	if json.Unmarshal([]byte(show), &m) != nil || m["node"] != "direct" || m["has_probe_url"] != true {
		t.Fatalf("show: %s", show)
	}
	if strings.Contains(show, "sig=1") {
		t.Fatal("пробная ссылка в выводе")
	}
	if code, _, errs := e.run("targets", "add", "Плохой ID", "--check", "youtube"); code != 1 || !strings.Contains(errs, "400") {
		t.Fatalf("валидация: %d %s", code, errs)
	}
	e.must(t, "targets", "rm", "videosite")
}

func TestEventsAndLogs(t *testing.T) {
	e := setup(t)
	a := e.node(t, "alpha")
	e.must(t, "connect", a[:8])
	out := e.must(t, "events")
	if !strings.Contains(out, "switch") || !strings.Contains(out, "started") {
		t.Fatalf("events: %s", out)
	}
	e.must(t, "logs", "-n", "5")
}

// Событий больше тысячи: «events» показывает последние, а не хвост первой тысячи.
func TestEventsShowsNewestWhenMoreThanThousand(t *testing.T) {
	e := setup(t)
	for i := 0; i < 1100; i++ {
		e.c.Bus().Publish(store.Event{Kind: "probe-run", Initiator: "auto", Detail: fmt.Sprintf("пачка %d", i)})
	}
	out := e.must(t, "events", "-n", "3")
	if !strings.Contains(out, "пачка 1099") || strings.Contains(out, "пачка 900") {
		t.Fatalf("events -n 3: %s", out)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n != 3 {
		t.Fatalf("строк %d: %s", n, out)
	}
}

func TestUsageAndUnavailableDaemon(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cli.Run([]string{"help"}, cli.Env{Out: &out, Err: &errb}); code != 0 || !strings.Contains(out.String(), "brodctl") {
		t.Fatalf("help: %d", code)
	}
	if code := cli.Run([]string{"--sock", filepath.Join(t.TempDir(), "нет.sock"), "status"}, cli.Env{Out: &out, Err: &errb}); code != 1 || !strings.Contains(errb.String(), "демон недоступен") {
		t.Fatalf("без демона: %d %s", code, errb.String())
	}
	if code := cli.Run([]string{"--sock", "x", "нет-такой"}, cli.Env{Out: &out, Err: &errb}); code != 2 {
		t.Fatalf("неизвестная команда: %d", code)
	}
}

// --json должен отдавать JSON у всех команд, включая те, что в обычном режиме печатают текст.
func TestJSONForActionCommands(t *testing.T) {
	e := setup(t)
	a := e.node(t, "alpha")
	cases := [][]string{
		{"connect", a[:8]}, {"fav", a[:8]}, {"token", "create", "myapp"},
		{"targets", "add", "videosite", "--keyword", "videosite", "--url", "https://r.example/"},
		{"targets", "switch", "videosite", "direct"}, {"targets", "probe", "videosite"}, {"test", "--force"}, {"refresh"},
		{"sources", "add", "m", "https://x.example/s"}, {"settings", "set", "security.hide_insecure", "1"},
	}
	for _, args := range cases {
		out := strings.TrimSpace(e.must(t, append([]string{"--json"}, args...)...))
		if !json.Valid([]byte(out)) {
			t.Errorf("%v: вывод не JSON: %q", args, out)
		}
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(strings.TrimSpace(e.must(t, "--json", "token", "create", "other"))), &m)
	if len(m["token"]) < 20 {
		t.Fatalf("токен в JSON: %v", m)
	}
}

func TestRulesAndProfilesCommands(t *testing.T) {
	e := setup(t)
	if out := e.must(t, "rules"); !strings.Contains(out, "правил нет") {
		t.Fatalf("пустой список: %s", out)
	}
	file := filepath.Join(t.TempDir(), "rules.json")
	body := `[{"enabled":true,"title":"реклама","action":"block","block_method":"drop","match":{"domain_suffix":["ads.example"],"protocol":["http"]}},
	          {"enabled":true,"action":"proxy","via":{"kind":"country","country":"nl","fallback":"direct"},"match":{"domain_suffix":["nl.example"]}}]`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out := e.must(t, "rules", "import", file)
	if !strings.Contains(out, "правил: 2") {
		t.Fatalf("import: %s", out)
	}
	list := e.must(t, "rules")
	for _, want := range []string{"block/drop", "реклама", "domain_suffix: ads.example", "страна NL (иначе direct)", "protocol: http"} {
		if !strings.Contains(list, want) {
			t.Fatalf("в списке нет %q:\n%s", want, list)
		}
	}
	// export отдаёт то, что можно импортировать обратно
	exported := e.must(t, "rules", "export")
	var back []map[string]any
	if err := json.Unmarshal([]byte(exported), &back); err != nil || len(back) != 2 || back[1]["via"] == nil {
		t.Fatalf("export: %v, %s", err, exported)
	}
	e.must(t, "rules", "via")       // без VPN привязок нет, но команда работает
	e.must(t, "rules", "countries") // и список стран (пустой)
	if code, _, errs := e.run("rules", "import", filepath.Join(t.TempDir(), "нет-такого.json")); code != 1 || !strings.Contains(errs, "ошибка") {
		t.Fatalf("несуществующий файл: %d %s", code, errs)
	}
	if err := os.WriteFile(file, []byte(`[{"enabled":true,"action":"teleport","match":{"domain":["a.example"]}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := e.run("rules", "import", file); code != 1 || !strings.Contains(errs, "действие") {
		t.Fatalf("негодное правило: %d %s", code, errs)
	}
	if code, _, _ := e.run("rules", "bogus"); code != 2 {
		t.Fatalf("неизвестная подкоманда: код %d", code)
	}

	// профили
	if out := e.must(t, "profiles"); !strings.Contains(out, "Основной") || !strings.Contains(out, "да") {
		t.Fatalf("профили: %s", out)
	}
	e.must(t, "profiles", "new", "Работа")
	e.must(t, "profiles", "new", "Копия", "--from", "Основной")
	if out := e.must(t, "profiles"); !strings.Contains(out, "Работа") || !strings.Contains(out, "Копия") {
		t.Fatalf("после создания: %s", out)
	}
	if code, _, errs := e.run("profiles", "new", "работа"); code != 1 || !strings.Contains(errs, "уже есть") {
		t.Fatalf("повтор названия: %d %s", code, errs)
	}
	link := strings.TrimSpace(e.must(t, "profiles", "share", "Копия"))
	if !strings.HasPrefix(link, "brod://route/") {
		t.Fatalf("ссылка: %q", link)
	}
	out = e.must(t, "profiles", "import", link, "--name", "Импорт")
	if !strings.Contains(out, "правил: 2") || !strings.Contains(out, "не активирован") {
		t.Fatalf("импорт ссылки: %s", out)
	}
	// импорт из файла со ссылкой
	lf := filepath.Join(t.TempDir(), "link.txt")
	if err := os.WriteFile(lf, []byte(link+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.must(t, "profiles", "import", lf, "--name", "Из файла")
	out = e.must(t, "profiles", "use", "Работа") // пустой профиль: правил 0
	if !strings.Contains(out, "правил: 0") {
		t.Fatalf("use: %s", out)
	}
	if out := e.must(t, "rules"); !strings.Contains(out, "правил нет") {
		t.Fatalf("после переключения на пустой профиль: %s", out)
	}
	e.must(t, "profiles", "rename", "Работа", "Работа 2")
	if code, _, errs := e.run("profiles", "rm", "Работа 2"); code != 1 || !strings.Contains(errs, "активный") {
		t.Fatalf("активный удалить нельзя: %d %s", code, errs)
	}
	e.must(t, "profiles", "use", "Основной")
	e.must(t, "profiles", "rm", "Работа 2")
	e.must(t, "profiles", "rm", "Импорт")
	if code, _, errs := e.run("profiles", "use", "нет-такого"); code != 1 || !strings.Contains(errs, "не найден") {
		t.Fatalf("несуществующий профиль: %d %s", code, errs)
	}
	var js []map[string]any
	if err := json.Unmarshal([]byte(e.must(t, "--json", "profiles")), &js); err != nil || len(js) < 2 {
		t.Fatalf("--json: %v", err)
	}
}

func TestUpdateCommands(t *testing.T) {
	data := []byte(strings.Repeat("brod-0.4.0-", 20_000))
	sum := sha256.Sum256(data)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases.json", "/releases.json.sig":
			doc, _ := json.Marshal(ota.ReleasesDoc{Version: 1, Release: ota.Release{App: "0.4.0", Notes: "правила маршрутизации", Assets: map[string]ota.Asset{
				"linux-amd64": {URL: srv.URL + "/brod.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}}}})
			if strings.HasSuffix(r.URL.Path, ".sig") {
				w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, doc))))
				return
			}
			w.Write(doc)
		case "/brod.tar.gz":
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e := setupWith(t, func(c *core.Config) {
		c.UpdateURLs, c.ReleasesKey, c.OTAClient, c.AppVersion, c.UpdatePlatform = []string{srv.URL + "/releases.json"}, pub, srv.Client(), "0.3.0", "linux-amd64"
	})
	if out := e.must(t, "update"); !strings.Contains(out, "версия: 0.3.0 (linux-amd64)") || !strings.Contains(out, "ещё не выполнялась") {
		t.Fatalf("исходное состояние: %s", out)
	}
	if code, _, errs := e.run("update", "download"); code != 1 || !strings.Contains(errs, "нет доступного обновления") {
		t.Fatalf("скачивание без обновления: %d %s", code, errs)
	}
	out := e.must(t, "update", "check")
	if !strings.Contains(out, "доступна: 0.4.0") || !strings.Contains(out, "правила маршрутизации") {
		t.Fatalf("check: %s", out)
	}
	out = e.must(t, "update", "download", "--wait")
	if !strings.Contains(out, "скачано и проверено") || !strings.Contains(out, "brod-0.4.0-linux-amd64.tar.gz") {
		t.Fatalf("download: %s", out)
	}
	if out := e.must(t, "update"); !strings.Contains(out, "скачано и проверено") {
		t.Fatalf("status после скачивания: %s", out)
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(e.must(t, "--json", "update")), &js); err != nil || js["available"] != true {
		t.Fatalf("--json: %v %v", err, js)
	}
	if code, _, _ := e.run("update", "bogus"); code != 2 {
		t.Fatalf("неизвестная подкоманда: код %d", code)
	}
	// сбой проверки: причина в выводе, команда не падает
	e2 := setup(t)
	if out := e2.must(t, "update", "check"); !strings.Contains(out, "последняя проверка не удалась") {
		t.Fatalf("check без сети: %s", out)
	}
}

func TestUDPCommandNeedsActiveNode(t *testing.T) {
	e := setup(t)
	code, _, errOut := e.run("udp")
	if code == 0 || !strings.Contains(errOut, "активного узла нет") {
		t.Fatalf("без активного узла замер UDP должен отказать: код %d, %q", code, errOut)
	}
}
