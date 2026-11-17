// brodd — демон клиента: держит узлы, проверяет их, ведёт «липкий» выбор и цели, отдаёт локальный API.
// Работает от обычного пользователя, без прав администратора: вход для приложений — mixed на 127.0.0.1 с паролем.
package main

import (
	"brod/buildinfo"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"brod/aggregator"
	"brod/api"
	"brod/core"
	"brod/killswitch"
	"brod/memwatch"
	"brod/notify"
	"brod/paths"
	"brod/supervisor"
)

// version: номер сборки и платформа (0.3.0-linux, 0.3.0-windows; у Android ядро в приложении называет себя 0.3.0-android). Подменяется при сборке: -ldflags "-X main.version=…".
var version = buildinfo.Version + "-" + runtime.GOOS

func main() {
	// на Windows демон работает службой «Brod»: диспетчер служб вызывает realMain сам и сообщает об остановке через serviceStop
	if runAsService(realMain) {
		return
	}
	realMain()
}

// baseContext отменяется по SIGINT/SIGTERM, а у службы Windows — по команде «остановить» от диспетчера служб.
func baseContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	if serviceStop != nil {
		go func() {
			<-serviceStop
			stop()
		}()
	}
	return ctx, stop
}

// quitForUpdate — демон завершается по просьбе установщика обновления: для службы Windows это не сбой (код 0, восстановление не нужно:
// помощник сам запускает службу после замены файлов).
var quitForUpdate atomic.Bool

func realMain() {
	var (
		stateDir   = flag.String("state-dir", paths.StateDir(), "каталог состояния")
		sock       = flag.String("sock", paths.Socket(), "unix-сокет API (владелец, токен не нужен)")
		apiAddr    = flag.String("api", "127.0.0.1:29081", "HTTP API с токеном для приложений (пусто — выключить)")
		appHost    = flag.String("app-host", "127.0.0.1", "адрес входа для приложений")
		appPort    = flag.Int("app-port", 29080, "порт входа для приложений (HTTP и SOCKS5, логин и пароль)")
		srcFile    = flag.String("sources-file", "", "файл дополнительных источников «имя|url|приоритет» (перечитывается по SIGHUP)")
		logLevel   = flag.String("log-level", "warn", "уровень лога движка")
		warpAcc    = flag.String("warp-account", "", "файл аккаунта WARP (секрет; по умолчанию warp-account.json в каталоге состояния; нет файла — WARP выключен)")
		warpMask   = flag.String("warp-mask", "quic", "маскировка WARP: quic | dns | stun | sip | none")
		warpEP     = flag.String("warp-endpoint", "", "адрес endpoint'а WARP (по умолчанию engage.cloudflareclient.com)")
		tun        = flag.Bool("tun", false, "режим TUN: весь трафик через активный выход (нужны права CAP_NET_ADMIN: deploy/tun-setcap.sh; проверка: scripts/tun-window.sh)")
		tunMode    = flag.String("tun-mode", "blocked", "режим TUN: blocked (только заблокированное) | all (всё) | all-but-ru (всё, кроме РФ)")
		tunIface   = flag.String("tun-interface", core.DefaultTUNInterface(), "имя TUN-интерфейса")
		tunNoDNS   = flag.Bool("tun-no-dns", false, "не направлять DNS системы в TUN (resolvectl); без этого DNS уходит мимо туннеля, если не настроить вручную")
		tunNoRoute = flag.Bool("tun-no-route", false, "TUN без изменения системных маршрутов (проверка конвейера рядом с другим VPN; трафик только по curl --interface)")
		tunV6      = flag.Bool("tun-allow-ipv6", false, "разрешить IPv6 в TUN (по умолчанию блокируется)")
		noDefault  = flag.Bool("no-default-sources", false, "не засевать источники по умолчанию (только --sources-file и добавленные пользователем)")
		noNotify   = flag.Bool("no-notify", false, "не показывать уведомления рабочего стола")
		showVer    = flag.Bool("version", false, "показать версию")
		ksRelease  = flag.Bool("release-killswitch", false, "снять правила киллсвитча (Linux: таблица inet brod_ks, Windows: фильтры WFP Brod) и выйти: аварийный выход, если демон упал при включённом VPN и трафик заблокирован")
		ksStatus   = flag.Bool("killswitch-status", false, "показать правила киллсвитча со счётчиками и выйти")
		ksProbe    = flag.Bool("killswitch-probe", false, "проверить права на установку правил киллсвитча (ничего не меняет) и выйти")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("brodd", version)
		return
	}
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("brodd: ")
	ignoreProxyEnv()
	startPprof()
	limitMemory()
	if *ksRelease || *ksStatus || *ksProbe {
		os.Exit(killswitchCommand(*ksRelease, *ksStatus))
	}

	// прежний каталог состояния (…/vpnclient) переименовывается в …/brod: узлы, выбор и WARP-аккаунт остаются; только для каталога по умолчанию
	var migrated string
	if *stateDir == paths.StateDir() {
		var merr error
		if migrated, merr = paths.MigrateLegacyState(); merr != nil {
			log.Fatal(merr)
		}
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatal(err)
	}
	// Windows: сокет лежит в подкаталоге run, его создаёт install.ps1 с правами для пользователя; здесь — только запасной путь
	// (разработка без службы). Права создаваемого здесь каталога — унаследованные от каталога состояния, то есть без пользователя.
	if runtime.GOOS == "windows" {
		if err := os.MkdirAll(filepath.Dir(*sock), 0o700); err != nil {
			log.Fatal(err)
		}
	}
	if migrated != "" {
		log.Print(migrated)
	}
	tightenStateFiles(*stateDir)
	if _, err := setupLog(*stateDir); err != nil {
		log.Printf("журнал brodd.log недоступен, пишу только в stderr: %v", err)
	}
	log.Printf("запуск brodd %s, PID %d, аргументы: %v", version, os.Getpid(), os.Args[1:])
	release, err := paths.Lock(filepath.Join(*stateDir, "brodd.lock"))
	if err != nil {
		log.Fatal(err)
	}
	defer release()

	// файл loglevel в каталоге состояния включает подробный лог движка без перезапуска с флагом (тот же приём, что на Android); явный флаг сильнее
	explicitLevel := false
	flag.Visit(func(f *flag.Flag) { explicitLevel = explicitLevel || f.Name == "log-level" })
	if lv, err := os.ReadFile(filepath.Join(*stateDir, "loglevel")); err == nil && !explicitLevel {
		switch v := strings.TrimSpace(string(lv)); v {
		case "trace", "debug", "info", "warn", "error":
			*logLevel = v
		}
	}

	seeds := loadSeeds(*srcFile, *noDefault)
	ctx, stop := baseContext()
	defer stop()
	go memwatch.Run(ctx, 10*time.Minute, 256) // расход памяти раз в 10 минут: ловит ступенчатый рост (см. limitMemory)

	// режим из командной строки перекрывает сохранённый, только если задан явно
	startMode := ""
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "tun-mode" {
			startMode = *tunMode
		}
	})
	srv := api.New(nil, version)
	notifyKind := "отключены"
	var sender notify.Sender
	var curPres atomic.Pointer[notify.Presenter]
	if !*noNotify {
		if runtime.GOOS == "windows" {
			// служба работает в сеансе 0 и не может показывать уведомления: их показывает приложение Brod (долгий опрос API)
			relay := notify.NewRelay()
			sender, notifyKind, srv.Notices = relay, "через приложение Brod (уведомления Windows)", relay
		} else {
			sender, notifyKind = notify.Open()
		}
		srv.NotifyTest = func() error { return curPres.Load().SelfTest() }
	}
	sup, err := supervisor.New(ctx, supervisor.Config{
		Base: core.Config{StateDir: *stateDir, AppHost: *appHost, AppPort: *appPort, LogLevel: *logLevel, SeedSources: seeds,
			OnUpdateInstall: func() { quitForUpdate.Store(true); stop() },
			Metered:         meteredFunc(),
			WarpAccountFile: *warpAcc, WarpMask: *warpMask, WarpEndpoint: *warpEP},
		KS:        newKillswitch(),
		KSStale:   killswitch.Active,
		TUN:       core.TUNConfig{Interface: *tunIface, AllowIPv6: *tunV6, NoAutoRoute: *tunNoRoute, NoSystemDNS: *tunNoDNS || systemDNSByEngine()},
		StartOn:   *tun,
		StartMode: startMode,
		// VPN, включённый пользователем, поднимается сам после падения, перезапуска службы и перезагрузки: иначе киллсвитч снимал бы
		// правила при старте демона, и защита от падения длилась бы только до перезапуска
		RestoreOn: true,
		// презентер уведомлений привязан к ядру: при переключении VPN ядро пересоздаётся, презентер — тоже
		OnCore: func(cctx context.Context, c *core.Core) {
			if sender == nil {
				return
			}
			pres := notify.New(c, sender)
			curPres.Store(pres)
			go pres.Run(cctx)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv.Core, srv.VPN = sup.Core, sup
	ln, err := api.ListenUnix(*sock)
	if err != nil {
		log.Fatalf("сокет %s: %v", *sock, err)
	}
	go func() {
		if err := api.Serve(ctx, ln, srv.Handler(true)); err != nil {
			log.Printf("сокет API: %v", err)
		}
	}()
	if *apiAddr != "" {
		tln, err := net.Listen("tcp", *apiAddr)
		if err != nil {
			log.Fatalf("API %s: %v", *apiAddr, err)
		}
		host, port, _ := net.SplitHostPort(*apiAddr)
		writeAPIAccess(filepath.Dir(*sock), *apiAddr, sup.Core().APIToken())
		go func() {
			// допустимые значения Host: адрес как есть и localhost с тем же портом
			if err := api.Serve(ctx, tln, srv.Handler(false, *apiAddr, net.JoinHostPort("localhost", port), net.JoinHostPort(host, port))); err != nil {
				log.Printf("HTTP API: %v", err)
			}
		}()
	}

	addr, _, _ := sup.Core().AppProxy()
	log.Printf("версия %s; состояние: %s", version, *stateDir)
	log.Printf("API: сокет %s%s", *sock, map[bool]string{true: ", HTTP " + *apiAddr, false: ""}[*apiAddr != ""])
	log.Printf("вход для приложений: %s (логин и пароль: brodctl app-proxy)", addr)
	log.Printf("уведомления: %s", notifyKind)
	if st := sup.State(); st.On {
		log.Printf("VPN включён (режим %s); списки: %s/rulesets, свои правила: rules-direct.txt / rules-proxy.txt в каталоге состояния", st.Mode, *stateDir)
	} else if st.Error != "" {
		log.Printf("VPN НЕ включён: %s", st.Error)
	} else {
		log.Printf("VPN выключен (режим %s; включить: brodctl vpn on)", st.Mode)
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	for {
		select {
		case <-hup:
			if err := sup.Core().SeedSources(loadSeeds(*srcFile, *noDefault)); err != nil {
				log.Printf("перечитывание источников: %v", err)
			} else {
				log.Printf("источники перечитаны")
			}
		case <-ctx.Done():
			log.Printf("останавливаюсь…")
			stopAt := time.Now()
			defer func() {
				log.Printf("остановка демона заняла %s", time.Since(stopAt).Round(time.Millisecond))
			}()
			done := make(chan error, 1)
			go func() { done <- sup.Close() }()
			select {
			case err := <-done:
				if err != nil {
					log.Printf("при остановке: %v", err)
				}
			case <-time.After(10 * time.Second):
				log.Printf("остановка заняла больше 10 с, выхожу принудительно; стеки горутин (что держит остановку):")
				var buf strings.Builder
				_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
				log.Print(buf.String())
				sup.ReleaseKillswitch() // штатный выход не должен оставлять сеть заблокированной
			}
			// слушатель закрывается и здесь: сервер API закрывает его параллельно, и без этого на Windows файл сокета ещё занят в момент удаления
			// и остаётся после остановки службы (нечитаемый файл ломал копирование каталога состояния)
			_ = ln.Close()
			removeSocket(*sock)
			return
		}
	}
}

// removeSocket удаляет файл сокета; пока его держит закрывающийся слушатель (Windows), пробует ещё до секунды.
func removeSocket(path string) {
	for i := 0; i < 10; i++ {
		if err := os.Remove(path); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("файл сокета %s не удалён", path)
}

// loadSeeds — источники по умолчанию плюс файл пользователя (если задан и читается).
func loadSeeds(file string, noDefault bool) []aggregator.Source {
	seeds := aggregator.Defaults()
	if noDefault {
		seeds = []aggregator.Source{}
	}
	if file == "" {
		return seeds
	}
	extra, err := aggregator.ParseSourcesFile(file)
	if os.IsNotExist(err) {
		return seeds // файл необязателен
	}
	if err != nil {
		log.Printf("файл источников %s: %v", file, err)
		return seeds
	}
	return append(seeds, extra...)
}

// ksTool — разовые операции с правилами киллсвитча платформы.
type ksTool interface {
	Release() error
	Status() (string, error)
	Probe() (string, error)
}

// killswitchCommand — разовые команды для аварийной работы с правилами; возвращает код выхода.
func killswitchCommand(release, status bool) int {
	n, manual := newKillswitchTool()
	switch {
	case release:
		if err := n.Release(); err != nil {
			fmt.Fprintln(os.Stderr, "brodd:", err)
			fmt.Fprintln(os.Stderr, manual)
			return 1
		}
		fmt.Println("правила киллсвитча сняты")
	case status:
		out, err := n.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, "brodd:", err)
			return 1
		}
		fmt.Print(out)
	default:
		if _, err := n.Probe(); err != nil {
			fmt.Fprintln(os.Stderr, "brodd:", err)
			return 1
		}
		fmt.Println("правила киллсвитча можно ставить: права есть")
	}
	return 0
}

// startPprof поднимает профилирование, если задан BRODD_PPROF=127.0.0.1:порт. По умолчанию выключено; только loopback. Пакет
// net/http/pprof намеренно не импортируется: его init регистрирует обработчики в http.DefaultServeMux, и профиль (а в дампах кучи бывают
// секреты) стал бы доступен через любой сервер на этом мультиплексоре. Здесь — отдельный мультиплексор и только runtime/pprof.
func startPprof() {
	addr := os.Getenv("BRODD_PPROF")
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		log.Printf("BRODD_PPROF=%q игнорируется: нужен адрес вида 127.0.0.1:порт", addr)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) { // CPU: ?seconds=N
		sec, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
		if sec <= 0 || sec > 120 {
			sec = 30
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if err := pprof.StartCPUProfile(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		time.Sleep(time.Duration(sec) * time.Second)
		pprof.StopCPUProfile()
	})
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) { // heap, goroutine, allocs …: ?debug=0|1|2
		name := strings.TrimPrefix(r.URL.Path, "/debug/pprof/")
		p := pprof.Lookup(name)
		if p == nil {
			http.NotFound(w, r)
			return
		}
		debugLevel, _ := strconv.Atoi(r.URL.Query().Get("debug"))
		if debugLevel == 0 {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		_ = p.WriteTo(w, debugLevel)
	})
	go func() { log.Printf("pprof: %v", http.ListenAndServe(addr, mux)) }()
	log.Printf("pprof: http://%s/debug/pprof/heap (профили: heap, goroutine, allocs, profile?seconds=N)", addr)
}

// limitMemory задаёт мягкий предел памяти рантайма (по умолчанию 1 ГиБ; GOMEMLIMIT перекрывает). Причина: WireGuard-пул буферов по
// 64 КБ на десктопном Linux не ограничен («allow for infinite memory growth»), а круги проверок через WARP гонят через него сотни
// соединений, и без предела память демона ступенчато росла до нескольких ГБ. Предел мягкий: у границы чаще работает сборщик мусора и
// возвращается память, процесс не падает.
func limitMemory() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	debug.SetMemoryLimit(1 << 30)
}

// ignoreProxyEnv: исходящие запросы демона (подписки, наборы правил, WARP) идут напрямую или через узел, который выбрал сам клиент, а не через
// HTTP(S)_PROXY из окружения. Другие VPN-клиенты ставят эти переменные глобально (на Windows — на уровне Machine), и при выключенном чужом
// клиенте запросы упирались бы в мёртвый порт. Движок sing-box этих переменных не читает.
func ignoreProxyEnv() {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if v := os.Getenv(k); v != "" {
			log.Printf("в окружении задан %s — игнорирую: запросы демона не идут через прокси окружения", k)
		}
	}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = nil
	}
}

// writeAPIAccess: на Windows приложение (обычные права) может не достучаться до сокета демона, запущенного задачей с наивысшими правами
// (разные уровни целостности при включённом UAC), и тогда идёт на loopback-HTTP с токеном. Токен лежит в каталоге сокета (на Windows подкаталог
// run состояния: единственное место, где у пользователя есть право записи), доступ тот же, что у сокета. На Linux сокет доступен владельцу всегда, файл не нужен.
func writeAPIAccess(dir, addr, token string) {
	if runtime.GOOS != "windows" || token == "" {
		return
	}
	b, _ := json.Marshal(map[string]string{"addr": addr, "token": token})
	if err := os.WriteFile(filepath.Join(dir, "api-access.json"), b, 0o600); err != nil {
		log.Printf("api-access.json: %v", err)
	}
}
