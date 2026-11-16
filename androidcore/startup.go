// Без тега android: части start, которые не зависят от libbox (повторный вызов, вшитые источники, конфиг супервизора, регистрация WARP,
// журнал), вынесены сюда, чтобы Start/StartVPN проверялись обычным go test на Linux (startup_test.go). Сам start (libbox, сокет, горутины)
// на хосте не запускается.
package androidcore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"brod/aggregator"
	"brod/api"
	"brod/core"
	"brod/notify"
	"brod/supervisor"
	"brod/warp"
)

// existingSock возвращает сокет уже работающего ядра: повторный Start/StartVPN его не пересоздаёт.
func existingSock() (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		return cur.sock, true
	}
	return "", false
}

// loadSeeds: источники по умолчанию плюс вшитый список (кладётся в stateDir/sources.txt и разбирается оттуда: тот же разбор, что у файла на Linux).
func loadSeeds(stateDir string) []aggregator.Source {
	seeds := aggregator.Defaults()
	srcFile := filepath.Join(stateDir, "sources.txt")
	if err := os.WriteFile(srcFile, sources, 0o600); err == nil {
		if extra, err := aggregator.ParseSourcesFile(srcFile); err == nil {
			seeds = append(seeds, extra...)
		} else {
			log.Printf("вшитые источники: %v", err)
		}
	}
	return seeds
}

// supervisorConfig — конфиг супервизора ядра Android. startOn: Start — false, StartVPN — true (VPN включён с первого создания ядра).
func supervisorConfig(stateDir string, startOn bool, seeds []aggregator.Source, snd *sender) supervisor.Config {
	return supervisor.Config{
		StartOn: startOn,
		Base: core.Config{
			StateDir: stateDir, AppHost: "127.0.0.1", AppPort: 29080, LogLevel: logLevel(stateDir),
			NetID: currentNetID,
			// без VPN и при скрытом интерфейсе фоновые проходы спят
			Idle:        func() bool { return !api.UIVisible() },
			Metered:     metered.Load,
			SeedSources: seeds,
			NoNetlink:   true,
			// IP вместо engage.cloudflareclient.com: при включённом TUN DNS-запрос эндпоинта WARP не проходил (таймауты, остановка ядра ~20 с)
			WarpEndpoint: "162.159.192.1",
			// пропавшие из источников узлы хранятся 7 суток, а не 14 (база телефона 330 МБ: треть узлов уже пропавшие); избранные и закреплённые не удаляются
			NodeRetention: 7 * 24 * time.Hour,
		},
		// киллсвитч на Android — системная «Постоянный VPN» с блокировкой соединений без VPN (настройки системы), не правила файрвола
		KS: nil,
		// чужие туннели на Android не ищем: VpnService в системе один, а /sys/class/net приложению закрыт (SELinux), сканирование шло бы каждые полсекунды
		Tunnels: func(string) []string { return nil },
		TUN:     core.TUNConfig{Interface: "tun0", NoSystemDNS: true},
		// презентер уведомлений привязан к ядру: при переключении VPN ядро пересоздаётся, презентер — тоже
		OnCore: func(cctx context.Context, c *core.Core) {
			pres := notify.New(c, snd)
			go pres.Run(cctx)
		},
	}
}

// ensureWarp регистрирует аккаунт WARP при первом запуске (на Linux его кладёт владелец командой warp): без аккаунта проверки узлов идут
// напрямую, а не через WARP-цепочку. Не больше одной попытки в сутки, чтобы недоступный API не тормозил каждый запуск.
func ensureWarp(dir string) {
	acc := filepath.Join(dir, "warp-account.json")
	if _, err := os.Stat(acc); err == nil {
		return
	}
	mark := filepath.Join(dir, "warp-register.try")
	if st, err := os.Stat(mark); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		return
	}
	_ = os.WriteFile(mark, nil, 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, err := warp.Register(ctx, nil)
	if err != nil {
		log.Printf("WARP: регистрация не удалась: %v", err)
		diagWarp()
		return
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	if err := os.WriteFile(acc, b, 0o600); err != nil {
		log.Printf("WARP: %v", err)
		return
	}
	log.Printf("WARP: аккаунт зарегистрирован")
}

// openLog направляет журнал ядра и аварийные трассы Go в stateDir/core.log: на Android stderr приложения никуда не попадает. Файл урезается
// при запуске, если вырос больше 512 КБ.
func openLog(dir string) {
	path := filepath.Join(dir, "core.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 512<<10 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
}

// diagWarp при неудачной регистрации пишет в журнал, где именно ломается путь до API: TCP или TLS.
func diagWarp() {
	for _, addr := range []string{"104.16.24.84:443", "104.16.192.82:443"} {
		t := time.Now()
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			log.Printf("WARP диагностика: TCP %s: %v (%s)", addr, err, time.Since(t).Round(time.Millisecond))
			continue
		}
		log.Printf("WARP диагностика: TCP %s ок (%s)", addr, time.Since(t).Round(time.Millisecond))
		t = time.Now()
		tc := tls.Client(c, &tls.Config{ServerName: "api.cloudflareclient.com"})
		_ = tc.SetDeadline(time.Now().Add(6 * time.Second))
		err = tc.Handshake()
		log.Printf("WARP диагностика: TLS %s: %v (%s)", addr, err, time.Since(t).Round(time.Millisecond))
		c.Close()
	}
}
