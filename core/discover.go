package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"brod/aggregator"
	"brod/discover"
	"brod/store"
)

// Автопоиск источников: раз в сутки демон читает README известных репозиториев (зерно: репозитории
// уже заведённых источников, aggregator.Defaults() и README агрегатора), находит в них файлы подписок и считает ссылки и схемы. Найденное
// лежит ОТДЕЛЬНО (таблица source_candidates): в sources и nodes кандидаты не попадают, пока владелец сам не нажмёт «Добавить». «Мягкий
// карантин»: у каждого кандидата копится статистика (дни наблюдения, доля ответивших ссылок из случайных выборок), жёсткого срока ожидания
// нет. Автодобавления нет вообще. Уже заведённые источники и скрытые кандидаты заново не предлагаются.
const (
	discoverSoon  = 10 * time.Minute // первый проход после старта ядра, если суток с прошлого ещё нет (после первого обновления подписок)
	discoverRetry = 30 * time.Minute // повторная проверка условий (платная сеть), а не полный период
	discoverStale = 30 * 24 * time.Hour
)

// DiscoverReport — итог прохода автопоиска.
type DiscoverReport struct {
	Repos     int           `json:"repos"`   // репозиториев просмотрено
	Readmes   int           `json:"readmes"` // README прочитано
	Files     int           `json:"files"`   // файлов скачано
	Found     int           `json:"found"`   // файлов-кандидатов (с достаточным числом ссылок)
	New       int           `json:"new"`     // из них новых
	Sampled   int           `json:"sampled"` // ссылок проверено в карантине
	Alive     int           `json:"alive"`   // из них ответило
	Inherited int           `json:"-"`       // новых кандидатов, скрытых как похожие на скрытую группу
	Probed    bool          `json:"probed"`  // карантинная проверка выполнялась (VPN включён, не Android)
	Took      time.Duration `json:"-"`
}

// CandidateView — кандидат для интерфейса: адрес файла (публичный, не секрет) и статистика карантина. Ссылок узлов здесь нет.
type CandidateView struct {
	ID         string
	Name       string
	Repo       string
	URL        string
	Links      int
	Schemes    map[string]int
	FirstSeen  time.Time
	LastSeen   time.Time
	DaysSeen   int
	Sampled    int // проверено ссылок (накопительно)
	SampleOK   int // из них ответило
	SampleDays int
	LastSample time.Time
	Group      string // идентификатор группы похожих по содержимому кандидатов (зеркал)
	GroupSize  int    // число кандидатов в группе (включая этого)
}

// GroupView — группа кандидатов-зеркал: представитель (самый живой) и остальные члены.
type GroupView struct {
	ID             string
	Representative CandidateView
	Members        []CandidateView // без представителя
	Size           int
	TotalLinksMax  int // наибольшее число ссылок среди членов группы
}

// errDiscoverOffline — ни один README не прочитан: проход не засчитывается.
var errDiscoverOffline = errors.New("автопоиск источников: ни один README не прочитан (нет сети или GitHub недоступен)")

func urlKey(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

// knownURLs — адреса всех источников (и их зеркал): они не предлагаются как кандидаты.
func (c *Core) knownURLs() (map[string]bool, []store.SourceRec, error) {
	recs, err := c.st.Sources()
	if err != nil {
		return nil, nil, err
	}
	known := make(map[string]bool, 2*len(recs))
	for _, r := range recs {
		known[urlKey(r.URL)] = true
		if r.Mirror != "" {
			known[urlKey(r.Mirror)] = true
		}
	}
	return known, recs, nil
}

// discoverSeeds — репозитории, из README которых ищем: адреса заведённых источников и aggregator.Defaults().
func (c *Core) discoverSeeds(recs []store.SourceRec, base string) []string {
	var urls []string
	for _, r := range recs {
		if r.Enabled { // «включённые» источники; выключенные владельцем в зерно не идут (но кандидатами не предлагаются, см. knownURLs)
			urls = append(urls, r.URL)
		}
	}
	for _, s := range aggregator.Defaults() {
		urls = append(urls, s.URL)
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range urls {
		for _, b := range []string{base, discover.RawBase} { // источники по умолчанию всегда на настоящем GitHub, даже когда тест подменил базу
			if r, ok := discover.RepoOfURL(b, u); ok && !seen[strings.ToLower(r)] {
				seen[strings.ToLower(r)] = true
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

// discoverSalt — соль установки для отпечатков наборов ссылок: случайная, создаётся один раз и хранится в настройках (не секрет доступа,
// но без неё хэш отпечатка не подобрать). Потеряется — отпечатки перестанут сравниваться со старыми до следующего прохода (старые кандидаты
// просто окажутся в своих группах из одного).
func (c *Core) discoverSalt() ([]byte, error) {
	const key = "discover.salt"
	if v, ok, err := c.st.Setting(key); err != nil {
		return nil, err
	} else if ok {
		if b, err := hex.DecodeString(v); err == nil && len(b) >= 16 {
			return b, nil
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, c.st.SetSetting(key, hex.EncodeToString(b))
}

func (c *Core) discoverBase() string {
	if c.cfg.DiscoverBase != "" {
		return c.cfg.DiscoverBase
	}
	return discover.RawBase
}

// ScanCandidates делает внеочередной или плановый проход автопоиска. ErrBusy — проход уже идёт; ErrMetered — сеть платная, а согласия нет
// (allowMetered): README и файлы подписок — десятки мегабайт.
func (c *Core) ScanCandidates(ctx context.Context, initiator string, allowMetered bool) (DiscoverReport, error) {
	if !c.discoverMu.TryLock() {
		return DiscoverReport{}, ErrBusy
	}
	defer c.discoverMu.Unlock()
	var rep DiscoverReport
	if !allowMetered && c.meteredNow() {
		return rep, ErrMetered
	}
	start := c.cfg.Now()
	known, recs, err := c.knownURLs()
	if err != nil {
		return rep, err
	}
	have, err := c.st.Candidates()
	if err != nil {
		return rep, err
	}
	byURL := make(map[string]store.CandidateRec, len(have))
	for _, h := range have {
		byURL[h.URL] = h
	}
	base := c.discoverBase()
	salt, err := c.discoverSalt()
	if err != nil {
		return rep, err
	}
	opt := discover.Options{Base: base, Client: c.cfg.DiscoverClient, MaxRepos: c.cfg.DiscoverRepos, Salt: salt,
		Skip: func(u string) bool {
			h, ok := byURL[u]
			return known[urlKey(u)] || (ok && (h.Dismissed || h.Added))
		}}
	found, st, err := discover.Run(ctx, opt, c.discoverSeeds(recs, base))
	rep.Repos, rep.Readmes, rep.Files, rep.Found = st.Repos, st.Readmes, st.Files, len(found)
	if err != nil {
		return rep, err
	}
	if st.Repos > 0 && st.Readmes == 0 && c.cfg.DiscoverClient == nil {
		// напрямую GitHub не отвечает (в некоторых сетях raw.githubusercontent режется): тот же проход через активный узел, как у обновления
		// источников (`proxyClient`); без активного узла прежний исход
		c.mu.Lock()
		hasActive := c.selected[store.ScopeActive] != ""
		c.mu.Unlock()
		if hasActive && ctx.Err() == nil {
			pc := c.proxyClient(20 * time.Second)
			pc.CheckRedirect = discover.SameHostRedirect(base)
			opt.Client = pc
			found, st, err = discover.Run(ctx, opt, c.discoverSeeds(recs, base))
			rep.Repos, rep.Readmes, rep.Files, rep.Found = st.Repos, st.Readmes, st.Files, len(found)
			if err != nil {
				return rep, err
			}
		}
	}
	if st.Repos > 0 && st.Readmes == 0 {
		// сети ещё нет или GitHub недоступен: проход не засчитывается (иначе он отложил бы поиск на сутки), цикл повторит позже
		return rep, errDiscoverOffline
	}
	day := start.Format("2006-01-02")
	hidden := hiddenSketches(have)
	for _, f := range found {
		h, seen := byURL[f.URL]
		if !seen {
			rep.New++
		}
		rec := store.CandidateRec{URL: f.URL, Name: f.Name, Repo: f.Repo, Links: f.Links, Schemes: f.Schemes, Sketch: discover.EncodeSketch(f.Sketch)}
		if err := c.st.UpsertCandidate(rec, day, start); err != nil {
			return rep, err
		}
		// новый член (или впервые получивший отпечаток после миграции), похожий на скрытую группу, наследует «скрыто»
		if (!seen || len(h.Sketch) == 0) && !h.Dismissed && !h.Added && similarToAny(f.Sketch, hidden) {
			if err := c.st.DismissCandidate(f.URL); err != nil {
				return rep, err
			}
			if cur, ok := byURL[f.URL]; ok {
				cur.Dismissed = true
				byURL[f.URL] = cur
			}
			rep.Inherited++
		}
	}
	if st.Readmes > 0 { // сеть не работала — ничего не «устарело»
		if _, err := c.st.PruneCandidates(start.Add(-discoverStale)); err != nil {
			log.Printf("автопоиск источников: чистка: %v", err)
		}
	}
	rep.Sampled, rep.Alive, rep.Probed = c.quarantine(ctx, found, byURL, start)
	rep.Took = c.cfg.Now().Sub(start)
	if ctx.Err() != nil {
		return rep, ctx.Err()
	}
	c.markLoopRun("discover")
	c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: fmt.Sprintf(
		"автопоиск источников: репозиториев %d, файлов %d, кандидатов %d (новых %d), проверено ссылок %d, ответило %d",
		rep.Repos, rep.Files, rep.Found, rep.New, rep.Sampled, rep.Alive)})
	return rep, nil
}

// discoverLoop — суточный проход. Время прошлого прохода помним (loop.discover.last): перезапуск ядра не запускает его заново.
func (c *Core) discoverLoop(ctx context.Context) {
	t := time.NewTimer(c.firstDelay("discover", discoverSoon, c.cfg.DiscoverInterval))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !c.waitActive(ctx) {
			return
		}
		next := c.cfg.DiscoverInterval
		c.waitForDefaultRoute(ctx, 2*time.Second, 2*time.Minute)
		_, err := c.ScanCandidates(ctx, "auto", false)
		switch {
		case ctx.Err() != nil:
			return
		case err == ErrMetered || err == ErrBusy || errors.Is(err, errDiscoverOffline):
			next = discoverRetry
		case err != nil:
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "автопоиск источников: " + err.Error()})
			next = discoverRetry * 2
		}
		t.Reset(next)
	}
}

// hiddenSketches — отпечатки скрытых кандидатов (не добавленных): по ним новые похожие кандидаты скрываются сразу.
func hiddenSketches(recs []store.CandidateRec) [][]uint64 {
	var out [][]uint64
	for _, r := range recs {
		if r.Dismissed && !r.Added {
			if sk := discover.DecodeSketch(r.Sketch); len(sk) > 0 {
				out = append(out, sk)
			}
		}
	}
	return out
}

func similarToAny(sk []uint64, set [][]uint64) bool {
	for _, h := range set {
		if discover.Similarity(sk, h) >= discover.SimilarThreshold {
			return true
		}
	}
	return false
}

// candScore — сглаженная доля ответивших ссылок.
func candScore(v CandidateView) float64 {
	return (float64(v.SampleOK) + 0.5) / (float64(v.Sampled) + 2)
}

// candBefore — порядок «лучше»: проверенные раньше непроверенных, затем сглаженная живость, затем больше ссылок. Дальше при равенстве
// (для выбора представителя группы) — короче имя, потом по имени и идентификатору.
func candBefore(a, b CandidateView) bool {
	if (a.Sampled > 0) != (b.Sampled > 0) {
		return a.Sampled > 0
	}
	if a.Sampled > 0 && candScore(a) != candScore(b) {
		return candScore(a) > candScore(b)
	}
	if a.Links != b.Links {
		return a.Links > b.Links
	}
	if len(a.Name) != len(b.Name) {
		return len(a.Name) < len(b.Name)
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

// visibleCandidates — предлагаемые кандидаты (без скрытых, добавленных и уже заведённых источников) с их отпечатками, по порядку candBefore.
func (c *Core) visibleCandidates() ([]CandidateView, [][]uint64, error) {
	known, _, err := c.knownURLs()
	if err != nil {
		return nil, nil, err
	}
	recs, err := c.st.Candidates()
	if err != nil {
		return nil, nil, err
	}
	type item struct {
		v  CandidateView
		sk []uint64
	}
	var items []item
	for _, r := range recs {
		if r.Dismissed || r.Added || known[urlKey(r.URL)] {
			continue
		}
		items = append(items, item{CandidateView{ID: store.CandidateID(r.URL), Name: r.Name, Repo: r.Repo, URL: r.URL, Links: r.Links, Schemes: r.Schemes,
			FirstSeen: r.FirstSeen, LastSeen: r.LastSeen, DaysSeen: r.DaysSeen, Sampled: r.Sampled, SampleOK: r.SampleOK, SampleDays: r.SampleDays,
			LastSample: r.LastSample}, discover.DecodeSketch(r.Sketch)})
	}
	sort.SliceStable(items, func(i, j int) bool { return candBefore(items[i].v, items[j].v) })
	views := make([]CandidateView, len(items))
	sks := make([][]uint64, len(items))
	for i, it := range items {
		views[i], sks[i] = it.v, it.sk
	}
	return views, sks, nil
}

// groupCandidates разбивает видимых кандидатов на группы по содержимому (связные компоненты оценки Жаккара >= discover.SimilarThreshold).
// Группа: идентификатор — хэш наименьшего идентификатора члена; представитель — первый по candBefore (живость, ссылки, короткое имя);
// группы идут по порядку представителей. Кандидаты без отпечатка — группы из одного.
func groupCandidates(views []CandidateView, sks [][]uint64) []GroupView {
	comp := discover.Group(sks, discover.SimilarThreshold)
	byComp := map[int][]int{}
	var order []int
	for i, k := range comp {
		if _, ok := byComp[k]; !ok {
			order = append(order, k)
		}
		byComp[k] = append(byComp[k], i)
	}
	out := make([]GroupView, 0, len(order))
	for _, k := range order {
		idx := byComp[k] // views отсортированы по candBefore: первый — представитель
		minID := views[idx[0]].ID
		maxLinks := 0
		for _, i := range idx {
			if views[i].ID < minID {
				minID = views[i].ID
			}
			if views[i].Links > maxLinks {
				maxLinks = views[i].Links
			}
		}
		g := GroupView{ID: store.CandidateID(minID), Representative: views[idx[0]], Size: len(idx), TotalLinksMax: maxLinks}
		for _, i := range idx[1:] {
			g.Members = append(g.Members, views[i])
		}
		out = append(out, g)
	}
	return out
}

// CandidateGroups — группы кандидатов-зеркал для интерфейса, сначала самые живые.
func (c *Core) CandidateGroups() ([]GroupView, error) {
	views, sks, err := c.visibleCandidates()
	if err != nil {
		return nil, err
	}
	groups := groupCandidates(views, sks)
	for i := range groups {
		g := &groups[i]
		g.Representative.Group, g.Representative.GroupSize = g.ID, g.Size
		for j := range g.Members {
			g.Members[j].Group, g.Members[j].GroupSize = g.ID, g.Size
		}
	}
	return groups, nil
}

// Candidates — кандидаты для интерфейса: без скрытых, добавленных и уже заведённых источников; сначала самые «живые» (по сглаженной доле
// ответивших ссылок), затем по размеру. Кандидаты без проверенных выборок — после проверенных. У каждого указана группа похожих.
func (c *Core) Candidates() ([]CandidateView, error) {
	views, sks, err := c.visibleCandidates()
	if err != nil {
		return nil, err
	}
	groups := groupCandidates(views, sks)
	gOf := make(map[string]GroupView, len(views))
	for _, g := range groups {
		gOf[g.Representative.ID] = g
		for _, m := range g.Members {
			gOf[m.ID] = g
		}
	}
	for i := range views {
		g := gOf[views[i].ID]
		views[i].Group, views[i].GroupSize = g.ID, g.Size
	}
	return views, nil
}

// DismissGroup скрывает всех членов группы (заново не предлагаются, похожие новые кандидаты тоже). Возвращает число скрытых.
func (c *Core) DismissGroup(id, initiator string) (int, error) {
	groups, err := c.CandidateGroups()
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		if g.ID != id {
			continue
		}
		n := 0
		for _, m := range append([]CandidateView{g.Representative}, g.Members...) {
			if err := c.st.DismissCandidate(m.URL); err != nil && !errors.Is(err, store.ErrNotFound) {
				return n, err
			}
			n++
		}
		c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: fmt.Sprintf("группа кандидатов скрыта: %s (%d)", g.Representative.Name, n)})
		return n, nil
	}
	return 0, store.ErrNotFound
}

func (c *Core) candidateByID(id string) (store.CandidateRec, error) {
	recs, err := c.st.Candidates()
	if err != nil {
		return store.CandidateRec{}, err
	}
	for _, r := range recs {
		if store.CandidateID(r.URL) == id {
			return r, nil
		}
	}
	return store.CandidateRec{}, store.ErrNotFound
}

// AddCandidate заводит источник из кандидата (только по команде владельца) и помечает кандидата добавленным. Возвращает имя источника.
// Имя не должно затирать существующий источник: при совпадении добавляется числовой суффикс.
func (c *Core) AddCandidate(id, initiator string) (string, error) {
	r, err := c.candidateByID(id)
	if err != nil {
		return "", err
	}
	if r.Added {
		return "", fmt.Errorf("%w: кандидат уже добавлен", ErrInvalid)
	}
	recs, err := c.st.Sources()
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(recs))
	for _, s := range recs {
		taken[s.ID] = true
		if urlKey(s.URL) == urlKey(r.URL) {
			return "", fmt.Errorf("%w: источник с таким адресом уже есть (%s)", ErrInvalid, s.ID)
		}
	}
	name := r.Name
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", r.Name, i)
	}
	prio := 1
	if r.Links >= 3000 {
		prio = 2 // большие дампы проверяются после небольших курируемых списков (как у scripts/discover-sources.py)
	}
	if err := c.AddSource(name, r.URL, "", prio, initiator); err != nil {
		return "", err
	}
	if err := c.st.MarkCandidateAdded(r.URL); err != nil {
		return name, err
	}
	return name, nil
}

// DismissCandidate скрывает кандидата: заново он не предлагается.
func (c *Core) DismissCandidate(id, initiator string) error {
	r, err := c.candidateByID(id)
	if err != nil {
		return err
	}
	if err := c.st.DismissCandidate(r.URL); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: "кандидат скрыт: " + r.Name})
	return nil
}
