package androidcore

// Жизненный цикл ядра и контракт моста на стороне Go. Сам Start (libbox, TUN) без устройства не запускается, поэтому проверяется то,
// что вынесено в bridge.go без тега android: Stop (stopInstance), Connect/Disconnect/Running/VPNOn, watchState, уведомления,
// вшитые источники и совпадение имён Go-функций с вызовами Kotlin.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"brod/aggregator"
	"brod/core"
	"brod/internal/testport"
	"brod/notify"
	"brod/supervisor"
)

const wait = 10 * time.Second

// startFake повторяет итог start без libbox: супервизор с ядром без TUN, сокет-файл, готовый instance в cur.
func startFake(t *testing.T) (*instance, string) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	sup, err := supervisor.New(ctx, supervisor.Config{
		Base: core.Config{StateDir: dir, AppPort: testport.Next(), LogLevel: "error", Manual: true, SeedSources: []aggregator.Source{}},
		NewCore: func(c core.Config) (*core.Core, error) {
			c.TUN = core.TUNConfig{} // TUN в тесте не поднимаем
			return core.New(c)
		},
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "brodd.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { <-ctx.Done(); close(done) }()
	in := &instance{cancel: cancel, sup: sup, sock: sock, done: done}
	mu.Lock()
	cur = in
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		if cur == in {
			cur = nil
		}
		mu.Unlock()
		cancel()
		_ = sup.Close()
	})
	return in, sock
}

func TestNoCoreErrors(t *testing.T) {
	mu.Lock()
	saved := cur
	cur = nil
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); cur = saved; mu.Unlock() })
	if Running() || VPNOn() {
		t.Fatal("без ядра Running/VPNOn должны быть false")
	}
	if err := Connect(""); err == nil || !strings.Contains(err.Error(), "не запущено") {
		t.Fatalf("Connect без ядра: %v", err)
	}
	Disconnect() // не должно падать
	Stop()       // не должно падать
	stopInstance(nil, time.Millisecond)
}

func TestConnectDisconnectAndStop(t *testing.T) {
	in, sock := startFake(t)
	if !Running() || VPNOn() {
		t.Fatalf("после старта: Running=%v VPNOn=%v", Running(), VPNOn())
	}
	if err := Connect(core.ModeAll); err != nil {
		t.Fatal(err)
	}
	if !VPNOn() || in.sup.Mode() != core.ModeAll {
		t.Fatalf("после Connect: on=%v mode=%q", VPNOn(), in.sup.Mode())
	}
	Disconnect()
	if VPNOn() {
		t.Fatal("после Disconnect VPN включён")
	}
	if err := Connect("нет-такого-режима"); err == nil {
		t.Fatal("неизвестный режим принят")
	}

	Stop()
	if Running() {
		t.Fatal("после Stop ядро числится запущенным")
	}
	select {
	case <-in.done:
	default:
		t.Fatal("Stop не отменил контекст")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("сокет не удалён: %v", err)
	}
	Stop() // повторный вызов безопасен
}

type listenerRec struct {
	mu    sync.Mutex
	vpn   []bool
	brief [][3]string
}

func (l *listenerRec) OnVPN(on bool) { l.mu.Lock(); l.vpn = append(l.vpn, on); l.mu.Unlock() }
func (l *listenerRec) OnBrief(m, p, c string) {
	l.mu.Lock()
	l.brief = append(l.brief, [3]string{m, p, c})
	l.mu.Unlock()
}
func (l *listenerRec) snap() ([]bool, [][3]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]bool(nil), l.vpn...), append([][3]string(nil), l.brief...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(dl) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Приложению сообщают о включении и выключении VPN сразу по событию супервизора, а режим — вместе с включением.
func TestWatchStateReportsVPNAndBrief(t *testing.T) {
	in, _ := startFake(t)
	rec := &listenerRec{}
	var l StateListener = rec
	listener.Store(&l)
	t.Cleanup(func() { listener.Store(nil) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { watchState(ctx, in.sup); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(wait):
			t.Error("watchState не вышел после отмены")
		}
	})
	on := true
	if _, err := in.sup.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	eventually(t, "OnVPN(true) и OnBrief", func() bool {
		v, b := rec.snap()
		return len(v) == 1 && v[0] && len(b) >= 1
	})
	if _, b := rec.snap(); b[0][0] != core.ModeAll {
		t.Fatalf("режим в OnBrief: %q", b[0][0])
	}
	off := false
	if _, err := in.sup.Set(&off, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "OnVPN(false)", func() bool { v, _ := rec.snap(); return len(v) == 2 && !v[1] })
}

// SetStateListener сразу сообщает текущее состояние работающего ядра.
func TestSetStateListenerReportsCurrentState(t *testing.T) {
	in, _ := startFake(t)
	on := true
	if _, err := in.sup.Set(&on, core.ModeAll); err != nil {
		t.Fatal(err)
	}
	rec := &listenerRec{}
	SetStateListener(rec)
	t.Cleanup(func() { listener.Store(nil) })
	v, b := rec.snap()
	if len(v) != 1 || !v[0] || len(b) != 1 || b[0][0] != core.ModeAll {
		t.Fatalf("текущее состояние: %v %v", v, b)
	}
	// ядро выключено VPN: OnBrief не вызывается (ok=false)
	if _, ok := briefOf(in.sup); !ok {
		t.Fatal("briefOf при включённом VPN должен вернуть ok")
	}
	off := false
	_, _ = in.sup.Set(&off, "")
	if _, ok := briefOf(in.sup); ok {
		t.Fatal("briefOf при выключенном VPN должен вернуть ok=false")
	}
}

type notifRec struct {
	mu      sync.Mutex
	shown   []shownRec
	cancels []int32
}
type shownRec struct {
	id                int32
	title, body, acts string
	important         bool
}

func (n *notifRec) Show(id int32, title, body, actions string, important bool) {
	n.mu.Lock()
	n.shown = append(n.shown, shownRec{id, title, body, actions, important})
	n.mu.Unlock()
}
func (n *notifRec) Cancel(id int32) { n.mu.Lock(); n.cancels = append(n.cancels, id); n.mu.Unlock() }

// Формат, который разбирает Kotlin (Notifications.kt): действия «ключ\x1fподпись» через \x1e, важность по Urgency, замена по ReplaceID.
func TestSenderFormatsNotificationForKotlin(t *testing.T) {
	rec := &notifRec{}
	SetNotifier(rec)
	t.Cleanup(func() { notifier.Store(nil); notifySender.Store(nil) })
	s := &sender{}
	id1, err := s.Send(notify.Notification{Summary: "Узел упал", Body: "переключён", Urgency: notify.Normal,
		Actions: []notify.Action{{Key: "sw:1", Label: "Переключить"}, {Key: "sw:2", Label: "Другой"}}})
	if err != nil || id1 == 0 {
		t.Fatalf("Send: %d %v", id1, err)
	}
	id2, _ := s.Send(notify.Notification{Summary: "тихо", Urgency: notify.Low})
	if id2 == id1 {
		t.Fatal("новые уведомления получили один идентификатор")
	}
	if id3, _ := s.Send(notify.Notification{Summary: "замена", ReplaceID: id1}); id3 != id1 {
		t.Fatalf("ReplaceID не сохранён: %d", id3)
	}
	s.Close(id2)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.shown) != 3 || len(rec.cancels) != 1 || rec.cancels[0] != int32(id2) {
		t.Fatalf("вызовы: %+v %v", rec.shown, rec.cancels)
	}
	if got := rec.shown[0]; got.title != "Узел упал" || got.body != "переключён" || got.acts != "sw:1\x1fПереключить\x1esw:2\x1fДругой" || !got.important {
		t.Fatalf("первое уведомление: %+v", got)
	}
	if rec.shown[1].important || rec.shown[1].acts != "" {
		t.Fatalf("тихое уведомление: %+v", rec.shown[1])
	}
}

// Нажатие кнопки в Kotlin (NotifyAction) доходит до обработчика ядра с теми же id и ключом; без отправителя вызов безопасен.
func TestNotifyActionReachesHandler(t *testing.T) {
	notifySender.Store(nil)
	NotifyAction(1, "k") // без отправителя: не падает
	s := &sender{}
	notifySender.Store(s)
	t.Cleanup(func() { notifySender.Store(nil) })
	NotifyAction(2, "k2") // обработчик ещё не задан
	type got struct {
		id  uint32
		key string
	}
	ch := make(chan got, 1)
	s.OnAction(func(id uint32, key string) { ch <- got{id, key} })
	NotifyAction(7, "sw:3")
	select {
	case g := <-ch:
		if g.id != 7 || g.key != "sw:3" {
			t.Fatalf("получено %+v", g)
		}
	case <-time.After(wait):
		t.Fatal("обработчик не вызван")
	}
}

// Вшитые источники разбираются тем же кодом, что и при старте.
func TestEmbeddedSourcesParse(t *testing.T) {
	if len(sources) == 0 {
		t.Fatal("sources.txt не вшит")
	}
	p := filepath.Join(t.TempDir(), "sources.txt")
	if err := os.WriteFile(p, sources, 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := aggregator.ParseSourcesFile(p)
	if err != nil || len(list) < 10 {
		t.Fatalf("вшитые источники: %d, %v", len(list), err)
	}
	for _, s := range list {
		if !strings.HasPrefix(s.URL, "https://") || s.Name == "" {
			t.Errorf("подозрительный источник: %+v", s)
		}
	}
}

// ---- контракт Kotlin ↔ Go ----

// mobileName — имя, под которым gomobile отдаёт функцию Go в Kotlin: ведущая последовательность заглавных опускается
// (StartVPN → startVPN, VPNOn → vpnOn, SetDNS → setDNS).
func mobileName(n string) string {
	r := []rune(n)
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) {
		i++
	}
	switch {
	case i == 0:
		return n
	case i == 1 || i == len(r):
		return strings.ToLower(string(r[:i])) + string(r[i:])
	default: // несколько заглавных подряд, дальше строчная: последняя заглавная начинает следующее слово
		return strings.ToLower(string(r[:i-1])) + string(r[i-1:])
	}
}

type goAPI struct {
	funcs  map[string]int            // имя для Kotlin → число параметров
	ifaces map[string]map[string]int // интерфейс → методы (имя для Kotlin → число параметров)
}

func paramCount(ft *ast.FuncType) int {
	n := 0
	for _, f := range ft.Params.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}

func parseGoAPI(t *testing.T) goAPI {
	t.Helper()
	fset := token.NewFileSet()
	api := goAPI{funcs: map[string]int{}, ifaces: map[string]map[string]int{}}
	for _, name := range []string{"androidcore.go", "bridge.go"} { // androidcore.go собирается только с тегом android: разбираем исходник
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					api.funcs[mobileName(d.Name.Name)] = paramCount(d.Type)
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					it, isI := ts.Type.(*ast.InterfaceType)
					if !isI {
						continue
					}
					m := map[string]int{}
					for _, meth := range it.Methods.List {
						if ft, ok := meth.Type.(*ast.FuncType); ok && len(meth.Names) == 1 {
							m[mobileName(meth.Names[0].Name)] = paramCount(ft)
						}
					}
					api.ifaces[ts.Name.Name] = m
				}
			}
		}
	}
	return api
}

func kotlinFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "app", "android", "app", "src", "main", "kotlin")
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".kt") {
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			out[p] = string(b)
		}
		return err
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("исходники Kotlin: %d файлов, %v", len(out), err)
	}
	return out
}

// Каждый вызов Androidcore.xxx в Kotlin соответствует экспортируемой функции Go, а число аргументов совпадает.
func TestKotlinCallsMatchGoFunctions(t *testing.T) {
	api := parseGoAPI(t)
	call := regexp.MustCompile(`Androidcore\.(\w+)\(`)
	seen := map[string]bool{}
	for path, src := range kotlinFiles(t) {
		for _, m := range call.FindAllStringSubmatchIndex(src, -1) {
			name := src[m[2]:m[3]]
			want, ok := api.funcs[name]
			if !ok {
				t.Errorf("%s: Androidcore.%s вызывается, а такой функции в Go нет (есть: %v)", filepath.Base(path), name, sortedKeys(api.funcs))
				continue
			}
			seen[name] = true
			if got := kotlinArgCount(src[m[1]:]); got != want {
				t.Errorf("%s: Androidcore.%s: аргументов %d, в Go %d", filepath.Base(path), name, got, want)
			}
		}
	}
	for _, must := range []string{"start", "startVPN", "setStateListener", "setNotifier", "setNetworkID", "setDNS", "setMetered", "disconnect", "connect", "vpnOn", "notifyAction", "sanitizedLog"} {
		if !seen[must] {
			t.Errorf("Kotlin больше не вызывает %s (или тест не нашёл вызов)", must)
		}
	}
}

// kotlinArgCount считает аргументы вызова: rest начинается сразу после «(».
func kotlinArgCount(rest string) int {
	depth, n, any := 1, 0, false
	for _, r := range rest {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if any {
					n++
				}
				return n
			}
		case ',':
			if depth == 1 {
				n++
				any = false
				continue
			}
		}
		if !unicode.IsSpace(r) {
			any = true
		}
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// Kotlin реализует интерфейсы StateListener и Notifier с теми же методами и числом параметров, что объявлены в Go.
func TestKotlinImplementsGoInterfaces(t *testing.T) {
	api := parseGoAPI(t)
	all := ""
	for _, src := range kotlinFiles(t) {
		all += src + "\n"
	}
	for iface, methods := range api.ifaces {
		if iface != "StateListener" && iface != "Notifier" {
			continue
		}
		if !strings.Contains(all, iface) {
			t.Errorf("Kotlin не использует интерфейс %s", iface)
			continue
		}
		for name, want := range methods {
			re := regexp.MustCompile(`override fun ` + name + `\(([^)]*)\)`)
			m := re.FindStringSubmatch(all)
			if m == nil {
				t.Errorf("%s.%s: в Kotlin нет override fun %s", iface, name, name)
				continue
			}
			got := 0
			if strings.TrimSpace(m[1]) != "" {
				got = strings.Count(m[1], ",") + 1
			}
			if got != want {
				t.Errorf("%s.%s: в Kotlin %d параметров, в Go %d", iface, name, got, want)
			}
		}
	}
}

// Каналовые методы Dart (android_bridge.dart) обрабатываются в MainActivity.kt.
func TestDartChannelMethodsHandledByKotlin(t *testing.T) {
	dart, err := os.ReadFile(filepath.Join("..", "app", "lib", "src", "android_bridge.dart"))
	if err != nil {
		t.Fatal(err)
	}
	var activity string
	for p, src := range kotlinFiles(t) {
		if filepath.Base(p) == "MainActivity.kt" {
			activity = src
		}
	}
	names := regexp.MustCompile(`invokeMethod<[^>]*>\('(\w+)'`).FindAllStringSubmatch(string(dart), -1)
	if len(names) < 10 {
		t.Fatalf("в android_bridge.dart найдено методов: %d", len(names))
	}
	for _, m := range names {
		if !regexp.MustCompile(`"` + m[1] + `"\s*->`).MatchString(activity) {
			t.Errorf("Dart вызывает %q, а в MainActivity.kt обработчика нет", m[1])
		}
	}
}
