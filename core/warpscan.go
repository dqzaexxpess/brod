package core

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"brod/prober"
	"brod/store"
	"brod/warp"
)

// Подбор профиля WARP под сеть. Одну и ту же учётную запись разные сети пропускают по-разному: на LTE одного оператора 162.159.192.1:2408
// молчит, а 188.114.97.9:987 с маскировкой quic и 188.114.96.74:2408 без неё отвечают быстро. Поэтому профиль
// (адрес, порт, маскировка) выбирается пробой каждого кандидата, лучший запоминается по ключу сети и применяется при возврате в неё.

// WarpProfile — набор параметров endpoint'а WARP, различающихся между сетями.
type WarpProfile struct {
	Endpoint string `json:"endpoint"`
	Port     uint16 `json:"port"`
	Mask     string `json:"mask"` // quic | dns | stun | sip | none
	MTU      uint32 `json:"mtu,omitempty"`
}

func (p WarpProfile) String() string {
	return p.Endpoint + ":" + strconv.Itoa(int(p.Port)) + "/" + p.Mask
}

// WarpScanResult — итог пробы одного профиля.
type WarpScanResult struct {
	Profile   WarpProfile `json:"profile"`
	Stable    int         `json:"stable,omitempty"` // сколько проб из 1+warpVerifyRounds прошло (устойчивость во времени); 0 — повторно не проверялся
	OK        bool        `json:"ok"`
	LatencyMs int64       `json:"latency_ms,omitempty"`
	Class     string      `json:"class,omitempty"`
}

// Кандидаты: сначала умолчание; дальше адреса из пулов WARP (любой адрес пула принимает любой аккаунт) и порты из эталона «portal wg»
// (Киберщит: 878, 939, 1074) и из списка известных портов WARP.
var warpScanCandidates = buildWarpCandidates()

func buildWarpCandidates() []WarpProfile {
	hosts := []string{"162.159.192.1", "188.114.96.74", "188.114.97.9", "188.114.99.157", "8.34.146.9", "162.159.195.1"}
	ports := []uint16{2408, 987, 878, 939, 1074, 500, 4500}
	var out []WarpProfile
	for _, m := range []string{"quic", "none"} {
		for _, p := range ports {
			for _, h := range hosts {
				out = append(out, WarpProfile{Endpoint: h, Port: p, Mask: m})
			}
		}
	}
	return out
}

// WarpScanOptions — параметры подбора.
type WarpScanOptions struct {
	Limit      int  // сколько кандидатов пробовать (0 — все)
	Parallel   int  // одновременных проб (0 — 6)
	Apply      bool // выбранный лучший профиль сохранить для сети и применить
	StopAtGood int  // остановиться, когда найдено столько рабочих (0 — пробовать все)
}

// profileOf — действующий профиль ядра (настройки поверх умолчаний).
func (c *Core) profileOf() WarpProfile {
	p := WarpProfile{Endpoint: c.cfg.WarpEndpoint, Port: c.cfg.WarpPort, Mask: c.cfg.WarpMask, MTU: c.cfg.WarpMTU}
	if p.Endpoint == "" {
		p.Endpoint = "engage.cloudflareclient.com"
	}
	if p.Port == 0 {
		p.Port = 2408
	}
	if p.Mask == "" {
		p.Mask = "quic"
	}
	return p
}

func (c *Core) probeProfile(ctx context.Context, p WarpProfile) prober.Result {
	if c.cfg.WarpScanProbe != nil {
		return c.cfg.WarpScanProbe(ctx, p)
	}
	mask := p.Mask
	if mask == "none" {
		mask = ""
	}
	ep := c.warpAcc.Endpoint(warp.Options{Tag: "warp-scan", Mask: mask, Endpoint: p.Endpoint, Port: p.Port, MTU: p.MTU, MaskHost: c.cfg.WarpMaskHost, Browser: c.cfg.WarpBrowser, IPv4Only: runtime.GOOS == "windows"})
	raw, err := json.Marshal(ep)
	if err != nil {
		return prober.Result{Class: "other"}
	}
	pr, err := c.eng.NewProbe(raw)
	if err != nil {
		return prober.Result{Class: "other"}
	}
	defer pr.Close()
	cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout*3)
	defer cancel()
	return prober.L2(cctx, pr)
}

// WarpScan пробует кандидатов (текущий профиль первым) и возвращает результаты по порядку кандидатов. С Apply лучший рабочий профиль
// запоминается для текущей сети и применяется.
func (c *Core) WarpScan(ctx context.Context, opt WarpScanOptions) ([]WarpScanResult, error) {
	if !c.warpOn || c.warpAcc == nil {
		return nil, fmt.Errorf("%w: WARP не настроен (нет файла аккаунта)", ErrInvalid)
	}
	c.wsMu.Lock()
	if c.wsRunning {
		c.wsMu.Unlock()
		return nil, ErrBusy
	}
	c.wsRunning = true
	c.wsMu.Unlock()
	defer func() { c.wsMu.Lock(); c.wsRunning = false; c.wsMu.Unlock() }()

	cands := append([]WarpProfile{c.profileOf()}, warpScanCandidates...)
	seen := map[WarpProfile]bool{}
	uniq := cands[:0:0]
	for _, p := range cands {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	cands = uniq
	if opt.Limit > 0 && len(cands) > opt.Limit {
		cands = cands[:opt.Limit]
	}
	par := opt.Parallel
	if par <= 0 {
		par = 6
	}
	res := make([]WarpScanResult, len(cands))
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	var mu sync.Mutex
	good := 0
	stop := false
	for i, p := range cands {
		mu.Lock()
		if stop || ctx.Err() != nil {
			mu.Unlock()
			res[i] = WarpScanResult{Profile: p, Class: "skipped"}
			continue
		}
		mu.Unlock()
		sem <- struct{}{}
		mu.Lock()
		if stop || ctx.Err() != nil { // пока ждали очереди, набралось достаточно рабочих
			mu.Unlock()
			<-sem
			res[i] = WarpScanResult{Profile: p, Class: "skipped"}
			continue
		}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			r := c.probeProfile(ctx, p)
			out := WarpScanResult{Profile: p, OK: r.OK, LatencyMs: r.LatencyMs, Class: string(r.Class)}
			mu.Lock()
			res[i] = out
			if r.OK {
				good++
				if opt.StopAtGood > 0 && good >= opt.StopAtGood {
					stop = true
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if opt.Apply {
		c.verifyWarpProfiles(ctx, res)
		if best, ok := bestWarp(res, c.profileOf()); ok {
			if err := c.rememberWarpProfile(best); err != nil {
				return res, err
			}
			// подбор выбирает профиль по боковым пробам, а основной endpoint может его не принять (WARP тогда лежит минутами). Если прежний профиль в этом же подборе отвечал, помним его и откатываемся при провале первой проверки
			if cur := c.profileOf(); best != cur {
				for _, r := range res {
					if r.OK && r.Profile == cur {
						b, _ := json.Marshal(warpTrial{New: best, Prev: cur})
						_ = c.st.SetSetting(settingWarpTrial, string(b))
						break
					}
				}
			}
			c.applyWarpProfile(best)
		}
	}
	return res, nil
}

const settingWarpTrial = "warp.trial"

// warpTrial — только что применённый подбором профиль и тот, на который откатываться, если он не заработает.
type warpTrial struct {
	New  WarpProfile `json:"new"`
	Prev WarpProfile `json:"prev"`
}

// settleWarpTrial вызывается после первой проверки WARP на новом ядре: удачная проверка закрепляет профиль, неудачная возвращает прежний
// (запоминается для сети и применяется). Опыт одноразовый: дальше обычная логика автоподбора.
func (c *Core) settleWarpTrial(ok bool) {
	v, found, _ := c.st.Setting(settingWarpTrial)
	if !found || v == "" {
		return
	}
	_ = c.st.SetSetting(settingWarpTrial, "")
	var t warpTrial
	if json.Unmarshal([]byte(v), &t) != nil || ok || c.profileOf() != t.New {
		return
	}
	log.Printf("WARP: подобранный профиль %s не заработал, возврат на %s", t.New, t.Prev)
	if err := c.rememberWarpProfile(t.Prev); err != nil {
		return
	}
	c.applyWarpProfile(t.Prev)
}

// Повторная проверка перед запоминанием. Одна проба показывает, что профиль ответил один раз, а не что он держится: на LTE замечено, что WARP после
// успешной первой проверки через 20–40 с снова «down». Поэтому лучшие по задержке профили (и действующий) проверяются ещё warpVerifyRounds раз с паузой
// warpVerifyGap (всего окно ~20 с), и выбирается самый устойчивый, а среди равных по устойчивости — самый быстрый.
var (
	warpVerifyRounds = 2
	warpVerifyGap    = 10 * time.Second
	warpVerifyTop    = 4
)

// verifyWarpProfiles дополняет результаты: Stable и усреднённую задержку у проверенных профилей, Stable=1 у остальных рабочих.
func (c *Core) verifyWarpProfiles(ctx context.Context, res []WarpScanResult) {
	var idx []int
	for i, r := range res {
		if r.OK {
			res[i].Stable = 1
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return res[idx[a]].LatencyMs < res[idx[b]].LatencyMs })
	cur := c.profileOf()
	top := idx[:min(len(idx), warpVerifyTop)]
	for _, i := range idx[len(top):] { // действующий профиль проверяется всегда: на него опирается правило «остаёмся на текущем»
		if res[i].Profile == cur {
			top = append(top, i)
		}
	}
	if len(top) < 2 { // сравнивать не с чем: единственный рабочий профиль берётся как есть
		return
	}
	sums := map[int]int64{}
	for _, i := range top {
		sums[i] = res[i].LatencyMs
	}
	for round := 0; round < warpVerifyRounds; round++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(warpVerifyGap):
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, i := range top {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := c.probeProfile(ctx, res[i].Profile)
				if r.OK {
					mu.Lock()
					res[i].Stable++
					sums[i] += r.LatencyMs
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}
	for _, i := range top {
		if res[i].Stable > 0 {
			res[i].LatencyMs = sums[i] / int64(res[i].Stable)
		}
	}
}

// bestWarp — самый устойчивый рабочий профиль, среди равных по устойчивости — с наименьшей задержкой; при близких значениях (в пределах 25%)
// остаётся текущий, чтобы не гонять перезапуски зря. Без повторных проверок (Stable одинаков) выбор по задержке.
func bestWarp(res []WarpScanResult, cur WarpProfile) (WarpProfile, bool) {
	var okr []WarpScanResult
	for _, r := range res {
		if r.OK {
			okr = append(okr, r)
		}
	}
	if len(okr) == 0 {
		return WarpProfile{}, false
	}
	sort.SliceStable(okr, func(i, j int) bool {
		if okr[i].Stable != okr[j].Stable {
			return okr[i].Stable > okr[j].Stable
		}
		return okr[i].LatencyMs < okr[j].LatencyMs
	})
	best := okr[0]
	for _, r := range okr {
		if r.Profile == cur && r.Stable == best.Stable && float64(r.LatencyMs) <= float64(best.LatencyMs)*1.25 {
			return cur, true
		}
	}
	return best.Profile, true
}

// ---- запоминание по сети ----

func warpNetKey(netID string) string {
	h := sha1.Sum([]byte(netID))
	return "warp.net." + hex.EncodeToString(h[:6]) // идентификатор содержит адрес шлюза — в базе только хэш
}

// rememberWarpProfile запоминает профиль для текущей сети.
func (c *Core) rememberWarpProfile(p WarpProfile) error {
	b, _ := json.Marshal(p)
	return c.st.SetSetting(warpNetKey(c.cfg.NetID()), string(b))
}

// warpNetUsable: идентификатор сети годится для запоминания профиля (не пустой и не «нет сети»).
func warpNetUsable(raw string) bool { return raw != "" && raw != "offline" && raw != "unknown" }

// rememberedWarpProfileIn читает профиль, запомненный для сети netID.
func rememberedWarpProfileIn(st *store.Store, netID string) (WarpProfile, bool) {
	v, ok, _ := st.Setting(warpNetKey(netID))
	if !ok || v == "" {
		return WarpProfile{}, false
	}
	var p WarpProfile
	if json.Unmarshal([]byte(v), &p) != nil || p.Endpoint == "" || p.Port == 0 {
		return WarpProfile{}, false
	}
	return p, true
}

// RememberedWarpProfile возвращает профиль, запомненный для текущей сети.
func (c *Core) RememberedWarpProfile() (WarpProfile, bool) {
	return rememberedWarpProfileIn(c.st, c.cfg.NetID())
}

// writeWarpProfile записывает профиль в настройки warp.* (ядро читает их при создании).
func writeWarpProfile(st *store.Store, p WarpProfile) {
	set := func(k, v string) { _ = st.SetSetting(k, v) }
	set(SetWarpEndpoint, p.Endpoint)
	set(SetWarpPort, strconv.Itoa(int(p.Port)))
	set(SetWarpMask, p.Mask)
	if p.MTU != 0 {
		set(SetWarpMTU, strconv.Itoa(int(p.MTU)))
	} else {
		set(SetWarpMTU, "")
	}
}

// preapplyWarpProfile вызывается при создании ядра до чтения настроек: если для текущей сети профиль уже подобран, он ставится сразу, и
// ядро не нужно пересоздавать через 20 секунд (холодный туннель WARP на каждом таком пересоздании).
func preapplyWarpProfile(st *store.Store, cfg Config) {
	if cfg.NetID == nil {
		return
	}
	if v, ok, _ := st.Setting(SetWarpAuto); ok && v == "0" {
		return
	}
	raw := cfg.NetID()
	if !warpNetUsable(raw) {
		return
	}
	if p, ok := rememberedWarpProfileIn(st, raw); ok {
		writeWarpProfile(st, p)
	}
}

// applyWarpProfile записывает профиль в настройки warp.* (если он отличается от действующего) и просит владельца ядра пересоздать его.
// Повторный вызов с тем же профилем до пересоздания ничего не делает: подбор и автоцикл иначе гонялись и давали два перезапуска подряд.
func (c *Core) applyWarpProfile(p WarpProfile) {
	c.warpApplyMu.Lock()
	if p == c.profileOf() || p == c.warpApplied {
		c.warpApplyMu.Unlock()
		return
	}
	c.warpApplied = p
	c.warpApplyMu.Unlock()
	writeWarpProfile(c.st, p)
	log.Printf("WARP: профиль для этой сети %s, ядро пересоздаётся", p)
	if c.cfg.OnWarpProfile != nil {
		go c.cfg.OnWarpProfile()
	}
}

// ResetWarpProfile убирает ручные параметры WARP (возврат к умолчанию); запомненные профили сетей остаются.
func (c *Core) ResetWarpProfile() {
	for _, k := range []string{SetWarpEndpoint, SetWarpPort, SetWarpMask, SetWarpMTU, SetWarpMaskHost, SetWarpBrowser} {
		_ = c.st.SetSetting(k, "")
	}
	if c.cfg.OnWarpProfile != nil {
		go c.cfg.OnWarpProfile()
	}
}

// ---- автоматика ----

// Настройка warp.auto: подбирать профиль под сеть сам (по умолчанию включено).
const SetWarpAuto = "warp.auto"

func (c *Core) warpAutoOn() bool {
	v, ok, _ := c.st.Setting(SetWarpAuto)
	return !ok || v != "0"
}

// warpAutoLoop при смене сети применяет запомненный для неё профиль, а если такого нет и WARP не отвечает, запускает подбор.
// Подбор не чаще раза в warpAutoEvery на сеть.
const warpAutoEvery = 6 * time.Hour

// warpAutoTick — период опроса сети в warpAutoLoop (переменная для теста).
var warpAutoTick = 20 * time.Second

func (c *Core) warpAutoLoop(ctx context.Context) {
	t := time.NewTicker(warpAutoTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !c.warpAutoOn() {
			continue
		}
		raw := c.cfg.NetID()
		if !warpNetUsable(raw) {
			continue
		}
		if p, ok := c.RememberedWarpProfile(); ok {
			if p != c.profileOf() {
				c.applyWarpProfile(p)
				return // ядро будет пересоздано, новый цикл стартует вместе с ним
			}
			continue
		}
		if !c.warpChecked.Load() || c.warpLastOK.Load() {
			continue // проверки WARP ещё не было либо он отвечает на действующем профиле: подбирать нечего
		}
		if time.Since(c.warpTriedAt(raw)) < warpAutoEvery {
			continue
		}
		c.markWarpTried(raw)
		log.Printf("WARP: подбор профиля для этой сети")
		if _, err := c.WarpScan(ctx, WarpScanOptions{Apply: true, StopAtGood: 3}); err != nil && ctx.Err() == nil {
			log.Printf("WARP: подбор профиля: %v", err)
		}
	}
}

// warpTriedKey — ключ настройки со временем последнего подбора для сети (в базе только хэш идентификатора: он содержит адрес шлюза).
func warpTriedKey(netID string) string {
	h := sha1.Sum([]byte(netID))
	return "warp.tried." + hex.EncodeToString(h[:6])
}

// warpTriedAt возвращает время последнего подбора профиля для сети (нулевое, если не было). Хранится в настройках, а не в памяти цикла:
// пересоздание ядра (включение VPN) иначе запускало бы полный перебор в сети, где WARP не отвечает ни на одном профиле.
func (c *Core) warpTriedAt(netID string) time.Time {
	v, ok, _ := c.st.Setting(warpTriedKey(netID))
	if !ok {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func (c *Core) markWarpTried(netID string) {
	_ = c.st.SetSetting(warpTriedKey(netID), strconv.FormatInt(time.Now().Unix(), 10))
}

// WarpState — что показывает окно «Cloudflare WARP».
type WarpState struct {
	Enabled    bool              `json:"enabled"`    // аккаунт WARP настроен
	Auto       bool              `json:"auto"`       // профиль подбирается под сеть сам
	Scanning   bool              `json:"scanning"`   // идёт подбор
	Down       bool              `json:"down"`       // цепочка временно отключена: WARP не отвечает
	Current    WarpProfile       `json:"current"`    // действующий профиль
	Remembered *WarpProfile      `json:"remembered"` // профиль, запомненный для текущей сети
	Manual     map[string]string `json:"manual"`     // заданные вручную параметры (пустые не показываются)
	AddrV4     string            `json:"addr_v4,omitempty"`
}

// WarpState возвращает состояние для окна WARP.
func (c *Core) WarpState() WarpState {
	st := WarpState{Enabled: c.warpOn, Auto: c.warpAutoOn(), Down: c.warpDown.Load(), Current: c.profileOf(), Manual: map[string]string{}}
	c.wsMu.Lock()
	st.Scanning = c.wsRunning
	c.wsMu.Unlock()
	if p, ok := c.RememberedWarpProfile(); ok {
		st.Remembered = &p
	}
	for _, k := range []string{SetWarpEndpoint, SetWarpPort, SetWarpMask, SetWarpMTU, SetWarpMaskHost, SetWarpBrowser} {
		if v, _, _ := c.st.Setting(k); v != "" {
			st.Manual[k] = v
		}
	}
	if c.warpAcc != nil {
		st.AddrV4 = c.warpAcc.AddrV4
	}
	return st
}

// SetWarpParams задаёт сразу несколько ручных параметров WARP (ключи warp.*): все значения проверяются до записи, чтобы ошибка в одном
// не оставила половину применённой. Пустое значение возвращает умолчание этого параметра.
func (c *Core) SetWarpParams(vals map[string]string) error {
	allowed := map[string]bool{SetWarpEndpoint: true, SetWarpPort: true, SetWarpMask: true, SetWarpMTU: true, SetWarpMaskHost: true, SetWarpBrowser: true}
	for k, v := range vals {
		if !allowed[k] {
			return fmt.Errorf("%w: неизвестный параметр %q", ErrInvalid, k)
		}
		if err := settingRules[k](v); err != nil {
			return err
		}
	}
	for k, v := range vals {
		if err := c.st.SetSetting(k, v); err != nil {
			return err
		}
	}
	c.rememberManualWarpProfile()
	return nil
}

// rememberManualWarpProfile переносит ручные параметры в профиль, запомненный для текущей сети. Иначе при следующем создании ядра
// preapplyWarpProfile записал бы поверх них ранее запомненный профиль, и «Применить» молча не действовало бы (ручной возврат на рабочий профиль
// откатывался бы при перезапуске ядра).
func (c *Core) rememberManualWarpProfile() {
	if !warpNetUsable(c.cfg.NetID()) {
		return
	}
	get := func(k string) string { v, _, _ := c.st.Setting(k); return v }
	port, err := strconv.ParseUint(get(SetWarpPort), 10, 16)
	ep := get(SetWarpEndpoint)
	if ep == "" || err != nil || port == 0 {
		return // умолчание: запоминать нечего
	}
	p := WarpProfile{Endpoint: ep, Port: uint16(port), Mask: get(SetWarpMask)}
	if p.Mask == "" {
		p.Mask = "quic"
	}
	if mtu, err := strconv.ParseUint(get(SetWarpMTU), 10, 32); err == nil {
		p.MTU = uint32(mtu)
	}
	_ = c.rememberWarpProfile(p)
}
