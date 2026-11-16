package core

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"brod/prober"
	"brod/store"
)

// Выход правила маршрутизации. Правило с действием proxy по умолчанию идёт в общий диспетчер (активный узел и цели). Поле via выбирает выход
// самого правила: конкретный узел, страну (лучший живой узел с выходом в этой стране, с автозаменой) или WARP. Для каждого такого правила
// в конфиг движка вносится свой switch-outbound (RuleTag), а цель switch'а ведёт ядро в рантайме без перезапуска VPN, как у целей.

// Виды выхода правила.
const (
	ViaNode    = "node"
	ViaCountry = "country"
	ViaWarp    = "warp"
)

// Что делать, когда подходящего выхода нет (в стране нет живого узла, узла нет в базе, WARP не настроен).
const (
	FallbackBlock  = "block"  // по умолчанию: не утекать, соединения отклоняются
	FallbackProxy  = "proxy"  // через активный узел
	FallbackDirect = "direct" // напрямую
)

// RouteVia — выход правила.
type RouteVia struct {
	Kind     string `json:"kind"`
	Node     string `json:"node,omitempty"`     // для kind=node: идентификатор узла
	Country  string `json:"country,omitempty"`  // для kind=country: код ISO 3166-1 (NL, DE…)
	Fallback string `json:"fallback,omitempty"` // block | proxy | direct; пусто = block
}

var (
	nodeIDRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
)

// RuleTag — тег switch-outbound'а правила в конфиге движка.
func RuleTag(id string) string { return "rt-" + id }

// RuleScope — ключ выбора правила в c.selected (в памяти, не в базе): по нему сборщик мусора знает, что узел занят правилом.
func RuleScope(id string) string { return "rule:" + id }

func (v *RouteVia) validate(action string) error {
	if v == nil {
		return nil
	}
	if action != ActionProxy {
		return fmt.Errorf("выход (via) задаётся только для действия proxy")
	}
	switch v.Fallback {
	case "", FallbackBlock, FallbackProxy, FallbackDirect:
	default:
		return fmt.Errorf("запасной вариант %q: нужно block, proxy или direct", v.Fallback)
	}
	switch v.Kind {
	case ViaNode:
		v.Node = strings.ToLower(strings.TrimSpace(v.Node))
		if !nodeIDRe.MatchString(v.Node) {
			return fmt.Errorf("неверный идентификатор узла")
		}
		v.Country = ""
	case ViaCountry:
		v.Country = strings.ToUpper(strings.TrimSpace(v.Country))
		if !countryRe.MatchString(v.Country) {
			return fmt.Errorf("страна %q: нужен двухбуквенный код (NL, DE…)", v.Country)
		}
		v.Node = ""
	case ViaWarp:
		v.Node, v.Country = "", ""
	default:
		return fmt.Errorf("вид выхода %q: нужно node, country или warp", v.Kind)
	}
	return nil
}

// hasVia: у правила задан собственный выход (и оно включено).
func (r RouteRule) hasVia() bool {
	return r.Enabled && r.Action == ActionProxy && r.Via != nil && r.Via.Kind != ""
}

// ViaState — текущая привязка выхода правила (для приложения и журнала).
type ViaState struct {
	Rule       string    `json:"rule"`
	Kind       string    `json:"kind"`
	Target     string    `json:"target"`         // node | warp | active | direct | none (соединения отклоняются)
	Node       string    `json:"node,omitempty"` // выбранный узел
	Fallback   bool      `json:"fallback"`       // подходящего выхода нет, действует запасной вариант
	Candidates int       `json:"candidates"`     // для страны: сколько живых узлов подходит
	Since      time.Time `json:"since"`
}

// CountryInfo — страна, в которой есть проверенные узлы.
type CountryInfo struct {
	Code  string `json:"code"`
	Alive int    `json:"alive"` // живых узлов (успех проверки не старше AltFresh)
	Total int    `json:"total"` // узлов с известным выходом в этой стране
}

// RouteCountries перечисляет страны выходов проверенных узлов (для выбора в правиле).
func (c *Core) RouteCountries() ([]CountryInfo, error) {
	exits, err := c.st.ExitInfos()
	if err != nil {
		return nil, err
	}
	now := c.cfg.Now()
	hist, err := c.st.Histories(c.netKey(), now)
	if err != nil {
		return nil, err
	}
	by := map[string]*CountryInfo{}
	for id, e := range exits {
		cc := strings.ToUpper(e.Country)
		if !countryRe.MatchString(cc) {
			continue
		}
		ci := by[cc]
		if ci == nil {
			ci = &CountryInfo{Code: cc}
			by[cc] = ci
		}
		ci.Total++
		if h := hist[id]; !h.L2OK.IsZero() && now.Sub(h.L2OK) <= c.cfg.AltFresh && !h.FailedLast {
			ci.Alive++
		}
	}
	out := make([]CountryInfo, 0, len(by))
	for _, ci := range by {
		out = append(out, *ci)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Alive != out[j].Alive {
			return out[i].Alive > out[j].Alive
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

// countryCandidates — живые узлы со страной выхода cc, лучшие первыми (ранг истории, затем скорость, затем ID).
func (c *Core) countryCandidates(cc string, now time.Time) ([]store.NodeRec, error) {
	exits, err := c.st.ExitInfos()
	if err != nil {
		return nil, err
	}
	hist, err := c.st.Histories(c.netKey(), now)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id, e := range exits {
		if !strings.EqualFold(e.Country, cc) {
			continue
		}
		if h := hist[id]; !h.L2OK.IsZero() && now.Sub(h.L2OK) <= c.cfg.AltFresh && !h.FailedLast {
			ids = append(ids, id)
		}
	}
	hide := c.hideInsecure()
	var recs []store.NodeRec
	for i := 0; i < len(ids); i += 500 {
		rs, err := c.st.Nodes(store.NodeFilter{IDs: ids[i:min(i+500, len(ids))], OnlyPresent: true, HideInsecure: hide})
		if err != nil {
			return nil, err
		}
		recs = append(recs, rs...)
	}
	speeds, _ := c.st.Speeds()
	// задержка последнего успеха, округлённая до 100 мс (0 — неизвестна, идёт после известных): при равном ранге выигрывает более быстрый
	lat := func(id string) int64 {
		if ms := hist[id].L2Ms; ms > 0 {
			return (ms + 50) / 100
		}
		return 1 << 20
	}
	sort.Slice(recs, func(i, j int) bool {
		a, b := hist[recs[i].ID].Score(now), hist[recs[j].ID].Score(now)
		if a != b {
			return a > b
		}
		if la, lb := lat(recs[i].ID), lat(recs[j].ID); la != lb {
			return la < lb
		}
		if sa, sb := speeds[recs[i].ID].KBps, speeds[recs[j].ID].KBps; sa != sb {
			return sa > sb
		}
		return recs[i].ID < recs[j].ID
	})
	return recs, nil
}

// viaChoice — что должно быть за switch'ем правила.
type viaChoice struct {
	sel        string // узел | store.SelWarp | store.SelDirect | viaActive | "" (отклонять)
	state      ViaState
	candidates int
}

const viaActive = "@active" // запасной вариант proxy: через активный узел (switch OutActive)

func (c *Core) fallbackChoice(v *RouteVia) string {
	switch v.Fallback {
	case FallbackProxy:
		return viaActive
	case FallbackDirect:
		return store.SelDirect
	}
	return ""
}

// chooseVia выбирает выход правила; cur — прежний выбор (его сохраняем, пока он годен: без дёрганья между равными узлами).
func (c *Core) chooseVia(r RouteRule, cur string, now time.Time) viaChoice {
	v := r.Via
	ch := viaChoice{state: ViaState{Rule: r.ID, Kind: v.Kind}}
	switch v.Kind {
	case ViaNode:
		if _, err := c.st.Node(v.Node); err == nil {
			ch.sel = v.Node
			return ch
		}
	case ViaWarp:
		if c.warpOn && !c.warpDown.Load() {
			ch.sel = store.SelWarp
			return ch
		}
	case ViaCountry:
		cands, err := c.countryCandidates(v.Country, now)
		if err == nil {
			ch.candidates = len(cands)
			for _, n := range cands {
				if n.ID == cur {
					ch.sel = cur
					return ch
				}
			}
			if len(cands) > 0 {
				ch.sel = cands[0].ID
				return ch
			}
		}
	}
	ch.sel = c.fallbackChoice(v)
	ch.state.Fallback = true
	return ch
}

// viaRules — правила с собственным выходом из конфига, с которым запущен движок.
func (c *Core) viaRules() []RouteRule {
	var rules []RouteRule
	for _, r := range c.cfg.TUN.UserRules {
		if r.hasVia() {
			rules = append(rules, r)
		}
	}
	return rules
}

// probeRouteVia проверяет узлы, к которым сейчас привязаны правила «страна» и «узел», через их собственный switch (тем же путём, каким идёт
// трафик правила). Результат пишется в историю: успех обновляет задержку, провал после HealthFails подряд исключает узел из кандидатов на
// 30 минут, и ближайшая привязка заменит его. Без этого падение такого узла заметил бы только плановый круг проверок раз в 5 минут.
func (c *Core) probeRouteVia(ctx context.Context) {
	var wg sync.WaitGroup
	for _, r := range c.viaRules() {
		if r.Via.Kind != ViaCountry && r.Via.Kind != ViaNode {
			continue
		}
		c.mu.Lock()
		id := c.selected[RuleScope(r.ID)]
		c.mu.Unlock()
		if id == "" || !c.eng.Has(RuleTag(r.ID)) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := c.eng.Existing(RuleTag(r.ID))
			if err != nil {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout)
			defer cancel()
			res := c.cfg.Liveness(cctx, p)
			if ctx.Err() != nil || res.Class == prober.ClassCanceled {
				return // остановка — не провал узла
			}
			c.noteRouteViaProbe(r.ID, id, res)
		}()
	}
	wg.Wait()
}

func (c *Core) noteRouteViaProbe(rule, node string, res prober.Result) {
	now := c.cfg.Now()
	cls := ""
	if rec, err := c.st.Node(node); err == nil {
		cls = rec.Class
	}
	c.viaMu.Lock()
	defer c.viaMu.Unlock()
	if res.OK {
		c.viaFails[rule] = 0
		if res.LatencyMs > 0 && now.Sub(c.viaPingAt[rule]) >= time.Minute {
			c.viaPingAt[rule] = now
			_ = c.st.Record([]store.Row{{Node: node, Net: c.netKey(), NodeClass: cls, Level: 2, OK: true, LatencyMs: res.LatencyMs, At: now}})
		}
		return
	}
	c.viaFails[rule]++
	if c.viaFails[rule] >= c.cfg.HealthFails {
		_ = c.st.Record([]store.Row{{Node: node, Net: c.netKey(), NodeClass: cls, Level: 2, OK: false, Class: string(res.Class), At: now}})
	}
}

// bindRouteVia приводит switch'и правил в соответствие с выбором. Правила берутся из конфига, с которым запущен движок (c.cfg.TUN.UserRules):
// сохранённый, но ещё не применённый список переприменяется перезапуском VPN.
func (c *Core) bindRouteVia() {
	rules := c.viaRules()
	if len(rules) == 0 {
		return
	}
	now := c.cfg.Now()
	c.mu.Lock()
	cur := make(map[string]string, len(rules))
	for _, r := range rules {
		cur[r.ID] = c.selected[RuleScope(r.ID)]
	}
	c.mu.Unlock()
	// выбор требует чтения базы: делаем без c.mu
	choices := make([]viaChoice, len(rules))
	for i, r := range rules {
		prev := cur[r.ID]
		if prev == "" {
			c.viaMu.Lock()
			if s, ok := c.viaState[r.ID]; ok && s.Target == "active" {
				prev = viaActive
			}
			c.viaMu.Unlock()
		}
		choices[i] = c.chooseVia(r, prev, now)
	}
	c.mu.Lock()
	changed := false
	for i, r := range rules {
		ch := choices[i]
		tag := RuleTag(r.ID)
		if !c.eng.Has(tag) {
			continue // правило добавлено, а движок ещё не перезапущен
		}
		var err error
		prev := c.selected[RuleScope(r.ID)]
		c.viaMu.Lock()
		old, had := c.viaState[r.ID]
		c.viaMu.Unlock()
		wantTarget := viaTargetName(ch.sel)
		if had && old.Target == wantTarget && prev == storedSel(ch.sel) {
			// ничего не менялось; обновим только счётчик кандидатов
			c.viaMu.Lock()
			s := old
			s.Candidates, s.Fallback = ch.candidates, ch.state.Fallback
			c.viaState[r.ID] = s
			c.viaMu.Unlock()
			continue
		}
		switch ch.sel {
		case "":
			err = c.eng.ClearSwitchTarget(tag)
			delete(c.selected, RuleScope(r.ID))
		case viaActive:
			err = c.eng.SetSwitchTarget(tag, OutActive)
			delete(c.selected, RuleScope(r.ID))
		default:
			var t string
			if t, err = c.tagFor(ch.sel); err == nil {
				err = c.eng.SetSwitchTarget(tag, t)
			}
			if err == nil {
				c.selected[RuleScope(r.ID)] = ch.sel
			}
		}
		if err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "выход правила " + r.ID + ": " + err.Error()})
			continue
		}
		changed = true
		st := ch.state
		st.Target, st.Candidates, st.Since = wantTarget, ch.candidates, now
		if ch.sel != "" && ch.sel != viaActive && ch.sel != store.SelDirect && ch.sel != store.SelWarp {
			st.Node = ch.sel
		}
		c.viaMu.Lock()
		c.viaState[r.ID] = st
		c.viaMu.Unlock()
		c.bus.Publish(store.Event{Kind: EvRouteVia, Initiator: "auto", Node: st.Node, Detail: detail(map[string]any{"rule": r.ID, "kind": st.Kind, "target": st.Target, "fallback": st.Fallback})})
	}
	if changed {
		c.gc()
	}
	c.mu.Unlock()
}

func viaTargetName(sel string) string {
	switch sel {
	case "":
		return "none"
	case viaActive:
		return "active"
	case store.SelDirect:
		return "direct"
	case store.SelWarp:
		return "warp"
	}
	return "node"
}

// storedSel — что хранится в c.selected для выбора (запасные «active» и «none» там не хранятся).
func storedSel(sel string) string {
	if sel == "" || sel == viaActive {
		return ""
	}
	return sel
}

// RouteViaStates возвращает текущие привязки выхода правил.
func (c *Core) RouteViaStates() []ViaState {
	c.viaMu.Lock()
	defer c.viaMu.Unlock()
	out := make([]ViaState, 0, len(c.viaState))
	for _, s := range c.viaState {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// routeViaFirst — задержка первого прохода routeViaLoop (переменная для теста).
var routeViaFirst = 200 * time.Millisecond

// routeViaLoop держит выходы правил актуальными: узел страны умер или появился лучший — switch перенаправляется без перезапуска VPN.
func (c *Core) routeViaLoop(ctx context.Context) {
	t := time.NewTimer(routeViaFirst) // движок к запуску циклов уже поднят; до первой привязки соединения по таким правилам отклоняются
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.probeRouteVia(ctx)
		c.bindRouteVia()
		t.Reset(c.cfg.RouteViaInterval)
	}
}
