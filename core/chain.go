package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"brod/prober"
	"brod/store"
)

// Цепочки прокси («hop-hop»): трафик идёт через несколько узлов подряд, клиент → звено 1 → звено 2 → выход. Цепочка — виртуальный узел с протоколом
// «chain»: в базе это обычная запись собственного сервера, а в Opts лежит снимок звеньев (параметры каждого звена скопированы, поэтому цепочка
// не ломается, если подписка перестала отдавать узел). Движку цепочка предъявляется набором исходящих: последнее звено — сам выход (с тегом узла),
// остальные — вспомогательные с тегами ch-… и detour друг на друга; первое звено при включённой цепочке через WARP идёт через WARP.
// Автоподбор (лучший узел, альтернативы при деградации) цепочки не выбирает: их выбирает только пользователь.
// WireGuard (в движке это endpoint, а не исходящий) допускается только первым звеном: он идёт напрямую по UDP, а следующие звенья пускаются через него.
// После другого звена WireGuard не работает (UDP внутри TCP-туннеля), поэтому дальше первого места его не берём.

const (
	chainProto   = "chain"
	minChainHops = 2
	maxChainHops = 3
)

// ChainHop — звено цепочки: снимок узла на момент создания цепочки.
type ChainHop struct {
	ID     string          `json:"id"`
	Proto  string          `json:"proto"`
	Server string          `json:"server"`
	Port   int             `json:"port"`
	Opts   json.RawMessage `json:"opts"`
}

type chainSpec struct {
	Type string     `json:"type"`
	Hops []ChainHop `json:"hops"`
}

// ChainHopView — звено цепочки для списка узлов (без параметров доступа).
type ChainHopView struct {
	ID     string
	Proto  string
	Server string
	Port   int
}

func isChain(r store.NodeRec) bool { return r.Proto == chainProto }

// hopIsWG: звено — WireGuard (endpoint в движке).
func hopIsWG(h ChainHop) bool {
	if strings.EqualFold(h.Proto, "wireguard") {
		return true
	}
	var t struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(h.Opts, &t) == nil && strings.EqualFold(t.Type, "wireguard")
}

func parseChain(r store.NodeRec) (chainSpec, error) {
	var s chainSpec
	if err := json.Unmarshal(r.Opts, &s); err != nil {
		return s, fmt.Errorf("цепочка %s: %w", short(r.ID), err)
	}
	if s.Type != chainProto || len(s.Hops) < minChainHops || len(s.Hops) > maxChainHops {
		return s, fmt.Errorf("цепочка %s: некорректный состав (%d звеньев)", short(r.ID), len(s.Hops))
	}
	return s, nil
}

// chainViewOf — звенья цепочки для интерфейса; для не цепочки и повреждённой записи nil.
func chainViewOf(r store.NodeRec) []ChainHopView {
	if !isChain(r) {
		return nil
	}
	s, err := parseChain(r)
	if err != nil {
		return nil
	}
	out := make([]ChainHopView, len(s.Hops))
	for i, h := range s.Hops {
		out[i] = ChainHopView{ID: h.ID, Proto: h.Proto, Server: h.Server, Port: h.Port}
	}
	return out
}

// nodeServerNames — адреса серверов узла (у цепочки адреса всех звеньев): им нужны правила DNS и обход туннеля.
func nodeServerNames(r store.NodeRec) []string {
	if !isChain(r) {
		return []string{r.Server}
	}
	s, err := parseChain(r)
	if err != nil {
		return nil
	}
	out := make([]string, len(s.Hops))
	for i, h := range s.Hops {
		out[i] = h.Server
	}
	return out
}

// chainTag — тег вспомогательного звена: ch-<id>-<w|d><номер>. w — первое звено идёт через WARP, d — напрямую: WARP бывает недоступен, и тогда нужен
// второй набор.
func chainTag(id string, viaWarp bool, i int) string {
	kind := "d"
	if viaWarp {
		kind = "w"
	}
	return "ch-" + short(id) + "-" + kind + strconv.Itoa(i)
}

func hopOutbound(h ChainHop, tag, detour string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(h.Opts, &m); err != nil {
		return nil, fmt.Errorf("опции звена: %w", err)
	}
	m["tag"] = tag
	if detour != "" {
		m["detour"] = detour
	}
	return json.Marshal(m)
}

// ensureChainHelpers регистрирует в движке вспомогательные звенья цепочки (все, кроме последнего) и возвращает тег последнего из них. Повторный вызов
// ничего не делает. Звенья живут до удаления цепочки или остановки ядра.
func (c *Core) ensureChainHelpers(rec store.NodeRec, viaWarp bool) (string, error) {
	s, err := parseChain(rec)
	if err != nil {
		return "", err
	}
	c.chainMu.Lock()
	defer c.chainMu.Unlock()
	if c.chainHelpers == nil {
		c.chainHelpers = map[string]bool{}
	}
	prev := ""
	if viaWarp && !hopIsWG(s.Hops[0]) { // WireGuard первым звеном идёт напрямую: UDP через WARP не вкладываем
		prev = OutWarp
	}
	if hopIsWG(s.Hops[0]) {
		viaWarp = false // у цепочки с WireGuard первым звеном один набор звеньев: WARP на неё не влияет
	}
	for i := 0; i < len(s.Hops)-1; i++ {
		tag := chainTag(rec.ID, viaWarp, i)
		if !c.chainHelpers[tag] {
			raw, err := hopOutbound(s.Hops[i], tag, prev)
			if err != nil {
				return "", err
			}
			if _, err := c.eng.Add(raw); err != nil {
				return "", fmt.Errorf("звено %d цепочки %s: %w", i+1, short(rec.ID), err)
			}
			c.chainHelpers[tag] = true
		}
		prev = tag
	}
	return prev, nil
}

// dropChainHelpers убирает вспомогательные звенья цепочки из движка (последнее первым: на ранние звенья ссылаются поздние).
func (c *Core) dropChainHelpers(id string) {
	c.chainMu.Lock()
	defer c.chainMu.Unlock()
	for _, viaWarp := range []bool{true, false} {
		for i := maxChainHops - 2; i >= 0; i-- {
			tag := chainTag(id, viaWarp, i)
			if c.chainHelpers[tag] {
				_ = c.eng.Remove(tag)
				delete(c.chainHelpers, tag)
			}
		}
	}
}

// nodeOutboundJSON — исходящее узла для движка с тегом tag. Для обычного узла это outboundJSON; для цепочки сначала регистрируются вспомогательные звенья,
// а выходом становится последнее звено с detour на предыдущее.
func (c *Core) nodeOutboundJSON(rec store.NodeRec, tag string, viaWarp bool) ([]byte, error) {
	if !isChain(rec) {
		return outboundJSON(rec, tag, viaWarp)
	}
	s, err := parseChain(rec)
	if err != nil {
		return nil, err
	}
	prev, err := c.ensureChainHelpers(rec, viaWarp)
	if err != nil {
		return nil, err
	}
	return hopOutbound(s.Hops[len(s.Hops)-1], tag, prev)
}

// AddChain создаёт цепочку из узлов hopIDs (от первого звена к выходу) и возвращает её идентификатор. Звенья — любые узлы, кроме WireGuard и самих цепочек.
// name необязателен: без него интерфейс покажет состав.
func (c *Core) AddChain(name string, hopIDs []string, initiator string) (string, error) {
	if len(hopIDs) < minChainHops || len(hopIDs) > maxChainHops {
		return "", fmt.Errorf("%w: в цепочке от %d до %d звеньев", ErrInvalid, minChainHops, maxChainHops)
	}
	seen := map[string]bool{}
	spec := chainSpec{Type: chainProto}
	for _, id := range hopIDs {
		if seen[id] {
			return "", fmt.Errorf("%w: один узел нельзя ставить в цепочку дважды", ErrInvalid)
		}
		seen[id] = true
		rec, err := c.st.Node(id)
		if err != nil {
			return "", fmt.Errorf("%w: узел %s не найден", ErrInvalid, short(id))
		}
		if isChain(rec) {
			return "", fmt.Errorf("%w: цепочку нельзя вкладывать в цепочку", ErrInvalid)
		}
		if (rec.Endpoint || strings.EqualFold(rec.Proto, "wireguard")) && len(spec.Hops) > 0 {
			return "", fmt.Errorf("%w: WireGuard может быть только первым звеном цепочки", ErrInvalid)
		}
		spec.Hops = append(spec.Hops, ChainHop{ID: rec.ID, Proto: rec.Proto, Server: rec.Server, Port: rec.Port, Opts: rec.Opts})
	}
	opts, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	id := store.NodeID("chain|" + hex.EncodeToString(rnd[:]))
	rec := store.NodeRec{ID: id, Class: fmt.Sprintf("%s/hops%d/none", chainProto, len(spec.Hops)), Proto: chainProto, Opts: opts}
	now := c.cfg.Now()
	if err := c.st.UpsertNodes(store.OwnSource, now.UnixNano(), []store.NodeRec{rec}, now); err != nil {
		return "", err
	}
	_ = c.st.SetNodeFlag(id, store.FlagPinned, true)
	_ = c.st.SetNodeFlag(id, store.FlagFavorite, true)
	if label := strings.TrimSpace(name); label != "" {
		if err := c.SetNodeLabel(id, label, initiator); err != nil {
			return id, err
		}
	}
	c.invalidateCounts()
	c.bus.Publish(store.Event{Kind: EvNode, Initiator: initiator, Node: id, Detail: fmt.Sprintf("цепочка из %d звеньев создана", len(spec.Hops))})
	return id, nil
}

// withoutChains убирает цепочки из списка записей: автоподбор их не выбирает.
func withoutChains(recs []store.NodeRec) []store.NodeRec {
	out := recs[:0:0]
	for _, r := range recs {
		if !isChain(r) {
			out = append(out, r)
		}
	}
	return out
}

// HopCheck — итог проверки одного звена цепочки: достижим ли его сервер по TCP напрямую. Звенья на UDP (hysteria2, tuic, WireGuard) так
// не проверить: они помечаются skip, а не ok.
type HopCheck struct {
	ID     string `json:"id"`
	Proto  string `json:"proto"`
	Server string `json:"server"`
	Port   int    `json:"port"`
	State  string `json:"state"` // ok | fail | skip
	Ms     int64  `json:"ms,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// hopUDPOnly: звено работает поверх UDP, проверка TCP-соединением к нему бессмысленна.
func hopUDPOnly(h ChainHop) bool {
	if hopIsWG(h) {
		return true
	}
	switch strings.ToLower(h.Proto) {
	case "hysteria", "hysteria2", "hy2", "tuic":
		return true
	}
	return false
}

// CheckChain проверяет каждое звено цепочки отдельно (TCP к серверу звена напрямую), чтобы при сбое цепочки было видно, какое звено умерло.
// Звенья проверяются одновременно, предел времени на каждое — 5 с.
func (c *Core) CheckChain(ctx context.Context, id string) ([]HopCheck, error) {
	rec, err := c.st.Node(id)
	if err != nil {
		return nil, fmt.Errorf("узел %s: %w", short(id), ErrNoNode)
	}
	if !isChain(rec) {
		return nil, fmt.Errorf("%w: узел не цепочка", ErrInvalid)
	}
	spec, err := parseChain(rec)
	if err != nil {
		return nil, err
	}
	out := make([]HopCheck, len(spec.Hops))
	var wg sync.WaitGroup
	for i, h := range spec.Hops {
		out[i] = HopCheck{ID: h.ID, Proto: h.Proto, Server: h.Server, Port: h.Port, State: "skip", Detail: "звено на UDP: напрямую не проверяется"}
		if hopUDPOnly(h) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := prober.L1(ctx, net.JoinHostPort(h.Server, strconv.Itoa(h.Port)), 5*time.Second)
			out[i].Ms, out[i].Detail = r.LatencyMs, ""
			if r.OK {
				out[i].State = "ok"
				return
			}
			out[i].State, out[i].Detail = "fail", r.Detail
		}()
	}
	wg.Wait()
	return out, nil
}
