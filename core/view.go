package core

import (
	"brod/translator"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"brod/prober"
	"brod/store"
)

// NodeView — узел с результатами проверок в текущей сети (без секретов).
type NodeView struct {
	NodeInfo
	Source     string    // не заполняется в списке (узел может быть в нескольких источниках)
	L2At       time.Time // последний успех L2 в текущей сети
	GoogleAPI  bool      // последняя проверка google-api в текущей сети прошла
	YouTube    bool      // последняя проверка YouTube прошла
	LastFail   time.Time
	FirstSeen  time.Time
	LastSeen   time.Time
	NoSecurity bool
	Notes      []string
	Exit       *ExitView // сведения о выходе (страна, чистота); nil — ещё не собраны
	Own        bool      // собственный сервер пользователя (добавлен вручную)
	Label      string    // пользовательское название узла
	LatencyMs  int64     // «пинг»: задержка последней успешной проверки L2 в текущей сети; 0 — нет данных
	SpeedKBps  float64   // скорость скачивания через узел (замер ~2 МБ), КБ/с; 0 — не замерялась
	SpeedAt    time.Time
	UDP        string // good | unstable | none; пусто — нет свежего замера UDP в текущей сети («для звонков»)
	UDPMs      int64  // медиана задержки STUN-ответов
	UDPAt      time.Time
	Chain      []ChainHopView // звенья, если это цепочка
}

// NodesQuery — выборка для списка.
type NodesQuery struct {
	Limit         int
	FavoritesOnly bool
	Source        string
	Class         string
	IncludeNoSec  bool
	Alive         bool // только с успехом L2 в текущей сети за AltFresh
}

// Nodes возвращает узлы с историей проверок текущей сети; живые и избранные первыми.
func (c *Core) Nodes(q NodesQuery) ([]NodeView, error) {
	now := c.cfg.Now()
	hist, err := c.st.Histories(c.netKey(), now)
	if err != nil {
		return nil, err
	}
	filter := store.NodeFilter{OnlyPresent: false, FavoritesOnly: q.FavoritesOnly, Source: q.Source, Class: q.Class,
		IncludeNoSecurity: q.IncludeNoSec, HideInsecure: c.hideInsecure()}
	var recs []store.NodeRec
	if q.Limit <= 0 {
		if recs, err = c.st.Nodes(filter); err != nil {
			return nil, err
		}
	} else if recs, err = c.topNodes(filter, hist, q); err != nil {
		return nil, err
	}
	exits, _ := c.st.ExitInfos() // не критично: без них список просто без стран
	speeds, _ := c.st.Speeds()
	udps, _ := c.st.UDPs()
	own, _ := c.st.OwnIDs()
	labels, _ := c.st.NodeLabels()
	out := make([]NodeView, 0, len(recs))
	for _, r := range recs {
		h := hist[r.ID]
		alive := !h.L2OK.IsZero() && now.Sub(h.L2OK) <= c.cfg.AltFresh && !(h.L2OK.Before(h.LastFail))
		if q.Alive && !alive {
			continue
		}
		out = append(out, NodeView{NodeInfo: nodeInfo(r), L2At: h.L2OK, GoogleAPI: !h.GoogleOK.IsZero() && !h.GoogleOK.Before(h.L2OK.Add(-time.Hour)),
			YouTube: !h.L4OK.IsZero(), LastFail: h.LastFail, FirstSeen: r.FirstSeen, LastSeen: r.LastSeen, NoSecurity: r.NoSecurity, Notes: r.Notes,
			Exit: exitView(exits, r.ID, !h.L4OK.IsZero()), LatencyMs: h.L2Ms, SpeedKBps: speeds[r.ID].KBps, SpeedAt: speeds[r.ID].At, Own: own[r.ID], Label: labels[r.ID]}.withUDP(c.udpFresh(udps[r.ID], now), udps[r.ID]))
		if r.Proto == chainProto { // звенья цепочки лежат в Opts, которых в лёгкой записи списка нет
			if full, err := c.st.Node(r.ID); err == nil {
				out[len(out)-1].Chain = chainViewOf(full)
			}
		}
	}
	rank := func(v NodeView) int {
		sc := 0
		if v.Own {
			sc += 2000 // свои серверы — наверху списка
		}
		if v.Favorite || v.Pinned {
			sc += 1000
		}
		if !v.L2At.IsZero() && !v.L2At.Before(v.LastFail) {
			sc += 500
		}
		if v.GoogleAPI {
			sc += 100
		}
		return sc
	}
	// порядок равных по рангу задан явно (свежие первыми, затем по ID), чтобы результат не зависел от пути выборки
	sortStable(out, func(a, b NodeView) bool {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra > rb
		}
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		return a.ID < b.ID
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// topNodes выбирает кандидатов в верх списка, не читая всю базу: узлы с успехом проверки в текущей сети, избранные и закреплённые,
// а недостающее до лимита добирается самыми свежими. Запрос «только живые» обходится первыми двумя группами.
func (c *Core) topNodes(filter store.NodeFilter, hist map[string]store.History, q NodesQuery) ([]store.NodeRec, error) {
	seen := map[string]bool{}
	var recs []store.NodeRec
	add := func(rs []store.NodeRec) {
		for _, r := range rs {
			if !seen[r.ID] {
				seen[r.ID] = true
				recs = append(recs, r)
			}
		}
	}
	var ids []string
	for id, h := range hist {
		if !h.L2OK.IsZero() {
			ids = append(ids, id)
		}
	}
	for i := 0; i < len(ids); i += 500 {
		f := filter
		f.IDs = ids[i:min(i+500, len(ids))]
		rs, err := c.st.Nodes(f)
		if err != nil {
			return nil, err
		}
		add(rs)
	}
	f := filter
	f.Flagged = true
	rs, err := c.st.Nodes(f)
	if err != nil {
		return nil, err
	}
	add(rs)
	if !q.Alive && !q.FavoritesOnly && len(recs) < q.Limit {
		f := filter
		f.Limit = q.Limit + len(recs) // часть самых свежих уже есть среди найденных
		rs, err := c.st.Nodes(f)
		if err != nil {
			return nil, err
		}
		add(rs)
	}
	return recs, nil
}

func sortStable[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}

// NodeByID возвращает один узел с историей.
func (c *Core) NodeByID(id string) (NodeView, error) {
	rec, err := c.st.Node(id)
	if err != nil {
		return NodeView{}, err
	}
	h, _, _ := c.st.History(c.netKey(), id, c.cfg.Now())
	return c.withExit(NodeView{NodeInfo: nodeInfo(rec), L2At: h.L2OK, LatencyMs: h.L2Ms, GoogleAPI: !h.GoogleOK.IsZero(), YouTube: !h.L4OK.IsZero(), LastFail: h.LastFail,
		FirstSeen: rec.FirstSeen, LastSeen: rec.LastSeen, NoSecurity: rec.NoSecurity, Notes: rec.Notes}), nil
}

// withExit дополняет вид узла сведениями о выходе.
func (c *Core) withExit(v NodeView) NodeView {
	if m, err := c.st.ExitInfos(); err == nil {
		v.Exit = exitView(m, v.ID, v.YouTube)
	}
	if m, err := c.st.Speeds(); err == nil {
		v.SpeedKBps, v.SpeedAt = m[v.ID].KBps, m[v.ID].At
	}
	if m, err := c.st.UDPs(); err == nil {
		v = v.withUDP(c.udpFresh(m[v.ID], c.cfg.Now()), m[v.ID])
	}
	if own, err := c.st.OwnIDs(); err == nil {
		v.Own = own[v.ID]
	}
	if labels, err := c.st.NodeLabels(); err == nil {
		v.Label = labels[v.ID]
	}
	return v
}

// SourceView — источник без секретов: адрес показывается без пути и параметров (в них бывает токен).
type SourceView struct {
	Name        string
	Host        string
	Priority    int
	Enabled     bool
	UserAdded   bool
	LastFetch   time.Time
	LastOK      time.Time
	LastErr     string
	LastCount   int
	Title       string
	UpdateHours int
	UsedBytes   int64
	TotalBytes  int64
	Expire      time.Time
	// Отдача за неделю (рейтинг источников, sourcerank.go): присутствующих узлов, из них проверено и ответило хотя бы раз; Rarely — автоматически обновляется редко.
	Present, Checked, Alive int
	Rarely                  bool
}

func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "?"
	}
	return u.Host
}

// Sources возвращает источники (адреса скрыты до хоста).
func (c *Core) Sources() ([]SourceView, error) {
	recs, err := c.st.Sources()
	if err != nil {
		return nil, err
	}
	out := make([]SourceView, len(recs))
	now := c.cfg.Now()
	yields := c.sourceYields(now)
	for i, r := range recs {
		out[i] = SourceView{Name: r.ID, Host: hostOnly(r.URL), Priority: r.Priority, Enabled: r.Enabled, UserAdded: r.UserAdded,
			LastFetch: r.LastFetch, LastOK: r.LastOK, LastErr: prober.Sanitize(r.LastErr), LastCount: r.LastCount,
			Title: r.Title, UpdateHours: r.UpdateHours, UsedBytes: r.UsedBytes, TotalBytes: r.TotalBytes, Expire: r.Expire}
		y := yields[r.ID]
		out[i].Present, out[i].Checked, out[i].Alive = y.Present, y.Checked, y.Alive
		out[i].Rarely = r.Enabled && sourceIsDead(r, y, now)
	}
	return out, nil
}

// NodeLink собирает ссылку «поделиться» для узла из его параметров (vless, vmess, trojan, shadowsocks, hysteria2). Ссылка содержит секреты.
func (c *Core) NodeLink(id string) (string, error) {
	rec, err := c.st.Node(id)
	if err != nil {
		return "", err
	}
	var opts map[string]any
	if err := json.Unmarshal(rec.Opts, &opts); err != nil {
		return "", fmt.Errorf("%w: параметры узла повреждены", ErrInvalid)
	}
	labels, _ := c.st.NodeLabels()
	link, err := translator.ToLink(opts, labels[id])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return link, nil
}

// ---- настройки ----

// Допустимые настройки: ключ → проверка значения. Секреты (app.*, api.*) через этот путь недоступны.
var settingRules = map[string]func(string) error{
	SetDegradeAction:         degradeActionSetting,
	SetNotifyLevel:           notifyLevelSetting,
	"security.hide_insecure": boolSetting,
	SetViaWarp:               boolSetting,
	SetOTASources:            boolSetting,
	SetUpdateCheck:           boolSetting,
	SetUpdateURL:             updateURLSetting,
	SetAutoGroup:             boolSetting,
	SetKillswitch:            boolSetting,
	SetFakeIP:                boolSetting,
	SetAppsMode:              appsModeSetting,
	SetWarpAuto:              boolSetting,
	SetWarpEndpoint:          warpEndpointSetting,
	SetWarpPort:              warpPortSetting,
	SetWarpMask:              warpMaskSetting,
	SetWarpMTU:               warpMTUSetting,
	SetWarpMaskHost:          warpMaskHostSetting,
	SetWarpBrowser:           warpBrowserSetting,
	SetApps:                  appsSetting,
}

// Per-app (Android): режим off | include (VPN только для выбранных приложений) | exclude (для всех, кроме выбранных) и список пакетов через запятую.
const (
	SetAppsMode = "tun.apps_mode"
	SetApps     = "tun.apps"
)

var packageRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)

func appsModeSetting(v string) error {
	switch v {
	case "", "off", "include", "exclude":
		return nil
	}
	return fmt.Errorf("%w: режим приложений: off | include | exclude", ErrInvalid)
}

func appsSetting(v string) error {
	if len(v) > 40000 {
		return fmt.Errorf("%w: слишком длинный список приложений", ErrInvalid)
	}
	for _, p := range splitApps(v) {
		if !packageRe.MatchString(p) {
			return fmt.Errorf("%w: неверное имя пакета %q", ErrInvalid, p)
		}
	}
	return nil
}

// SetTunApps записывает режим и список приложений одной операцией (Android, раздельное туннелирование): оба значения проверяются до записи,
// событие одно, а вызывающий перезапускает VPN один раз, а не по разу на каждую настройку. Режим include с пустым списком отклоняется.
func (c *Core) SetTunApps(mode string, apps []string, initiator string) error {
	if err := appsModeSetting(mode); err != nil {
		return err
	}
	seen := map[string]bool{}
	var list []string
	for _, p := range apps {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			list = append(list, p)
		}
	}
	sort.Strings(list)
	joined := strings.Join(list, ",")
	if err := appsSetting(joined); err != nil {
		return err
	}
	if mode == "include" && len(list) == 0 {
		return fmt.Errorf("%w: режим «только выбранные» без единого приложения", ErrInvalid)
	}
	if err := c.st.SetSetting(SetApps, joined); err != nil {
		return err
	}
	if err := c.st.SetSetting(SetAppsMode, mode); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: fmt.Sprintf("%s=%s (%d прил.)", SetAppsMode, mode, len(list))})
	return nil
}

// splitApps разбирает список пакетов через запятую.
func splitApps(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SetNotifyLevel — настройка уведомлений: all (все), important (по умолчанию: о деградации только при включённом VPN, без «липких»),
// off (никаких, кроме проверочного).
const SetNotifyLevel = "notify.level"

// SetDegradeAction — что делать, когда активный узел деградировал и найдены проверенные альтернативы:
// notify (по умолчанию: уведомление с предложением, узел меняет пользователь — принцип P1) или auto (автоматически перейти на лучшую
// проверенную альтернативу). Переход всегда атомарный и без «прямого» пути: пока идёт смена, новые соединения идут только через туннель.
const SetDegradeAction = "degrade.action"

// SetKillswitch — файрвольный киллсвитч (пакет killswitch): при включённом VPN в режимах «всё» и «всё, кроме РФ» трафик мимо туннеля блокируется,
// в том числе после падения демона. 1 или 0; по умолчанию — killswitchDefaultOn.
const SetKillswitch = "killswitch.enabled"

func degradeActionSetting(v string) error {
	switch v {
	case "notify", "auto":
		return nil
	}
	return fmt.Errorf("нужно notify или auto")
}

// DegradeAction возвращает выбранное действие при деградации (по умолчанию notify).
func (c *Core) DegradeAction() string {
	if v, ok, _ := c.st.Setting(SetDegradeAction); ok && degradeActionSetting(v) == nil {
		return v
	}
	return "notify"
}

func notifyLevelSetting(v string) error {
	switch v {
	case "all", "important", "off":
		return nil
	}
	return fmt.Errorf("нужно all, important или off")
}

// NotifyLevel возвращает уровень уведомлений (по умолчанию important).
func (c *Core) NotifyLevel() string {
	if v, ok, _ := c.st.Setting(SetNotifyLevel); ok && notifyLevelSetting(v) == nil {
		return v
	}
	return "important"
}

// killswitchImplemented: файрвольный киллсвитч есть в Linux (nftables) и Windows (WFP). На Android роль киллсвитча играет системная
// «Постоянный VPN»: показывать там «включено» значило бы обещать защиту, которой нет.
func killswitchImplemented(goos string) bool { return goos == "linux" || goos == "windows" }

// killswitchDefaultOn — значение настройки, пока её не меняли: на Linux включён; на Windows (WFP — новая реализация) включается вручную.
func killswitchDefaultOn(goos string) bool { return goos == "linux" }

// KillswitchEnabled — включён ли киллсвитч (по умолчанию — killswitchDefaultOn). Переменная окружения BRODD_KILLSWITCH=on|off перекрывает
// настройку (окна проверок и разработка).
func (c *Core) KillswitchEnabled() bool {
	if !killswitchImplemented(runtime.GOOS) {
		return false
	}
	switch strings.ToLower(os.Getenv("BRODD_KILLSWITCH")) {
	case "off", "0":
		return false
	case "on", "1":
		return true
	}
	if v, ok, _ := c.st.Setting(SetKillswitch); ok {
		return v != "0"
	}
	return killswitchDefaultOn(runtime.GOOS)
}

// VPNEnabled сообщает, что это ядро запущено с включённым VPN (TUN).
func (c *Core) VPNEnabled() bool { return c.cfg.TUN.Enabled }

func boolSetting(v string) error {
	if v != "0" && v != "1" {
		return fmt.Errorf("%w: ожидается 0 или 1", ErrInvalid)
	}
	return nil
}

// Settings возвращает доступные настройки со значениями.
func (c *Core) Settings() (map[string]string, error) {
	all, err := c.st.Settings()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k := range settingRules {
		out[k] = all[k]
	}
	if _, set := all[SetDegradeAction]; !set {
		out[SetDegradeAction] = "notify"
	}
	if _, set := all[SetNotifyLevel]; !set {
		out[SetNotifyLevel] = "important"
	}
	if _, set := all[SetKillswitch]; !set {
		out[SetKillswitch] = "0"
		if killswitchImplemented(runtime.GOOS) && killswitchDefaultOn(runtime.GOOS) {
			out[SetKillswitch] = "1"
		}
	}
	if _, set := all[SetFakeIP]; !set {
		out[SetFakeIP] = "0"
	}
	if _, set := all[SetOTASources]; !set {
		out[SetOTASources] = "1"
	}
	if _, set := all[SetUpdateCheck]; !set {
		out[SetUpdateCheck] = "1"
	}
	if _, set := all[SetViaWarp]; !set && c.warpOn {
		out[SetViaWarp] = "1" // умолчание: цепочка включена, пока не выключена явно
	}
	return out, nil
}

// SetSetting меняет настройку из белого списка.
func (c *Core) SetSetting(key, val, initiator string) error {
	check, ok := settingRules[key]
	if !ok {
		return fmt.Errorf("%w: неизвестная настройка %q", ErrInvalid, key)
	}
	if err := check(val); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if key == SetAutoGroup && val == "1" {
		return fmt.Errorf("%w: авто-группа ещё не реализована (v1); работает только ручной режим (P1)", ErrInvalid)
	}
	if key == SetViaWarp && val == "1" && !c.warpOn {
		return fmt.Errorf("%w: %s: WARP не настроен (нет файла аккаунта)", ErrInvalid, key)
	}
	if err := c.st.SetSetting(key, val); err != nil {
		return err
	}
	if key == SetViaWarp {
		c.viaWarpMem.Store(viaWarpUnknown)
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: key + "=" + val})
	return nil
}

// ---- токены приложений ----

const tokenPrefix = "api.app."

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

var appNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// CreateAppToken выдаёт приложению токен (показывается один раз; в базе только хэш). Повторный вызов заменяет токен.
func (c *Core) CreateAppToken(name, initiator string) (string, error) {
	if !appNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: имя приложения %q", ErrInvalid, name)
	}
	tok := randomSecret(32)
	if err := c.st.SetSetting(tokenPrefix+name, hashToken(tok)); err != nil {
		return "", err
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: "токен приложения " + name + " выдан"})
	return tok, nil
}

// RevokeAppToken отзывает токен приложения.
func (c *Core) RevokeAppToken(name, initiator string) error {
	if _, ok, _ := c.st.Setting(tokenPrefix + name); !ok {
		return store.ErrNotFound
	}
	if err := c.st.DeleteSetting(tokenPrefix + name); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: "токен приложения " + name + " отозван"})
	return nil
}

// CallerActive сообщает, действует ли ещё токен вызывающего: "user" всегда, "api:<приложение>" — пока токен не отозван.
// Один точечный запрос к базе (не весь список настроек), поэтому годится для долгих потоков.
func (c *Core) CallerActive(who string) bool {
	name, ok := strings.CutPrefix(who, "api:")
	if !ok {
		return true
	}
	s, err := c.authSnapshot()
	return err != nil || s.names[name] // сбой чтения не рвёт поток: следующая проверка повторит
}

// AppTokens возвращает имена приложений с токенами.
func (c *Core) AppTokens() ([]string, error) {
	all, err := c.st.Settings()
	if err != nil {
		return nil, err
	}
	var out []string
	for k := range all {
		if strings.HasPrefix(k, tokenPrefix) {
			out = append(out, strings.TrimPrefix(k, tokenPrefix))
		}
	}
	sortStable(out, func(a, b string) bool { return a < b })
	return out, nil
}

// Authenticate определяет, кто предъявил токен: "user" (главный токен) либо "api:<приложение>". Сравнение постоянного времени.
func (c *Core) Authenticate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s, err := c.authSnapshot() // снимок в памяти: ни запроса к базе, ни разбора таблицы настроек на каждый запрос
	if err != nil {
		return "", false
	}
	if s.master != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.master)) == 1 {
		return "user", true
	}
	h := hashToken(token)
	found := ""
	for _, a := range s.apps { // обходим все, чтобы время не зависело от того, какой токен совпал
		if subtle.ConstantTimeCompare([]byte(h), []byte(a.hash)) == 1 {
			found = a.name
		}
	}
	if found != "" {
		return "api:" + found, true
	}
	return "", false
}

// APIToken возвращает главный токен (секрет: только для владельца).
func (c *Core) APIToken() string {
	v, _, _ := c.st.Setting(SetAPIToken)
	return v
}

// ---- лог ----

var (
	rePass = regexp.MustCompile(`(?i)(password|passwd|token|auth)[=:]\s*\S+`)
	reUser = regexp.MustCompile(`(?i)username=\S+`)
)

// logTailMax — сколько байт с конца engine.log просматривается в поисках n строк (в логе бывают долгие серии шума WG-проб).
const logTailMax = 8 << 20

const logNoise = "failed to send data packets: address family not supported"

// Logs возвращает последние n строк лога движка с вычищенными адресами, паролями и именами. Читается только хвост файла: журнал
// на телефоне весит десятки мегабайт, а монитор в приложении запрашивает его раз в несколько секунд, поэтому цена не должна расти с размером.
func (c *Core) Logs(n int) ([]string, error) {
	if n <= 0 || n > 2000 {
		n = 200
	}
	f, err := os.Open(filepath.Join(c.cfg.StateDir, "engine.log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const chunk = 256 << 10
	var picked []string // от новых к старым
	noise := 0
	var carry []byte // начало строки, оборванное границей блока: дочитывается следующим (более ранним) блоком
	pos, scanned, first := st.Size(), int64(0), true
	for pos > 0 && len(picked) < n && scanned < logTailMax {
		step := min(int64(chunk), pos)
		pos -= step
		buf := make([]byte, step, step+int64(len(carry)))
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, err
		}
		scanned += step
		data := append(buf, carry...)
		start := 0
		if pos > 0 { // первая строка блока может начинаться в предыдущем
			k := bytes.IndexByte(data, '\n')
			if k < 0 {
				carry = data
				continue
			}
			carry, start = data[:k:k], k+1
		} else {
			carry = nil
		}
		text := string(data[start:])
		if first { // завершающий перевод строки файла не даёт пустой строки
			text, first = strings.TrimSuffix(text, "\n"), false
		}
		lines := strings.Split(text, "\n")
		for k := len(lines) - 1; k >= 0 && len(picked) < n; k-- {
			line := strings.TrimSuffix(lines[k], "\r")
			if strings.Contains(line, logNoise) {
				noise++
				continue
			}
			picked = append(picked, line)
		}
	}
	ring := make([]string, 0, len(picked)+1)
	for k := len(picked) - 1; k >= 0; k-- {
		ring = append(ring, scrubLogLine(picked[k]))
	}
	if noise > 0 {
		ring = append(ring, fmt.Sprintf("(скрыто однотипных строк шума: %d — пробы WireGuard-узлов на хосте без IPv6)", noise))
	}
	return ring, nil
}

// scrubLogLine убирает из строки журнала пароли, имена пользователей, адреса и имена хостов.
func scrubLogLine(line string) string {
	line = rePass.ReplaceAllString(line, "$1=<скрыто>")
	line = reUser.ReplaceAllString(line, "username=<скрыто>")
	return prober.Sanitize(line)
}

// SanitizedLogTail возвращает не больше maxBytes байт с конца файла журнала, каждая строка очищена как в Logs; оборванная
// первая строка отбрасывается. Для «Поделиться журналом» на Android: файл уходит другому человеку и весит десятки мегабайт.
func SanitizedLogTail(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := max(st.Size()-maxBytes, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", err
	}
	if off > 0 { // первая строка могла начаться до границы
		if k := bytes.IndexByte(buf, '\n'); k >= 0 {
			buf = buf[k+1:]
		} else {
			buf = nil
		}
	}
	lines := strings.Split(strings.TrimSuffix(string(buf), "\n"), "\n")
	for i, l := range lines {
		lines[i] = scrubLogLine(strings.TrimSuffix(l, "\r"))
	}
	// очистка заменяет текст на метки и может его удлинить («lookup x» -> «lookup <dom>»): держим предел и после неё
	out := strings.Join(lines, "\n") + "\n"
	for int64(len(out)) > maxBytes {
		k := strings.IndexByte(out, '\n')
		if k < 0 || k+1 >= len(out) {
			return "", nil
		}
		out = out[k+1:]
	}
	return out, nil
}

// ResolveNode принимает полный ID узла либо однозначный префикс (от 6 символов) и возвращает полный ID.
func (c *Core) ResolveNode(ref string) (string, error) {
	if len(ref) < 6 {
		return "", fmt.Errorf("%w: идентификатор узла слишком короткий (нужно не меньше 6 символов)", ErrInvalid)
	}
	ids, err := c.st.NodeIDsByPrefix(ref, 3)
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("узел %s: %w", ref, ErrNoNode)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("%w: префикс %q неоднозначен", ErrInvalid, ref)
}

// ActiveBrief возвращает протокол и код страны выхода активного узла (для уведомления); пусто — сведений нет.
func (c *Core) ActiveBrief() (proto, country string) {
	c.mu.Lock()
	id := c.selected[store.ScopeActive]
	c.mu.Unlock()
	switch id {
	case "":
		return "", ""
	case store.SelWarp:
		return "warp", ""
	}
	rec, err := c.st.Node(id)
	if err != nil {
		return "", ""
	}
	return rec.Proto, c.st.ExitCountry(id)
}

// withUDP добавляет замер UDP, если он свежий и снят в текущей сети.
func (v NodeView) withUDP(fresh bool, r store.UDPRec) NodeView {
	if fresh {
		v.UDP, v.UDPMs, v.UDPAt = r.Grade, r.MedianMs, r.At
	}
	return v
}
