package store

import (
	"database/sql"
	"encoding/json"
	"sync"
	"time"
)

// settingsCache — кэш всей таблицы настроек (десятки строк). Settings() зовут на каждый HTTP-запрос со сторонним токеном,
// а писать в таблицу можно только через SetSetting/DeleteSetting, которые сбрасывают кэш.
type settingsCache struct {
	mu  sync.Mutex
	gen uint64 // растёт при каждой записи; в кэш кладём, только если gen не изменился за время чтения
	m   map[string]string
}

// ---- настройки (k/v) ----

// Setting возвращает значение настройки; ok=false, если её нет.
func (s *Store) Setting(k string) (v string, ok bool, err error) {
	err = s.rd.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetSetting записывает настройку.
func (s *Store) SetSetting(k, v string) error {
	defer s.settingsChanged()
	_, err := s.db.Exec(`INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// DeleteSetting удаляет настройку (возврат к умолчанию).
func (s *Store) DeleteSetting(k string) error {
	defer s.settingsChanged()
	_, err := s.db.Exec(`DELETE FROM settings WHERE k=?`, k)
	return err
}

// SettingsGen — версия таблицы настроек: растёт после каждой записи (SetSetting, DeleteSetting). По ней ядро сбрасывает снимок токенов.
func (s *Store) SettingsGen() uint64 {
	s.sc.mu.Lock()
	defer s.sc.mu.Unlock()
	return s.sc.gen
}

func (s *Store) settingsChanged() {
	s.sc.mu.Lock()
	s.sc.gen++
	s.sc.m = nil
	s.sc.mu.Unlock()
}

// Settings возвращает все настройки (копию: менять её можно).
func (s *Store) Settings() (map[string]string, error) {
	s.sc.mu.Lock()
	if s.sc.m != nil {
		out := copyMap(s.sc.m)
		s.sc.mu.Unlock()
		return out, nil
	}
	gen := s.sc.gen
	s.sc.mu.Unlock()
	out, err := s.loadSettings()
	if err != nil {
		return nil, err
	}
	s.sc.mu.Lock()
	if s.sc.gen == gen {
		s.sc.m = copyMap(out)
	}
	s.sc.mu.Unlock()
	return out, nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) loadSettings() (map[string]string, error) {
	rows, err := s.rd.Query(`SELECT k,v FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ---- журнал событий ----

// Event — запись журнала: что произошло, кто инициировал (user | auto | api:<приложение>), с каким узлом/целью.
type Event struct {
	ID        int64
	At        time.Time
	Kind      string
	Initiator string
	Node      string
	Target    string
	Detail    string
}

// AddEvent дописывает событие и возвращает его с присвоенным ID.
func (s *Store) AddEvent(e Event) (Event, error) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO events(at,kind,initiator,node,target,detail) VALUES(?,?,?,?,?,?)`,
		e.At.UnixMilli(), e.Kind, e.Initiator, e.Node, e.Target, e.Detail)
	if err != nil {
		return e, err
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

// Events возвращает события с ID > afterID по возрастанию (limit ≤ 0 — без ограничения).
func (s *Store) Events(afterID int64, limit int) ([]Event, error) {
	q := `SELECT id,at,kind,initiator,node,target,detail FROM events WHERE id>? ORDER BY id`
	args := []any{afterID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.queryEvents(q, args...)
}

// LastEvents возвращает последние n событий в порядке возрастания времени.
func (s *Store) LastEvents(n int) ([]Event, error) {
	evs, err := s.queryEvents(`SELECT id,at,kind,initiator,node,target,detail FROM events ORDER BY id DESC LIMIT ?`, n)
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
	}
	return evs, err
}

func (s *Store) queryEvents(q string, args ...any) ([]Event, error) {
	rows, err := s.rd.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.Initiator, &e.Node, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// TrimEvents оставляет в журнале не больше keep последних событий.
func (s *Store) TrimEvents(keep int) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE id <= (SELECT COALESCE(MAX(id),0) FROM events) - ?`, keep)
	return err
}

// ---- цели (P7) ----

// Политики переключения цели.
const (
	PolicyStickyManual = "sticky-manual"
	PolicyAutoFailover = "auto-failover"
)

// TargetRules — какой трафик относится к цели (синтаксис правил sing-box).
type TargetRules struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
}

// TargetCheck — как проверять цель. Kind: youtube | google-api | telegram | http.
type TargetCheck struct {
	Kind        string `json:"kind"`
	URL         string `json:"url,omitempty"`          // для http: что запрашивать
	ExpectCodes []int  `json:"expect_codes,omitempty"` // для http: допустимые коды (пусто = 200)
	MinBytes    int    `json:"min_bytes,omitempty"`    // для потоковых: сколько байт должно прийти (Range-GET)
	MinKBps     int    `json:"min_kbps,omitempty"`     // для потоковых: нижний порог скорости Range-загрузки, КиБ/с (0 = без порога)
}

// Target — цель. Пробная ссылка (подписанный URL с коротким сроком жизни) здесь не хранится: её держит демон в памяти.
type Target struct {
	ID          string
	Owner       string // "builtin" или имя приложения
	Title       string
	Rules       TargetRules
	Check       TargetCheck
	Policy      string // PolicyStickyManual | PolicyAutoFailover
	FailoverK   int    // подряд проваленных проверок до переключения (auto-failover)
	AllowDirect bool   // direct среди кандидатов
	AllowWarp   bool   // WARP среди кандидатов
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type targetSpec struct {
	Rules       TargetRules `json:"rules"`
	Check       TargetCheck `json:"check"`
	Policy      string      `json:"policy"`
	FailoverK   int         `json:"failover_k,omitempty"`
	AllowDirect bool        `json:"allow_direct,omitempty"`
	AllowWarp   bool        `json:"allow_warp,omitempty"`
}

// PutTarget создаёт или обновляет цель; created сохраняется при обновлении.
func (s *Store) PutTarget(t Target, now time.Time) error {
	spec, err := json.Marshal(targetSpec{t.Rules, t.Check, t.Policy, t.FailoverK, t.AllowDirect, t.AllowWarp})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO targets(id,owner,title,spec,created,updated) VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET owner=excluded.owner, title=excluded.title, spec=excluded.spec, updated=excluded.updated`,
		t.ID, t.Owner, t.Title, string(spec), now.Unix(), now.Unix())
	return err
}

// Targets возвращает все цели.
func (s *Store) Targets() ([]Target, error) {
	rows, err := s.rd.Query(`SELECT id,owner,title,spec,created,updated FROM targets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Target
	for rows.Next() {
		var t Target
		var spec string
		var c, u int64
		if err := rows.Scan(&t.ID, &t.Owner, &t.Title, &spec, &c, &u); err != nil {
			return nil, err
		}
		var sp targetSpec
		if err := json.Unmarshal([]byte(spec), &sp); err != nil {
			return nil, err
		}
		t.Rules, t.Check, t.Policy, t.FailoverK, t.AllowDirect, t.AllowWarp = sp.Rules, sp.Check, sp.Policy, sp.FailoverK, sp.AllowDirect, sp.AllowWarp
		t.CreatedAt, t.UpdatedAt = time.Unix(c, 0), time.Unix(u, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Target возвращает цель по ID.
func (s *Store) Target(id string) (Target, error) {
	all, err := s.Targets()
	if err != nil {
		return Target{}, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return Target{}, ErrNotFound
}

// DeleteTarget удаляет цель вместе с её выбором.
func (s *Store) DeleteTarget(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := s.mustAffect(tx.Exec(`DELETE FROM targets WHERE id=?`, id)); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM selection WHERE scope=?`, TargetScope(id)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- выбор узла ----

// ScopeActive — область выбора основного активного узла.
const ScopeActive = "active"

// TargetScope — область выбора цели.
func TargetScope(id string) string { return "target:" + id }

// Special-значения выбора, не являющиеся узлами.
const (
	SelDirect = "direct"
	SelWarp   = "warp"
)

// Selection — выбранный узел области и упорядоченные запасные. Сохраняется между запусками (P1).
type Selection struct {
	Scope     string
	Node      string
	Fallbacks []string
	SetAt     time.Time
	By        string // кто выбрал: user | auto | api:<приложение>
}

// SetSelection записывает выбор.
func (s *Store) SetSelection(sel Selection) error {
	fb, _ := json.Marshal(sel.Fallbacks)
	if sel.Fallbacks == nil {
		fb = []byte("[]")
	}
	if sel.SetAt.IsZero() {
		sel.SetAt = time.Now()
	}
	_, err := s.db.Exec(`
INSERT INTO selection(scope,node,fallbacks,set_at,by) VALUES(?,?,?,?,?)
ON CONFLICT(scope) DO UPDATE SET node=excluded.node, fallbacks=excluded.fallbacks, set_at=excluded.set_at, by=excluded.by`,
		sel.Scope, sel.Node, string(fb), sel.SetAt.Unix(), sel.By)
	return err
}

// Selection возвращает выбор области; ErrNotFound, если выбора ещё не было.
func (s *Store) Selection(scope string) (Selection, error) {
	var sel Selection
	var fb string
	var at int64
	err := s.rd.QueryRow(`SELECT scope,node,fallbacks,set_at,by FROM selection WHERE scope=?`, scope).Scan(&sel.Scope, &sel.Node, &fb, &at, &sel.By)
	if err == sql.ErrNoRows {
		return sel, ErrNotFound
	}
	if err != nil {
		return sel, err
	}
	_ = json.Unmarshal([]byte(fb), &sel.Fallbacks)
	sel.SetAt = time.Unix(at, 0)
	return sel, nil
}

// Selections возвращает все выборы (для защиты узлов от PurgeNodes и восстановления при старте).
func (s *Store) Selections() ([]Selection, error) {
	rows, err := s.rd.Query(`SELECT scope FROM selection ORDER BY scope`)
	if err != nil {
		return nil, err
	}
	var scopes []string
	for rows.Next() {
		var sc string
		if err := rows.Scan(&sc); err != nil {
			rows.Close()
			return nil, err
		}
		scopes = append(scopes, sc)
	}
	rows.Close()
	var out []Selection
	for _, sc := range scopes {
		sel, err := s.Selection(sc)
		if err != nil {
			return nil, err
		}
		out = append(out, sel)
	}
	return out, nil
}
