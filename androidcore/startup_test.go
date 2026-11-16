package androidcore

// Start/StartVPN целиком на хосте не запускаются: androidcore.go собирается только с тегом android и вызывает libbox.RegisterPlatform
// (нужны платформенный интерфейс libbox, VpnService с TUN-дескриптором и среда gomobile). Поэтому всё, что start делает до и вокруг libbox,
// вынесено в startup.go без тега и проверяется здесь: повторный вызов, выбор startOn, состав конфига супервизора, вшитые источники,
// регистрация WARP (только ранние выходы, без сети) и журнал. Компиляцию самого start держит `GOOS=android go vet ./androidcore` в CI.

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brod/aggregator"
	"brod/core"
	"brod/internal/testport"
	"brod/supervisor"
)

// Повторный Start/StartVPN при работающем ядре возвращает тот же сокет и ничего не пересоздаёт.
func TestExistingSockIdempotent(t *testing.T) {
	mu.Lock()
	saved := cur
	cur = nil
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); cur = saved; mu.Unlock() })
	if s, ok := existingSock(); ok || s != "" {
		t.Fatalf("без ядра: %q %v", s, ok)
	}
	in, sock := startFake(t)
	for range 3 {
		s, ok := existingSock()
		if !ok || s != sock {
			t.Fatalf("повторный вызов: %q %v, ожидался %q", s, ok, sock)
		}
	}
	mu.Lock()
	same := cur == in
	mu.Unlock()
	if !same {
		t.Fatal("работающее ядро подменено")
	}
	Stop()
	if _, ok := existingSock(); ok {
		t.Fatal("после Stop ядро числится работающим")
	}
}

// Start даёт StartOn=false, StartVPN — true; остальное в конфиге Android не зависит от startOn.
func TestSupervisorConfigStartOn(t *testing.T) {
	dir := t.TempDir()
	off := supervisorConfig(dir, false, nil, &sender{})
	on := supervisorConfig(dir, true, nil, &sender{})
	if off.StartOn || !on.StartOn {
		t.Fatalf("StartOn: Start=%v StartVPN=%v", off.StartOn, on.StartOn)
	}
	for name, c := range map[string]supervisor.Config{"Start": off, "StartVPN": on} {
		if c.KS != nil {
			t.Errorf("%s: киллсвитч файрвола на Android не используется", name)
		}
		if c.TUN.Interface != "tun0" || !c.TUN.NoSystemDNS {
			t.Errorf("%s: TUN %+v", name, c.TUN)
		}
		if got := c.Tunnels("tun0"); len(got) != 0 {
			t.Errorf("%s: чужие туннели не ищутся, а вернулось %v", name, got)
		}
		b := c.Base
		if b.StateDir != dir || b.AppHost != "127.0.0.1" || b.AppPort != 29080 || !b.NoNetlink || b.NodeRetention != 7*24*time.Hour {
			t.Errorf("%s: Base %+v", name, b)
		}
		if b.NetID() == "" { // значение зависит от SetNetworkID в других тестах, но не пустое
			t.Errorf("%s: NetID %q", name, b.NetID())
		}
		if b.LogLevel != "warn" {
			t.Errorf("%s: уровень лога по умолчанию %q", name, b.LogLevel)
		}
		if b.Idle == nil || b.Metered == nil || c.OnCore == nil {
			t.Errorf("%s: не заданы Idle/Metered/OnCore", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "loglevel"), []byte("debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := supervisorConfig(dir, false, nil, &sender{}).Base.LogLevel; got != "debug" {
		t.Fatalf("файл loglevel не учтён: %q", got)
	}
}

// Конфиг с startOn=true доходит до супервизора: без TUN в тесте включить VPN не выходит, и по контракту StartVPN ядро всё равно стартует
// (без VPN, причина в состоянии); с startOn=false ядро стартует выключенным без ошибки.
func TestSupervisorFromConfigStartOn(t *testing.T) {
	for _, startOn := range []bool{false, true} {
		dir := t.TempDir()
		cfg := supervisorConfig(dir, startOn, []aggregator.Source{}, &sender{})
		cfg.Base.AppPort = testport.Next()
		cfg.Base.LogLevel = "error"
		cfg.Base.Manual = true
		cfg.OnCore = nil
		cfg.NewCore = func(c core.Config) (*core.Core, error) {
			c.TUN = core.TUNConfig{} // TUN-дескриптора на хосте нет
			return core.New(c)
		}
		ctx, cancel := context.WithCancel(context.Background())
		sup, err := supervisor.New(ctx, cfg)
		if err != nil {
			cancel()
			t.Fatalf("startOn=%v: ядро не стартовало: %v", startOn, err)
		}
		if sup.Core() == nil {
			t.Errorf("startOn=%v: ядра нет", startOn)
		}
		if sup.On() != startOn {
			t.Errorf("startOn=%v, а VPN включён=%v", startOn, sup.On())
		}
		cancel()
		_ = sup.Close()
	}
}

// Вшитые источники кладутся в stateDir/sources.txt и добавляются к источникам по умолчанию.
func TestLoadSeeds(t *testing.T) {
	dir := t.TempDir()
	seeds := loadSeeds(dir)
	b, err := os.ReadFile(filepath.Join(dir, "sources.txt"))
	if err != nil || !bytes.Equal(b, sources) {
		t.Fatalf("sources.txt: %v", err)
	}
	if len(seeds) <= len(aggregator.Defaults()) {
		t.Fatalf("вшитые источники не добавились: %d при %d по умолчанию", len(seeds), len(aggregator.Defaults()))
	}
	// каталог недоступен для записи: остаются источники по умолчанию, паники нет
	if got := loadSeeds(filepath.Join(dir, "нет", "такого")); len(got) != len(aggregator.Defaults()) {
		t.Fatalf("без записи файла: %d источников", len(got))
	}
}

// ensureWarp: при готовом аккаунте и при свежей метке попытки (не чаще раза в сутки) сеть не трогается и файлы не меняются.
func TestEnsureWarpEarlyExits(t *testing.T) {
	dir := t.TempDir()
	acc := filepath.Join(dir, "warp-account.json")
	if err := os.WriteFile(acc, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensureWarp(dir)
	if _, err := os.Stat(filepath.Join(dir, "warp-register.try")); err == nil {
		t.Fatal("при готовом аккаунте создана метка попытки")
	}
	if b, _ := os.ReadFile(acc); string(b) != "{}" {
		t.Fatalf("аккаунт перезаписан: %q", b)
	}

	dir2 := t.TempDir()
	mark := filepath.Join(dir2, "warp-register.try")
	if err := os.WriteFile(mark, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(mark, old, old); err != nil {
		t.Fatal(err)
	}
	ensureWarp(dir2)
	st, err := os.Stat(mark)
	if err != nil || !st.ModTime().Equal(old) {
		t.Fatalf("свежая метка тронута: %v %v", err, st)
	}
	if _, err := os.Stat(filepath.Join(dir2, "warp-account.json")); err == nil {
		t.Fatal("аккаунт создан без попытки")
	}
}

// openLog: журнал пишется в stateDir/core.log; файл больше 512 КБ при запуске урезается.
func TestOpenLogTruncatesBigFile(t *testing.T) {
	prevOut, prevFlags, prevPrefix := log.Writer(), log.Flags(), log.Prefix()
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags); log.SetPrefix(prevPrefix) })
	dir := t.TempDir()
	path := filepath.Join(dir, "core.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 600<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	openLog(dir)
	log.Print("метка-теста")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 1<<10 || !strings.Contains(string(b), "метка-теста") {
		t.Fatalf("журнал не урезан или не пишется: %d байт", len(b))
	}
	// небольшой файл дописывается
	openLog(dir)
	log.Print("вторая")
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "метка-теста") || !strings.Contains(string(b), "вторая") {
		t.Fatalf("журнал не дописан: %q", b)
	}
}
