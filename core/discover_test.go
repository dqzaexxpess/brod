package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brod/aggregator"
	"brod/discover"
	"brod/prober"
	"brod/store"
)

func discList(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "vless://11111111-2222-3333-4444-%012d@%s%d.example.org:443?security=tls&sni=x.example.org#%s%d\n", i, tag, i, tag, i)
	}
	return b.String()
}

// discoverEnv — ядро с локальной заменой raw.githubusercontent.com и управляемым временем.
type discoverEnv struct {
	c     *Core
	srv   *httptest.Server
	now   atomic.Pointer[time.Time]
	url   string // база с косой чертой
	mu    sync.Mutex
	files map[string]string
}

// setFile подменяет или добавляет файл сервера (между проходами).
func (e *discoverEnv) setFile(path, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.files[path] = body
}

func (e *discoverEnv) setDay(d int) {
	t := time.Date(2026, 11, d, 12, 0, 0, 0, time.Local)
	e.now.Store(&t)
}

func newDiscoverEnv(t *testing.T, probe func(context.Context, store.NodeRec) prober.Result, canProbe bool, mut func(*Config)) *discoverEnv {
	t.Helper()
	e := &discoverEnv{}
	e.setDay(16)
	var base string
	files := map[string]string{
		"/o1/r1/main/README.md": "[известный](BASE/o1/r1/main/known.txt) [живой](BASE/o1/r1/main/live.txt) [мёртвый](BASE/o1/r1/main/dead.txt) [мало](BASE/o1/r1/main/tiny.txt)",
		"/o1/r1/main/known.txt": discList(30, "k"),
		"/o1/r1/main/live.txt":  discList(30, "live"),
		"/o1/r1/main/dead.txt":  discList(25, "dead"),
		"/o1/r1/main/tiny.txt":  discList(2, "t"),
	}
	e.files = files
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		body, ok := files[r.URL.Path]
		e.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(body, "BASE", strings.TrimSuffix(base, "/")))
	}))
	t.Cleanup(e.srv.Close)
	base = e.srv.URL + "/"
	e.url = base
	cfg := Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error", Manual: true,
		SeedSources:  []aggregator.Source{{Name: "known", URL: base + "o1/r1/main/known.txt", Priority: 1}},
		Now:          func() time.Time { return *e.now.Load() },
		DiscoverBase: base, DiscoverProbe: probe, DiscoverCanProbe: func() bool { return canProbe }}
	if mut != nil {
		mut(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	// фиксированная соль: иначе оценка похожести (bottom-k по 64 хэшам) шумит от запуска к запуску, и тесты с близкими к порогу наборами мигают
	if err := c.st.SetSetting("discover.salt", "000102030405060708090a0b0c0d0e0f"); err != nil {
		t.Fatal(err)
	}
	e.c = c
	return e
}

func liveProbe(_ context.Context, n store.NodeRec) prober.Result {
	if strings.Contains(string(n.Opts), "live") {
		return prober.Result{Level: 2, OK: true}
	}
	return prober.Result{Level: 2, Class: prober.ClassOther}
}

func candByName(t *testing.T, c *Core, name string) CandidateView {
	t.Helper()
	list, err := c.Candidates()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list {
		if strings.HasSuffix(v.Name, "/"+name) {
			return v
		}
	}
	t.Fatalf("кандидата %q нет в %+v", name, list)
	return CandidateView{}
}

func TestScanCandidatesSeparateStorageAndNoRepeat(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	c := e.c
	rep, err := c.ScanCandidates(context.Background(), "user", false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Found != 2 || rep.New != 2 || rep.Probed {
		t.Fatalf("итог: %+v", rep)
	}
	list, _ := c.Candidates()
	if len(list) != 2 {
		t.Fatalf("кандидатов %d: %+v", len(list), list)
	}
	for _, v := range list {
		if strings.HasSuffix(v.Name, "/known") || strings.HasSuffix(v.Name, "/tiny") {
			t.Errorf("предложен лишний: %s", v.Name)
		}
		if strings.Contains(fmt.Sprintf("%+v", v), "11111111-2222") {
			t.Errorf("в кандидате ссылка узла: %+v", v)
		}
	}
	live := candByName(t, c, "live")
	if live.Links != 30 || live.Schemes["vless"] != 30 || live.DaysSeen != 1 || live.Repo != "o1/r1" {
		t.Errorf("live: %+v", live)
	}
	// в источники и узлы кандидаты не попадают
	if recs, _ := c.st.Sources(); len(recs) != 1 {
		t.Errorf("кандидаты попали в sources: %d", len(recs))
	}
	if total, _, _ := c.st.CountNodes(); total != 0 {
		t.Errorf("кандидаты попали в nodes: %d", total)
	}
	// повторный проход в тот же день: те же, дни не растут; на следующий день +1
	if rep, _ = c.ScanCandidates(context.Background(), "user", false); rep.New != 0 {
		t.Errorf("повторный проход нашёл новых: %+v", rep)
	}
	if candByName(t, c, "live").DaysSeen != 1 {
		t.Error("два прохода за день засчитаны за два дня")
	}
	e.setDay(17)
	if _, err := c.ScanCandidates(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	if candByName(t, c, "live").DaysSeen != 2 {
		t.Error("следующий день не засчитан")
	}
	// loop.discover.last запоминается: ядро не запускает проход заново сразу после перезапуска
	if d := c.firstDelay("discover", discoverSoon, c.cfg.DiscoverInterval); d < 23*time.Hour {
		t.Errorf("память прохода не работает: %s", d)
	}
}

func TestDismissedAndAddedNotOffered(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	c := e.c
	if _, err := c.ScanCandidates(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	live, dead := candByName(t, c, "live"), candByName(t, c, "dead")
	if err := c.DismissCandidate(dead.ID, "user"); err != nil {
		t.Fatal(err)
	}
	name, err := c.AddCandidate(live.ID, "user")
	if err != nil || name != live.Name {
		t.Fatalf("add: %q %v", name, err)
	}
	recs, _ := c.st.Sources()
	var got *store.SourceRec
	for i := range recs {
		if recs[i].ID == name {
			got = &recs[i]
		}
	}
	if got == nil || !got.UserAdded || !got.Enabled || got.URL != live.URL {
		t.Fatalf("источник из кандидата: %+v", got)
	}
	// узлы кандидата не в базе до обновления
	if total, _, _ := c.st.CountNodes(); total != 0 {
		t.Errorf("узлы в базе: %d", total)
	}
	rep, err := c.ScanCandidates(context.Background(), "user", false)
	if err != nil || rep.Found != 0 {
		t.Fatalf("после добавления и скрытия: %+v %v", rep, err)
	}
	if list, _ := c.Candidates(); len(list) != 0 {
		t.Errorf("список не пуст: %+v", list)
	}
	if _, err := c.AddCandidate(live.ID, "user"); !errors.Is(err, ErrInvalid) {
		t.Errorf("повторное добавление: %v", err)
	}
	if _, err := c.AddCandidate("000000000000", "user"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестный id: %v", err)
	}
	if err := c.DismissCandidate("000000000000", "user"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестный id: %v", err)
	}
}

func TestAddCandidateDoesNotOverwriteSameNameSource(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	c := e.c
	if _, err := c.ScanCandidates(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	live := candByName(t, c, "live")
	if err := c.AddSource(live.Name, "https://other.example.org/sub.txt", "", 1, "user"); err != nil {
		t.Fatal(err)
	}
	name, err := c.AddCandidate(live.ID, "user")
	if err != nil || name != live.Name+"-2" {
		t.Fatalf("имя %q %v", name, err)
	}
	recs, _ := c.st.Sources()
	for _, r := range recs {
		if r.ID == live.Name && r.URL != "https://other.example.org/sub.txt" {
			t.Errorf("существующий источник затёрт: %+v", r)
		}
	}
}

func TestQuarantineStatsAccumulatePerDay(t *testing.T) {
	var cur, peak atomic.Int32
	probe := func(ctx context.Context, n store.NodeRec) prober.Result {
		v := cur.Add(1)
		for p := peak.Load(); v > p && !peak.CompareAndSwap(p, v); p = peak.Load() {
		}
		time.Sleep(2 * time.Millisecond)
		cur.Add(-1)
		return liveProbe(ctx, n)
	}
	e := newDiscoverEnv(t, probe, true, nil)
	c := e.c
	rep, err := c.ScanCandidates(context.Background(), "user", false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Probed || rep.Sampled != 40 || rep.Alive != 20 {
		t.Fatalf("итог карантина: %+v", rep)
	}
	live, dead := candByName(t, c, "live"), candByName(t, c, "dead")
	if live.Sampled != 20 || live.SampleOK != 20 || dead.Sampled != 20 || dead.SampleOK != 0 || live.SampleDays != 1 {
		t.Errorf("статистика: live %+v dead %+v", live, dead)
	}
	if peak.Load() > quarantineParallel {
		t.Errorf("параллелизм %d больше предела %d", peak.Load(), quarantineParallel)
	}
	// тот же день: выборка не берётся повторно
	if _, err := c.ScanCandidates(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	if v := candByName(t, c, "live"); v.Sampled != 20 || v.SampleDays != 1 {
		t.Errorf("повторная выборка в тот же день: %+v", v)
	}
	// следующий день: накопление
	e.setDay(17)
	if _, err := c.ScanCandidates(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	v := candByName(t, c, "live")
	if v.Sampled != 40 || v.SampleOK != 40 || v.SampleDays != 2 || v.DaysSeen != 2 {
		t.Errorf("накопление: %+v", v)
	}
	// сортировка по живости: живой выше мёртвого
	list, _ := c.Candidates()
	if len(list) != 2 || !strings.HasSuffix(list[0].Name, "/live") {
		t.Errorf("порядок: %+v", list)
	}
	// ни узлов, ни ссылок в базе
	if total, _, _ := c.st.CountNodes(); total != 0 {
		t.Errorf("узлы проверки попали в базу: %d", total)
	}
	if strings.Contains(fmt.Sprintf("%+v", list), "11111111-2222") {
		t.Error("ссылка узла в данных кандидата")
	}
}

func TestQuarantineSkippedWhenCannotProbe(t *testing.T) {
	var calls atomic.Int32
	probe := func(ctx context.Context, n store.NodeRec) prober.Result { calls.Add(1); return liveProbe(ctx, n) }
	e := newDiscoverEnv(t, probe, false, nil)
	rep, err := e.c.ScanCandidates(context.Background(), "user", false)
	if err != nil || rep.Probed || rep.Sampled != 0 || calls.Load() != 0 {
		t.Fatalf("%+v %v проб %d", rep, err, calls.Load())
	}
	if v := candByName(t, e.c, "live"); v.Links != 30 || v.Sampled != 0 {
		t.Errorf("только счёт ссылок: %+v", v)
	}
}

func TestScanCancelDuringQuarantine(t *testing.T) {
	started := make(chan struct{}, 64)
	var hang atomic.Bool
	hang.Store(true)
	probe := func(ctx context.Context, n store.NodeRec) prober.Result {
		if hang.Load() {
			started <- struct{}{}
			<-ctx.Done()
		}
		return prober.Result{Level: 2, Class: prober.ClassOther}
	}
	e := newDiscoverEnv(t, probe, true, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.c.ScanCandidates(ctx, "user", false); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ждали отмену, получили %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("проход не прервался по отмене")
	}
	for _, v := range func() []CandidateView { l, _ := e.c.Candidates(); return l }() {
		if v.Sampled != 0 {
			t.Errorf("отменённая выборка засчитана: %+v", v)
		}
	}
	// после отмены проход можно запустить снова (замок снят)
	hang.Store(false)
	if _, err := e.c.ScanCandidates(context.Background(), "user", false); errors.Is(err, ErrBusy) {
		t.Error("замок прохода не снят")
	}
}

func TestScanCancelBeforeNetwork(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.c.ScanCandidates(ctx, "user", false); !errors.Is(err, context.Canceled) {
		t.Errorf("получили %v", err)
	}
	if list, _ := e.c.Candidates(); len(list) != 0 {
		t.Errorf("отменённый проход записал кандидатов: %d", len(list))
	}
}

func TestScanBusyAndMetered(t *testing.T) {
	var metered atomic.Bool
	e := newDiscoverEnv(t, nil, false, func(c *Config) { c.Metered = metered.Load })
	e.c.discoverMu.Lock()
	if _, err := e.c.ScanCandidates(context.Background(), "user", false); !errors.Is(err, ErrBusy) {
		t.Errorf("занятость: %v", err)
	}
	e.c.discoverMu.Unlock()
	metered.Store(true)
	if _, err := e.c.ScanCandidates(context.Background(), "auto", false); !errors.Is(err, ErrMetered) {
		t.Errorf("платная сеть без согласия: %v", err)
	}
	if list, _ := e.c.Candidates(); len(list) != 0 {
		t.Error("платная сеть: что-то скачано без согласия")
	}
	if rep, err := e.c.ScanCandidates(context.Background(), "user", true); err != nil || rep.Found != 2 {
		t.Errorf("с согласием: %+v %v", rep, err)
	}
}

func TestDiscoverSeedsFromSources(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	recs, _ := e.c.st.Sources()
	seeds := e.c.discoverSeeds(recs, e.url)
	var hasKnown, hasDefault bool
	for _, s := range seeds {
		hasKnown = hasKnown || s == "o1/r1"
		hasDefault = hasDefault || s == "igareck/vpn-configs-for-russia"
	}
	if !hasKnown || !hasDefault {
		t.Errorf("зерно: %v", seeds)
	}
}

// Проход без единого прочитанного README (сети нет) не засчитывается: loop.discover.last не ставится, иначе поиск отложился бы на сутки.
func TestScanOfflineNotCounted(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	e.srv.Close() // сервер недоступен: ни один README не читается
	if _, err := e.c.ScanCandidates(context.Background(), "auto", false); !errors.Is(err, errDiscoverOffline) {
		t.Fatalf("ждали errDiscoverOffline, получили %v", err)
	}
	if _, ok, _ := e.c.st.Setting(loopLastKey("discover")); ok {
		t.Error("проход без сети засчитан как выполненный")
	}
	if d := e.c.firstDelay("discover", discoverSoon, e.c.cfg.DiscoverInterval); d != discoverSoon {
		t.Errorf("после неудачного прохода ждали %s, получили %s", discoverSoon, d)
	}
}

// Карантин проверяет сначала тех, у кого выборка самая давняя, а ни разу не бравшуюся — раньше всех: потолок времени не должен
// кормить каждый день одних и тех же.
func TestQuarantineOrderRotates(t *testing.T) {
	now := time.Now()
	found := []discover.Found{{URL: "big", Links: 900}, {URL: "mid", Links: 500}, {URL: "new", Links: 100}, {URL: "old", Links: 50}}
	have := map[string]store.CandidateRec{
		"big": {LastSample: now.Add(-time.Hour)},
		"mid": {LastSample: now.Add(-3 * time.Hour)},
		"old": {LastSample: now.Add(-48 * time.Hour)},
	}
	var got []string
	for _, f := range quarantineOrder(found, have) {
		got = append(got, f.URL)
	}
	if strings.Join(got, ",") != "new,old,mid,big" {
		t.Errorf("порядок %v", got)
	}
}

func TestDiscoverSeedsSkipDisabled(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	if err := e.c.st.SetSourceEnabled("known", false); err != nil {
		t.Fatal(err)
	}
	recs, _ := e.c.st.Sources()
	for _, s := range e.c.discoverSeeds(recs, e.url) {
		if s == "o1/r1" {
			t.Errorf("репозиторий выключенного источника попал в зерно")
		}
	}
	if known, _, _ := e.c.knownURLs(); !known[urlKey(e.url+"o1/r1/main/known.txt")] {
		t.Error("выключенный источник не должен предлагаться как кандидат")
	}
}
