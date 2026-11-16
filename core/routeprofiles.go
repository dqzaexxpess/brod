package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"brod/store"
)

// Профили маршрутизации (как в Throne): несколько именованных наборов правил с переключением, обмен ссылкой. Правила активного профиля всегда
// лежат в настройке route.rules (так их читает остальной код: загрузка в TUN, резервная копия, API правил), остальные — в route.profiles.
// Переключение подменяет route.rules и перезапускает VPN (как сохранение правил).

// SetRouteProfiles — ключ настройки со списком профилей (JSON).
const SetRouteProfiles = "route.profiles"

const (
	maxRouteProfiles = 20
	maxProfileName   = 60
	defaultProfileID = "main"
	shareLinkPrefix  = "brod://route/"
	maxShareSize     = 256 << 10
)

type storedProfile struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Rules []RouteRule `json:"rules,omitempty"` // у активного профиля устарели: его правила — в route.rules
}

type profilesDoc struct {
	Active string          `json:"active"`
	List   []storedProfile `json:"list"`
}

// RouteProfileInfo — профиль для списка.
type RouteProfileInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Rules  int    `json:"rules"`
	Active bool   `json:"active"`
}

var profMu sync.Mutex // сериализует операции с профилями

func (c *Core) loadProfiles() (profilesDoc, error) {
	v, ok, err := c.st.Setting(SetRouteProfiles)
	if err != nil {
		return profilesDoc{}, err
	}
	if !ok || v == "" {
		return profilesDoc{Active: defaultProfileID, List: []storedProfile{{ID: defaultProfileID, Name: "Основной"}}}, nil
	}
	var d profilesDoc
	if err := json.Unmarshal([]byte(v), &d); err != nil || len(d.List) == 0 {
		return profilesDoc{}, fmt.Errorf("сохранённые профили повреждены")
	}
	return d, nil
}

func (c *Core) saveProfiles(d profilesDoc) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return c.st.SetSetting(SetRouteProfiles, string(b))
}

func (d profilesDoc) find(id string) int {
	for i, p := range d.List {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (d profilesDoc) nameTaken(name string) bool {
	for _, p := range d.List {
		if strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}

// RouteProfiles возвращает профили и активный.
func (c *Core) RouteProfiles() ([]RouteProfileInfo, error) {
	profMu.Lock()
	defer profMu.Unlock()
	d, err := c.loadProfiles()
	if err != nil {
		return nil, err
	}
	cur, err := c.RouteRules()
	if err != nil {
		return nil, err
	}
	out := make([]RouteProfileInfo, len(d.List))
	for i, p := range d.List {
		n := len(p.Rules)
		if p.ID == d.Active {
			n = len(cur)
		}
		out[i] = RouteProfileInfo{ID: p.ID, Name: p.Name, Rules: n, Active: p.ID == d.Active}
	}
	return out, nil
}

func cleanProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxProfileName {
		return "", fmt.Errorf("%w: название профиля: от 1 до %d символов", ErrInvalid, maxProfileName)
	}
	return name, nil
}

// profileRules — правила профиля (у активного — текущие).
func (c *Core) profileRules(d profilesDoc, id string) ([]RouteRule, error) {
	i := d.find(id)
	if i < 0 {
		return nil, fmt.Errorf("%w: профиль %q не найден", store.ErrNotFound, id)
	}
	if id == d.Active {
		return c.RouteRules()
	}
	return d.List[i].Rules, nil
}

func cloneRules(rs []RouteRule) []RouteRule {
	b, _ := json.Marshal(rs)
	var out []RouteRule
	_ = json.Unmarshal(b, &out)
	return out
}

// CreateRouteProfile создаёт профиль: пустой или копией from. Не активирует.
func (c *Core) CreateRouteProfile(name, from, initiator string) (RouteProfileInfo, error) {
	profMu.Lock()
	defer profMu.Unlock()
	name, err := cleanProfileName(name)
	if err != nil {
		return RouteProfileInfo{}, err
	}
	d, err := c.loadProfiles()
	if err != nil {
		return RouteProfileInfo{}, err
	}
	if len(d.List) >= maxRouteProfiles {
		return RouteProfileInfo{}, fmt.Errorf("%w: профилей больше %d", ErrInvalid, maxRouteProfiles)
	}
	if d.nameTaken(name) {
		return RouteProfileInfo{}, fmt.Errorf("%w: профиль «%s» уже есть", ErrInvalid, name)
	}
	var rules []RouteRule
	if from != "" {
		if rules, err = c.profileRules(d, from); err != nil {
			return RouteProfileInfo{}, err
		}
		rules = regenIDs(cloneRules(rules))
	}
	return c.addProfile(d, name, rules, initiator)
}

func regenIDs(rs []RouteRule) []RouteRule {
	for i := range rs {
		rs[i].ID = newRuleID()
	}
	return rs
}

func (c *Core) addProfile(d profilesDoc, name string, rules []RouteRule, initiator string) (RouteProfileInfo, error) {
	id := newRuleID()
	d.List = append(d.List, storedProfile{ID: id, Name: name, Rules: rules})
	if err := c.saveProfiles(d); err != nil {
		return RouteProfileInfo{}, err
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: fmt.Sprintf("%s: профиль «%s», %d правил", SetRouteProfiles, name, len(rules))})
	return RouteProfileInfo{ID: id, Name: name, Rules: len(rules)}, nil
}

// RenameRouteProfile переименовывает профиль.
func (c *Core) RenameRouteProfile(id, name, initiator string) error {
	profMu.Lock()
	defer profMu.Unlock()
	name, err := cleanProfileName(name)
	if err != nil {
		return err
	}
	d, err := c.loadProfiles()
	if err != nil {
		return err
	}
	i := d.find(id)
	if i < 0 {
		return fmt.Errorf("%w: профиль %q не найден", store.ErrNotFound, id)
	}
	for j, p := range d.List {
		if j != i && strings.EqualFold(p.Name, name) {
			return fmt.Errorf("%w: профиль «%s» уже есть", ErrInvalid, name)
		}
	}
	d.List[i].Name = name
	return c.saveProfiles(d)
}

// DeleteRouteProfile удаляет неактивный профиль.
func (c *Core) DeleteRouteProfile(id, initiator string) error {
	profMu.Lock()
	defer profMu.Unlock()
	d, err := c.loadProfiles()
	if err != nil {
		return err
	}
	i := d.find(id)
	if i < 0 {
		return fmt.Errorf("%w: профиль %q не найден", store.ErrNotFound, id)
	}
	if id == d.Active {
		return fmt.Errorf("%w: активный профиль удалить нельзя (сначала переключитесь на другой)", ErrInvalid)
	}
	d.List = append(d.List[:i], d.List[i+1:]...)
	return c.saveProfiles(d)
}

// ActivateRouteProfile делает профиль активным: правила текущего сохраняются в нём, route.rules заменяется правилами выбранного. Применение —
// перезапуском VPN (делает вызывающий, как после PutRouteRules). При ошибке всё остаётся как было.
func (c *Core) ActivateRouteProfile(id, initiator string) error {
	profMu.Lock()
	defer profMu.Unlock()
	d, err := c.loadProfiles()
	if err != nil {
		return err
	}
	i := d.find(id)
	if i < 0 {
		return fmt.Errorf("%w: профиль %q не найден", store.ErrNotFound, id)
	}
	if id == d.Active {
		return nil
	}
	cur, err := c.RouteRules()
	if err != nil {
		return err
	}
	prev := d
	prev.List = append([]storedProfile(nil), d.List...)
	next := d
	next.List = append([]storedProfile(nil), d.List...)
	next.List[next.find(d.Active)].Rules = cloneRules(cur)
	target := cloneRules(next.List[i].Rules)
	next.List[i].Rules = nil
	next.Active = id
	if err := c.saveProfiles(next); err != nil {
		return err
	}
	if err := c.PutRouteRules(target, initiator); err != nil {
		_ = c.saveProfiles(prev)
		return err
	}
	return nil
}

// ---- обмен ссылкой ----

type shareDoc struct {
	V     int         `json:"v"`
	Name  string      `json:"name"`
	Rules []RouteRule `json:"rules"`
}

// ShareRouteProfile возвращает ссылку brod://route/… с правилами профиля. В ссылку идут только правила (без id): ни токенов, ни секретов в них нет.
func (c *Core) ShareRouteProfile(id string) (string, error) {
	profMu.Lock()
	defer profMu.Unlock()
	d, err := c.loadProfiles()
	if err != nil {
		return "", err
	}
	rules, err := c.profileRules(d, id)
	if err != nil {
		return "", err
	}
	if len(rules) == 0 {
		return "", fmt.Errorf("%w: в профиле нет правил, делиться нечем (импорт принимает от 1 правила)", ErrInvalid)
	}
	rules = cloneRules(rules)
	for i := range rules {
		rules[i].ID = ""
	}
	b, err := json.Marshal(shareDoc{V: 1, Name: d.List[d.find(id)].Name, Rules: rules})
	if err != nil {
		return "", err
	}
	return shareLinkPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ImportRouteProfile разбирает ссылку brod://route/… или JSON и создаёт из него профиль (не активирует: сначала посмотреть правила).
// Чужие правила проходят ту же проверку, что и свои; id выдаются заново.
func (c *Core) ImportRouteProfile(text, name, initiator string) (RouteProfileInfo, error) {
	text = strings.TrimSpace(text)
	if len(text) > maxShareSize {
		return RouteProfileInfo{}, fmt.Errorf("%w: слишком большой профиль", ErrInvalid)
	}
	raw := []byte(text)
	if strings.HasPrefix(text, shareLinkPrefix) {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(text, shareLinkPrefix), "="))
		if err != nil {
			return RouteProfileInfo{}, fmt.Errorf("%w: ссылка повреждена", ErrInvalid)
		}
		raw = b
	}
	var doc shareDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		var bare []RouteRule // допускаем и голый список правил
		d2 := json.NewDecoder(bytes.NewReader(raw))
		d2.DisallowUnknownFields()
		if err2 := d2.Decode(&bare); err2 != nil {
			return RouteProfileInfo{}, fmt.Errorf("%w: не профиль маршрутизации: %v", ErrInvalid, err)
		}
		doc = shareDoc{V: 1, Rules: bare}
	}
	if doc.V != 1 {
		return RouteProfileInfo{}, fmt.Errorf("%w: версия профиля %d не поддерживается", ErrInvalid, doc.V)
	}
	if len(doc.Rules) == 0 || len(doc.Rules) > maxRouteRules {
		return RouteProfileInfo{}, fmt.Errorf("%w: в профиле должно быть от 1 до %d правил", ErrInvalid, maxRouteRules)
	}
	for i := range doc.Rules {
		doc.Rules[i].ID = newRuleID()
		if err := doc.Rules[i].validate(); err != nil {
			return RouteProfileInfo{}, fmt.Errorf("%w: правило %d: %v", ErrInvalid, i+1, err)
		}
	}
	profMu.Lock()
	defer profMu.Unlock()
	if strings.TrimSpace(name) == "" {
		name = doc.Name
	}
	if strings.TrimSpace(name) == "" {
		name = "Импортированный"
	}
	name, err := cleanProfileName(name)
	if err != nil {
		return RouteProfileInfo{}, err
	}
	d, err := c.loadProfiles()
	if err != nil {
		return RouteProfileInfo{}, err
	}
	if len(d.List) >= maxRouteProfiles {
		return RouteProfileInfo{}, fmt.Errorf("%w: профилей больше %d", ErrInvalid, maxRouteProfiles)
	}
	base := name
	for n := 2; d.nameTaken(name); n++ {
		name = fmt.Sprintf("%s (%d)", base, n)
	}
	return c.addProfile(d, name, doc.Rules, initiator)
}

// ---- резервная копия ----

// BackupProfile — неактивный профиль маршрутизации в копии (правила активного лежат в Backup.Rules).
type BackupProfile struct {
	Name  string      `json:"name"`
	Rules []RouteRule `json:"rules,omitempty"`
}

// backupProfiles возвращает имя активного профиля и остальные профили; одного профиля без имени по умолчанию в копию не кладём.
func (c *Core) backupProfiles() (active string, others []BackupProfile, err error) {
	profMu.Lock()
	defer profMu.Unlock()
	d, err := c.loadProfiles()
	if err != nil {
		return "", nil, err
	}
	for _, p := range d.List {
		if p.ID == d.Active {
			active = p.Name
			continue
		}
		others = append(others, BackupProfile{Name: p.Name, Rules: p.Rules})
	}
	return active, others, nil
}

// restoreProfiles заменяет неактивные профили профилями из копии (правила проверяются; негодные профили пропускаются с пояснением).
func (c *Core) restoreProfiles(activeName string, others []BackupProfile) (skipped []string, err error) {
	profMu.Lock()
	defer profMu.Unlock()
	d, err := c.loadProfiles()
	if err != nil {
		return nil, err
	}
	ai := d.find(d.Active)
	if n, e := cleanProfileName(activeName); e == nil {
		d.List[ai].Name = n
	}
	list := []storedProfile{d.List[ai]}
	for _, bp := range others {
		if len(list) >= maxRouteProfiles {
			skipped = append(skipped, "профиль «"+bp.Name+"»: слишком много профилей")
			continue
		}
		name, e := cleanProfileName(bp.Name)
		if e != nil {
			skipped = append(skipped, "профиль без названия пропущен")
			continue
		}
		if (profilesDoc{List: list}).nameTaken(name) {
			skipped = append(skipped, "профиль «"+name+"»: такое название уже есть")
			continue
		}
		rules := regenIDs(cloneRules(bp.Rules))
		ok := len(rules) <= maxRouteRules
		for i := range rules {
			if e := rules[i].validate(); e != nil {
				skipped = append(skipped, fmt.Sprintf("профиль «%s», правило %d: %v", name, i+1, e))
				ok = false
				break
			}
		}
		if ok {
			list = append(list, storedProfile{ID: newRuleID(), Name: name, Rules: rules})
		}
	}
	d.List = list
	return skipped, c.saveProfiles(d)
}

// FetchProfileText скачивает профиль по https-адресу (JSON профиля или ссылка brod://route/… в тексте): напрямую, при сбое через активный узел.
// Размер ограничен maxShareSize. Содержимое разбирает ImportRouteProfile тем же строгим разбором, что и ссылку; профиль не активируется и
// сам не обновляется (повторный импорт создаёт новый профиль): чужой адрес не должен молча менять маршрутизацию.
func (c *Core) FetchProfileText(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%w: нужен https-адрес без логина и пароля", ErrInvalid)
	}
	get := func(hc *http.Client) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxShareSize+1))
		if err != nil {
			return "", err
		}
		if len(b) > maxShareSize {
			return "", fmt.Errorf("профиль больше %d КиБ", maxShareSize>>10)
		}
		return string(b), nil
	}
	client := c.cfg.RuleSetClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	text, err := get(client)
	if err == nil || ctx.Err() != nil || c.cfg.RuleSetClient != nil {
		if err != nil {
			err = fmt.Errorf("%w: профиль не скачался: %v", ErrInvalid, err)
		}
		return text, err
	}
	c.mu.Lock()
	hasActive := c.selected[store.ScopeActive] != ""
	c.mu.Unlock()
	if hasActive {
		if t2, err2 := get(c.proxyClient(30 * time.Second)); err2 == nil {
			return t2, nil
		}
	}
	return "", fmt.Errorf("%w: профиль не скачался: %v", ErrInvalid, err)
}
