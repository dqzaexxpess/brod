package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"brod/rulesets"
	"brod/store"
)

// Свои списки (rule-set по URL). Хранятся в настройке route.usersets (JSON), файлы — rulesets/ext/<id>.srs. В правилах на них ссылаются по
// id (`ext-` и 8 знаков хеша адреса) так же, как на встроенные. Жизненный цикл: скачивание напрямую, при сбое через активный узел; формат
// проверяется парсером движка, размер ограничен, обрезанный ответ отбрасывается; прежний файл остаётся при любом сбое; обновление раз в 12 часов.

const (
	settingUserSets = "route.usersets"
	maxUserSets     = 20
)

var userSetRe = regexp.MustCompile(`^ext-[0-9a-f]{8}$`)

// UserSet — свой список.
type UserSet struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	URL     string    `json:"url"`
	Updated time.Time `json:"updated,omitzero"`
	Size    int64     `json:"size,omitempty"` // размер файла srs
	Error   string    `json:"error,omitempty"`
}

func userSetID(u string) string {
	h := sha256.Sum256([]byte(u))
	return "ext-" + hex.EncodeToString(h[:4])
}

func (c *Core) userSetPath(id string) string { return filepath.Join(c.ruleSetDir(), "ext", id+".srs") }

func loadUserSets(st *store.Store) []UserSet {
	v, ok, _ := st.Setting(settingUserSets)
	if !ok || v == "" {
		return nil
	}
	var out []UserSet
	if json.Unmarshal([]byte(v), &out) != nil {
		return nil
	}
	return out
}

func saveUserSets(st *store.Store, l []UserSet) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return st.SetSetting(settingUserSets, string(b))
}

// UserSets возвращает свои списки.
func (c *Core) UserSets() []UserSet {
	c.usMu.Lock()
	defer c.usMu.Unlock()
	return loadUserSets(c.st)
}

// userSetDefs — определения для конфига движка (только скачанные): тег → путь файла.
func userSetDefs(st *store.Store, ruleSetDir string) map[string]string {
	out := map[string]string{}
	for _, s := range loadUserSets(st) {
		p := filepath.Join(ruleSetDir, "ext", s.ID+".srs")
		if _, err := os.Stat(p); err == nil {
			out[s.ID] = p
		}
	}
	return out
}

func validUserSetURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 1000 {
		return "", fmt.Errorf("%w: нужен https-адрес без логина и пароля", ErrInvalid)
	}
	return raw, nil
}

func (c *Core) fetchUserSet(ctx context.Context, rawURL string, prev int) ([]byte, error) {
	client := c.cfg.RuleSetClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	b, err := rulesets.FetchRemote(ctx, client, rawURL, prev)
	if err == nil || ctx.Err() != nil || c.cfg.RuleSetClient != nil {
		return b, err
	}
	c.mu.Lock()
	hasActive := c.selected[store.ScopeActive] != ""
	c.mu.Unlock()
	if hasActive { // напрямую не вышло: через активный узел
		if b2, err2 := rulesets.FetchRemote(ctx, c.proxyClient(90*time.Second), rawURL, prev); err2 == nil {
			return b2, nil
		} else {
			err = fmt.Errorf("%v; через активный узел: %v", err, err2)
		}
	}
	return nil, err
}

// AddUserSet добавляет список: он должен скачаться и пройти проверку сразу (иначе не добавляется).
func (c *Core) AddUserSet(ctx context.Context, rawURL, title, initiator string) (UserSet, error) {
	rawURL, err := validUserSetURL(rawURL)
	if err != nil {
		return UserSet{}, err
	}
	c.usMu.Lock()
	defer c.usMu.Unlock()
	cur := loadUserSets(c.st)
	id := userSetID(rawURL)
	for _, s := range cur {
		if s.ID == id {
			return s, fmt.Errorf("%w: такой список уже добавлен", ErrInvalid)
		}
	}
	if len(cur) >= maxUserSets {
		return UserSet{}, fmt.Errorf("%w: не больше %d своих списков", ErrInvalid, maxUserSets)
	}
	b, err := c.fetchUserSet(ctx, rawURL, 0)
	if err != nil {
		return UserSet{}, fmt.Errorf("%w: список не скачался или не прошёл проверку: %v", ErrInvalid, err)
	}
	if err := os.MkdirAll(filepath.Dir(c.userSetPath(id)), 0o700); err != nil {
		return UserSet{}, err
	}
	if err := rulesets.WriteFile(c.userSetPath(id), b); err != nil {
		return UserSet{}, err
	}
	if t := strings.TrimSpace(title); len([]rune(t)) > 80 {
		title = string([]rune(t)[:80])
	}
	s := UserSet{ID: id, Title: strings.TrimSpace(title), URL: rawURL, Updated: c.cfg.Now(), Size: int64(len(b))}
	if err := saveUserSets(c.st, append(cur, s)); err != nil {
		_ = os.Remove(c.userSetPath(id))
		return UserSet{}, err
	}
	c.bus.Publish(store.Event{Kind: EvRuleSets, Initiator: initiator, Detail: detail(map[string]any{"added": id})})
	return s, nil
}

// RemoveUserSet удаляет список, если на него не ссылается ни одно правило.
func (c *Core) RemoveUserSet(id, initiator string) error {
	rules, err := loadRouteRules(c.st)
	if err != nil {
		return err
	}
	for _, r := range rules {
		for _, s := range r.Match.RuleSet {
			if s == id {
				return fmt.Errorf("%w: на список ссылается правило «%s»: сначала измените или удалите его", ErrInvalid, orStr(r.Title, r.ID))
			}
		}
	}
	c.usMu.Lock()
	defer c.usMu.Unlock()
	cur := loadUserSets(c.st)
	var out []UserSet
	found := false
	for _, s := range cur {
		if s.ID == id {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return fmt.Errorf("%w: списка %s нет", store.ErrNotFound, id)
	}
	if err := saveUserSets(c.st, out); err != nil {
		return err
	}
	_ = os.Remove(c.userSetPath(id))
	c.bus.Publish(store.Event{Kind: EvRuleSets, Initiator: initiator, Detail: detail(map[string]any{"removed": id})})
	return nil
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// RefreshUserSets обновляет свои списки; возвращает id изменившихся. Сбой одного не мешает остальным: причина кладётся в Error, файл остаётся прежним.
func (c *Core) RefreshUserSets(ctx context.Context, initiator string) ([]string, error) {
	c.usMu.Lock()
	defer c.usMu.Unlock()
	cur := loadUserSets(c.st)
	var changed []string
	for i := range cur {
		if ctx.Err() != nil {
			return changed, ctx.Err()
		}
		s := &cur[i]
		old, _ := os.ReadFile(c.userSetPath(s.ID))
		b, err := c.fetchUserSet(ctx, s.URL, len(old))
		if err != nil {
			s.Error = err.Error()
			continue
		}
		if msg := userSetKindChange(old, b); msg != "" { // вид списка сменился — молча не принимаем
			s.Error = msg
			continue
		}
		s.Error = ""
		if sha256.Sum256(b) != sha256.Sum256(old) {
			if err := os.MkdirAll(filepath.Dir(c.userSetPath(s.ID)), 0o700); err == nil {
				err = rulesets.WriteFile(c.userSetPath(s.ID), b)
			}
			if err != nil {
				s.Error = err.Error()
				continue
			}
			changed = append(changed, s.ID)
		}
		s.Updated, s.Size = c.cfg.Now(), int64(len(b))
	}
	if err := saveUserSets(c.st, cur); err != nil {
		return changed, err
	}
	if len(changed) > 0 {
		c.bus.Publish(store.Event{Kind: EvRuleSets, Initiator: initiator, Detail: detail(map[string]any{"updated_user": changed})})
	}
	return changed, nil
}

// userSetKindChange сравнивает вид прежнего и нового содержимого своего списка. Список, бывший доменным и ставший адресным (или наоборот),
// меняет смысл правил, которые на него ссылаются, поэтому автоматически не принимается: прежний файл остаётся, причина видна в Error.
// Пустое сообщение — вид не менялся (или прежнего файла нет).
func userSetKindChange(old, nw []byte) string {
	if len(old) == 0 {
		return ""
	}
	ok, err := rulesets.KindOf(old)
	if err != nil { // прежний файл не разбирается — сравнивать не с чем
		return ""
	}
	nk, err := rulesets.KindOf(nw)
	if err != nil || nk == ok {
		return ""
	}
	return fmt.Sprintf("вид списка изменился: были %s условия, пришли %s; обновление не принято, прежний список сохранён (если так задумано, удалите список и добавьте заново)", rulesets.KindName(ok), rulesets.KindName(nk))
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
