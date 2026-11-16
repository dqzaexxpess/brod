package core

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"brod/monitor"
	"brod/netwatch"
	"brod/prober"
	"brod/store"
)

// healthState — состояние здоровья активного узла. Деградация только сообщается (событие, уведомление, альтернативы):
// автоматической смены узла в ручном режиме нет (P1).
type healthState struct {
	mu       sync.Mutex
	fails    int
	oks      int
	degraded bool
	since    time.Time
	hint     string    // последнее срабатывание пассивного детектора (для текста причины)
	altAt    time.Time // последний поиск альтернатив во время деградации (повтор не чаще altRetry)
	altBusy  bool      // поиск альтернатив уже идёт (повторы не накладываются)
	altEmpty bool      // последний поиск не дал ни одной живой альтернативы: следующий раньше (altRetryEmpty)
	everOK   bool      // с момента смены узла (или старта) была хоть одна успешная проверка
	resetAt  time.Time // момент смены узла/старта: в первую минуту, пока успехов не было, проверки идут чаще (healthWarm)
}

// healthWarm — сколько после смены узла проверки идут ускоренно, пока нет первого успеха: мёртвый узел (например, восстановленный
// при запуске) обнаруживается за секунды, а не за два обычных периода.
const healthWarm = time.Minute

// altRetry — период повторного поиска альтернатив, пока активный узел деградирует: первый поиск мог дать пустой список (все проверки шли
// через тот же сломанный путь) или ни одна альтернатива не поднялась; без повтора пользователь сидел бы на мёртвом узле без новых попыток.
const altRetry = 45 * time.Second

// altRetryEmpty — то же, если последний поиск ничего не нашёл: сразу после запуска ядра WARP ещё холодный и проверки через него проваливаются,
// через 10 секунд картина обычно другая, а ждать полных 45 с незачем.
const altRetryEmpty = 10 * time.Second

func altRetryAfter(lastEmpty bool) time.Duration {
	if lastEmpty {
		return altRetryEmpty
	}
	return altRetry
}

func detail(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// ProbeActive проверяет живой активный узел (по тому outbound'у, через который идёт трафик) и обновляет состояние здоровья.
func (c *Core) ProbeActive(ctx context.Context) (prober.Result, error) {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if id == "" {
		return prober.Result{}, fmt.Errorf("активного узла нет")
	}
	p, err := c.eng.Existing(OutActive)
	if err != nil {
		return prober.Result{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout)
	defer cancel()
	r := c.cfg.Liveness(cctx, p)
	if ctx.Err() != nil || r.Class == prober.ClassCanceled {
		return r, ctx.Err() // остановка демона — не провал узла
	}
	c.noteLiveness(id, r)
	return r, nil
}

// ProbeActiveQuick — быстрая проверка «выход поднялся» (два круга вместо пяти): для момента подключения и смены узла. Результат не
// учитывается в здоровье узла (деградация считается только по полной проверке ProbeActive).
func (c *Core) ProbeActiveQuick(ctx context.Context) (prober.Result, error) {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if id == "" {
		return prober.Result{}, fmt.Errorf("активного узла нет")
	}
	p, err := c.eng.Existing(OutActive)
	if err != nil {
		return prober.Result{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout)
	defer cancel()
	return c.cfg.Quick(cctx, p), nil
}

// noteLiveness учитывает результат активной проверки; решение о деградации принимается по K провалов подряд.
func (c *Core) noteLiveness(nodeID string, r prober.Result) {
	// результат относится к уже сменённому узлу — игнорируем. c.mu берётся до h.mu и не вкладывается: connectLocked держит
	// c.mu и зовёт resetHealth (h.mu), обратный порядок дал бы взаимную блокировку.
	c.mu.Lock()
	cur := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if cur != nodeID {
		return
	}
	if r.OK {
		c.recordActivePing(nodeID, r)
	}
	h := &c.health
	h.mu.Lock()
	var announce func()
	if r.OK {
		h.fails = 0
		h.everOK = true
		if h.degraded {
			h.oks++
			if h.oks >= 2 { // два успеха подряд: одиночное мигание не считается восстановлением
				h.degraded, h.oks = false, 0
				dur := c.cfg.Now().Sub(h.since).Round(time.Second)
				announce = func() {
					c.bus.Publish(store.Event{Kind: EvRecovered, Initiator: "auto", Node: nodeID, Detail: detail(map[string]any{"after": dur.String()})})
				}
			}
		}
	} else {
		h.oks = 0
		h.fails++
		if h.fails >= c.cfg.HealthFails && !h.degraded {
			h.degraded, h.since = true, c.cfg.Now()
			h.altAt = h.since
			reason := fmt.Sprintf("активная проверка: %s ×%d", r.Class, h.fails)
			if h.hint != "" {
				reason += " (" + h.hint + ")"
			}
			announce = func() { c.announceDegraded(nodeID, reason) }
		} else if h.degraded && c.cfg.Now().Sub(h.altAt) >= altRetryAfter(h.altEmpty) {
			h.altAt = c.cfg.Now()
			announce = func() { c.searchAlternatives(nodeID) }
		}
	}
	h.mu.Unlock()
	if announce != nil {
		announce()
	}
}

// onMonitor принимает срабатывание пассивного детектора. Само по себе оно деградацией не считается: у детектора есть
// ложные срабатывания (long-poll, удержанные ответы), поэтому запускается внеочередная подтверждающая проверка, а решение —
// за обычной логикой K провалов подряд (P2/P7: «внеочередная проверка + событие»).
func (c *Core) onMonitor(ev monitor.Event) {
	if ev.Scope == monitor.ScopeActive {
		c.health.mu.Lock()
		c.health.hint = fmt.Sprintf("пассивный детектор: %d соединений без ответа", ev.Stalled)
		c.health.mu.Unlock()
		c.confirm("active", func(ctx context.Context) (bool, bool) {
			r, err := c.ProbeActive(ctx)
			return r.OK, err == nil
		})
		return
	}
	c.onTargetStall(ev)
}

// confirm проводит до K подряд внеочередных проверок; остановка при первом успехе (ложная тревога) или ошибке запуска.
// Параллельные подтверждения одной области не копятся.
func (c *Core) confirm(scope string, probe func(ctx context.Context) (ok, ran bool)) {
	if c.baseCtx == nil {
		return
	}
	c.confirmMu.Lock()
	if c.confirming[scope] {
		c.confirmMu.Unlock()
		return
	}
	c.confirming[scope] = true
	c.confirmMu.Unlock()
	c.goLoop("confirm-"+scope, func() {
		defer func() { c.confirmMu.Lock(); delete(c.confirming, scope); c.confirmMu.Unlock() }()
		for i := 0; i < c.cfg.HealthFails && c.baseCtx.Err() == nil; i++ {
			if ok, ran := probe(c.baseCtx); ok || !ran {
				return
			}
		}
	})
}

// announceDegraded публикует деградацию и в фоне подбирает альтернативы, проверенные в текущей сети.
func (c *Core) announceDegraded(nodeID, reason string) {
	c.bus.Publish(store.Event{Kind: EvDegraded, Initiator: "auto", Node: nodeID, Detail: detail(map[string]any{"reason": reason})})
	c.searchAlternatives(nodeID)
}

// searchAlternatives в фоне подбирает альтернативы, проверенные в текущей сети, и (при degrade.action=auto) переключается на первую живую.
func (c *Core) searchAlternatives(nodeID string) {
	if c.cancel == nil {
		return
	}
	c.health.mu.Lock()
	if c.health.altBusy {
		c.health.mu.Unlock()
		return
	}
	c.health.altBusy = true
	c.health.mu.Unlock()
	c.goLoop("alternatives", func() {
		defer func() {
			c.health.mu.Lock()
			c.health.altBusy = false
			c.health.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(c.baseCtx, 60*time.Second)
		defer cancel()
		// быстрый путь: горячий резерв (см. standby.go) — запасные, проверенные недавно; достаточно убедиться, что они живы сейчас
		if alive := c.liveStandby(ctx, nodeID); len(alive) > 0 {
			ids := make([]string, len(alive))
			for i, a := range alive {
				ids[i] = a.Node.ID
			}
			c.saveFallbacks(alive)
			c.setAltEmpty(false)
			c.bus.Publish(store.Event{Kind: EvAlternatives, Initiator: "auto", Node: nodeID, Detail: detail(map[string]any{"alternatives": ids, "standby": true})})
			c.maybeAutoSwap(nodeID, ids)
			return
		}
		alts, err := c.Alternatives(ctx, c.cfg.AltCount)
		if err != nil && ctx.Err() == nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "альтернативы: " + err.Error()})
			return
		}
		ids := make([]string, len(alts))
		for i, a := range alts {
			ids[i] = a.Node.ID
		}
		c.setAltEmpty(len(ids) == 0)
		c.bus.Publish(store.Event{Kind: EvAlternatives, Initiator: "auto", Node: nodeID, Detail: detail(map[string]any{"alternatives": ids})})
		c.maybeAutoSwap(nodeID, ids)
	})
}

func (c *Core) setAltEmpty(v bool) {
	c.health.mu.Lock()
	c.health.altEmpty = v
	c.health.mu.Unlock()
}

// resetHealth начинает отсчёт заново (смена узла).
func (c *Core) resetHealth() {
	c.health.mu.Lock()
	c.health.fails, c.health.oks, c.health.degraded, c.health.hint, c.health.altEmpty = 0, 0, false, "", false
	c.health.everOK, c.health.resetAt = false, c.cfg.Now()
	c.health.mu.Unlock()
	c.mon.Reset(monitor.ScopeActive)
}

// warming: активный узел сменился меньше healthWarm назад и ещё ни разу не ответил.
func (c *Core) warming() bool {
	c.health.mu.Lock()
	defer c.health.mu.Unlock()
	return !c.health.everOK && !c.health.resetAt.IsZero() && c.cfg.Now().Sub(c.health.resetAt) < healthWarm
}

// Degraded сообщает, считается ли активный узел деградировавшим.
func (c *Core) Degraded() bool {
	c.health.mu.Lock()
	defer c.health.mu.Unlock()
	return c.health.degraded
}

// ---- альтернативы ----

// Alternatives подбирает до n узлов, проходящих проверку прямо сейчас в текущей сети: сначала недавно проверенные
// (с google-api выше), затем непроверенные. Активный узел исключается. Результат сохраняется как запасные активного выбора.
func (c *Core) Alternatives(ctx context.Context, n int) ([]Candidate, error) {
	now := c.cfg.Now()
	netID := c.netKey()
	hist, err := c.st.Histories(netID, now)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	active := c.selected[store.ScopeActive]
	c.mu.Unlock()
	fresh, err := c.freshNodes(hist, now, active)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(fresh, func(i, j int) bool { return hist[fresh[i].ID].Score(now) > hist[fresh[j].ID].Score(now) })
	out, err := c.verify(ctx, netID, fresh, n, 3*n)
	if err != nil || len(out) >= n {
		c.saveFallbacks(out)
		return out, err
	}
	// не хватило свежих — пробуем непроверенных (так бывает после смены сети)
	// при деградации активного узла «недавно проваленные» не пропускаем: пока путь был сломан, проверки проваливались у всех, и без Force повторный
	// поиск (см. altRetry) исключал бы ровно тех, кто теперь жив
	more, perr := c.pickCandidates(netID, CheckOptions{Limit: 6 * n, Force: c.Degraded()}, now)
	if perr == nil {
		tried := map[string]bool{}
		for _, nd := range fresh[:min(len(fresh), 3*n)] { // уже проверены первым проходом
			tried[nd.ID] = true
		}
		var rest []store.NodeRec
		for _, nd := range more {
			if !tried[nd.ID] {
				rest = append(rest, nd)
			}
		}
		extra, _ := c.verify(ctx, netID, rest, n-len(out), len(rest))
		out = append(out, extra...)
	}
	c.saveFallbacks(out)
	return out, ctx.Err()
}

// freshNodes возвращает видимые (отдаваемые источниками, не скрытые политикой P6) узлы с успехом L2 в сети не позже AltFresh назад,
// кроме skip. Идентификаторы берутся из истории, а не из полного списка узлов: на больших базах (сотни тысяч узлов) это разница между
// секундами и миллисекундами. Порядок тот же, что у Store.Nodes (свежие первыми, затем по ID): ниже его стабильно уточняют по рангу.
func (c *Core) freshNodes(hist map[string]store.History, now time.Time, skip string) ([]store.NodeRec, error) {
	var ids []string
	for id, h := range hist {
		if id != skip && !h.L2OK.IsZero() && now.Sub(h.L2OK) <= c.cfg.AltFresh {
			ids = append(ids, id)
		}
	}
	hide := c.hideInsecure()
	var out []store.NodeRec
	for i := 0; i < len(ids); i += 500 {
		rs, err := c.st.Nodes(store.NodeFilter{IDs: ids[i:min(i+500, len(ids))], OnlyPresent: true, HideInsecure: hide})
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// verify проверяет кандидатов живыми проверками партиями и возвращает первые want прошедших в порядке очереди.
func (c *Core) verify(ctx context.Context, netID string, cands []store.NodeRec, want, maxTry int) ([]Candidate, error) {
	if want <= 0 {
		return nil, nil
	}
	if len(cands) > maxTry {
		cands = cands[:maxTry]
	}
	type outcome struct {
		res []prober.Result
		at  time.Time
	}
	var out, slow []Candidate // slow: прошли L2, но медленнее slowCandidateMs; берутся только если быстрых не хватило
	batch := max(want*2, 4)
	for i := 0; i < len(cands) && len(out) < want && ctx.Err() == nil; i += batch {
		part := cands[i:min(i+batch, len(cands))]
		results := make([]outcome, len(part))
		var wg sync.WaitGroup
		for j, nd := range part {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[j] = outcome{c.checkOne(ctx, nd), c.cfg.Now()}
			}()
		}
		wg.Wait()
		var rows []store.Row
		for j, nd := range part {
			cand := Candidate{Node: nodeInfo(nd)}
			okL2, canceled := false, false
			for _, r := range results[j].res {
				if r.Class == prober.ClassCanceled {
					canceled = true
				}
				rows = append(rows, store.Row{Node: nd.ID, NodeClass: nd.Class, Net: netID, Target: r.Target, Level: r.Level, OK: r.OK, Class: string(r.Class),
					LatencyMs: r.LatencyMs, Bytes: r.Bytes, SpeedKBps: r.SpeedKBps, At: results[j].at})
				if r.Level == 2 && r.OK {
					okL2, cand.L2At, cand.LatencyMs = true, results[j].at, r.LatencyMs
				}
				if r.Level == 4 && r.Target == "google-api" && r.OK {
					cand.GoogleAPI = true
				}
			}
			if canceled {
				continue
			}
			if okL2 && cand.LatencyMs > slowCandidateMs {
				slow = append(slow, cand)
			} else if okL2 && len(out) < want {
				out = append(out, cand)
			}
		}
		if err := c.st.Record(filterCanceled(rows)); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "запись результатов: " + err.Error()})
		}
	}
	// быстрые идут первыми и по возрастанию задержки: автозамена берёт первый поднявшийся; узел с ответом около таймаута здоровья (5 с)
	// проходит одну проверку и отваливается на следующей
	sort.SliceStable(out, func(i, j int) bool { return out[i].LatencyMs < out[j].LatencyMs })
	if len(out) < want {
		sort.SliceStable(slow, func(i, j int) bool { return slow[i].LatencyMs < slow[j].LatencyMs })
		out = append(out, slow[:min(len(slow), want-len(out))]...)
	}
	return out, nil
}

// slowCandidateMs — задержка L2, выше которой узел считается медленным кандидатом (в замене и резерве предпочитаются более быстрые).
const slowCandidateMs = 2500

func filterCanceled(rows []store.Row) []store.Row {
	out := rows[:0]
	for _, r := range rows {
		if r.Class != string(prober.ClassCanceled) {
			out = append(out, r)
		}
	}
	return out
}

// saveFallbacks записывает найденные альтернативы как запасные активного выбора (выбор и автор не меняются).
func (c *Core) saveFallbacks(alts []Candidate) {
	if len(alts) == 0 {
		return // пустой поиск (например, через упавший WARP) не стирает прежний резерв: он может пригодиться, когда путь восстановится
	}
	sel, err := c.st.Selection(store.ScopeActive)
	if err != nil {
		return
	}
	sel.Fallbacks = sel.Fallbacks[:0:0]
	for _, a := range alts {
		sel.Fallbacks = append(sel.Fallbacks, a.Node.ID)
	}
	if err := c.st.SetSelection(sel); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "запасные не сохранены: " + err.Error()})
	}
}

// ---- области монитора ----

// scopeOf относит соединение к области наблюдения: активный узел либо цель.
func (c *Core) scopeOf(md adapter.InboundContext, out adapter.Outbound) string {
	if out == nil {
		return ""
	}
	switch out.Tag() {
	case OutActive:
		return monitor.ScopeActive
	case OutAppTUN:
		host := md.Destination.Fqdn
		if host == "" {
			host = md.Domain
		}
		if host != "" {
			if tag := c.eng.MatchDispatch(OutAppTUN, host); tag != "" {
				return c.scopeOfTargetTag(tag)
			}
		}
		return "" // обычный трафик напрямую — не про активный узел
	case OutApp:
		host := md.Destination.Fqdn
		if host == "" {
			host = md.Domain // TUN: домен определён по SNI/Host, назначение — IP
		}
		if host != "" {
			if tag := c.eng.MatchDispatch(OutApp, host); tag != "" {
				return c.scopeOfTargetTag(tag)
			}
		}
		return monitor.ScopeActive
	}
	return ""
}

// ---- смена сети ----

func netHash(id string) string {
	h := sha1.Sum([]byte(id))
	return hex.EncodeToString(h[:4]) // сам идентификатор содержит адрес шлюза — в журнал не пишем
}

// CheckNetwork сравнивает сеть с запомненной. При смене: событие, внеочередной круг проверок (результаты прежней сети
// для новой не годятся — они привязаны к NetID), проверка активного узла. Активный узел не меняется (P1).
func (c *Core) CheckNetwork() bool {
	key, raw := c.netKey(), c.cfg.NetID()
	c.netMu.Lock()
	prevKey, prevRaw := c.lastNet, c.lastRawNet
	c.lastNet, c.lastRawNet = key, raw
	// связь пропала и вернулась в ту же сотовую сеть (режим полёта, новая регистрация у оператора): состояние сети другое, а ключ истории прежний,
	// поэтому провалы, записанные до возврата, не должны держать узлы в очереди «повторять рано»
	reattach := isMobileNet(raw) && prevRaw == "offline" && c.lastOnlineRaw == raw
	if raw != "" && raw != "offline" {
		c.lastOnlineRaw = raw
	}
	c.netMu.Unlock()
	if reattach {
		c.reattachAt.Store(c.cfg.Now().UnixNano())
	}
	if prevKey == "" || prevKey == key {
		return false
	}
	if prevRaw == raw {
		// сменился только ключ истории (включили или выключили цепочку через WARP): сеть та же, события о смене сети нет,
		// но результаты прежнего режима для нового не годятся — проверки идут заново
		c.KickCheck()
		return false
	}
	c.bus.Publish(store.Event{Kind: EvNetChanged, Initiator: "auto", Detail: detail(map[string]any{"from": netHash(prevRaw), "to": netHash(raw)})})
	c.health.mu.Lock()
	c.health.fails, c.health.oks = 0, 0
	c.health.mu.Unlock()
	c.KickCheck()
	if c.baseCtx != nil {
		c.goLoop("net-probe", func() { _, _ = c.ProbeActive(c.baseCtx) })
	}
	return true
}

func (c *Core) healthLoop(ctx context.Context) {
	t := time.NewTicker(c.cfg.HealthInterval)
	defer t.Stop()
	warm := time.NewTicker(c.cfg.HealthWarmInterval)
	defer warm.Stop()
	net := time.NewTicker(c.cfg.NetInterval)
	defer net.Stop()
	// netlink сообщает о смене сети сразу; опрос остаётся запасным путём (другая ОС, ошибка подписки)
	var netEvents <-chan struct{}
	if !c.cfg.NoNetlink {
		if ch, err := netwatch.Watch(ctx); err == nil {
			netEvents = ch
			// события приходят сами; опрос остаётся страховкой на случай пропущенного события, поэтому в десять раз реже (30 с вместо 3 с)
			net.Reset(c.cfg.NetInterval * 10)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-netEvents:
			if !ok {
				netEvents = nil
				continue
			}
			if ctx.Err() != nil { // select выбирает случайный готовый случай: при остановке не начинаем обращение к базе
				return
			}
			c.CheckNetwork()
		case <-net.C:
			if ctx.Err() != nil {
				return
			}
			c.CheckNetwork()
		case <-t.C:
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			has := c.selected[store.ScopeActive] != ""
			c.mu.Unlock()
			if has {
				_, _ = c.ProbeActive(ctx)
			}
		case <-warm.C:
			if ctx.Err() != nil {
				return
			}
			// ускоренные проверки первой минуты после смены узла, пока не было ни одного успеха
			c.mu.Lock()
			has := c.selected[store.ScopeActive] != ""
			c.mu.Unlock()
			if has && c.warming() {
				_, _ = c.ProbeActive(ctx)
			}
		}
	}
}

// recordActivePing записывает задержку успешной проверки активного узла как результат L2 (не чаще раза в минуту): так «пинг» активного
// узла в окне свежий, а не от последнего круга проверок.
func (c *Core) recordActivePing(id string, r prober.Result) {
	if id == store.SelWarp || id == store.SelDirect || id == "" || r.LatencyMs <= 0 {
		return
	}
	now := c.cfg.Now()
	c.pingMu.Lock()
	if now.Sub(c.pingAt) < time.Minute {
		c.pingMu.Unlock()
		return
	}
	c.pingAt = now
	c.pingMu.Unlock()
	cls := ""
	if rec, err := c.st.Node(id); err == nil {
		cls = rec.Class
	}
	_ = c.st.Record([]store.Row{{Node: id, Net: c.netKey(), NodeClass: cls, Level: 2, OK: true, LatencyMs: r.LatencyMs, At: now}})
}

// neverOKGrace — льгота выбору пользователя для узла, который с момента старта ни разу не ответил (вместо UserSwapGrace).
var neverOKGrace = 10 * time.Second

// deferAutoSwap повторяет решение об автозамене, когда закончится льгота выбора пользователя. Без этого замена ждала бы следующего круга
// поиска (altRetry), хотя список живых альтернатив уже готов.
func (c *Core) deferAutoSwap(from string, alts []string, after time.Duration) {
	if c.baseCtx == nil {
		return
	}
	time.AfterFunc(after+50*time.Millisecond, func() {
		if c.baseCtx.Err() == nil {
			c.maybeAutoSwap(from, alts)
		}
	})
}

// maybeAutoSwap при настройке degrade.action=auto переводит активный выбор на первую проверенную альтернативу, которая поднялась.
// Условия: деградация всё ещё идёт, выбран тот же узел (пользователь не успел сменить сам), с прошлого автоперехода прошло не меньше
// минуты (без дребезга). Смена атомарная (switch меняет цель целиком, зависшие соединения старого узла рвутся), прямого пути в этот момент нет.
func (c *Core) maybeAutoSwap(from string, alts []string) {
	if len(alts) == 0 || c.DegradeAction() != "auto" {
		return
	}
	c.mu.Lock()
	cur := c.selected[store.ScopeActive]
	c.mu.Unlock()
	if cur != from || !c.Degraded() {
		return
	}
	now := c.cfg.Now()
	// выбор пользователя (или приложения) автозамена не перебивает сразу: короткая деградация сразу после включения (холодный узел, один
	// провал проверок) не повод уводить с узла, который человек выбрал сам. Заменяем, только когда деградация держится дольше UserSwapGrace;
	// повторный поиск альтернатив (altRetry) вернётся к этому решению.
	if grace := c.cfg.UserSwapGrace; grace > 0 {
		if sel, err := c.st.Selection(store.ScopeActive); err == nil && sel.By != "auto" {
			c.health.mu.Lock()
			since, everOK := c.health.since, c.health.everOK
			c.health.mu.Unlock()
			if !everOK && grace > neverOKGrace {
				grace = neverOKGrace // узел с момента старта ни разу не ответил (восстановленный мёртвый): ждать «холодного старта» нечего
			}
			if !since.IsZero() && now.Sub(since) < grace {
				c.deferAutoSwap(from, alts, grace-now.Sub(since))
				return
			}
		}
	}
	c.swapMu.Lock()
	if !c.swapAt.IsZero() && now.Sub(c.swapAt) < time.Minute {
		c.swapMu.Unlock()
		return
	}
	c.swapAt = now
	c.swapMu.Unlock()
	for _, id := range alts {
		if err := c.Connect(id, "auto"); err == nil {
			c.bus.Publish(store.Event{Kind: EvAutoSwap, Initiator: "auto", Node: id, Detail: detail(map[string]any{"from": from})})
			return
		}
	}
	c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "автопереключение: ни одна из проверенных альтернатив не поднялась"})
}
