package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"brod/engine"
	"brod/ext/dispatch"
	"brod/monitor"
	"brod/prober"
	"brod/store"
)

// Цели (P7): набор доменов, своя проверка, закреплённый узел и запасные, политика переключения.
// Цель маршрутизируется отдельно от активного узла, только пока у неё есть выбранный узел: цель без узла не ломает
// трафик (он идёт через active), а вернуть её «в строй» можно выбором узла.

// TargetCheckFunc проверяет цель через живой (или пробный) outbound p выбора node (NodeID/«direct»).
// probeURL — свежая пробная ссылка владельца ("" если нет).
type TargetCheckFunc func(ctx context.Context, t store.Target, node, probeURL string, p *engine.Probe) prober.Result

// Встроенные цели. У google-api нет доменных правил: он нужен как проверка и значок узла (работает напрямую, P7).
func builtinTargets() []store.Target {
	return []store.Target{
		{ID: "youtube", Owner: "builtin", Title: "YouTube",
			Rules: store.TargetRules{DomainSuffix: []string{"youtube.com", "googlevideo.com", "ytimg.com", "ggpht.com", "youtu.be", "youtube-nocookie.com"}},
			Check: store.TargetCheck{Kind: "youtube"}, Policy: store.PolicyStickyManual, FailoverK: 3, AllowDirect: true, AllowWarp: true},
		{ID: "google-api", Owner: "builtin", Title: "Google API",
			Check: store.TargetCheck{Kind: "google-api"}, Policy: store.PolicyStickyManual, FailoverK: 3, AllowDirect: true, AllowWarp: true},
		{ID: "telegram", Owner: "builtin", Title: "Telegram",
			Rules: store.TargetRules{DomainSuffix: []string{"telegram.org", "t.me", "telegram.me", "telesco.pe", "tdesktop.com"}},
			Check: store.TargetCheck{Kind: "telegram"}, Policy: store.PolicyStickyManual, FailoverK: 3, AllowDirect: true, AllowWarp: true},
	}
}

// targetState — оперативное состояние цели (в памяти).
type targetState struct {
	lastProbe   time.Time // когда цель проверялась в последний раз (для периодов по виду проверки)
	fails, oks  int
	degraded    bool
	reported    bool // «нет рабочих кандидатов» уже сообщено в этом эпизоде
	last        prober.Result
	lastAt      time.Time
	probeURL    string
	probeURLExp time.Time
}

// TargetStatus — цель с её состоянием для API.
type TargetStatus struct {
	store.Target
	Node        string // выбранный узел: NodeID, "direct", "warp" или "" (не выбран)
	NodeInfo    *NodeInfo
	Fallbacks   []string
	SetBy       string
	Last        prober.Result
	LastAt      time.Time
	Degraded    bool
	Fails       int
	HasProbeURL bool
}

// Типизированные ошибки для API (коды ответа).
var (
	ErrForbidden    = errors.New("нет прав")
	ErrConflict     = errors.New("работает другой VPN") // включение VPN отклонено: в системе уже есть чужой туннель
	ErrInvalid      = errors.New("недопустимые данные")
	ErrNoCandidates = errors.New("нет подходящих кандидатов")
)

var targetIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// SwitchTag — тег switch'а цели в движке.
func SwitchTag(id string) string { return "t-" + id }

// checkExternalURL отклоняет для стороннего приложения адреса, по которым демон дошёл бы до внутренних служб машины или сети:
// localhost и *.localhost, IP-литералы loopback, частных (RFC 1918, ULA), link-local, CGNAT, неуказанные и многоадресные.
// Имя, которое при разрешении даёт внутренний адрес, так не поймать (нужна проверка при соединении) — это известное ограничение.
func checkExternalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("%w: некорректный адрес %q", ErrInvalid, raw)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	internal := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		internal = ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
			ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
	}
	if internal {
		return fmt.Errorf("%w: приложению нельзя проверять внутренний адрес %q", ErrInvalid, host)
	}
	return nil
}

func validateTarget(t *store.Target) error {
	if !targetIDRe.MatchString(t.ID) {
		return fmt.Errorf("%w: идентификатор цели %q (a-z, 0-9, . _ -, до 32 символов)", ErrInvalid, t.ID)
	}
	if t.Policy == "" {
		t.Policy = store.PolicyStickyManual
	}
	if t.Policy != store.PolicyStickyManual && t.Policy != store.PolicyAutoFailover {
		return fmt.Errorf("%w: неизвестная политика %q", ErrInvalid, t.Policy)
	}
	if t.FailoverK <= 0 {
		t.FailoverK = 3
	}
	switch t.Check.Kind {
	case "youtube", "google-api", "telegram":
	case "http":
		if !strings.HasPrefix(t.Check.URL, "http://") && !strings.HasPrefix(t.Check.URL, "https://") {
			return fmt.Errorf("%w: для проверки http нужен URL с http:// или https://", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: неизвестный вид проверки %q", ErrInvalid, t.Check.Kind)
	}
	if t.Check.MinBytes < 0 || t.Check.MinKBps < 0 {
		return fmt.Errorf("%w: min_bytes и min_kbps не могут быть отрицательными", ErrInvalid)
	}
	if t.Check.MinKBps > 0 && (t.Check.Kind != "http" || t.Check.MinBytes == 0) {
		return fmt.Errorf("%w: min_kbps проверяется только Range-загрузкой: нужны kind=http и min_bytes", ErrInvalid)
	}
	n := 0
	for _, list := range [][]string{t.Rules.Domain, t.Rules.DomainSuffix, t.Rules.DomainKeyword} {
		for _, s := range list {
			if s == "" || strings.ContainsAny(s, " \t/\\:") {
				return fmt.Errorf("%w: правило %q", ErrInvalid, s)
			}
			n++
		}
	}
	if n > 200 {
		return fmt.Errorf("%w: слишком много правил (%d, не больше 200)", ErrInvalid, n)
	}
	return nil
}

// authorize: владелец цели или user. Встроенные меняет только user.
func authorize(t store.Target, caller string) error {
	if caller == "user" {
		return nil
	}
	if t.Owner == "builtin" || t.Owner != caller {
		return fmt.Errorf("%w: цель %q принадлежит %s, а не %s", ErrForbidden, t.ID, t.Owner, caller)
	}
	return nil
}

// seedTargets создаёт встроенные цели, которых ещё нет (изменённые пользователем не перезаписываются).
func (c *Core) seedTargets() error {
	existing, err := c.st.Targets()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t.ID] = true
	}
	for _, t := range builtinTargets() {
		if !have[t.ID] {
			if err := c.st.PutTarget(t, c.cfg.Now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreTargets создаёт switch'и целей и поднимает сохранённые выборы. Под c.mu.
func (c *Core) restoreTargets() {
	ts, err := c.st.Targets()
	if err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "цели: " + err.Error()})
		return
	}
	for _, t := range ts {
		if err := c.ensureTargetSwitch(t); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Target: t.ID, Detail: err.Error()})
			continue
		}
		sel, err := c.st.Selection(store.TargetScope(t.ID))
		if err != nil || sel.Node == "" {
			continue
		}
		if err := c.applyTargetSelection(t.ID, sel.Node); err != nil {
			c.bus.Publish(store.Event{Kind: EvRestoreFailed, Initiator: "auto", Target: t.ID, Node: sel.Node, Detail: err.Error()})
		}
	}
	c.rebuildDispatchLocked()
}

func (c *Core) ensureTargetSwitch(t store.Target) error {
	tag := SwitchTag(t.ID)
	if !c.eng.Has(tag) {
		if err := c.eng.AddSwitch(tag, "", true); err != nil {
			return err
		}
	}
	c.tgMu.Lock()
	c.targetByTag[tag] = t.ID
	if c.tstate[t.ID] == nil {
		c.tstate[t.ID] = &targetState{}
	}
	c.tgMu.Unlock()
	return nil
}

// tagFor возвращает тег outbound'а для выбора: узел регистрируется в движке, direct — встроенный.
func (c *Core) tagFor(sel string) (string, error) {
	switch sel {
	case store.SelDirect:
		return OutDirect, nil
	case store.SelWarp:
		if !c.warpOn {
			return "", fmt.Errorf("%w: WARP не настроен (нет файла аккаунта)", ErrInvalid)
		}
		return OutWarp, nil
	}
	return c.ensureRegistered(sel)
}

// applyTargetSelection направляет switch цели на выбор и запоминает его в памяти. Под c.mu.
func (c *Core) applyTargetSelection(id, sel string) error {
	tag, err := c.tagFor(sel)
	if err != nil {
		c.gc()
		return err
	}
	if err := c.eng.SetSwitchTarget(SwitchTag(id), tag); err != nil {
		c.gc()
		return err
	}
	c.selected[store.TargetScope(id)] = sel
	return nil
}

// rebuildDispatchLocked пересобирает таблицу dispatch: в неё попадают цели с выбранным узлом и непустыми правилами.
func (c *Core) rebuildDispatchLocked() {
	ts, err := c.st.Targets()
	if err != nil {
		return
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	var rules []dispatch.Rule
	for _, t := range ts {
		if c.selected[store.TargetScope(t.ID)] == "" {
			continue
		}
		r := dispatch.Rule{Target: SwitchTag(t.ID), Domain: t.Rules.Domain, DomainSuffix: t.Rules.DomainSuffix, DomainKeyword: t.Rules.DomainKeyword}
		if len(r.Domain)+len(r.DomainSuffix)+len(r.DomainKeyword) > 0 {
			rules = append(rules, r)
		}
	}
	for _, tag := range []string{OutApp, OutAppTUN} {
		if tag == OutAppTUN && !c.eng.Has(tag) {
			continue
		}
		if err := c.eng.SetDispatchRules(tag, rules); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "правила целей: " + err.Error()})
		}
	}
}

// PutTarget регистрирует или обновляет цель. caller: user | api:<приложение>; внешние цели принадлежат приложению.
func (c *Core) PutTarget(t store.Target, caller string) error {
	if t.Owner == "" || caller != "user" {
		t.Owner = caller
	}
	if err := validateTarget(&t); err != nil {
		return err
	}
	if caller != "user" && t.Check.Kind == "http" {
		if err := checkExternalURL(t.Check.URL); err != nil {
			return err
		}
	}
	if prev, err := c.st.Target(t.ID); err == nil {
		if err := authorize(prev, caller); err != nil {
			return err
		}
		if prev.Owner != t.Owner && caller == "user" && t.Owner == "user" {
			t.Owner = prev.Owner // пользователь правит чужую/встроенную цель, не присваивая её себе
		}
	} else if t.Owner == "builtin" && caller != "user" {
		return fmt.Errorf("%w: встроенные цели создаёт только демон", ErrForbidden)
	}
	if caller != "user" {
		if err := c.checkForeignTarget(t, caller); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.st.PutTarget(t, c.cfg.Now()); err != nil {
		return err
	}
	if err := c.ensureTargetSwitch(t); err != nil {
		return err
	}
	c.rebuildDispatchLocked()
	c.bus.Publish(store.Event{Kind: EvTargetChange, Initiator: caller, Target: t.ID, Detail: "зарегистрирована или обновлена"})
	return nil
}

// maxAppTargets — предел числа целей, которые может создать одно приложение.
const maxAppTargets = 5

var domainLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validAppDomain: имя из не менее двух меток (a-z, 0-9, дефис); одиночное слово и «.» не проходят.
func validAppDomain(s string) bool {
	labels := strings.Split(strings.ToLower(s), ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !domainLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

func domainCovers(a, b string) bool { // a совпадает с b или является его суффиксом по границе метки
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || strings.HasSuffix(b, "."+a)
}

// checkForeignTarget: ограничения для вызывающих не user. Сторонний токен не может перехватить чужие домены,
// выставить allow_direct без разрешения владельца (user выставляет его сам, приложение лишь сохраняет уже выданное)
// и создать больше maxAppTargets целей.
func (c *Core) checkForeignTarget(t store.Target, caller string) error {
	prev, prevErr := c.st.Target(t.ID)
	if t.AllowDirect && (prevErr != nil || !prev.AllowDirect) {
		return fmt.Errorf("%w: allow_direct для цели приложения выставляет только владелец", ErrForbidden)
	}
	if len(t.Rules.DomainKeyword) > 0 {
		return fmt.Errorf("%w: ключевые слова в правилах цели приложения не допускаются, нужны домены и суффиксы", ErrInvalid)
	}
	var mine []string
	for _, list := range [][]string{t.Rules.Domain, t.Rules.DomainSuffix} {
		for _, s := range list {
			if !validAppDomain(s) {
				return fmt.Errorf("%w: правило %q: нужно имя из двух и более меток (example.org)", ErrInvalid, s)
			}
			mine = append(mine, s)
		}
	}
	all, err := c.st.Targets()
	if err != nil {
		return err
	}
	own := 0
	for _, o := range all {
		if o.ID == t.ID {
			continue
		}
		if o.Owner == caller {
			own++
		}
		for _, list := range [][]string{o.Rules.Domain, o.Rules.DomainSuffix} {
			for _, theirs := range list {
				for _, m := range mine {
					if domainCovers(m, theirs) || domainCovers(theirs, m) {
						return fmt.Errorf("%w: правило %q пересекается с целью %q", ErrConflict, m, o.ID)
					}
				}
			}
		}
	}
	if prevErr != nil && own >= maxAppTargets {
		return fmt.Errorf("%w: у приложения уже %d целей (предел %d)", ErrInvalid, own, maxAppTargets)
	}
	return nil
}

// DeleteTarget удаляет цель; её узел освобождается, правила пропадают из dispatch.
func (c *Core) DeleteTarget(id, caller string) error {
	t, err := c.st.Target(id)
	if err != nil {
		return err
	}
	if t.Owner == "builtin" {
		return fmt.Errorf("%w: встроенную цель нельзя удалить", ErrForbidden)
	}
	if err := authorize(t, caller); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.selected, store.TargetScope(id))
	c.rebuildDispatchLocked() // сначала убрать правила: пока они ссылаются на switch, он защищён от удаления
	if err := c.st.DeleteTarget(id); err != nil {
		return err
	}
	tag := SwitchTag(id)
	if err := c.eng.Remove(tag); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Target: id, Detail: "switch цели: " + err.Error()})
	}
	c.tgMu.Lock()
	delete(c.targetByTag, tag)
	delete(c.tstate, id)
	c.tgMu.Unlock()
	c.gc()
	c.bus.Publish(store.Event{Kind: EvTargetChange, Initiator: caller, Target: id, Detail: "удалена"})
	return nil
}

// SwitchTarget выбирает узел цели: NodeID, "direct", "warp" либо "next" — следующий кандидат, прошедший проверку цели сейчас.
func (c *Core) SwitchTarget(ctx context.Context, id, to, caller string) (string, error) {
	t, err := c.st.Target(id)
	if err != nil {
		return "", err
	}
	if err := authorize(t, caller); err != nil {
		return "", err
	}
	if to == "next" {
		cur := c.currentOf(id)
		cands, err := c.verifyTargetCandidates(ctx, t, 1, cur)
		if err != nil {
			return "", err
		}
		if len(cands) == 0 {
			return "", fmt.Errorf("%w: ни один не прошёл проверку цели %s в текущей сети", ErrNoCandidates, id)
		}
		to = cands[0]
	}
	if err := c.switchTargetTo(t, to, caller); err != nil {
		return "", err
	}
	return to, nil
}

func (c *Core) currentOf(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.selected[store.TargetScope(id)]
}

// switchTargetTo — единственное место, где меняется узел цели (кроме восстановления при старте).
func (c *Core) switchTargetTo(t store.Target, to, initiator string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.selected[store.TargetScope(t.ID)]
	if old == to {
		return nil
	}
	if err := c.applyTargetSelection(t.ID, to); err != nil {
		c.bus.Publish(store.Event{Kind: EvConnectFailed, Initiator: initiator, Target: t.ID, Node: to, Detail: err.Error()})
		return err
	}
	sel := store.Selection{Scope: store.TargetScope(t.ID), Node: to, By: initiator, SetAt: c.cfg.Now()}
	if prev, err := c.st.Selection(sel.Scope); err == nil {
		sel.Fallbacks = prev.Fallbacks
	}
	if err := c.st.SetSelection(sel); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Target: t.ID, Detail: "выбор цели не сохранён: " + err.Error()})
	}
	c.rebuildDispatchLocked()
	c.gc()
	c.resetTarget(t.ID)
	c.mon.Reset(store.TargetScope(t.ID))
	c.bus.Publish(store.Event{Kind: EvTargetSwitch, Initiator: initiator, Target: t.ID, Node: to, Detail: "с " + short(old)})
	return nil
}

func (c *Core) resetTarget(id string) {
	c.tgMu.Lock()
	if st := c.tstate[id]; st != nil {
		st.fails, st.oks, st.degraded, st.reported = 0, 0, false, false
		st.lastProbe = time.Time{} // после смены узла проверить скоро
	}
	c.tgMu.Unlock()
}

// SetProbeURL принимает свежую пробную ссылку цели (подписанные ссылки CDN живут часы). Хранится только в памяти.
func (c *Core) SetProbeURL(id, url string, ttl time.Duration, caller string) error {
	t, err := c.st.Target(id)
	if err != nil {
		return err
	}
	if err := authorize(t, caller); err != nil {
		return err
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("%w: пробная ссылка должна быть http(s)", ErrInvalid)
	}
	if caller != "user" {
		if err := checkExternalURL(url); err != nil {
			return err
		}
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = time.Hour
	}
	c.tgMu.Lock()
	defer c.tgMu.Unlock()
	st := c.tstate[id]
	if st == nil {
		st = &targetState{}
		c.tstate[id] = st
	}
	st.probeURL, st.probeURLExp = url, c.cfg.Now().Add(ttl)
	return nil
}

func (c *Core) probeURLOf(id string) string {
	c.tgMu.RLock()
	defer c.tgMu.RUnlock()
	if st := c.tstate[id]; st != nil && c.cfg.Now().Before(st.probeURLExp) {
		return st.probeURL
	}
	return ""
}

// runTargetCheck выполняет проверку цели по её виду.
func (c *Core) runTargetCheck(ctx context.Context, t store.Target, node string, p *engine.Probe) prober.Result {
	if c.cfg.TargetCheck != nil {
		return c.cfg.TargetCheck(ctx, t, node, c.probeURLOf(t.ID), p)
	}
	switch t.Check.Kind {
	case "youtube":
		return prober.L4YouTube(ctx, p)
	case "google-api":
		return prober.L4GoogleAPI(ctx, p)
	case "telegram":
		return prober.L4Telegram(ctx, p)
	}
	return prober.CheckHTTP(ctx, p, prober.HTTPSpec{ID: t.ID, URL: t.Check.URL, ExpectCodes: t.Check.ExpectCodes,
		RangeURL: c.probeURLOf(t.ID), MinBytes: int64(t.Check.MinBytes), MinKBps: float64(t.Check.MinKBps)})
}

// ProbeTarget проверяет цель через её живой switch и учитывает результат (деградация, failover по политике).
func (c *Core) ProbeTarget(ctx context.Context, id string) (prober.Result, error) {
	t, err := c.st.Target(id)
	if err != nil {
		return prober.Result{}, err
	}
	node := c.currentOf(id)
	if node == "" {
		return prober.Result{}, fmt.Errorf("у цели %s нет выбранного узла", id)
	}
	p, err := c.eng.Existing(SwitchTag(id))
	if err != nil {
		return prober.Result{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout*3)
	defer cancel()
	r := c.runTargetCheck(cctx, t, node, p)
	if ctx.Err() != nil || r.Class == prober.ClassCanceled {
		return r, ctx.Err()
	}
	c.recordTarget(t, node, r)
	c.noteTarget(t, node, r)
	return r, nil
}

func (c *Core) recordTarget(t store.Target, node string, r prober.Result) {
	row := store.Row{Node: node, Net: c.netKey(), Level: 4, Target: t.ID, OK: r.OK, Class: string(r.Class), LatencyMs: r.LatencyMs, Bytes: r.Bytes, SpeedKBps: r.SpeedKBps, At: c.cfg.Now()}
	if err := c.st.Record([]store.Row{row}); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Target: t.ID, Detail: "запись проверки цели: " + err.Error()})
	}
}

// noteTarget — машина состояний цели: K провалов подряд → деградация, 2 успеха → восстановление.
func (c *Core) noteTarget(t store.Target, node string, r prober.Result) {
	if c.currentOf(t.ID) != node {
		return // результат прежнего узла
	}
	c.tgMu.Lock()
	st := c.tstate[t.ID]
	if st == nil {
		st = &targetState{}
		c.tstate[t.ID] = st
	}
	st.last, st.lastAt, st.lastProbe = r, c.cfg.Now(), c.cfg.Now()
	var after func()
	if r.OK {
		st.fails = 0
		if st.degraded {
			st.oks++
			if st.oks >= 2 {
				st.degraded, st.oks, st.reported = false, 0, false
				after = func() { c.bus.Publish(store.Event{Kind: EvTargetOK, Initiator: "auto", Target: t.ID, Node: node}) }
			}
		}
	} else {
		st.oks = 0
		st.fails++
		if st.fails >= t.FailoverK && !st.degraded {
			st.degraded = true
			after = func() {
				c.onTargetDegraded(t, node, fmt.Sprintf("проверка цели: %s ×%d", r.Class, t.FailoverK))
			}
		}
	}
	c.tgMu.Unlock()
	if after != nil {
		after()
	}
}

// onTargetStall — пассивный детектор заметил зависание соединений цели: запускается подтверждающая проверка цели,
// деградацию объявляет обычная логика K провалов подряд (как и для активного узла).
func (c *Core) onTargetStall(ev monitor.Event) {
	id := strings.TrimPrefix(ev.Scope, "target:")
	if _, err := c.st.Target(id); err != nil || c.currentOf(id) == "" {
		return
	}
	c.confirm(ev.Scope, func(ctx context.Context) (bool, bool) {
		r, err := c.ProbeTarget(ctx, id)
		return r.OK, err == nil
	})
}

// onTargetDegraded: публикует событие и по политике либо переключает цель на проверенного кандидата (auto-failover),
// либо подбирает проверенные альтернативы (sticky-manual).
func (c *Core) onTargetDegraded(t store.Target, node, reason string) {
	c.bus.Publish(store.Event{Kind: EvTargetDegrade, Initiator: "auto", Target: t.ID, Node: node, Detail: detail(map[string]any{"reason": reason, "policy": t.Policy})})
	if c.baseCtx == nil {
		return
	}
	c.goLoop("target-"+t.ID, func() {
		ctx, cancel := context.WithTimeout(c.baseCtx, 90*time.Second)
		defer cancel()
		want := c.cfg.AltCount
		if t.Policy == store.PolicyAutoFailover {
			want = 1
		}
		cands, err := c.verifyTargetCandidates(ctx, t, want, node)
		if err != nil && ctx.Err() == nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Target: t.ID, Detail: "кандидаты цели: " + err.Error()})
			return
		}
		if t.Policy == store.PolicyAutoFailover {
			if len(cands) == 0 {
				c.reportNoCandidates(t)
				return
			}
			// только кандидат, прошедший проверку цели сейчас в этой сети; журнал и событие — внутри switchTargetTo
			_ = c.switchTargetTo(t, cands[0], "auto")
			return
		}
		c.saveTargetFallbacks(t.ID, cands)
		c.bus.Publish(store.Event{Kind: EvAlternatives, Initiator: "auto", Target: t.ID, Node: node, Detail: detail(map[string]any{"alternatives": cands})})
	})
}

func (c *Core) reportNoCandidates(t store.Target) {
	c.tgMu.Lock()
	st := c.tstate[t.ID]
	first := st != nil && !st.reported
	if st != nil {
		st.reported = true
	}
	c.tgMu.Unlock()
	if first {
		c.bus.Publish(store.Event{Kind: EvTargetDegrade, Initiator: "auto", Target: t.ID, Detail: detail(map[string]any{"reason": "нет кандидатов, прошедших проверку цели"})})
	}
}

func (c *Core) saveTargetFallbacks(id string, cands []string) {
	sel, err := c.st.Selection(store.TargetScope(id))
	if err != nil {
		return
	}
	sel.Fallbacks = append([]string(nil), cands...)
	_ = c.st.SetSelection(sel)
}

// verifyTargetCandidates подбирает до want кандидатов, проходящих проверку цели прямо сейчас: сначала direct (если разрешён и
// проходит — он предпочтительнее), затем запасные цели, затем недавно проверенные узлы по очереди. exclude — текущий выбор.
func (c *Core) verifyTargetCandidates(ctx context.Context, t store.Target, want int, exclude string) ([]string, error) {
	now := c.cfg.Now()
	netID := c.netKey()
	var order []string
	if t.AllowDirect && exclude != store.SelDirect {
		order = append(order, store.SelDirect)
	}
	if sel, err := c.st.Selection(store.TargetScope(t.ID)); err == nil {
		order = append(order, sel.Fallbacks...)
	}
	hist, err := c.st.Histories(netID, now)
	if err != nil {
		return nil, err
	}
	nodes, err := c.freshNodes(hist, now, "")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(nodes, func(i, j int) bool { return hist[nodes[i].ID].Score(now) > hist[nodes[j].ID].Score(now) })
	for i := 0; i < len(nodes) && i < 10; i++ {
		order = append(order, nodes[i].ID)
	}
	if t.AllowWarp && c.warpOn {
		order = append(order, store.SelWarp) // последним: порядок P7 — direct → узлы → WARP
	}
	seen := map[string]bool{exclude: true}
	var uniq []string
	for _, id := range order {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
		if len(uniq) >= 13 {
			break
		}
	}
	var out []string
	for i := 0; i < len(uniq) && len(out) < want && ctx.Err() == nil; i += 4 {
		part := uniq[i:min(i+4, len(uniq))]
		oks := make([]bool, len(part))
		var wg sync.WaitGroup
		for j, id := range part {
			wg.Add(1)
			go func() {
				defer wg.Done()
				oks[j] = c.checkCandidateForTarget(ctx, t, id, netID)
			}()
		}
		wg.Wait()
		for j, id := range part {
			if oks[j] && len(out) < want {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// checkCandidateForTarget проверяет одного кандидата проверкой цели через пробный outbound и записывает результат.
func (c *Core) checkCandidateForTarget(ctx context.Context, t store.Target, id, netID string) bool {
	var p *engine.Probe
	switch id {
	case store.SelDirect:
		d, err := c.eng.Existing(OutDirect)
		if err != nil {
			return false
		}
		p = d
	case store.SelWarp:
		if !c.warpOn {
			return false
		}
		w, err := c.eng.Existing(OutWarp)
		if err != nil {
			return false
		}
		p = w
	default:
		rec, err := c.st.Node(id)
		if err != nil {
			return false
		}
		opts, err := c.probeOpts(rec)
		if err != nil {
			return false
		}
		np, err := c.eng.NewProbe(opts)
		if err != nil {
			return false
		}
		defer np.Close()
		p = np
	}
	cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout*3)
	defer cancel()
	r := c.runTargetCheck(cctx, t, id, p)
	if ctx.Err() != nil || r.Class == prober.ClassCanceled {
		return false
	}
	_ = c.st.Record([]store.Row{{Node: id, Net: netID, Level: 4, Target: t.ID, OK: r.OK, Class: string(r.Class), LatencyMs: r.LatencyMs, At: c.cfg.Now()}})
	return r.OK
}

// Targets возвращает цели с состоянием.
func (c *Core) Targets() ([]TargetStatus, error) {
	ts, err := c.st.Targets()
	if err != nil {
		return nil, err
	}
	out := make([]TargetStatus, 0, len(ts))
	for _, t := range ts {
		out = append(out, c.statusOf(t))
	}
	return out, nil
}

// TargetStatus возвращает одну цель.
func (c *Core) TargetStatus(id string) (TargetStatus, error) {
	t, err := c.st.Target(id)
	if err != nil {
		return TargetStatus{}, err
	}
	return c.statusOf(t), nil
}

func (c *Core) statusOf(t store.Target) TargetStatus {
	s := TargetStatus{Target: t, Node: c.currentOf(t.ID)}
	if sel, err := c.st.Selection(store.TargetScope(t.ID)); err == nil {
		s.Fallbacks, s.SetBy = sel.Fallbacks, sel.By
	}
	if s.Node != "" && s.Node != store.SelDirect && s.Node != store.SelWarp {
		if rec, err := c.st.Node(s.Node); err == nil {
			ni := nodeInfo(rec)
			s.NodeInfo = &ni
		}
	}
	c.tgMu.RLock()
	if st := c.tstate[t.ID]; st != nil {
		s.Last, s.LastAt, s.Degraded, s.Fails = st.last, st.lastAt, st.degraded, st.fails
		s.HasProbeURL = st.probeURL != "" && c.cfg.Now().Before(st.probeURLExp)
	}
	c.tgMu.RUnlock()
	return s
}

// scopeOfTargetTag переводит тег switch'а цели в область монитора ("" — не наблюдать).
func (c *Core) scopeOfTargetTag(tag string) string {
	c.tgMu.RLock()
	defer c.tgMu.RUnlock()
	if id, ok := c.targetByTag[tag]; ok {
		return store.TargetScope(id)
	}
	return ""
}

// targetLoop периодически проверяет цели с выбранным узлом.
func (c *Core) targetLoop(ctx context.Context) {
	t := time.NewTicker(c.cfg.TargetInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ts, err := c.st.Targets()
		if err != nil {
			continue
		}
		var wg sync.WaitGroup
		for _, tg := range ts {
			if c.currentOf(tg.ID) == "" || !c.targetDue(tg) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = c.ProbeTarget(ctx, tg.ID)
			}()
		}
		wg.Wait()
	}
}

// targetDue: пора ли проверять цель с учётом периода по виду проверки.
func (c *Core) targetDue(t store.Target) bool {
	every := c.cfg.TargetInterval
	if t.Check.Kind != "http" {
		every = c.cfg.HeavyTargetInterval
	}
	c.tgMu.RLock()
	defer c.tgMu.RUnlock()
	st := c.tstate[t.ID]
	return st == nil || st.lastProbe.IsZero() || c.cfg.Now().Sub(st.lastProbe) >= every
}
