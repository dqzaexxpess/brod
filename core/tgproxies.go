package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"brod/store"
	"brod/tgproxy"
)

// TGProxyInfo — прокси Telegram для показа.
type TGProxyInfo struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // mtproto | socks5 | webproxy
	Mode      string    `json:"mode"` // plain | secure | faketls ("" у SOCKS5)
	Server    string    `json:"server"`
	Port      int       `json:"port"`
	Link      string    `json:"link"` // tg://… для открытия в Telegram
	Favorite  bool      `json:"favorite"`
	Own       bool      `json:"own"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	Checked   bool      `json:"checked"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
	Source    string    `json:"source,omitempty"`
}

// TGProxyList — список и сводка.
type TGProxyList struct {
	Items []TGProxyInfo `json:"items"`
	Total int           `json:"total"`
	Alive int           `json:"alive"`
}

// TGCheckReport — итог круга проверки прокси Telegram.
type TGCheckReport struct {
	Picked int           `json:"picked"`
	OK     int           `json:"ok"`
	Took   time.Duration `json:"took_ns"`
}

func tgInfo(r store.TGProxyRec) TGProxyInfo {
	p := tgproxy.Proxy{Kind: r.Kind, Server: r.Server, Port: r.Port, Secret: r.Secret, User: r.User, Pass: r.Pass}
	return TGProxyInfo{ID: r.ID, Kind: r.Kind, Mode: p.Mode(), Server: r.Server, Port: r.Port, Link: p.Link(), Favorite: r.Favorite, Own: r.Own,
		OK: r.OK, LatencyMs: r.LatencyMs, Checked: !r.Checked.IsZero(), CheckedAt: r.Checked, Source: r.Source}
}

// TGProxies возвращает прокси Telegram: kind — "" | mtproto | socks5.
func (c *Core) TGProxies(aliveOnly, favOnly bool, kind string, limit int) (TGProxyList, error) {
	recs, err := c.st.TGProxies(store.TGFilter{AliveOnly: aliveOnly, FavoritesOnly: favOnly, Kind: kind, Limit: limit})
	if err != nil {
		return TGProxyList{}, err
	}
	out := TGProxyList{Items: make([]TGProxyInfo, 0, len(recs))}
	for _, r := range recs {
		out.Items = append(out.Items, tgInfo(r))
	}
	out.Total, out.Alive, err = c.st.CountTGProxies()
	return out, err
}

// AddTGProxies добавляет свои прокси из текста (ссылки tg://proxy, t.me/proxy, tg://socks…): избранные, из базы не пропадают.
// Возвращает число добавленных; пустой результат — ошибка (ничего не распознано).
func (c *Core) AddTGProxies(text, initiator string) (int, error) {
	links := tgproxy.ExtractLinks([]byte(text))
	n := 0
	for _, l := range links {
		p, err := tgproxy.Parse(l)
		if err != nil {
			continue
		}
		if err := c.st.AddOwnTGProxy(store.TGProxyRec{ID: p.ID(), Kind: p.Kind, Server: p.Server, Port: p.Port, Secret: p.Secret, User: p.User, Pass: p.Pass}, c.cfg.Now()); err != nil {
			return n, err
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("%w: ссылок Telegram-прокси не найдено (ждём tg://proxy?…, https://t.me/proxy?… или tg://socks?…)", ErrInvalid)
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: fmt.Sprintf("прокси Telegram добавлено: %d", n)})
	c.kickTG()
	return n, nil
}

// DeleteTGProxy удаляет свой прокси.
func (c *Core) DeleteTGProxy(id string) error { return c.st.DeleteOwnTGProxy(id) }

// SetTGFavorite — избранное.
func (c *Core) SetTGFavorite(id string, on bool) error { return c.st.SetTGFavorite(id, on) }

// kickTG просит цикл проверки прокси провести круг как можно скорее.
func (c *Core) kickTG() {
	if c.tgKick == nil {
		return
	}
	select {
	case c.tgKick <- struct{}{}:
	default:
	}
}

// CheckTGProxies проверяет пачку прокси настоящим обменом с датацентром Telegram (см. пакет tgproxy). limit 0 — размер пачки по умолчанию.
func (c *Core) CheckTGProxies(ctx context.Context, limit int) (TGCheckReport, error) {
	if !c.tgProxyMu.TryLock() {
		return TGCheckReport{}, fmt.Errorf("%w: проверка прокси Telegram уже идёт", ErrBusy)
	}
	defer c.tgProxyMu.Unlock()
	// круг проверки узлов важнее и тяжелее для сети: пока он идёт, прокси Telegram ждут (не накладываем нагрузку)
	for i := 0; i < 120 && c.nodeCheckActive.Load() && ctx.Err() == nil; i++ {
		select { // ожидание прерывается остановкой сразу, а не по истечении секунды
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	start := c.cfg.Now()
	if limit <= 0 {
		limit = c.cfg.TGBatch
	}
	recs, err := c.st.TGPickForCheck(limit, start)
	if err != nil {
		return TGCheckReport{}, err
	}
	rep := TGCheckReport{Picked: len(recs)}
	c.act.update(func(a *Activity) { a.TGChecking, a.TGPicked, a.TGDone, a.TGOK = true, len(recs), 0, 0 })
	defer c.act.update(func(a *Activity) { a.TGChecking = false })
	probe := c.cfg.TGProbe
	if probe == nil {
		d := (&net.Dialer{}).DialContext
		probe = func(ctx context.Context, p tgproxy.Proxy) tgproxy.Result {
			return tgproxy.Probe(ctx, p, d, c.cfg.TGTimeout)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.cfg.TGParallel)
	for _, r := range recs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(r store.TGProxyRec) {
			defer wg.Done()
			defer func() { <-sem }()
			res := probe(ctx, tgproxy.Proxy{Kind: r.Kind, Server: r.Server, Port: r.Port, Secret: r.Secret, User: r.User, Pass: r.Pass})
			if ctx.Err() != nil {
				return // остановка: прерванная проверка результатом не считается
			}
			_ = c.st.RecordTGProbe(r.ID, res.OK, res.LatencyMs, c.cfg.Now())
			c.act.update(func(a *Activity) {
				a.TGDone++
				if res.OK {
					a.TGOK++
				}
			})
			if res.OK {
				mu.Lock()
				rep.OK++
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	rep.Took = c.cfg.Now().Sub(start)
	_, _ = c.st.PurgeTGProxies(c.cfg.Now().Add(-c.cfg.NodeRetention))
	return rep, ctx.Err()
}

func (c *Core) tgLoop(ctx context.Context) {
	timer := time.NewTimer(c.firstDelay("tgproxy", 20*time.Second, c.cfg.TGInterval)) // сразу после старта дать демону устояться; недавний проход помним
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.tgKick:
		case <-timer.C:
		}
		if !c.waitActive(ctx) {
			return
		}
		if _, err := c.CheckTGProxies(ctx, 0); err != nil && ctx.Err() == nil && !errors.Is(err, ErrBusy) {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "проверка прокси Telegram: " + err.Error()})
		} else if err == nil {
			c.markLoopRun("tgproxy")
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(c.cfg.TGInterval)
	}
}
