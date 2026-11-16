package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// Обновление приложения (API /v1/update): состояние, проверка сейчас, скачивание с проверкой. Установку ядро не делает.

func humanBytes(n float64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МиБ", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f КиБ", n/(1<<10))
	}
	return fmt.Sprintf("%.0f Б", n)
}

func (a *app) printUpdate(u map[string]any) {
	fmt.Fprintf(a.env.Out, "версия: %s (%s)\n", str(u, "current"), str(u, "platform"))
	if u["available"] == true {
		fmt.Fprintf(a.env.Out, "доступна: %s, файл %s, sha256 %s…\n", str(u, "app"), humanBytes(num(u["size"])), strings.TrimSpace(str(u, "sha256"))[:min(12, len(str(u, "sha256")))])
		if n := str(u, "notes"); n != "" {
			fmt.Fprintf(a.env.Out, "что нового: %s\n", n)
		}
	} else if str(u, "checked") == "" {
		fmt.Fprintln(a.env.Out, "проверка ещё не выполнялась (`brodctl update check`)")
	} else {
		fmt.Fprintf(a.env.Out, "обновлений нет (проверено %s)\n", ago(str(u, "checked")))
	}
	if e := str(u, "error"); e != "" {
		fmt.Fprintf(a.env.Out, "последняя проверка не удалась: %s\n", e)
	}
	if l, ok := u["last"].(map[string]any); ok {
		if l["ok"] == true {
			fmt.Fprintf(a.env.Out, "последняя установка: %s → %s, успешно (%s)\n", str(l, "from"), str(l, "to"), ago(str(l, "at")))
			if w := str(l, "warning"); w != "" {
				fmt.Fprintf(a.env.Out, "ВНИМАНИЕ: %s\n", w)
			}
		} else {
			fmt.Fprintf(a.env.Out, "последняя установка: %s → %s НЕ удалась (%s): %s\n", str(l, "from"), str(l, "to"), ago(str(l, "at")), str(l, "error"))
		}
	}
	d, _ := u["download"].(map[string]any)
	switch str(d, "state") {
	case "downloading":
		fmt.Fprintf(a.env.Out, "скачивание: %s из %s\n", humanBytes(num(d["bytes"])), humanBytes(num(d["total"])))
	case "ready":
		fmt.Fprintf(a.env.Out, "скачано и проверено (размер и SHA-256 по подписанному манифесту): %s\nУстановить: Android — кнопка «Установить» в приложении; Linux и Windows — `brodctl update install` или кнопка в приложении\n", str(d, "path"))
	case "failed":
		fmt.Fprintf(a.env.Out, "скачивание не удалось: %s\n", str(d, "error"))
	}
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func (a *app) update(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	var u map[string]any
	switch sub {
	case "status", "":
		if err := a.call("GET", "/v1/update", nil, &u); err != nil {
			return err
		}
	case "check":
		if err := a.call("POST", "/v1/update/check", nil, &u); err != nil {
			return err
		}
	case "download":
		fs := flag.NewFlagSet("update download", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		wait := fs.Bool("wait", false, "ждать окончания скачивания и показывать ход")
		metered := fs.Bool("metered", false, "разрешить скачивание по мобильной сети или сети с оплатой трафика")
		reorder(fs, args)
		if err := a.call("POST", "/v1/update/download", map[string]any{"allow_metered": *metered}, &u); err != nil {
			return err
		}
		for *wait && !a.asJSON {
			d, _ := u["download"].(map[string]any)
			if s := str(d, "state"); s != "downloading" {
				break
			}
			fmt.Fprintf(a.env.Err, "\rскачано %s из %s   ", humanBytes(num(d["bytes"])), humanBytes(num(d["total"])))
			time.Sleep(time.Second)
			if err := a.call("GET", "/v1/update", nil, &u); err != nil {
				return err
			}
		}
		if *wait {
			fmt.Fprintln(a.env.Err)
		}
	case "install":
		// закройте приложение заранее: демон завершится сразу после запуска установки, а помощник дождётся и приложения
		if err := a.call("POST", "/v1/update/install", map[string]any{}, &u); err != nil {
			return err
		}
		if !a.asJSON {
			fmt.Fprintln(a.env.Out, "установка запущена: демон сейчас завершится, помощник заменит файлы и запустит приложение; итог — `brodctl update status` после запуска")
		}
	default:
		return errUsage
	}
	if a.show(u) {
		return nil
	}
	a.printUpdate(u)
	if d, _ := u["download"].(map[string]any); sub == "download" && str(d, "state") == "failed" {
		return fmt.Errorf("скачивание не удалось: %s", str(d, "error"))
	}
	return nil
}
