package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/store"
	"brod/warp"
)

// SetViaWarp — настройка «проверять и подключать узлы цепочкой через WARP» (`detour: warp`). Помогает, когда узлы недоступны
// напрямую (прямой путь: живых 0,4–1%, через WARP на общих узлах ×2,6).
const SetViaWarp = "check.via_warp"

// WarpEnabled сообщает, настроен ли WARP.
func (c *Core) WarpEnabled() bool { return c.warpOn }

// viaWarp: включена ли цепочка через WARP (нужен настроенный аккаунт). По умолчанию включена (решение пользователя 2026-11-03:
// демон становится основным клиентом; цепочка WARP → узел заметно увеличивает число живых узлов); явное «0» в настройках выключает.
func (c *Core) viaWarp() bool {
	if !c.warpOn {
		return false
	}
	if c.warpDown.Load() {
		return false // WARP не отвечает: цепочка через него оставила бы все узлы «мёртвыми» (см. warpLoop)
	}
	// значение держим в памяти: viaWarp зовут с каждого круга проверок (netKey), а чтение из базы при занятом единственном соединении
	// держало остановку ядра. Сбрасывается в SetSetting.
	switch c.viaWarpMem.Load() {
	case viaWarpOn:
		return true
	case viaWarpOff:
		return false
	}
	v, ok, err := c.st.Setting(SetViaWarp)
	on := !ok || v != "0"
	if err == nil {
		if on {
			c.viaWarpMem.Store(viaWarpOn)
		} else {
			c.viaWarpMem.Store(viaWarpOff)
		}
	}
	return on
}

// warpFastWindow и warpFastEvery: частые проверки WARP сразу после старта ядра.
const warpFastWindow = 2 * time.Minute

var warpFastEvery = 5 * time.Second

// warpColdGrace: сколько после создания ядра провалы проверки WARP не засчитываются, если на этой сети он недавно отвечал. Каждое включение и выключение
// VPN создаёт ядро заново, а туннель WARP начинается с нуля; первая проверка по холодному туннелю проваливается в половине запусков (журналы 2026-11-11:
// 17 из 40 успешны на телефоне, 9 из 15 на компьютере), а ещё через несколько секунд тот же WARP отвечает. Два провала подряд раньше отключали цепочку,
// и активный узел перерегистрировался напрямую, где живо около 1% узлов: после каждого переключения VPN выходила «деградация». Если же WARP на сети
// не отвечал или не проверялся, вердикт прежний (два провала).
var warpColdGrace = 30 * time.Second

// warpMemo помнит по сети (и каталогу состояния), когда WARP последний раз отвечал и когда был признан упавшим. Живёт дольше ядра: ядро пересоздаётся
// при каждом переключении VPN, процесс остаётся.
var warpMemo = struct {
	sync.Mutex
	m map[string]warpMemoRec
}{m: map[string]warpMemoRec{}}

type warpMemoRec struct{ okAt, downAt time.Time }

// warpKnownGoodWindow: насколько свежим должен быть последний успех, чтобы холодный провал считался задержкой, а не поломкой.
const warpKnownGoodWindow = 30 * time.Minute

func (c *Core) warpMemoKey() string { return c.cfg.StateDir + "|" + c.cfg.NetID() }

func (c *Core) warpMemoSet(f func(*warpMemoRec)) {
	k := c.warpMemoKey()
	warpMemo.Lock()
	r := warpMemo.m[k]
	f(&r)
	warpMemo.m[k] = r
	warpMemo.Unlock()
}

// warpColdAndKnownGood: ядро молодое, а на этой сети WARP недавно отвечал и с тех пор упавшим не признавался.
func (c *Core) warpColdAndKnownGood() bool {
	if time.Since(c.startedAt) >= warpColdGrace {
		return false
	}
	warpMemo.Lock()
	r := warpMemo.m[c.warpMemoKey()]
	warpMemo.Unlock()
	return !r.okAt.IsZero() && time.Since(r.okAt) < warpKnownGoodWindow && !r.downAt.After(r.okAt)
}

const (
	viaWarpUnknown int32 = iota
	viaWarpOn
	viaWarpOff
)

// warpLoop раз в WarpWatch проверяет сам WARP (рукопожатие и HTTPS через его endpoint). Пока цепочка включена, WARP — единственный путь ко всем
// узлам: если он перестал отвечать (замер: сеть режет его UDP, `sendmsg: input/output error`), все узлы выглядят мёртвыми, а поиск
// альтернатив идёт тем же путём и пуст. Два провала подряд → цепочка отключается (активный узел перерегистрируется напрямую), два успеха подряд
// → включается снова. Настройка check.via_warp при этом не меняется: это временный обход, а не выбор пользователя.
func (c *Core) warpLoop(ctx context.Context) {
	// в первые warpFastWindow после старта WARP проверяется чаще (warpFastEvery): туннель холодный, и «упал» или «поднялся» выясняется за секунды,
	// а не за пару обычных периодов; цепочка не висит на мёртвом WARP, а восстановленный WARP возвращается в работу раньше
	started := time.Now()
	t := time.NewTicker(min(c.cfg.WarpWatch, warpFastEvery))
	defer t.Stop()
	last := time.Now()
	// первая проверка сразу после старта: новое ядро поднимает новый WireGuard-туннель, и его рукопожатие (на нестабильных сетях оно занимает
	// секунды) лучше начать до первого запроса пользователя; длительность пишется в журнал
	first := time.NewTimer(500 * time.Millisecond)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			c.warpCheck(ctx)
			last = time.Now()
		case <-t.C:
			every := c.cfg.WarpWatch
			if time.Since(started) < warpFastWindow {
				every = min(every, warpFastEvery)
			}
			if time.Since(last) >= every {
				c.warpCheck(ctx)
				last = time.Now()
			}
		}
	}
}

func (c *Core) warpCheck(ctx context.Context) {
	if !c.warpOn {
		return
	}
	if v, ok, _ := c.st.Setting(SetViaWarp); ok && v == "0" { // пользователь выключил цепочку сам — следить не за чем
		c.warpFails, c.warpOKs = 0, 0
		c.warpDown.Store(false)
		return
	}
	p, err := c.eng.Existing(OutWarp)
	if err != nil {
		return
	}
	// Первая проверка после старта ядра идёт по ещё не согретому туннелю: на телефоне 3 из 5 запусков упёрлись в таймаут 6 с против ~450 мс
	// у остальных (повтор рукопожатия WireGuard наступает через 5 с). Окно шире, чтобы такой запуск не засчитывался в провал.
	limit := c.cfg.HealthTimeout * 2
	if !c.warpFirstLogged {
		limit = c.cfg.HealthTimeout * 4
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	live := c.cfg.WarpLiveness
	if live == nil {
		// потолок самой проверки равен окну (иначе окно шире L2Timeout=6 с ничего не давало: холодный туннель на LTE отвечает за 4–10 с)
		live = func(ctx context.Context, p *engine.Probe) prober.Result { return prober.L2Within(ctx, p, limit) }
	}
	t0 := time.Now()
	r := live(cctx, p)
	if ctx.Err() != nil || r.Class == prober.ClassCanceled {
		return
	}
	if !c.warpFirstLogged {
		c.warpFirstLogged = true
		log.Printf("WARP: первая проверка после старта ядра: ok=%v, %s, класс %q", r.OK, time.Since(t0).Round(10*time.Millisecond), r.Class)
		c.settleWarpTrial(r.OK)
	}
	c.warpChecked.Store(true)
	c.warpLastOK.Store(r.OK)
	if r.OK {
		c.warpMemoSet(func(m *warpMemoRec) { m.okAt = time.Now() })
		c.warpFails = 0
		if c.warpDown.Load() {
			c.warpOKs++
			if c.warpOKs >= 2 {
				c.warpOKs = 0
				c.warpDown.Store(false)
				c.bus.Publish(store.Event{Kind: EvWarpUp, Initiator: "auto", Detail: "WARP снова отвечает, цепочка включена для новых подключений"})
			}
		}
		return
	}
	c.warpOKs = 0
	if c.warpColdAndKnownGood() {
		return // холодный туннель на сети, где WARP только что работал: ждём рукопожатия, цепочку не трогаем
	}
	c.warpFails++
	if c.warpFails >= 2 && !c.warpDown.Load() {
		c.warpDown.Store(true)
		c.warpMemoSet(func(m *warpMemoRec) { m.downAt = time.Now() })
		c.bus.Publish(store.Event{Kind: EvWarpDown, Initiator: "auto", Detail: detail(map[string]any{"class": string(r.Class)})})
		c.rechainActive()
	}
}

// rechainActive перерегистрирует активный узел без цепочки через WARP: новый outbound под тегом с суффиксом «-d», switch переводится на него,
// прежний убирается. Если на прежний ещё что-то смотрит (цели), он остаётся в движке — это безвредно.
func (c *Core) rechainActive() {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.selected[store.ScopeActive]
	if id == "" || id == store.SelWarp || id == store.SelDirect {
		return
	}
	oldTag, ok := c.reg[id]
	if !ok {
		return
	}
	rec, err := c.st.Node(id)
	if err != nil || rec.Endpoint {
		return
	}
	newTag := NodeTag(id) + "-d"
	raw, err := outboundJSON(rec, newTag, false)
	if err != nil {
		return
	}
	if _, err := c.eng.Add(raw); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Node: id, Detail: "обход WARP: " + err.Error()})
		return
	}
	if err := c.eng.SetSwitchTarget(OutActive, newTag); err != nil {
		_ = c.eng.Remove(newTag)
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Node: id, Detail: "обход WARP: " + err.Error()})
		return
	}
	c.reg[id] = newTag
	_ = c.eng.Remove(oldTag)
	c.resetHealth() // отсчёт деградации — заново: прежние провалы были из-за WARP, а не узла
}

// netKey — ключ сети для истории проверок. Результаты «через WARP» и «напрямую» — разные измерения одной сети, их нельзя смешивать
// (узел, живой только через WARP, не должен выглядеть живым при прямом подключении).
func (c *Core) netKey() string {
	n := c.cfg.NetID()
	if c.viaWarp() {
		return n + "|warp"
	}
	return n
}

// probeOpts — опции узла для пробного outbound'а: при режиме через WARP добавляется detour (тот же код, что у боевого — P2).
func (c *Core) probeOpts(rec store.NodeRec) ([]byte, error) {
	if !c.viaWarp() {
		return rec.Opts, nil
	}
	return outboundJSON(rec, "probe", true)
}

// ProbeWarp проверяет сам WARP: рукопожатие и HTTPS через endpoint (нестабильное рукопожатие у WARP — норма).
func (c *Core) ProbeWarp(ctx context.Context) (prober.Result, error) {
	if !c.warpOn {
		return prober.Result{}, fmt.Errorf("%w: WARP не настроен (нет файла аккаунта)", ErrInvalid)
	}
	p, err := c.eng.Existing(OutWarp)
	if err != nil {
		return prober.Result{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.cfg.HealthTimeout*4)
	defer cancel()
	return prober.L2(cctx, p), nil
}

// ---- ручная регистрация и импорт аккаунта WARP ----

// WarpRegisterTimeout — сколько ждать регистрацию (прямой путь плюс запасной через узел).
const WarpRegisterTimeout = 45 * time.Second

// RegisterWarp создаёт аккаунт WARP и сохраняет его в warp-account.json (права 0600). Сначала напрямую (с запасным путём без SNI), а если
// сеть режет и его, через вход приложений — то есть через активный узел, если он выбран. Уже настроенный WARP заменяется только при replace.
// Ядро собирается с аккаунтом при создании: после вызова демон пересоздаёт ядро (supervisor.Restart).
func (c *Core) RegisterWarp(ctx context.Context, replace bool) error {
	if c.warpOn && !replace {
		return fmt.Errorf("%w: WARP уже настроен (заменить: replace)", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, WarpRegisterTimeout)
	defer cancel()
	acc, err := warp.Register(ctx, nil)
	if err != nil {
		if st, serr := c.Status(); serr == nil && st.Active != nil {
			addr, user, pass := c.AppProxy()
			u := &url.URL{Scheme: "http", User: url.UserPassword(user, pass), Host: addr}
			hc := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
			if a2, err2 := warp.Register(ctx, hc); err2 == nil {
				acc, err = a2, nil
			} else {
				err = fmt.Errorf("%w; через активный узел: %v", err, err2)
			}
		}
	}
	if err != nil {
		return err
	}
	return c.saveWarpAccount(acc)
}

// ImportWarp сохраняет готовый аккаунт WARP (файл warp-account.json от `warp register` или другого устройства). Проверяет, что в нём есть
// всё нужное для подключения; уже настроенный WARP заменяется только при replace.
func (c *Core) ImportWarp(raw []byte, replace bool) error {
	if c.warpOn && !replace {
		return fmt.Errorf("%w: WARP уже настроен (заменить: replace)", ErrInvalid)
	}
	var acc warp.Account
	if err := json.Unmarshal(raw, &acc); err != nil {
		return fmt.Errorf("%w: это не файл аккаунта WARP (%v)", ErrInvalid, err)
	}
	if acc.PrivateKey == "" || acc.PeerPublicKey == "" || acc.AddrV4 == "" {
		return fmt.Errorf("%w: в аккаунте не хватает полей (нужны private_key, peer_public_key, addr_v4)", ErrInvalid)
	}
	for name, v := range map[string]string{"private_key": acc.PrivateKey, "peer_public_key": acc.PeerPublicKey} {
		if b, err := base64.StdEncoding.DecodeString(v); err != nil || len(b) != 32 {
			return fmt.Errorf("%w: %s — не ключ WireGuard (ожидается base64 из 32 байт)", ErrInvalid, name)
		}
	}
	return c.saveWarpAccount(&acc)
}

// saveWarpAccount пишет аккаунт атомарно: временный файл с правами 0600 и переименование (при обрыве прежний аккаунт цел).
func (c *Core) saveWarpAccount(acc *warp.Account) error {
	b, err := json.MarshalIndent(acc, "", " ")
	if err != nil {
		return err
	}
	tmp := c.cfg.WarpAccountFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.cfg.WarpAccountFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
