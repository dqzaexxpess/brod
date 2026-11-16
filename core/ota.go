package core

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"brod/aggregator"
	"brod/ota"
	"brod/store"
)

// SetOTASources — автообновление списка источников подписок из подписанного документа издателя (пакет ota): 1 (по умолчанию) или 0.
const SetOTASources = "ota.sources"

const settingOTAVersion = "ota.sources.version"

// Где издатель публикует список (sources.json и sources.json.sig): основной адрес и зеркало jsDelivr — так же качаются встроенные источники.
// Репозиторий публичный и содержит только подписанные документы; без подписи ключом издателя клиент ничего не принимает.
var defaultOTASourcesURLs = []string{
	"https://raw.githubusercontent.com/dqzaexxpess/brod-releases/main/" + ota.SourcesFile,
	"https://cdn.jsdelivr.net/gh/dqzaexxpess/brod-releases@main/" + ota.SourcesFile,
	// второй хостинг, независимый от GitHub: тот же подписанный документ в репозитории GitLab
	"https://gitlab.com/misavladimirov120/brod-releases/-/raw/main/" + ota.SourcesFile,
}

// OTAResult — итог обновления списка источников.
type OTAResult struct {
	Version  int    `json:"version"`           // версия документа издателя
	Applied  bool   `json:"applied"`           // список применён (версия новее прежней)
	Added    int    `json:"added"`             // новых источников
	Updated  int    `json:"updated"`           // источников с изменившимся адресом, зеркалом или приоритетом
	Disabled int    `json:"disabled"`          // снятых издателем (отключены у клиента)
	Skipped  int    `json:"skipped"`           // совпали по имени с добавленными пользователем — не тронуты
	Message  string `json:"message,omitempty"` // пояснение для человека
}

// UpdateOTASources забирает подписанный список источников и применяет его к «управляемым» источникам (добавленные пользователем не
// трогаются, отключённый пользователем остаётся отключённым). Документ принимается только с верной подписью, https-адресами и версией
// новее применённой. Сначала прямой путь, при сбое — через вход приложений (активный узел).
func (c *Core) UpdateOTASources(ctx context.Context, initiator string) (OTAResult, error) {
	urls := c.cfg.OTASourcesURLs
	if len(urls) == 0 {
		urls = defaultOTASourcesURLs
	}
	key := c.cfg.OTAKey
	if len(key) == 0 {
		key = ota.DefaultKey()
	}
	doc, sig, err := c.fetchOTA(ctx, urls, false, c.sourcesCheck(key))
	if err != nil {
		return OTAResult{}, fmt.Errorf("список источников: %w", err)
	}
	return c.applyOTASources(doc, sig, key, initiator)
}

func (c *Core) applyOTASources(doc, sig []byte, key ed25519.PublicKey, initiator string) (OTAResult, error) {
	if err := ota.Verify(key, doc, sig); err != nil {
		return OTAResult{}, fmt.Errorf("список источников отвергнут: %w", err)
	}
	d, err := ota.ParseSources(doc)
	if err != nil {
		return OTAResult{}, fmt.Errorf("список источников отвергнут: %w", err)
	}
	for _, e := range d.Sources {
		if err := aggregator.ValidName(e.Name); err != nil {
			return OTAResult{}, fmt.Errorf("список источников отвергнут: %q: %w", e.Name, err)
		}
	}
	res := OTAResult{Version: d.Version}
	cur := 0
	if v, ok, _ := c.st.Setting(settingOTAVersion); ok {
		cur, _ = strconv.Atoi(v)
	}
	if d.Version <= cur {
		res.Message = fmt.Sprintf("список актуален (версия %d)", cur)
		return res, nil
	}
	have, err := c.st.Sources()
	if err != nil {
		return res, err
	}
	byID := make(map[string]store.SourceRec, len(have))
	for _, r := range have {
		byID[r.ID] = r
	}
	now := c.cfg.Now()
	for _, e := range d.Sources {
		ex, found := byID[e.Name]
		switch {
		case found && ex.UserAdded:
			res.Skipped++
		case found && ex.URL == e.URL && ex.Mirror == e.Mirror && ex.Priority == e.Priority:
		default:
			if err := c.st.UpsertSource(store.SourceRec{ID: e.Name, URL: e.URL, Mirror: e.Mirror, Priority: e.Priority, Enabled: true}, now); err != nil {
				return res, err
			}
			if found {
				res.Updated++
			} else {
				res.Added++
			}
		}
	}
	for _, name := range d.Retired {
		if ex, found := byID[name]; found && !ex.UserAdded && ex.Enabled {
			if err := c.st.SetSourceEnabled(name, false); err != nil {
				return res, err
			}
			res.Disabled++
		}
	}
	if err := c.st.SetSetting(settingOTAVersion, strconv.Itoa(d.Version)); err != nil {
		return res, err
	}
	res.Applied = true
	res.Message = fmt.Sprintf("версия %d: новых %d, изменено %d, снято %d", d.Version, res.Added, res.Updated, res.Disabled)
	c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: "список источников обновлён: " + res.Message})
	if res.Added > 0 && c.baseCtx != nil {
		c.goLoop("ota-refresh", func() { _, _ = c.Refresh(c.baseCtx, "auto") }) // новые источники подтягиваем сразу, а не через полчаса
	}
	return res, nil
}

// errHTTPNotFound — адрес документа отвечает «нет такого файла» (репозиторий достижим, документа в нём нет, например релизов ещё не публиковали).
var errHTTPNotFound = errors.New("документа нет на сервере")

// otaCheck проверяет скачанные документ и подпись (подпись, разбор, версия не старше виденной); ошибка означает «этот адрес не годится,
// пробуй следующий». nil — не проверять.
type otaCheck func(doc, sig []byte) error

// fetchOTA скачивает документ и подпись: прямым путём, а если не вышло — через вход приложений (активный узел). check выполняется
// внутри перебора адресов: устаревшее зеркало или рассинхрон документа с подписью не мешают взять свежий документ с другого адреса.
func (c *Core) fetchOTA(ctx context.Context, urls []string, loopback bool, check otaCheck) (doc, sig []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	direct := c.cfg.OTAClient
	if direct == nil {
		direct = &http.Client{Timeout: 20 * time.Second}
	}
	doc, sig, err = fetchPair(ctx, direct, urls, loopback, check)
	if err == nil {
		return doc, sig, nil
	}
	if errors.Is(err, errHTTPNotFound) {
		return nil, nil, err // сервер достижим и ответил «нет»: через узел спрашивать то же самое незачем
	}
	if st, serr := c.Status(); serr == nil && st.Active != nil {
		addr, user, pass := c.AppProxy()
		u := &url.URL{Scheme: "http", User: url.UserPassword(user, pass), Host: addr}
		via := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
		if d2, s2, err2 := fetchPair(ctx, via, urls, loopback, check); err2 == nil {
			return d2, s2, nil
		} else {
			err = fmt.Errorf("%w; через активный узел: %v", err, err2)
		}
	}
	return nil, nil, err
}

// errOTARejected — документ скачался, но не прошёл проверку (подпись, разбор, откат). Такая ошибка важнее поздней 404 с другого адреса.
type errOTARejected struct{ err error }

func (e errOTARejected) Error() string { return e.err.Error() }
func (e errOTARejected) Unwrap() error { return e.err }

// fetchPair пробует адреса по очереди (основной, зеркало): документ и его подпись берутся с одного и того же адреса и должны пройти check;
// иначе берётся следующий адрес. Если не подошёл ни один, возвращается ошибка отказа проверки (если она была), иначе последняя ошибка сети.
func fetchPair(ctx context.Context, hc *http.Client, urls []string, loopback bool, check otaCheck) (doc, sig []byte, err error) {
	var last, rejected error
	for _, u := range urls {
		d, e1 := fetchLimited(ctx, hc, u, ota.MaxDocBytes, loopback)
		if e1 != nil {
			last = e1
			continue
		}
		s, e2 := fetchLimited(ctx, hc, u+".sig", 4096, loopback)
		if e2 != nil {
			last = e2
			continue
		}
		if check != nil {
			if ce := check(d, s); ce != nil {
				rejected = errOTARejected{fmt.Errorf("%s: %w", otaHost(u), ce)}
				continue
			}
		}
		return d, s, nil
	}
	if rejected != nil {
		return nil, nil, rejected
	}
	return nil, nil, last
}

func otaHost(u string) string {
	if pu, err := url.Parse(u); err == nil && pu.Host != "" {
		return pu.Host
	}
	return u
}

// httpsOnlyRedirects возвращает копию клиента, который не следует перенаправлению на не-https (кроме http на адрес этой машины при
// loopback=true): иначе хостинг или зеркало могли бы увести запрос на открытый http. Свой CheckRedirect клиента сохраняется.
func httpsOnlyRedirects(hc *http.Client, loopback bool) *http.Client {
	cp := *hc
	prev := hc.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" && !(loopback && req.URL.Scheme == "http" && ota.IsLoopbackHost(req.URL.Hostname())) {
			return fmt.Errorf("перенаправление на %s отклонено: нужен https-адрес", req.URL.Redacted())
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 10 {
			return errors.New("слишком много перенаправлений")
		}
		return nil
	}
	return &cp
}

func fetchLimited(ctx context.Context, hc *http.Client, u string, limit int64, loopback bool) ([]byte, error) {
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "https" && !(loopback && pu.Scheme == "http" && ota.IsLoopbackHost(pu.Hostname()))) {
		return nil, fmt.Errorf("%s: нужен https-адрес", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpsOnlyRedirects(hc, loopback).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s: HTTP 404", errHTTPNotFound, pu.Host)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", pu.Host, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: ответ больше %d байт", pu.Host, limit)
	}
	return b, nil
}

// sourcesCheck — проверка списка источников для fetchOTA: подпись, разбор и версия не старше применённой (равная допустима: «актуально»).
func (c *Core) sourcesCheck(key ed25519.PublicKey) otaCheck {
	return func(doc, sig []byte) error {
		if err := ota.Verify(key, doc, sig); err != nil {
			return err
		}
		d, err := ota.ParseSources(doc)
		if err != nil {
			return err
		}
		cur := 0
		if v, ok, _ := c.st.Setting(settingOTAVersion); ok {
			cur, _ = strconv.Atoi(v)
		}
		if d.Version < cur {
			return fmt.Errorf("версия %d старее применённой %d", d.Version, cur)
		}
		return nil
	}
}

func (c *Core) otaEnabled() bool {
	v, ok, _ := c.st.Setting(SetOTASources)
	return !ok || v != "0"
}

// otaLoop раз в сутки обновляет список источников (первый раз через несколько минут после старта: сначала должны пройти обычные подписки).
// Сбой записывается в журнал событий один раз на каждую новую причину: недоступный репозиторий не засоряет журнал.
func (c *Core) otaLoop(ctx context.Context) {
	timer := time.NewTimer(c.firstDelay("ota", c.cfg.OTAFirstDelay, c.cfg.OTAInterval))
	defer timer.Stop()
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if c.otaEnabled() {
			if _, err := c.UpdateOTASources(ctx, "auto"); err != nil && ctx.Err() == nil {
				if msg := err.Error(); msg != lastErr {
					lastErr = msg
					c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: msg})
				}
			} else if err == nil {
				lastErr = ""
				c.markLoopRun("ota")
			}
		}
		timer.Reset(c.cfg.OTAInterval)
	}
}
