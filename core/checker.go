package core

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"brod/engine"
	"brod/mobile"
	"brod/prober"
	"brod/store"
)

// CheckFunc проверяет один узел через его пробный outbound и возвращает результаты уровней.
type CheckFunc func(ctx context.Context, node store.NodeRec, p *engine.Probe) []prober.Result

// defaultCheck: рукопожатие и HTTPS через узел (L2), при успехе — доступность API Google (цель google-api, P7). Тяжёлые проверки целей
// (YouTube, Telegram, пользовательские) делаются только для кандидатов этих целей. Запрос к API Google идёт ПОСЛЕ L2, не одновременно:
// параллельный вариант давал −27% времени круга при включённом чужом туннеле, но в «чистой» сети (без Throne) не проверялся и мог
// перегружать цепочку через WARP, поэтому выбран прежний, проверенный порядок.
func defaultCheck(ctx context.Context, _ store.NodeRec, p *engine.Probe) []prober.Result {
	l2 := prober.L2(ctx, p)
	res := []prober.Result{l2}
	if l2.OK {
		res = append(res, prober.L4GoogleAPI(ctx, p))
	}
	return res
}

// CheckReport — итог круга проверок.
type CheckReport struct {
	NetID    string
	Picked   int // узлов отобрано
	Checked  int // проверено до конца
	OK       int // прошли L2
	GoogleOK int // из них с google-api
	Canceled int
	Took     time.Duration
	Fails    map[string]int // отказы L2 по классам (timeout, tls/handshake, connect…): объясняет «живых 0»
}

// CheckOptions — параметры ручного запуска.
type CheckOptions struct {
	Limit int      // сколько узлов проверить (0 = CheckBatch)
	Force bool     // перепроверять и свежие/недавно проваленные
	IDs   []string // проверить ровно эти узлы в заданном порядке (политика отбора, TTL и скрытие P6 не применяются): для сравнения режимов на одном наборе
}

// pickCandidates отбирает узлы для проверки: сначала избранные и запасные, дальше по истории и априорной доле успеха класса.
// Активный узел проверяется отдельно (по живому outbound'у), скрытые политикой P6 узлы не проверяются.
func (c *Core) pickCandidates(netID string, opt CheckOptions, now time.Time) ([]store.NodeRec, error) {
	if len(opt.IDs) > 0 {
		return c.nodesByIDs(opt.IDs)
	}
	hist, err := c.st.Histories(netID, now)
	if err != nil {
		return nil, err
	}
	priors, err := c.st.ClassPriorsCached(netID, now, priorsTTL)
	if err != nil {
		return nil, err
	}
	// отбор идёт по лёгким записям из памяти, а не по всей таблице узлов; полные записи читаются только для выбранных
	lites, err := c.st.PresentLites()
	if err != nil {
		return nil, err
	}
	hide := c.hideInsecure()
	c.mu.Lock()
	active := c.selected[store.ScopeActive]
	c.mu.Unlock()
	boost := map[string]bool{}
	if sels, err := c.st.Selections(); err == nil {
		for _, s := range sels {
			for _, f := range s.Fallbacks {
				boost[f] = true
			}
		}
	}
	limit := opt.Limit
	if limit <= 0 {
		limit = c.cfg.CheckBatch
	}
	var wl *mobile.Lists // белые списки операторов: только в мобильной сети
	if isMobileNet(c.cfg.NetID()) {
		wl, _ = mobile.Embedded()
	}
	reattach := c.reattachAt.Load()
	top := newTopK(limit)
	for i := range lites {
		n := &lites[i]
		if n.ID == active {
			continue
		}
		flagged := n.Favorite || n.Pinned
		if (n.NoSecurity || (hide && n.Insecure)) && !flagged { // политика P6, как у Store.Nodes
			continue
		}
		h := hist[n.ID]
		if !opt.Force {
			if !h.L2OK.IsZero() && now.Sub(h.L2OK) < c.cfg.CheckTTL {
				continue // свежий успех
			}
			if !h.LastFail.IsZero() && h.L2OK.Before(h.LastFail) && now.Sub(h.LastFail) < c.cfg.RetryAfter && h.LastFail.UnixNano() > reattach {
				continue // недавно провалился: повторять рано, иначе одни и те же мёртвые узлы забивают каждый круг
			}
		}
		pr, ok := priors[n.Class]
		if !ok {
			pr = 0.05
		}
		sc := float64(h.Score(now)) + pr*100
		if flagged {
			sc += 2000
		}
		if boost[n.ID] {
			sc += 1500
		}
		if mobileListed(wl, n.IP4) {
			sc += mobileBoost
		}
		top.offer(n, sc)
	}
	picked := top.sorted()
	ids := make([]string, len(picked))
	for i, p := range picked {
		ids[i] = p.n.ID
	}
	return c.nodesByIDs(ids)
}

// priorsTTL — сколько переиспользуются доли успеха по классам при отборе кандидатов.
const priorsTTL = time.Minute

// topK держит k лучших кандидатов: больше балл, при равенстве свежее (last_seen), затем меньший ID — тот же порядок, что давала
// стабильная сортировка списка из Store.Nodes (ORDER BY last_seen DESC, id).
type scoredLite struct {
	n     *store.NodeLite
	score float64
}

func lbetter(a, b scoredLite) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.n.LastSeen != b.n.LastSeen {
		return a.n.LastSeen > b.n.LastSeen
	}
	return a.n.ID < b.n.ID
}

type topK struct {
	k int
	h []scoredLite // куча: корень — худший из принятых
}

func newTopK(k int) *topK { return &topK{k: k} }

func (t *topK) Len() int           { return len(t.h) }
func (t *topK) Less(i, j int) bool { return lbetter(t.h[j], t.h[i]) }
func (t *topK) Swap(i, j int)      { t.h[i], t.h[j] = t.h[j], t.h[i] }
func (t *topK) Push(x any)         { t.h = append(t.h, x.(scoredLite)) }
func (t *topK) Pop() any {
	x := t.h[len(t.h)-1]
	t.h = t.h[:len(t.h)-1]
	return x
}

func (t *topK) offer(n *store.NodeLite, score float64) {
	if t.k <= 0 {
		return
	}
	s := scoredLite{n, score}
	if len(t.h) < t.k {
		heap.Push(t, s)
		return
	}
	if lbetter(s, t.h[0]) {
		t.h[0] = s
		heap.Fix(t, 0)
	}
}

func (t *topK) sorted() []scoredLite {
	out := append([]scoredLite(nil), t.h...)
	sort.Slice(out, func(i, j int) bool { return lbetter(out[i], out[j]) })
	return out
}

// hideInsecure — политика P6: скрывать ли узлы с allowInsecure (настройка security.hide_insecure).
func (c *Core) hideInsecure() bool {
	v, ok, _ := c.st.Setting("security.hide_insecure")
	return ok && v == "1"
}

// CheckNow запускает круг проверок. Параллельность ограничена CheckParallel; результаты пишутся в базу пачками.
// Одновременно идёт один круг (повторный вызов возвращает ErrBusy).
func (c *Core) CheckNow(ctx context.Context, opt CheckOptions, initiator string) (CheckReport, error) {
	if !c.checkMu.TryLock() {
		return CheckReport{}, ErrBusy
	}
	defer c.checkMu.Unlock()
	c.nodeCheckActive.Store(true)
	defer c.nodeCheckActive.Store(false)
	start := c.cfg.Now()
	rep := CheckReport{NetID: c.netKey()}
	cands, err := c.pickCandidates(rep.NetID, opt, start)
	if err != nil {
		return rep, err
	}
	rep.Picked = len(cands)
	c.act.update(func(a *Activity) { a.Checking, a.CheckPicked, a.CheckDone, a.CheckOK = true, len(cands), 0, 0 })
	defer c.act.update(func(a *Activity) { a.Checking = false })

	var (
		repMu sync.Mutex
		rowMu sync.Mutex
		rows  []store.Row
	)
	flush := func(force bool) {
		rowMu.Lock()
		defer rowMu.Unlock()
		if len(rows) == 0 || (!force && len(rows) < 24) { // небольшими порциями: результаты видны в списке по ходу круга
			return
		}
		if err := c.st.Record(rows); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "запись результатов: " + err.Error()})
		}
		rows = rows[:0]
	}
	sem := make(chan struct{}, c.checkParallel())
	var wg sync.WaitGroup
	for _, n := range cands {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(n store.NodeRec) {
			defer wg.Done()
			defer func() { <-sem }()
			results := c.checkOne(ctx, n)
			at := c.cfg.Now()
			repMu.Lock()
			canceled := false
			for _, r := range results {
				switch {
				case r.Class == prober.ClassCanceled:
					canceled = true
				case r.Level == 2 && r.OK:
					rep.OK++
				case r.Level == 2 && !r.OK:
					if rep.Fails == nil {
						rep.Fails = map[string]int{}
					}
					rep.Fails[string(r.Class)]++
				case r.Level == 4 && r.Target == "google-api" && r.OK:
					rep.GoogleOK++
				}
			}
			if canceled {
				rep.Canceled++
			} else {
				rep.Checked++
			}
			repMu.Unlock()
			if !canceled {
				ok := false
				for _, r := range results {
					if r.Level == 2 && r.OK {
						ok = true
					}
				}
				c.act.update(func(a *Activity) {
					a.CheckDone++
					if ok {
						a.CheckOK++
					}
				})
			}
			if canceled {
				return // прерванная нами проверка результатом не считается
			}
			rowMu.Lock()
			for _, r := range results {
				rows = append(rows, store.Row{Node: n.ID, NodeClass: n.Class, Net: rep.NetID, Target: r.Target, Level: r.Level, OK: r.OK,
					Class: string(r.Class), LatencyMs: r.LatencyMs, Bytes: r.Bytes, SpeedKBps: r.SpeedKBps, At: at})
			}
			rowMu.Unlock()
			flush(false)
		}(n)
	}
	wg.Wait()
	flush(true)
	rep.Took = c.cfg.Now().Sub(start)
	c.bus.Publish(store.Event{Kind: EvProbe, Initiator: initiator,
		Detail: fmt.Sprintf("проверено %d из %d, L2 ок %d, google-api ок %d, за %s%s", rep.Checked, rep.Picked, rep.OK, rep.GoogleOK, rep.Took.Round(time.Millisecond), failsText(rep.Fails))})
	return rep, ctx.Err()
}

// checkOne создаёт пробный outbound узла (тот же код, что у боевого — паритет конфигов, P2), проверяет и освобождает.
func (c *Core) checkOne(ctx context.Context, n store.NodeRec) []prober.Result {
	full, err := c.st.Node(n.ID)
	if err != nil {
		return []prober.Result{{Level: 2, Class: prober.ClassOther, Detail: "узел исчез из базы"}}
	}
	opts, err := c.probeOpts(full)
	if err != nil {
		return []prober.Result{{Level: 2, Class: prober.ClassOther, Detail: "опции узла: " + err.Error()}}
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		return []prober.Result{{Level: 2, Class: prober.ClassOther, Detail: "не создаётся: " + err.Error()}}
	}
	defer p.Close()
	cctx, cancel := context.WithTimeout(ctx, c.cfg.CheckTimeout)
	defer cancel()
	res := c.cfg.Check(cctx, full, p)
	if ctx.Err() != nil { // остановка демона: то, что не успело, не считается провалом
		for i := range res {
			if !res[i].OK {
				res[i].Class = prober.ClassCanceled
			}
		}
	}
	return res
}

// Candidate — узел, проверенный в текущей сети.
type Candidate struct {
	Node      NodeInfo
	L2At      time.Time
	GoogleAPI bool
	LatencyMs int64
}

// nodesByIDs возвращает записи узлов в порядке ids (несуществующие пропускаются).
func (c *Core) nodesByIDs(ids []string) ([]store.NodeRec, error) {
	by := make(map[string]store.NodeRec, len(ids))
	for i := 0; i < len(ids); i += 500 {
		rs, err := c.st.Nodes(store.NodeFilter{IDs: ids[i:min(i+500, len(ids))], IncludeNoSecurity: true})
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			by[r.ID] = r
		}
	}
	out := make([]store.NodeRec, 0, len(ids))
	for _, id := range ids {
		if r, ok := by[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// failsText — «; отказы: timeout 120, tls/handshake 30» (по убыванию, не больше четырёх классов); пусто, если отказов нет.
func failsText(f map[string]int) string {
	if len(f) == 0 {
		return ""
	}
	type kv struct {
		k string
		n int
	}
	var l []kv
	for k, n := range f {
		if k == "" {
			k = "другое"
		}
		l = append(l, kv{k, n})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n || (l[i].n == l[j].n && l[i].k < l[j].k) })
	var parts []string
	for i, e := range l {
		if i == 4 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", e.k, e.n))
	}
	return "; отказы: " + strings.Join(parts, ", ")
}
