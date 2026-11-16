//go:build android

// Package androidcore — ядро brodd, встроенное в процесс приложения Android (gomobile bind).
//
// Приложение (Kotlin) вызывает Start с платформенным интерфейсом libbox: TUN-дескриптор, защита сокетов и монитор сети даёт VpnService.
// Flutter-интерфейс говорит с ядром по unix-сокету в каталоге приложения (тот же API и тот же клиент, что на Linux): сокет доступен
// только приложению, а loopback-порт был бы виден любому приложению на устройстве.
package androidcore

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/sagernet/sing-box/experimental/libbox"

	"brod/api"
	"brod/buildinfo"
	"brod/core"
	"brod/dnsfallback"
	"brod/engine"
	"brod/memwatch"
	"brod/supervisor"
)

var version = buildinfo.Version + "-android"

// Мягкий лимит памяти Go-рантайма: на Linux демон без лимита раздувался до 3,4 ГБ из-за буферов WireGuard; на телефоне запас ~3-4 ГБ
// на всю систему. Цифра предварительная, уточняется замером на устройстве.
const memoryLimit = 768 << 20

// Start запускает ядро и возвращает путь unix-сокета API. stateDir — каталог приложения (например, filesDir/brod). Повторный вызов
// при работающем ядре возвращает тот же сокет.
func Start(stateDir string, platform libbox.PlatformInterface) (string, error) {
	return start(stateDir, platform, false)
}

// StartVPN — то же, но ядро сразу создаётся с включённым VPN (служба VPN уже работает): холодный старт «плитка / постоянный VPN / после
// перезагрузки» без второго пересоздания ядра и второго холодного туннеля WARP. Если TUN поднять не вышло, ядро стартует без VPN.
func StartVPN(stateDir string, platform libbox.PlatformInterface) (string, error) {
	return start(stateDir, platform, true)
}

func start(stateDir string, platform libbox.PlatformInterface, startOn bool) (string, error) {
	if sock, ok := existingSock(); ok {
		return sock, nil
	}
	mu.Lock()
	defer mu.Unlock()
	if cur != nil { // между проверкой и захватом мьютекса другой вызов успел создать ядро
		return cur.sock, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	// на Android нет /tmp: каталог для временных файлов Go и SQLite — в каталоге приложения
	tmp := filepath.Join(stateDir, "tmp")
	_ = os.MkdirAll(tmp, 0o700)
	_ = os.Setenv("TMPDIR", tmp)
	_ = os.Setenv("SQLITE_TMPDIR", tmp)
	openLog(stateDir)
	core.SystemDNS = currentDNS
	// у Go на Android нет /etc/resolv.conf: DNS сети задаёт приложение, а если он молчит или отвечает отказом (мобильные сети, блокировки),
	// запрос уходит в DoH на IP-адрес (прямой HTTPS ядра: обновления, списки, профили)
	net.DefaultResolver = dnsfallback.Resolver(currentDNS, dnsfallback.DefaultDoH)
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("brod: ")
	debug.SetMemoryLimit(memoryLimit)
	// Go ≥1.25 сам следит за лимитом cgroup и периодически перечитывает его (на Android cgroup v1: заметная доля процессора в простое);
	// явное значение GOMAXPROCS это отключает
	runtime.GOMAXPROCS(runtime.NumCPU())
	engine.ContextHook = func(ctx context.Context) { libbox.RegisterPlatform(ctx, platform) }

	ctx, cancel := context.WithCancel(context.Background())
	ensureWarp(stateDir)
	snd := &sender{}
	notifySender.Store(snd)
	seeds := loadSeeds(stateDir)
	srv := api.New(nil, version)
	sup, err := supervisor.New(ctx, supervisorConfig(stateDir, startOn, seeds, snd))
	if err != nil {
		cancel()
		return "", fmt.Errorf("ядро: %w", err)
	}
	srv.Core, srv.VPN = sup.Core, sup
	sock := filepath.Join(stateDir, "brodd.sock")
	ln, err := api.ListenUnix(sock)
	if err != nil {
		cancel()
		_ = sup.Close()
		return "", fmt.Errorf("сокет %s: %w", sock, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := api.Serve(ctx, ln, srv.Handler(true)); err != nil {
			log.Printf("сокет API: %v", err)
		}
	}()
	cur = &instance{cancel: cancel, sup: sup, sock: sock, done: done}
	go watchState(ctx, sup)
	go memwatch.Run(ctx, time.Minute, 192) // на телефоне память ограничена: лишнее возвращаем системе
	log.Printf("ядро запущено, версия %s, сокет %s", version, sock)
	return sock, nil
}
