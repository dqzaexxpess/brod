package cli

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"brod/killswitch"
)

// killswitchCmd — работа с правилами киллсвитча Windows (WFP) напрямую, без демона: аварийное снятие, просмотр, сухой прогон и
// проверка набора в динамической сессии. Нужна консоль администратора. На Linux правила снимает `brodd --release-killswitch`.
func (a *app) killswitchCmd(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("на этой системе: brodd --release-killswitch | --killswitch-status")
	}
	fs := flag.NewFlagSet("killswitch", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	app := fs.String("app", defaultBrodd(), "исполняемый файл движка, которому разрешён выход мимо туннеля")
	iface := fs.String("iface", "Brod", "интерфейс TUN")
	noLAN := fs.Bool("no-lan", false, "не пропускать локальную сеть")
	hold := fs.Duration("hold", 60*time.Second, "test: сколько держать набор (не больше 10 мин)")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}
	cfg := killswitch.Config{Interface: *iface, AllowLAN: !*noLAN}
	switch args[0] {
	case "release":
		if err := (&killswitch.WFP{}).Release(); err != nil {
			return fmt.Errorf("%w (в крайнем случае — перезагрузка: правила не постоянные)", err)
		}
		fmt.Fprintln(a.env.Out, "правила киллсвитча сняты")
	case "status":
		out, err := (&killswitch.WFP{}).Status()
		if err != nil {
			return err
		}
		fmt.Fprint(a.env.Out, out)
	case "probe":
		out, err := (&killswitch.WFP{}).Probe()
		if err != nil {
			return err
		}
		fmt.Fprintln(a.env.Out, out)
	case "dry-run":
		out, err := killswitch.DescribeWFP(killswitch.WFPConfig{AppPath: *app, TunLUID: 1, AllowLAN: cfg.AllowLAN})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.env.Out, "набор (LUID TUN условно 0x1):\n%s", out)
	case "test":
		return a.killswitchTest(*app, cfg, *hold)
	default:
		return errUsage
	}
	return nil
}

// killswitchTest ставит набор в динамической сессии этого процесса и держит его hold. Фильтры динамической сессии Windows снимает сама,
// когда процесс завершается любым образом (таймер, Ctrl+C, закрытие окна, принудительное завершение) — сеть вернётся гарантированно.
func (a *app) killswitchTest(app string, cfg killswitch.Config, hold time.Duration) error {
	if hold <= 0 || hold > 10*time.Minute {
		return fmt.Errorf("--hold: от 1 с до 10 мин")
	}
	if _, err := os.Stat(app); err != nil {
		return fmt.Errorf("--app: %w", err)
	}
	luid, err := killswitch.InterfaceLUID(cfg.Interface)
	if err != nil {
		return err
	}
	if luid == 0 {
		return fmt.Errorf("интерфейс %s не найден (VPN выключен?) — без него проверка туннеля бессмысленна", cfg.Interface)
	}
	cfg.RequireInterface = true
	w := &killswitch.WFP{AppPath: app, Dynamic: true}
	defer w.Close()
	if err := w.Install(cfg); err != nil {
		return err
	}
	st, _ := w.Status()
	fmt.Fprintf(a.env.Out, "набор поставлен в динамической сессии (движок: %s, TUN: %s LUID %#x, LAN: %v), держу %s; Ctrl+C — снять сразу\n%s",
		app, cfg.Interface, luid, cfg.AllowLAN, hold, st)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	select {
	case <-time.After(hold):
	case <-stop:
	}
	if err := w.Close(); err != nil {
		return err
	}
	fmt.Fprintln(a.env.Out, "набор снят")
	return nil
}

// defaultBrodd — brodd.exe рядом с brodctl (так их ставит установщик).
func defaultBrodd() string {
	exe, err := os.Executable()
	if err != nil {
		return "brodd.exe"
	}
	return filepath.Join(filepath.Dir(exe), "brodd.exe")
}
