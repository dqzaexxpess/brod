// Package core — ядро демона: держит список узлов, выбранный узел «липким» (P1), проверяет узлы и цели, публикует события.
// Сеть и время подменяются через Config, чтобы сценарии приёмки (смена сети, «умирающий» узел) шли в тестах на loopback.
package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"brod/aggregator"
	"brod/engine"
	"brod/monitor"
	"brod/prober"
	"brod/store"
	"brod/tgproxy"
	"brod/warp"
)

// Config — параметры ядра. Нулевые значения заменяются умолчаниями.
type Config struct {
	StateDir string // каталог состояния: база, кэш подписок, лог движка (создаётся с правами 0700)
	LogLevel string // уровень лога движка (по умолчанию warn)
	AppHost  string // адрес app-входа (по умолчанию 127.0.0.1)
	AppPort  int    // порт app-входа mixed (по умолчанию 29080)

	Now   func() time.Time // часы (тесты)
	NetID func() string    // идентификатор сети (по умолчанию store.NetID)

	TUN           TUNConfig     // режим TUN (по умолчанию выключен; см. tun.go)
	Resolvectl    resolvectlRun // запуск resolvectl (тесты); nil = настоящий
	RuleSetClient *http.Client  // HTTP-клиент обновления списков (тесты); nil = напрямую, затем через активный узел

	// WARP (Cloudflare): endpoint поднимается в движке, если есть аккаунт. Файл — секрет (приватный ключ), права 0600.
	WarpAccount     *warp.Account // готовый аккаунт (тесты); nil = читать из WarpAccountFile
	WarpAccountFile string        // по умолчанию StateDir/warp-account.json; нет файла — WARP выключен
	WarpMask        string        // quic | dns | stun | sip | none (по умолчанию quic: клиентская AWG-маскировка)
	WarpEndpoint    string        // адрес endpoint'а (по умолчанию engage.cloudflareclient.com)
	WarpPort        uint16        // порт (по умолчанию 2408)
	WarpMTU         uint32        // MTU (по умолчанию 1280)
	WarpMaskHost    string        // домен маскировки (quic: SNI; по умолчанию www.google.com)
	WarpBrowser     string        // chrome | firefox | curl (только quic)

	// Хранение: без чистки база и лог растут без предела (проверки дают сотни тысяч строк в сутки).
	ProbeRetention time.Duration // сколько хранить результаты проверок (по умолчанию 8 суток: окно истории 7)
	EventRetention int           // сколько последних событий хранить (по умолчанию 20000)
	LogMaxBytes    int64         // размер лога движка, после которого он ротируется (по умолчанию 20 МБ)

	// Источники подписок.
	SeedSources   []aggregator.Source // засеваются при каждом запуске (включённость не сбрасывается); nil = aggregator.Defaults()
	CacheDir      string              // кэш подписок (по умолчанию StateDir/cache)
	Fetch         Fetcher             // загрузчик источников; nil = напрямую, при сбое через активный узел (P3); подменяется в тестах
	FetchTimeout  time.Duration       // таймаут одного источника (по умолчанию 15 с, P3)
	Idle          func() bool         // true — интерфейс не на экране; при выключенном VPN периодические задания тогда спят (nil — не спят)
	NodeRetention time.Duration       // сколько хранить пропавшие из источников узлы (по умолчанию 14 суток)
	MaxNodes      int                 // общий потолок узлов в базе (по умолчанию DefaultMaxNodes; отрицательное — без потолка)

	// Проверки. Параллельность намеренно мала: массовая проверка с домашней сети деградирует роутер.
	Check         CheckFunc     // проверка одного узла (по умолчанию L2 + google-api); подменяется в тестах
	CheckParallel int           // одновременных проверок (по умолчанию 48)
	CheckBatch    int           // узлов за один плановый круг (по умолчанию 200)
	CheckTimeout  time.Duration // таймаут проверки одного узла (по умолчанию 10 с)
	// Прокси Telegram: период кругов проверки (10 мин), прокси за круг (120), параллельно (32), таймаут одной проверки (8 с).
	TGInterval   time.Duration
	TGBatch      int
	TGParallel   int
	TGTimeout    time.Duration
	TGProbe      func(ctx context.Context, p tgproxy.Proxy) tgproxy.Result // подмена в тестах
	Exit         ExitFunc                                                  // проверка выхода узла: страна и «чистота» (тесты подменяют); nil = prober.Exit
	YouTube      YouTubeFunc                                               // проверка YouTube при сборе сведений о выходе (тесты подменяют); nil = prober.L4YouTube
	Speed        SpeedFunc                                                 // замер скорости узла (тесты подменяют); nil = prober.L3 на 2 МБ
	SpeedBatch   int                                                       // сколько узлов замерять за круг (по умолчанию 4)
	SpeedTTL     time.Duration                                             // через сколько замер устаревает (по умолчанию 6 ч)
	UDPTTL       time.Duration                                             // то же для замера UDP («для звонков»; по умолчанию 6 ч)
	ExitInterval time.Duration                                             // период фонового сбора сведений о выходах (по умолчанию 3 мин)
	ExitBatch    int                                                       // сколько узлов за круг (по умолчанию 12)
	ExitTTL      time.Duration                                             // через сколько сведения устаревают (по умолчанию 24 ч)
	CheckTTL     time.Duration                                             // свежесть успешной проверки, до истечения которой узел не перепроверяется (по умолчанию 15 мин)
	RetryAfter   time.Duration                                             // через сколько пробовать провалившийся узел снова (по умолчанию 2 ч)

	NetInterval time.Duration // период опроса смены сети (по умолчанию 3 с); при рабочей подписке netlink опрос в 10 раз реже (страховка)
	NoNetlink   bool          // не подписываться на netlink (тесты с подменой NetID)

	// Metered сообщает, что текущая сеть платная (мобильная, раздача с телефона): большие скачивания идут только с согласия пользователя.
	// nil — сеть считается безлимитной (Linux, Windows).
	Metered func() bool

	WarpScanProbe func(ctx context.Context, p WarpProfile) prober.Result // подмена проверки профиля WARP (тесты); nil — проба через движок

	// Автопоиск источников (discover.go). Все поля, кроме DiscoverInterval, нужны тестам; по умолчанию поиск идёт на raw.githubusercontent.com.
	DiscoverInterval time.Duration                                            // период автоматического прохода (по умолчанию сутки)
	DiscoverBase     string                                                   // база raw-адресов (по умолчанию discover.RawBase)
	DiscoverClient   *http.Client                                             // HTTP-клиент поиска (nil — свой, с запретом редиректа на чужой хост)
	DiscoverRepos    int                                                      // предел репозиториев за проход (по умолчанию 150)
	DiscoverProbe    func(ctx context.Context, n store.NodeRec) prober.Result // подмена пробы узла кандидата (тесты); nil — engine.NewProbe + prober.L2
	DiscoverCanProbe func() bool                                              // подмена условия «выборку можно проверять» (тесты); nil — VPN включён и это не Android

	// OnWarpProfile вызывается, когда подбор выбрал и сохранил новый профиль WARP: ядро читает параметры при создании, поэтому владелец ядра
	// (супервизор) пересоздаёт его. Вызывается в отдельной горутине.
	OnWarpProfile func()

	NetModeProbe func(ctx context.Context, url string) bool // подмена проверки достижимости адреса (тесты); nil — прямой HTTPS-запрос

	// Пассивный детектор зависания (по умолчанию: 8 с без ответа на 3 соединениях).
	Monitor monitor.Config

	// Цели (P7).
	TargetCheck    TargetCheckFunc // подмена проверки цели (тесты); nil = проверка по виду цели
	TargetInterval time.Duration   // период проверки целей с проверкой http (по умолчанию 10 с: K=3 провала дают ≤30 с до решения)
	// Встроенные проверки (youtube, google-api, telegram) ходят на чужие API с антибот-защитой: частые запросы дают 429 и ложную
	// деградацию, поэтому они реже.
	HeavyTargetInterval time.Duration // по умолчанию 3 мин

	// Здоровье активного узла (P1: уведомление ≤30 с после «смерти»: интервал 8 с и два провала подряд дают ≤26 с).
	Liveness           LivenessFunc                                       // проверка живого активного узла (по умолчанию L2)
	Quick              LivenessFunc                                       // быстрая проверка «выход поднялся» для момента подключения (по умолчанию prober.Quick), на деградацию не влияет
	HealthInterval     time.Duration                                      // период активной проверки (по умолчанию 8 с)
	WarpWatch          time.Duration                                      // период проверки самого WARP для обхода цепочки, когда он не отвечает (по умолчанию 15 с)
	WarpLiveness       func(context.Context, *engine.Probe) prober.Result // проверка WARP (подмена в тестах; по умолчанию prober.L2)
	HealthTimeout      time.Duration                                      // таймаут одной проверки (по умолчанию 5 с)
	HealthFails        int                                                // провалов подряд до объявления деградации (по умолчанию 2)
	AltCount           int                                                // сколько альтернатив подбирать при деградации (по умолчанию 3)
	HealthWarmInterval time.Duration                                      // период проверок в первую минуту после смены узла, пока нет первого успеха (по умолчанию 3 с)
	StandbyInterval    time.Duration                                      // период обновления горячего резерва (по умолчанию 3 мин)
	StandbyFirstDelay  time.Duration                                      // через сколько после старта первое обновление резерва (по умолчанию 45 с)
	UserSwapGrace      time.Duration                                      // сколько узел, выбранный пользователем (или приложением), должен быть в деградации, прежде чем автозамена (degrade.action=auto) его заменит (по умолчанию 40 с; <0 — без паузы)
	StandbyFresh       time.Duration                                      // насколько свеж резерв, чтобы им пользоваться при деградации (по умолчанию 6 мин)
	AltFresh           time.Duration                                      // насколько свежим должен быть успех кандидата в альтернативы (по умолчанию 30 мин)

	// Фоновые задания. Manual=true отключает все (тесты и разовые запуски вызывают методы явно).
	Manual           bool
	RefreshInterval  time.Duration           // период обновления подписок (по умолчанию 30 мин)
	OTASourcesURLs   []string                // адреса подписанного списка источников (по умолчанию defaultOTASourcesURLs); подпись лежит рядом: адрес + .sig
	OTAKey           ed25519.PublicKey       // ключ издателя (по умолчанию вшитый, ota.PublicKey); подменяется в тестах
	ReleasesKey      ed25519.PublicKey       // ключ подписи релизов (по умолчанию вшитый ota.ReleasesPublicKey); подменяется в тестах
	UpdateURLs       []string                // адреса манифеста релизов (по умолчанию defaultUpdateURLs); подпись лежит рядом: адрес + .sig
	UpdateInterval   time.Duration           // период проверки обновлений (по умолчанию сутки)
	UpdateFirstDelay time.Duration           // через сколько после старта первая проверка (по умолчанию 5 мин)
	AppVersion       string                  // версия для сравнения (по умолчанию buildinfo.Version); подменяется в тестах
	UpdatePlatform   string                  // ключ платформы в манифесте (по умолчанию по GOOS/GOARCH); подменяется в тестах
	OTAClient        *http.Client            // тесты: клиент для запросов OTA напрямую (доверяет самоподписанному серверу); по умолчанию обычный
	UpdateAllowHTTP  bool                    // тесты: скачивание по http с локального сервера
	UpdateSpawn      func(InstallSpec) error // тесты: запуск помощника установки (по умолчанию настоящий)
	OnUpdateInstall  func()                  // вызывается после запуска помощника: демон должен завершиться (cmd/brodd)
	InstallDir       string                  // тесты: каталог установленной программы (по умолчанию каталог запущенного brodd)
	OTAInterval      time.Duration           // период обновления списка источников (по умолчанию сутки)
	OTAFirstDelay    time.Duration           // через сколько после старта первое обновление (по умолчанию 3 мин)
	RouteViaInterval time.Duration           // период пересмотра выходов правил «через страну» (по умолчанию 20 с)
	CheckInterval    time.Duration           // период плановых проверок (по умолчанию 5 мин)
}

// LivenessFunc — быстрая проверка живого активного узла (тот же outbound, через который идёт трафик).
type LivenessFunc func(ctx context.Context, p *engine.Probe) prober.Result

// Fetcher загружает источники параллельно и отдаёт результаты по мере готовности.
type Fetcher func(ctx context.Context, srcs []aggregator.Source, perSource time.Duration, cacheDir string) <-chan aggregator.Result

// Имена настроек в базе.
const (
	SetAppUser  = "app.user"
	SetAppPass  = "app.pass"
	SetAPIToken = "api.token"
)

func (c *Config) defaults() error {
	if c.WarpEndpoint == "" && runtime.GOOS == "windows" {
		// адрес литералом, как на Android: на Windows имя engage.cloudflareclient.com не разрешалось внутри endpoint'а («no known endpoint for
		// peer», `lookup … context deadline exceeded`), а IPv6-ответ шёл в udp4-сокет
		c.WarpEndpoint = "162.159.192.1"
	}
	if c.StateDir == "" {
		return fmt.Errorf("не задан каталог состояния")
	}
	if c.LogLevel == "" {
		c.LogLevel = "warn"
	}
	if c.AppHost == "" {
		c.AppHost = "127.0.0.1"
	}
	if c.AppPort == 0 {
		c.AppPort = 29080
	}
	if c.WarpAccountFile == "" {
		c.WarpAccountFile = filepath.Join(c.StateDir, "warp-account.json")
	}
	if c.WarpMask == "" {
		c.WarpMask = "quic"
	}
	if c.CacheDir == "" {
		c.CacheDir = filepath.Join(c.StateDir, "cache")
	}
	if c.FetchTimeout == 0 {
		c.FetchTimeout = 15 * time.Second
	}
	if c.NodeRetention == 0 {
		c.NodeRetention = 14 * 24 * time.Hour
	}
	if c.MaxNodes == 0 {
		c.MaxNodes = DefaultMaxNodes
	}
	if c.SeedSources == nil {
		c.SeedSources = aggregator.Defaults()
	}
	if c.ProbeRetention == 0 {
		c.ProbeRetention = 8 * 24 * time.Hour
	}
	if c.EventRetention == 0 {
		c.EventRetention = 20000
	}
	if c.LogMaxBytes == 0 {
		c.LogMaxBytes = 20 << 20
	}
	if c.NetInterval == 0 {
		c.NetInterval = 3 * time.Second
	}
	if c.Check == nil {
		c.Check = defaultCheck
	}
	if c.TGInterval == 0 {
		c.TGInterval = 10 * time.Minute
	}
	if c.TGBatch == 0 {
		c.TGBatch = 120
	}
	if c.TGParallel == 0 {
		c.TGParallel = 32
	}
	if c.TGTimeout == 0 {
		c.TGTimeout = 8 * time.Second
	}
	if c.CheckParallel == 0 {
		c.CheckParallel = 48
	}
	if c.CheckBatch == 0 {
		c.CheckBatch = 200
	}
	if c.Exit == nil {
		c.Exit = func(ctx context.Context, _ store.NodeRec, p *engine.Probe) prober.ExitInfo {
			return prober.Exit(ctx, p)
		}
	}
	if c.YouTube == nil {
		c.YouTube = func(ctx context.Context, _ store.NodeRec, p *engine.Probe) prober.Result {
			return prober.L4YouTube(ctx, p)
		}
	}
	if c.Speed == nil {
		c.Speed = func(ctx context.Context, _ store.NodeRec, p *engine.Probe) prober.Result {
			return prober.L3(ctx, p, 2<<20)
		}
	}
	if c.SpeedBatch == 0 {
		c.SpeedBatch = 4
	}
	if c.UDPTTL == 0 {
		c.UDPTTL = 6 * time.Hour
	}
	if c.SpeedTTL == 0 {
		c.SpeedTTL = 6 * time.Hour
	}
	if c.ExitInterval == 0 {
		c.ExitInterval = 3 * time.Minute
	}
	if c.ExitBatch == 0 {
		c.ExitBatch = 12
	}
	if c.ExitTTL == 0 {
		c.ExitTTL = 24 * time.Hour
	}
	if c.CheckTimeout == 0 {
		c.CheckTimeout = 20 * time.Second
	}
	if c.CheckTTL == 0 {
		c.CheckTTL = 15 * time.Minute
	}
	if c.RetryAfter == 0 {
		c.RetryAfter = 2 * time.Hour
	}
	if c.Quick == nil {
		c.Quick = func(ctx context.Context, p *engine.Probe) prober.Result { return prober.Quick(ctx, p) }
	}
	if c.Liveness == nil {
		c.Liveness = func(ctx context.Context, p *engine.Probe) prober.Result { return prober.L2(ctx, p) }
	}
	if c.HeavyTargetInterval == 0 {
		c.HeavyTargetInterval = 3 * time.Minute
	}
	if c.TargetInterval == 0 {
		c.TargetInterval = 10 * time.Second
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = 8 * time.Second
	}
	if c.WarpWatch == 0 {
		c.WarpWatch = 15 * time.Second
	}
	if c.HealthTimeout == 0 {
		c.HealthTimeout = 5 * time.Second
	}
	if c.HealthFails == 0 {
		c.HealthFails = 2
	}
	if c.AltCount == 0 {
		c.AltCount = 3
	}
	if c.UserSwapGrace == 0 {
		c.UserSwapGrace = 40 * time.Second
	}
	if c.HealthWarmInterval == 0 {
		c.HealthWarmInterval = 3 * time.Second
	}
	if c.StandbyInterval == 0 {
		c.StandbyInterval = 3 * time.Minute
	}
	if c.StandbyFirstDelay == 0 {
		c.StandbyFirstDelay = 45 * time.Second
	}
	if c.StandbyFresh == 0 {
		c.StandbyFresh = 6 * time.Minute
	}
	if c.AltFresh == 0 {
		c.AltFresh = 30 * time.Minute
	}
	if c.OTAInterval == 0 {
		c.OTAInterval = 24 * time.Hour
	}
	if c.UpdateInterval == 0 {
		c.UpdateInterval = 24 * time.Hour
	}
	if c.UpdateFirstDelay == 0 {
		c.UpdateFirstDelay = 5 * time.Minute
	}
	if c.OTAFirstDelay == 0 {
		c.OTAFirstDelay = 3 * time.Minute
	}
	if c.RefreshInterval == 0 {
		c.RefreshInterval = 30 * time.Minute
	}
	if c.DiscoverInterval == 0 {
		c.DiscoverInterval = 24 * time.Hour
	}
	if c.CheckInterval == 0 {
		c.CheckInterval = 5 * time.Minute
	}
	if c.RouteViaInterval == 0 {
		c.RouteViaInterval = 10 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.NetID == nil {
		c.NetID = store.NetID
	}
	return nil
}

func randomSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // источник случайности недоступен — продолжать небезопасно
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ensureSecrets создаёт при первом запуске логин/пароль app-входа и токен API (хранятся только в базе, права 0600).
func ensureSecrets(st *store.Store) (user, pass string, err error) {
	get := func(k string, mk func() string) (string, error) {
		v, ok, err := st.Setting(k)
		if err != nil {
			return "", err
		}
		if ok && v != "" {
			return v, nil
		}
		v = mk()
		return v, st.SetSetting(k, v)
	}
	if user, err = get(SetAppUser, func() string { return "app-" + randomSecret(6) }); err != nil {
		return
	}
	if pass, err = get(SetAppPass, func() string { return randomSecret(24) }); err != nil {
		return
	}
	_, err = get(SetAPIToken, func() string { return randomSecret(32) })
	return
}

// engineConfig — базовый конфиг движка: app-вход → dispatch(app) → цели / active → узел.
// Лог движка уходит в файл 0600 (в нём бывают пароли из неудачных входов).
func engineConfig(c *Config, user, pass string, acc *warp.Account) ([]byte, error) {
	logPath := filepath.Join(c.StateDir, "engine.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	_ = os.Chmod(logPath, 0o600)
	cfg := map[string]any{
		"log": map[string]any{"level": c.LogLevel, "output": logPath, "timestamp": true},
		"inbounds": []any{map[string]any{
			"type": "mixed", "tag": InboundApp, "listen": c.AppHost, "listen_port": c.AppPort,
			"users": []any{map[string]any{"username": user, "password": pass}},
		}},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": OutDirect},
			map[string]any{"type": "switch", "tag": OutActive, "interrupt_existing": true}, // смена узла рвёт зависшие туннели старого
			map[string]any{"type": "dispatch", "tag": OutApp, "default": OutActive},
		},
		"route": map[string]any{"final": OutApp},
	}
	if c.TUN.Enabled {
		applyTUN(cfg, c.TUN)
	} else if runtime.GOOS == "android" {
		// без TUN движок берёт DNS из /etc/resolv.conf, а на Android его нет: запросы шли бы на [::1]:53 и не работали (ни имена узлов, ни WARP)
		if d := SystemDNS(); d != "" {
			cfg["dns"] = map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "local", "server": d}}, "final": "local"}
			cfg["route"].(map[string]any)["default_domain_resolver"] = "local"
		}
	}
	if acc != nil {
		mask := c.WarpMask
		if mask == "none" {
			mask = ""
		}
		cfg["endpoints"] = []any{acc.Endpoint(warp.Options{Tag: OutWarp, Mask: mask, Endpoint: c.WarpEndpoint, Port: c.WarpPort, MTU: c.WarpMTU, MaskHost: c.WarpMaskHost, Browser: c.WarpBrowser, IPv4Only: runtime.GOOS == "windows"})}
	}
	return json.Marshal(cfg)
}

// Теги встроенных элементов движка.
const (
	InboundApp = "app-in"
	OutDirect  = "direct"
	OutActive  = "active"  // switch активного узла
	OutApp     = "app"     // dispatch app-входа
	OutAppTUN  = "app-tun" // dispatch «остального» трафика TUN в режиме blocked: те же цели, по умолчанию direct
	OutWarp    = "warp"    // WireGuard/AWG-endpoint WARP (есть, только если настроен аккаунт)
)

// loadWarpAccount возвращает аккаунт WARP: из конфига либо из файла (нет файла — nil без ошибки).
func loadWarpAccount(c *Config) (*warp.Account, error) {
	if c.WarpAccount != nil {
		return c.WarpAccount, nil
	}
	b, err := os.ReadFile(c.WarpAccountFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var acc warp.Account
	if err := json.Unmarshal(b, &acc); err != nil || acc.PrivateKey == "" || acc.PeerPublicKey == "" {
		return nil, fmt.Errorf("%s: неверный файл аккаунта WARP", c.WarpAccountFile)
	}
	if st, err := os.Stat(c.WarpAccountFile); err == nil && st.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(c.WarpAccountFile, 0o600) // в файле приватный ключ
	}
	return &acc, nil
}

// DefaultMaxNodes — общий потолок узлов в базе. Рабочая база бывает порядка сотен тысяч узлов, самый большой
// источник отдаёт больше ста тысяч ссылок; потолок с запасом над этим, чтобы не ломать базу, но не давать источнику с миллионами
// разных ссылок раздуть state.db и память обновления.
const DefaultMaxNodes = 400_000
