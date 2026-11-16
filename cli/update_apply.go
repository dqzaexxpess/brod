package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"brod/updater"
)

// brodctl update-apply — служебная команда: её запускает демон (core.spawnInstaller) из копии прежнего brodctl, чтобы заменить каталог
// установленной программы после выхода демона и приложения (Linux). Вручную не нужна.
func (a *app) updateApply(args []string) error {
	fs := flag.NewFlagSet("update-apply", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	stage := fs.String("stage", "", "распакованный и проверенный релиз")
	target := fs.String("target", "", "каталог установленной программы")
	from := fs.String("from", "", "прежняя версия")
	to := fs.String("to", "", "новая версия")
	result := fs.String("result", "", "файл итога")
	wait := fs.String("wait", "", "PID процессов, выхода которых нужно дождаться, через запятую")
	relaunch := fs.Bool("relaunch", false, "запустить приложение после установки")
	archive := fs.String("archive", "", "скачанный архив выпуска (для помощника brod-helper)")
	manifest := fs.String("manifest", "", "подписанный манифест выпуска (releases.json)")
	sig := fs.String("sig", "", "подпись манифеста (releases.json.sig)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *stage == "" || *target == "" || *to == "" {
		return errUsage
	}
	var pids []int
	for _, s := range strings.Split(*wait, ",") {
		if s = strings.TrimSpace(s); s != "" {
			p, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("--wait: %q", s)
			}
			pids = append(pids, p)
		}
	}
	ap := &updater.LinuxApply{Stage: *stage, Target: *target, From: *from, To: *to, WaitPIDs: pids, Result: *result, Relaunch: *relaunch,
		Archive: *archive, Manifest: *manifest, Sig: *sig,
		Log: func(f string, v ...any) { fmt.Fprintf(a.env.Out, time.Now().Format("15:04:05 ")+f+"\n", v...) }}
	if err := ap.Run(); err != nil {
		return err
	}
	fmt.Fprintln(a.env.Out, "установлено:", *to)
	return nil
}
