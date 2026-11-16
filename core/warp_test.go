package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brod/engine"
	"brod/prober"

	"brod/store"
	"brod/warp"
)

// fakeWarp — аккаунт с фиктивными ключами и адресом из TEST-NET: endpoint поднимается, но трафик никуда не уходит.
func fakeWarp() *warp.Account {
	return &warp.Account{PrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=", PeerPublicKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=", AddrV4: "10.9.9.2"}
}

// warpCore — ядро с WARP; цепочка через WARP в нём выключена (умолчание «включена» проверяет TestViaWarpDefaultOn),
// чтобы остальные тесты не зависели от неё.
func warpCore(t *testing.T, tc *tcheck, mutate func(*Config)) *Core {
	c := rawWarpCore(t, tc, mutate)
	if err := c.st.SetSetting(SetViaWarp, "0"); err != nil {
		t.Fatal(err)
	}
	c.viaWarpMem.Store(viaWarpUnknown)
	return c
}

func rawWarpCore(t *testing.T, tc *tcheck, mutate func(*Config)) *Core {
	return targetCore(t, tc, func(cfg *Config) {
		cfg.WarpAccount = fakeWarp()
		cfg.WarpEndpoint = "192.0.2.1"
		cfg.WarpMask = "none"
		if mutate != nil {
			mutate(cfg)
		}
	})
}

// Цепочка через WARP по умолчанию включена (при настроенном аккаунте); явное «0» выключает, «1» снова включает.
func TestViaWarpDefaultOn(t *testing.T) {
	c := rawWarpCore(t, newTCheck(), nil)
	if !c.viaWarp() {
		t.Fatal("по умолчанию цепочка через WARP должна быть включена")
	}
	if s, _ := c.Settings(); s[SetViaWarp] != "1" {
		t.Fatalf("настройка в списке: %q", s[SetViaWarp])
	}
	if err := c.SetSetting(SetViaWarp, "0", "user"); err != nil {
		t.Fatal(err)
	}
	if c.viaWarp() {
		t.Fatal("явное «0» должно выключать")
	}
	if s, _ := c.Settings(); s[SetViaWarp] != "0" {
		t.Fatalf("настройка в списке: %q", s[SetViaWarp])
	}
	if err := c.SetSetting(SetViaWarp, "1", "user"); err != nil || !c.viaWarp() {
		t.Fatalf("включение: %v", err)
	}
	// без аккаунта WARP цепочки нет
	if c2 := targetCore(t, newTCheck(), nil); c2.viaWarp() {
		t.Fatal("без аккаунта WARP цепочки быть не должно")
	}
}

func TestWarpDisabledWithoutAccount(t *testing.T) {
	c := targetCore(t, newTCheck(), nil)
	if c.WarpEnabled() || c.eng.Has(OutWarp) {
		t.Fatal("без аккаунта WARP быть не должно")
	}
	_ = putAsApp(c, videosite(""))
	if _, err := c.SwitchTarget(context.Background(), "videosite", store.SelWarp, "api:myapp"); err == nil || !strings.Contains(err.Error(), "не настроен") {
		t.Fatalf("выбор WARP без аккаунта: %v", err)
	}
	if err := c.SetSetting(SetViaWarp, "1", "user"); err == nil {
		t.Fatal("цепочка через WARP без аккаунта включилась")
	}
	if _, err := c.ProbeWarp(context.Background()); err == nil {
		t.Fatal("ProbeWarp без аккаунта")
	}
}

func TestWarpAccountFileLoadedAndPermissionsFixed(t *testing.T) {
	dir := t.TempDir()
	b, _ := json.Marshal(fakeWarp())
	path := filepath.Join(dir, "warp-account.json")
	if err := os.WriteFile(path, b, 0o644); err != nil { // небезопасные права: в файле приватный ключ
		t.Fatal(err)
	}
	c := newCore(t, dir)
	defer c.Close()
	if !c.WarpEnabled() || !c.eng.Has(OutWarp) {
		t.Fatal("аккаунт из файла не подхвачен")
	}
	if st, _ := os.Stat(path); posixPerms && st.Mode().Perm() != 0o600 {
		t.Fatalf("права файла аккаунта: %v", st.Mode().Perm())
	}
	// битый файл — ошибка старта, а не молчаливое отключение
	_ = os.WriteFile(path, []byte(`{"private_key":""}`), 0o600)
	if _, err := New(Config{StateDir: dir, AppPort: nextPort(), Manual: true}); err == nil {
		t.Fatal("битый файл аккаунта принят")
	}
}

// WARP как узел цели: выбор принимается, переживает перезапуск; endpoint защищён от удаления, пока цель на нём.
func TestWarpAsTargetNodeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	tc := newTCheck()
	mk := func() *Core {
		c := newCore(t, dir)
		c.cfg.TargetCheck = tc.fn
		return c
	}
	b, _ := json.Marshal(fakeWarp())
	_ = os.WriteFile(filepath.Join(dir, "warp-account.json"), b, 0o600)
	c := mk()
	_ = c.Start(context.Background())
	_ = putAsApp(c, videosite(""))
	if _, err := c.SwitchTarget(context.Background(), "videosite", store.SelWarp, "api:myapp"); err != nil {
		t.Fatal(err)
	}
	if tag, _ := c.eng.SwitchTarget(SwitchTag("videosite")); tag != OutWarp {
		t.Fatalf("switch цели смотрит на %q", tag)
	}
	if err := c.eng.Remove(OutWarp); err == nil {
		t.Fatal("endpoint WARP удалился, пока на нём цель")
	}
	_ = c.Close()
	c2 := mk()
	if err := c2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if got := c2.currentOf("videosite"); got != store.SelWarp {
		t.Fatalf("выбор WARP не восстановлен: %q", got)
	}
	if st, _ := c2.TargetStatus("videosite"); st.Node != store.SelWarp || st.NodeInfo != nil {
		t.Fatalf("статус: %+v", st)
	}
}

// Порядок кандидатов P7: direct → узлы → WARP; WARP берётся, когда ничего другого не проходит.
func TestWarpIsLastCandidate(t *testing.T) {
	tc := newTCheck()
	c := warpCore(t, tc, nil)
	cur, other := addNode(t, c, "cur"), addNode(t, c, "other")
	_ = c.st.Record([]store.Row{{Node: other, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	tg := videosite(store.PolicyAutoFailover)
	tg.AllowWarp = true
	_ = putAsApp(c, tg)
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")

	// проходят и direct, и узел, и WARP — первым берётся direct
	tc.set(store.SelDirect, true)
	tc.set(other, true)
	tc.set(store.SelWarp, true)
	got, _ := c.verifyTargetCandidates(context.Background(), mustTarget(t, c, "videosite"), 3, cur)
	if len(got) != 3 || got[0] != store.SelDirect || got[1] != other || got[2] != store.SelWarp {
		t.Fatalf("порядок кандидатов: %v", got)
	}
	// direct и узел не проходят — остаётся WARP
	tc.set(store.SelDirect, false)
	tc.set(other, false)
	got, _ = c.verifyTargetCandidates(context.Background(), mustTarget(t, c, "videosite"), 1, cur)
	if len(got) != 1 || got[0] != store.SelWarp {
		t.Fatalf("только WARP: %v", got)
	}
	// политика запрещает WARP
	tg.AllowWarp = false
	_ = putAsApp(c, tg)
	got, _ = c.verifyTargetCandidates(context.Background(), mustTarget(t, c, "videosite"), 1, cur)
	if len(got) != 0 {
		t.Fatalf("allow_warp=false, а кандидат %v", got)
	}
}

func mustTarget(t *testing.T, c *Core, id string) store.Target {
	t.Helper()
	tg, err := c.st.Target(id)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// auto-failover уходит на WARP, когда остальные кандидаты не проходят проверку цели.
func TestAutoFailoverToWarp(t *testing.T) {
	tc := newTCheck()
	c := warpCore(t, tc, nil)
	cur := addNode(t, c, "cur")
	tg := videosite(store.PolicyAutoFailover)
	tg.AllowWarp = true
	_ = putAsApp(c, tg)
	_, _ = c.SwitchTarget(context.Background(), "videosite", cur, "user")
	tc.set(store.SelDirect, false)
	tc.set(store.SelWarp, true)
	_, _ = c.ProbeTarget(context.Background(), "videosite")
	_, _ = c.ProbeTarget(context.Background(), "videosite")
	waitFor(t, "переключение на WARP", func() bool { return c.currentOf("videosite") == store.SelWarp })
	if tag, _ := c.eng.SwitchTarget(SwitchTag("videosite")); tag != OutWarp {
		t.Fatalf("switch цели: %q", tag)
	}
	if c.eng.Has(NodeTag(cur)) {
		t.Fatal("прежний узел остался в движке")
	}
}

// Цепочка через WARP: боевые и пробные outbound'ы получают detour, история ведётся отдельно от прямой.
func TestViaWarpChainsNodesAndSeparatesHistory(t *testing.T) {
	tc := newTCheck()
	c := warpCore(t, tc, nil)
	n := store.NodeRec{ID: store.NodeID("n"), Class: "vless/tcp/tls", Proto: "vless", Opts: []byte(`{"type":"vless","server":"h.example","server_port":443,"uuid":"5b0e5c2e-0000-4000-8000-000000000001"}`)}
	wg := store.NodeRec{ID: store.NodeID("wg"), Class: "wireguard/tcp/none", Proto: "wireguard", Endpoint: true, Opts: []byte(`{"type":"wireguard"}`)}

	direct, _ := outboundJSON(n, "t", c.viaWarp())
	if strings.Contains(string(direct), "detour") {
		t.Fatalf("без цепочки detour быть не должно: %s", direct)
	}
	if err := c.SetSetting(SetViaWarp, "1", "user"); err != nil {
		t.Fatal(err)
	}
	if !c.viaWarp() {
		t.Fatal("цепочка не включилась")
	}
	chained, _ := outboundJSON(n, "t", c.viaWarp())
	if !strings.Contains(string(chained), `"detour":"warp"`) {
		t.Fatalf("detour не добавлен: %s", chained)
	}
	if wgJSON, _ := outboundJSON(wg, "t", true); strings.Contains(string(wgJSON), "detour") {
		t.Fatalf("WireGuard-узел не должен цепляться к WARP: %s", wgJSON)
	}
	if !strings.HasSuffix(c.netKey(), "|warp") {
		t.Fatalf("ключ сети: %q", c.netKey())
	}
	if st, _ := c.Status(); !st.WarpEnabled || !st.ViaWarp {
		t.Fatalf("статус: %+v", st)
	}
	// результаты прямого режима не видны в режиме через WARP
	_ = c.st.Record([]store.Row{{Node: n.ID, Net: "net1", Level: 2, OK: true, At: time.Now()}})
	if h, _ := c.st.Histories(c.netKey(), time.Now()); len(h) != 0 {
		t.Fatal("прямые результаты попали в историю режима через WARP")
	}
}

// WARP как активный выход: выбирается, переживает перезапуск, виден в статусе; direct активным быть не может.
func TestWarpAsActiveExit(t *testing.T) {
	dir := t.TempDir()
	b, _ := json.Marshal(fakeWarp())
	_ = os.WriteFile(filepath.Join(dir, "warp-account.json"), b, 0o600)
	c := newCore(t, dir)
	_ = c.Start(context.Background())
	if err := c.Connect(store.SelDirect, "user"); err == nil {
		t.Fatal("direct принят как активный выход")
	}
	if err := c.Connect(store.SelWarp, "user"); err != nil {
		t.Fatal(err)
	}
	if tag, _ := c.eng.SwitchTarget(OutActive); tag != OutWarp {
		t.Fatalf("active смотрит на %q", tag)
	}
	st, _ := c.Status()
	if st.Active == nil || st.Active.ID != store.SelWarp || st.Active.Proto != "warp" {
		t.Fatalf("статус: %+v", st.Active)
	}
	// узел и затем снова WARP: у узла gc не оставляет мусора
	n := addNode(t, c, "n")
	_ = c.Connect(n, "user")
	_ = c.Connect(store.SelWarp, "user")
	if c.eng.Has(NodeTag(n)) {
		t.Fatal("прежний узел остался в движке")
	}
	_ = c.Close()
	c2 := newCore(t, dir)
	if err := c2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if st, _ := c2.Status(); st.Active == nil || st.Active.ID != store.SelWarp {
		t.Fatalf("после перезапуска: %+v", st.Active)
	}
	// без аккаунта WARP активным не выбрать
	c3 := startCore(t, t.TempDir())
	if err := c3.Connect(store.SelWarp, "user"); err == nil || !strings.Contains(err.Error(), "не настроен") {
		t.Fatalf("без аккаунта: %v", err)
	}
}

// Пока цепочка через WARP включена, он единственный путь ко всем узлам. Если WARP не отвечает, цепочку нужно отключить (иначе все узлы «мертвы»
// и поиск альтернатив пуст), активный узел перерегистрировать напрямую; когда WARP оживает — вернуть цепочку. Настройка пользователя не меняется.
func TestWarpDownBypassesChainAndRecovers(t *testing.T) {
	defer func(g time.Duration) { warpColdGrace = g }(warpColdGrace)
	warpColdGrace = 0 // здесь проверяется сам вердикт «два провала»; холодное окно — в TestWarpColdFailuresAreForgivenOnKnownGoodNetwork
	lv := &liveness{ok: true}
	c := rawWarpCore(t, newTCheck(), func(cfg *Config) { cfg.WarpLiveness = lv.fn })
	id := addVless(t, c, "chained")
	if err := c.Connect(id, "user"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if !c.viaWarp() || c.reg[id] != NodeTag(id) {
		t.Fatalf("исходно цепочка включена, тег %q", c.reg[id])
	}
	lv.set(false)
	c.warpCheck(ctx)
	if !c.viaWarp() {
		t.Fatal("одиночный провал не должен отключать цепочку")
	}
	c.warpCheck(ctx)
	if c.viaWarp() || !c.warpDown.Load() {
		t.Fatal("два провала подряд должны отключить цепочку")
	}
	if len(eventsOf(c, EvWarpDown)) != 1 {
		t.Fatalf("событий warp-down: %d", len(eventsOf(c, EvWarpDown)))
	}
	if got := c.reg[id]; got != NodeTag(id)+"-d" {
		t.Fatalf("активный узел не перерегистрирован напрямую: тег %q", got)
	}
	if tgt, _ := c.eng.SwitchTarget(OutActive); tgt != NodeTag(id)+"-d" {
		t.Fatalf("switch смотрит на %q", tgt)
	}
	if v, ok, _ := c.st.Setting(SetViaWarp); ok && v == "0" {
		t.Fatal("настройка пользователя не должна меняться")
	}
	sel, _ := c.st.Selection(store.ScopeActive)
	if sel.Node != id {
		t.Fatalf("выбор активного узла сменился: %+v", sel)
	}
	// оживление: нужны два успеха подряд
	lv.set(true)
	c.warpCheck(ctx)
	if c.viaWarp() {
		t.Fatal("одиночный успех не должен возвращать цепочку")
	}
	c.warpCheck(ctx)
	if !c.viaWarp() || len(eventsOf(c, EvWarpUp)) != 1 {
		t.Fatalf("после двух успехов цепочка должна вернуться (событий warp-up: %d)", len(eventsOf(c, EvWarpUp)))
	}
	// пользователь выключил цепочку сам: наблюдение сбрасывает флаг упавшего WARP
	lv.set(false)
	c.warpCheck(ctx)
	c.warpCheck(ctx)
	if !c.warpDown.Load() {
		t.Fatal("WARP снова должен считаться упавшим")
	}
	if err := c.st.SetSetting(SetViaWarp, "0"); err != nil {
		t.Fatal(err)
	}
	c.viaWarpMem.Store(viaWarpUnknown)
	c.warpCheck(ctx)
	if c.warpDown.Load() {
		t.Fatal("при выключенной пользователем цепочке флаг упавшего WARP сбрасывается")
	}
}

// addVless кладёт в базу узел vless: у outbound типа direct (addNode) detour невозможен, а цепочке через WARP нужен настоящий протокол.
func addVless(t *testing.T, c *Core, key string) string {
	t.Helper()
	id := store.NodeID(key)
	opts := fmt.Sprintf(`{"type":"vless","server":"%s.example","server_port":443,"uuid":"5b0e5c2e-0000-4000-8000-000000000001"}`, key)
	err := c.st.UpsertNodes("src", time.Now().UnixNano(), []store.NodeRec{{
		ID: id, Class: "vless/tcp/tls", Proto: "vless", Server: key + ".example", Port: 443, Opts: []byte(opts),
	}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Импорт аккаунта: проверяется содержимое, файл пишется с правами 0600, настроенный аккаунт без replace не заменяется.
func TestImportWarp(t *testing.T) {
	dir := t.TempDir()
	c := newCore(t, dir)
	defer c.Close()
	path := filepath.Join(dir, "warp-account.json")

	for name, raw := range map[string]string{
		"не JSON":         `не json`,
		"нет полей":       `{"private_key":"x"}`,
		"ключ не base64":  `{"private_key":"!!","peer_public_key":"!!","addr_v4":"172.16.0.2/32","endpoint_host":"e:2408"}`,
		"ключ не 32 байт": `{"private_key":"AAAA","peer_public_key":"AAAA","addr_v4":"172.16.0.2/32","endpoint_host":"e:2408"}`,
	} {
		if err := c.ImportWarp([]byte(raw), false); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: ждали ErrInvalid, получили %v", name, err)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s: файл не должен создаваться", name)
		}
	}

	good, _ := json.Marshal(fakeWarp())
	if err := c.ImportWarp(good, false); err != nil {
		t.Fatalf("корректный аккаунт: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || (posixPerms && st.Mode().Perm() != 0o600) {
		t.Fatalf("файл аккаунта: %v %v", st, err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("временный файл остался")
	}

	// ядро с аккаунтом (так будет после перезапуска): повторный импорт без replace отклоняется, с replace проходит
	c2 := newCore(t, dir)
	defer c2.Close()
	if !c2.WarpEnabled() {
		t.Fatal("аккаунт не подхвачен после перезапуска")
	}
	if err := c2.ImportWarp(good, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("без replace: %v", err)
	}
	if err := c2.ImportWarp(good, true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := c2.RegisterWarp(context.Background(), false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("регистрация поверх настроенного без replace: %v", err)
	}
}

// Первая проверка WARP после старта ядра получает окно шире обычного (рукопожатие холодного туннеля может занять ~5 с), следующие — обычное.
func TestWarpFirstCheckHasLongerWindow(t *testing.T) {
	var limits []time.Duration
	c := rawWarpCore(t, newTCheck(), func(cfg *Config) {
		cfg.HealthTimeout = 3 * time.Second
		cfg.WarpLiveness = func(ctx context.Context, _ *engine.Probe) prober.Result {
			dl, _ := ctx.Deadline()
			limits = append(limits, time.Until(dl).Round(time.Second))
			return prober.Result{OK: true}
		}
	})
	c.warpCheck(context.Background())
	c.warpCheck(context.Background())
	if len(limits) != 2 || limits[0] != 12*time.Second || limits[1] != 6*time.Second {
		t.Fatalf("окна проверок: %v, ждали [12s 6s]", limits)
	}
}

// Каждое включение VPN создаёт ядро заново, и первая проверка по холодному туннелю часто проваливается, хотя через несколько секунд WARP отвечает.
// На сети, где WARP только что работал, такие провалы в первые warpColdGrace не отключают цепочку; на незнакомой сети вердикт обычный.
func TestWarpColdFailuresAreForgivenOnKnownGoodNetwork(t *testing.T) {
	lv := &liveness{ok: true}
	c := rawWarpCore(t, newTCheck(), func(cfg *Config) { cfg.WarpLiveness = lv.fn })
	id := addVless(t, c, "cold")
	if err := c.Connect(id, "user"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// сеть без истории: два провала подряд отключают цепочку
	lv.set(false)
	c.warpCheck(ctx)
	c.warpCheck(ctx)
	if !c.warpDown.Load() {
		t.Fatal("на сети, где WARP ещё не отвечал, два провала должны отключать цепочку")
	}
	lv.set(true)
	c.warpCheck(ctx)
	c.warpCheck(ctx)
	if c.warpDown.Load() {
		t.Fatal("после двух успехов цепочка возвращается")
	}

	// тот же процесс, ядро создано заново (переключение VPN): WARP только что отвечал, холодные провалы прощаются
	c.startedAt = time.Now()
	tagBefore := c.reg[id]
	lv.set(false)
	for i := 0; i < 5; i++ {
		c.warpCheck(ctx)
	}
	if c.warpDown.Load() || !c.viaWarp() {
		t.Fatal("холодные провалы на сети с недавним успехом не должны отключать цепочку")
	}
	if got := c.reg[id]; got != tagBefore {
		t.Fatalf("активный узел не должен перерегистрироваться: тег %q, был %q", got, tagBefore)
	}

	// холодное окно кончилось, а WARP всё молчит: вердикт обычный
	c.startedAt = time.Now().Add(-2 * warpColdGrace)
	c.warpCheck(ctx)
	c.warpCheck(ctx)
	if !c.warpDown.Load() {
		t.Fatal("после холодного окна два провала должны отключать цепочку")
	}
	// признанный упавшим WARP больше не «недавно работал»: следующий холодный запуск без прощения
	c.startedAt = time.Now()
	lv.set(true)
	c.warpCheck(ctx)
	c.warpCheck(ctx)
	c.startedAt = time.Now()
	c.warpMemoSet(func(m *warpMemoRec) { m.downAt = time.Now().Add(time.Second) })
	if c.warpColdAndKnownGood() {
		t.Fatal("после признания упавшим прощения быть не должно")
	}
}

// viaWarp не ходит в базу на каждом круге: значение в памяти, сбрасывается записью настройки через SetSetting.
func TestViaWarpCachedInMemory(t *testing.T) {
	c := rawWarpCore(t, newTCheck(), nil)
	if !c.viaWarp() {
		t.Fatal("по умолчанию цепочка включена")
	}
	if err := c.st.SetSetting(SetViaWarp, "0"); err != nil { // мимо SetSetting ядра: кэш не знает
		t.Fatal(err)
	}
	if !c.viaWarp() {
		t.Fatal("значение должно браться из памяти, а не из базы")
	}
	if err := c.SetSetting(SetViaWarp, "0", "user"); err != nil {
		t.Fatal(err)
	}
	if c.viaWarp() {
		t.Fatal("после SetSetting кэш сброшен, цепочка выключена")
	}
}
