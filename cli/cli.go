// Package cli — клиент командной строки brodctl для локального API демона.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"brod/paths"
)

// Env — окружение запуска (подменяется в тестах).
type Env struct {
	Out, Err io.Writer
	Getenv   func(string) string
}

type app struct {
	env     Env
	base    string
	token   string
	hc      *http.Client
	asJSON  bool
	fullIDs bool
	printed bool   // вывод уже сделан командой
	lastRaw []byte // последний ответ API (для --json у команд с текстовым выводом)
}

const usage = `brodctl — управление демоном brodd

Использование: brodctl [--sock ПУТЬ | --addr URL --token ТОКЕН] [--json] КОМАНДА [аргументы]

Узлы
  status                          состояние: активный узел, сеть, число узлов, деградация
  list [--alive] [--fav] [-n N] [--all]   узлы (живые и избранные первыми; --all включает скрытые security=none)
  connect УЗЕЛ                    сделать узел активным (ID или однозначный префикс от 6 символов)
  fav УЗЕЛ [--off]                избранное
  pin УЗЕЛ [--off]                закрепить (не чистится)
  test [--limit N] [--force] [--ids-file Ф]   проверить узлы сейчас (--ids-file: ровно эти узлы, ID по одному в строке)
  probe                           проверить активный узел сейчас
  alternatives [-n N]             подобрать проверенные альтернативы активному узлу
  refresh                         обновить подписки
  speed УЗЕЛ                      замерить скорость скачивания через узел сейчас (в фоне замеряется само)
  exit-info УЗЕЛ                  узнать страну и чистоту выхода узла сейчас (в фоне собирается само)
  vpn [on|off] [--mode РЕЖИМ]    переключатель VPN (TUN): состояние / включить / выключить; режим blocked|all|all-but-ru запоминается
  vpn autoconnect last|always|off  включать ли VPN при запуске демона: как было / всегда / нет
  vpn selftest                    самотест работающего туннеля: узел насквозь, DNS заблокированного, внешний запрос через туннель (IP выхода), заблокированный сайт
  node-check [УЗЕЛ]               сквозная проверка узла: запрос ЧЕРЕЗ узел до внешнего сайта, выход (IP, страна) или причина провала; без аргумента — активный
  udp [УЗЕЛ]                      замер UDP через узел (STUN): потери и задержка, годится ли узел для звонков; без аргумента — активный
  rulesets update                 обновить списки маршрутизации TUN (заблокированное, банки, «только внутри РФ») и свои списки
  sets [list|add URL [--title Н]|rm ID]   свои списки маршрутизации по URL (.srs или JSON sing-box); в правилах — rule_set: ["ext-…"]
  notify-test                     проверить уведомления рабочего стола (две тестовые кнопки, узлы не затрагиваются)
  ota sources                     забрать подписанный список источников у издателя (раз в сутки ядро делает это само)
  warp probe                      проверить WARP (рукопожатие и HTTPS через endpoint)
  warp register [-force]          зарегистрировать аккаунт WARP (напрямую, иначе через активный узел)
  warp import [-force] ФАЙЛ       загрузить готовый warp-account.json
                                  цепочка через WARP: settings set check.via_warp 1

Источники
  sources                         список
  sources add ИМЯ URL [--prio N]  добавить
  sources enable|disable ИМЯ
  sources rm ИМЯ
  candidates [list]               найденные автопоиском источники (отдельно от списка источников): ссылок, схемы, дни наблюдения, живых из проверенных
  candidates add ID               завести источник из кандидата (только по вашей команде)
  candidates dismiss ID           скрыть кандидата: заново не предлагается
  candidates scan [--metered]     внеочередной проход автопоиска (раз в сутки ядро делает это само)
  candidates groups               те же кандидаты, сгруппированные по содержимому (зеркала): представитель, размер группы, ссылок, живых
  candidates dismiss-group ID     скрыть всю группу (новые похожие кандидаты тоже не предлагаются)

Обновление приложения (подписанный манифест релизов; ставится вручную)
  update [status]                 текущая версия, доступное обновление, ход скачивания
  update check                    проверить сейчас (раз в сутки ядро делает это само; выключить: settings set update.check 0)
  update download [--wait] [--metered]  скачать файл своей платформы и проверить размер и SHA-256 по манифесту
  update install                  Linux и Windows: поставить скачанное (демон завершится, файлы заменит помощник; приложение закройте заранее)

Правила маршрутизации (работают при включённом VPN; применение перезапускает VPN)
  rules [list]                    правила: действие, выход (страна/узел/WARP), текущая привязка, условия
  rules export                    правила JSON в стандартный вывод
  rules import ФАЙЛ|-             заменить весь список (JSON: список правил или {"rules":[…]})
  rules via                       текущие привязки выходов правил (узел, запасной вариант, кандидатов)
  rules countries                 страны с проверенными узлами (для выхода «страна»)
  profiles [list]                 профили маршрутизации
  profiles new ИМЯ [--from ПРОФИЛЬ]   создать (пустой или копией)
  profiles use ПРОФИЛЬ            переключить (перезапускает VPN); ПРОФИЛЬ — id, префикс или название
  profiles rename ПРОФИЛЬ ИМЯ | rm ПРОФИЛЬ
  profiles share ПРОФИЛЬ          ссылка brod://route/…
  profiles import ССЫЛКА|ФАЙЛ|- [--name ИМЯ]   добавить профиль (не активируется)

Цели
  targets                         список целей
  targets show ЦЕЛЬ
  targets add ЦЕЛЬ [--title Т] [--domain Д]... [--suffix С]... [--keyword К]... [--check http|youtube|google-api|telegram]
                  [--url U] [--expect 200,403] [--policy sticky-manual|auto-failover] [--k N] [--no-direct]
  targets rm ЦЕЛЬ
  targets switch ЦЕЛЬ УЗЕЛ|direct|warp|next
  targets probe ЦЕЛЬ
  targets probe-url ЦЕЛЬ URL [--ttl СЕК]

Прочее
  events [-f] [--after ID]        журнал событий (-f — следить)
  logs [-n N]                     лог движка (адреса и пароли скрыты)
  settings                        настройки; settings set КЛЮЧ ЗНАЧЕНИЕ
  app-proxy                       адрес и учётные данные входа для приложений
  token create ИМЯ | list | revoke ИМЯ   токены приложений (показывается один раз)

Киллсвитч Windows (без демона, консоль администратора; включение: settings set killswitch.enabled 1)
  killswitch release              снять правила (аварийно: демон упал и не поднимается, сеть заблокирована)
  killswitch status               установленные фильтры WFP
  killswitch probe                проверить доступ к WFP (ничего не меняет)
  killswitch dry-run [--no-lan]   показать набор фильтров, ничего не ставя
  killswitch test [--hold 60s] [--app brodd.exe] [--iface Brod] [--no-lan]
                                  поставить набор на время в динамической сессии (снимется с выходом процесса)
`

// Run выполняет команду и возвращает код выхода.
func Run(args []string, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	a := &app{env: env}
	fs := flag.NewFlagSet("brodctl", flag.ContinueOnError)
	fs.SetOutput(env.Err)
	sock := fs.String("sock", "", "unix-сокет демона")
	addr := fs.String("addr", "", "адрес API (http://127.0.0.1:порт) вместо сокета")
	token := fs.String("token", env.Getenv("VPNCTL_TOKEN"), "токен для --addr")
	fs.BoolVar(&a.asJSON, "json", false, "вывод в JSON")
	fs.BoolVar(&a.fullIDs, "full", false, "полные идентификаторы")
	fs.Usage = func() { fmt.Fprint(env.Err, usage) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "help" {
		fmt.Fprint(env.Out, usage)
		return 0
	}
	if *addr != "" {
		u, err := url.Parse(*addr)
		if err != nil || u.Host == "" {
			fmt.Fprintln(env.Err, "неверный --addr")
			return 2
		}
		a.base, a.token, a.hc = strings.TrimRight(*addr, "/"), *token, &http.Client{Timeout: 5 * time.Minute}
	} else {
		path := *sock
		if path == "" {
			path = paths.Socket()
		}
		a.base = "http://brodd"
		a.hc = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			}}}
	}
	if err := a.dispatch(rest[0], rest[1:]); err != nil {
		if err == errUsage {
			fmt.Fprint(env.Err, usage)
			return 2
		}
		fmt.Fprintln(env.Err, "ошибка:", err)
		return 1
	}
	if a.asJSON && !a.printed && len(a.lastRaw) > 0 { // команда с текстовым выводом: при --json отдаём ответ API как есть
		fmt.Fprintln(env.Out, strings.TrimSpace(string(a.lastRaw)))
	}
	return 0
}

var errUsage = fmt.Errorf("usage")

func (a *app) dispatch(cmd string, args []string) error {
	switch cmd {
	case "status":
		return a.status()
	case "list", "ls":
		return a.list(args)
	case "connect":
		return a.nodeAction(args, "connect", nil)
	case "fav":
		return a.flagCmd(args, "favorite")
	case "pin":
		return a.flagCmd(args, "pin")
	case "test":
		return a.test(args)
	case "probe":
		return a.simplePost("/v1/probe-active", "активный узел", func(m map[string]any) string {
			if m["ok"] == true {
				return fmt.Sprintf("работает, %v мс", m["latency_ms"])
			}
			return fmt.Sprintf("НЕ работает: %v %v", m["class"], m["detail"])
		})
	case "udp":
		return a.nodeUDP(args)
	case "alternatives", "alt":
		return a.alternatives(args)
	case "refresh":
		return a.refresh()
	case "speed":
		return a.nodeAction(args, "speed", nil)
	case "exit-info":
		return a.nodeAction(args, "exit-info", nil)
	case "node-check":
		return a.nodeCheck(args)
	case "vpn":
		if len(args) == 1 && args[0] == "selftest" {
			return a.vpnSelfTest()
		}
		return a.vpn(args)
	case "rulesets":
		if len(args) == 1 && args[0] == "update" {
			var r map[string]any
			if err := a.call("POST", "/v1/rulesets/update", nil, &r); err != nil {
				return err
			}
			if a.show(r) {
				return nil
			}
			upd, _ := r["updated"].([]any)
			fail, _ := r["failed"].(map[string]any)
			fmt.Fprintf(a.env.Out, "обновлено %d, без изменений %s, не удалось %d\n", len(upd), str(r, "unchanged"), len(fail))
			for k, v := range fail {
				fmt.Fprintf(a.env.Out, "  %s: %v\n", k, v)
			}
			return nil
		}
		return errUsage
	case "sets", "set":
		return a.sets(args)
	case "killswitch":
		return a.killswitchCmd(args)
	case "notify-test":
		if err := a.call("POST", "/v1/notify-test", nil, nil); err != nil {
			return err
		}
		if !a.asJSON {
			fmt.Fprintln(a.env.Out, "уведомление отправлено: нажмите кнопку, затем `brodctl events -n 3`")
		}
		return nil
	case "warp":
		if len(args) == 1 && args[0] == "probe" {
			return a.simplePost("/v1/warp/probe", "WARP", func(m map[string]any) string {
				if m["ok"] == true {
					return fmt.Sprintf("работает, %v мс", m["latency_ms"])
				}
				return fmt.Sprintf("НЕ работает: %v %v (рукопожатие WARP нестабильно — повторите)", m["class"], m["detail"])
			})
		}
		if len(args) >= 1 && args[0] == "register" {
			return a.warpRegister(args[1:])
		}
		if len(args) >= 1 && args[0] == "import" {
			return a.warpImport(args[1:])
		}
		return errUsage
	case "ota":
		if len(args) == 1 && args[0] == "sources" {
			return a.simplePost("/v1/ota/sources", "список источников", func(m map[string]any) string {
				return fmt.Sprint(m["message"])
			})
		}
		return errUsage
	case "sources":
		return a.sources(args)
	case "candidates", "candidate":
		return a.candidates(args)
	case "update":
		return a.update(args)
	case "update-apply":
		return a.updateApply(args)
	case "rules", "rule":
		return a.rules(args)
	case "profiles", "profile":
		return a.profiles(args)
	case "targets", "target":
		return a.targets(args)
	case "events":
		return a.events(args)
	case "logs":
		return a.logs(args)
	case "settings":
		return a.settings(args)
	case "app-proxy":
		return a.appProxy()
	case "token":
		return a.tokenCmd(args)
	}
	fmt.Fprintf(a.env.Err, "неизвестная команда %q\n\n", cmd)
	return errUsage
}

// ---- HTTP ----

type apiError struct {
	Code int
	Msg  string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Msg, e.Code) }

func (a *app) call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		return err
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "forbidden by its access permissions") {
			// служба работает, но у этого пользователя нет доступа к сокету (Brod установлен для другого пользователя)
			return fmt.Errorf("нет доступа к демону: %w\n(Brod установлен для другого пользователя; переустановите его для этой учётной записи)", err)
		}
		return fmt.Errorf("демон недоступен: %w\n(запущен ли brodd? путь сокета можно задать --sock)", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &apiError{resp.StatusCode, e.Error}
	}
	a.lastRaw = data
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// show печатает сырой JSON при --json и сообщает, что вывод уже сделан.
func (a *app) show(v any) bool {
	if !a.asJSON {
		return false
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(a.env.Out, string(b))
	a.printed = true
	return true
}

func (a *app) id(s string) string {
	if a.fullIDs || len(s) <= 10 {
		return s
	}
	return s[:10]
}

func ago(s string) string {
	if s == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "сейчас"
	case d < time.Minute:
		return fmt.Sprintf("%d с назад", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	}
	return fmt.Sprintf("%d дн назад", int(d.Hours()/24))
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		switch x := v.(type) {
		case string:
			return x
		case float64:
			if x == float64(int64(x)) {
				return fmt.Sprint(int64(x))
			}
		}
		return fmt.Sprint(v)
	}
	return ""
}

func (a *app) tw() *tabwriter.Writer { return tabwriter.NewWriter(a.env.Out, 0, 4, 2, ' ', 0) }

func yn(b any) string {
	if v, _ := b.(bool); v {
		return "да"
	}
	return "нет"
}

// ---- команды ----

func (a *app) status() error {
	var st map[string]any
	if err := a.call("GET", "/v1/status", nil, &st); err != nil {
		return err
	}
	if a.show(st) {
		return nil
	}
	fmt.Fprintf(a.env.Out, "brodd %s, запущен %s\n", str(st, "version"), ago(str(st, "started_at")))
	if act, ok := st["active"].(map[string]any); ok {
		fmt.Fprintf(a.env.Out, "активный узел: %s  %s %s:%s (%s), выбрал: %s, %s\n", a.id(str(act, "id")), str(act, "proto"), str(act, "server"), str(act, "port"),
			str(act, "class"), str(st, "active_by"), ago(str(st, "active_since")))
		if act["present"] == false {
			fmt.Fprintln(a.env.Out, "  узел пропал из источников подписки (продолжает работать, пока вы его не смените)")
		}
	} else {
		fmt.Fprintln(a.env.Out, "активный узел: не выбран (трафик через вход приложений не идёт)")
	}
	if st["degraded"] == true {
		fmt.Fprintln(a.env.Out, "!!! активный узел деградирует — см. `brodctl alternatives` и `brodctl events`")
	}
	fmt.Fprintf(a.env.Out, "узлов: %s, доступны в источниках: %s\n", str(st, "nodes"), str(st, "present"))
	if w, ok := st["warp"].(map[string]any); ok {
		switch {
		case w["via_warp"] == true:
			fmt.Fprintln(a.env.Out, "WARP: настроен, проверки и подключения идут цепочкой через него")
		case w["enabled"] == true:
			fmt.Fprintln(a.env.Out, "WARP: настроен (кандидат целей; цепочка выключена: settings set check.via_warp 1)")
		default:
			fmt.Fprintln(a.env.Out, "WARP: не настроен (brodctl warp register или brodctl warp import ФАЙЛ)")
		}
	}
	if p := str(st, "app_proxy"); p != "" {
		fmt.Fprintf(a.env.Out, "вход для приложений: %s (логин и пароль: brodctl app-proxy)\n", p)
	}
	return nil
}

func (a *app) list(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	alive := fs.Bool("alive", false, "только живые в текущей сети")
	fav := fs.Bool("fav", false, "только избранные")
	n := fs.Int("n", 30, "сколько показать")
	all := fs.Bool("all", false, "включая скрытые (security=none)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	q := fmt.Sprintf("/v1/nodes?limit=%d&alive=%v&favorites=%v&include_no_security=%v", *n, *alive, *fav, *all)
	var nodes []map[string]any
	if err := a.call("GET", q, nil, &nodes); err != nil {
		return err
	}
	if a.show(nodes) {
		return nil
	}
	w := a.tw()
	fmt.Fprintln(w, "ID\tПРОТОКОЛ\tАДРЕС\tСТРАНА\tЧИСТОТА\tПИНГ\tСКОРОСТЬ\tL2\tGOOGLE\tYT\tМЕТКИ")
	for _, n := range nodes {
		var marks []string
		if n["favorite"] == true {
			marks = append(marks, "★")
		}
		if n["pinned"] == true {
			marks = append(marks, "закреплён")
		}
		if n["insecure"] == true {
			marks = append(marks, "insecure")
		}
		if n["no_security"] == true {
			marks = append(marks, "без-TLS")
		}
		if n["present"] == false {
			marks = append(marks, "нет-в-источнике")
		}
		l2 := "—"
		if s := str(n, "l2_at"); s != "" {
			l2 = ago(s)
		}
		country, clean := "—", "—"
		if ex, ok := n["exit"].(map[string]any); ok {
			country = str(ex, "country")
			clean = map[string]string{"clean": "чистый", "mixed": "средний", "dirty": "грязный"}[str(ex, "clean")]
			if clean == "" {
				clean = "—"
			}
		}
		ping, speed := "—", "—"
		if v, ok := n["latency_ms"].(float64); ok && v > 0 {
			ping = fmt.Sprintf("%.0f мс", v)
		}
		if v, ok := n["speed_kbps"].(float64); ok && v > 0 {
			speed = fmt.Sprintf("%.1f Мбит/с", v*8/1024)
		}
		fmt.Fprintf(w, "%s\t%s\t%s:%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.id(str(n, "id")), str(n, "class"), str(n, "server"), str(n, "port"), country, clean, ping, speed, l2,
			yn(n["google_api"]), yn(n["youtube"]), strings.Join(marks, " "))
	}
	return w.Flush()
}

func (a *app) nodeAction(args []string, action string, body any) error {
	if len(args) != 1 {
		return errUsage
	}
	var out map[string]any
	if err := a.call("POST", "/v1/nodes/"+url.PathEscape(args[0])+"/"+action, body, &out); err != nil {
		return err
	}
	if !a.asJSON {
		fmt.Fprintln(a.env.Out, "готово")
	}
	return nil
}

func (a *app) flagCmd(args []string, what string) error {
	fs := flag.NewFlagSet(what, flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	off := fs.Bool("off", false, "снять флаг")
	// допускаем флаг после аргумента: «fav ID --off»
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return errUsage
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return a.nodeAction(pos, what, map[string]bool{"on": !*off})
}

func (a *app) test(args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	limit := fs.Int("limit", 0, "сколько узлов проверить")
	force := fs.Bool("force", false, "перепроверять и свежие")
	idsFile := fs.String("ids-file", "", "файл с полными ID узлов, по одному в строке")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	var ids []string
	if *idsFile != "" {
		b, err := os.ReadFile(*idsFile)
		if err != nil {
			return err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				ids = append(ids, l)
			}
		}
	}
	var r map[string]any
	if err := a.call("POST", "/v1/check", map[string]any{"limit": *limit, "force": *force, "ids": ids}, &r); err != nil {
		return err
	}
	if a.show(r) {
		return nil
	}
	fmt.Fprintf(a.env.Out, "отобрано %s, проверено %s, L2 прошли %s, из них с Google API %s, за %s мс\n",
		str(r, "picked"), str(r, "checked"), str(r, "ok"), str(r, "google_ok"), str(r, "took_ms"))
	if f, ok := r["fails"].(map[string]any); ok && len(f) > 0 {
		var parts []string
		for k, v := range f {
			parts = append(parts, fmt.Sprintf("%s %v", k, v))
		}
		sort.Strings(parts)
		fmt.Fprintf(a.env.Out, "отказы L2: %s\n", strings.Join(parts, ", "))
	}
	return nil
}

// nodeCheck: сквозная проверка узла (запрос через сам узел до внешнего сайта). Без аргумента — активный выход. Код возврата 1, если узел не отвечает.
func (a *app) nodeCheck(args []string) error {
	path := "/v1/active/check"
	switch len(args) {
	case 0:
	case 1:
		path = "/v1/nodes/" + url.PathEscape(args[0]) + "/check"
	default:
		return errUsage
	}
	var r map[string]any
	if err := a.call("POST", path, nil, &r); err != nil {
		return err
	}
	if a.show(r) {
		if r["ok"] != true {
			return errors.New("узел не отвечает")
		}
		return nil
	}
	who := str(r, "node")
	if who == "" {
		who = str(r, "proto")
	} else if len(who) > 10 {
		who = who[:10]
	}
	if r["ok"] == true {
		fmt.Fprintf(a.env.Out, "узел %s РАБОТАЕТ: выход %s, %s (%s); Google: %s, сайт: %s; %v мс\n", who, str(r, "ip"), str(r, "country"), str(r, "colo"), str(r, "google"), str(r, "site"), r["ms"])
		return nil
	}
	fmt.Fprintf(a.env.Out, "узел %s НЕ РАБОТАЕТ: %s (%v мс)\n", who, str(r, "detail"), r["ms"])
	return errors.New("узел не отвечает")
}

// udpGradeText — понятная оценка UDP-качества узла (grade из prober).
var udpGradeText = map[string]string{
	"good":     "ХОРОШО (звонки пойдут)",
	"unstable": "НЕСТАБИЛЬНО (звонки будут рваться)",
	"none":     "UDP НЕ ПРОХОДИТ",
}

// nodeUDP: замер UDP через узел (STUN): потери и задержка. Без аргумента — активный выход. Код возврата 1, если UDP не проходит.
func (a *app) nodeUDP(args []string) error {
	path := "/v1/active/udp"
	switch len(args) {
	case 0:
	case 1:
		path = "/v1/nodes/" + url.PathEscape(args[0]) + "/udp"
	default:
		return errUsage
	}
	var r map[string]any
	if err := a.call("POST", path, nil, &r); err != nil {
		return err
	}
	grade := str(r, "grade")
	if a.show(r) {
		if grade == "none" {
			return errors.New("UDP через узел не проходит")
		}
		return nil
	}
	fmt.Fprintln(a.env.Out, udpLine(r))
	if grade == "none" {
		return errors.New("UDP через узел не проходит")
	}
	return nil
}

// udpLine — одна строка итога замера UDP для консоли.
func udpLine(r map[string]any) string {
	grade := str(r, "grade")
	who := str(r, "node")
	if who == "" {
		who = str(r, "proto")
	} else if len(who) > 10 {
		who = who[:10]
	}
	text := udpGradeText[grade]
	if text == "" {
		text = grade
	}
	if grade == "none" {
		return fmt.Sprintf("узел %s: %s (%s; сервер %s)", who, text, str(r, "detail"), str(r, "server"))
	}
	return fmt.Sprintf("узел %s (%s): %s. Ответов %v из %v, потери %.0f%%, задержка %v мс (от %v до %v); сервер %s",
		who, str(r, "proto"), text, r["received"], r["sent"], r["loss_pct"], r["median_ms"], r["min_ms"], r["max_ms"], str(r, "server"))
}

// vpnSelfTest: самотест работающего туннеля; код возврата 1, если хоть один шаг не прошёл.
func (a *app) vpnSelfTest() error {
	var r struct {
		OK        bool   `json:"ok"`
		Mode      string `json:"mode"`
		Interface string `json:"interface"`
		Steps     []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"steps"`
	}
	if err := a.call("POST", "/v1/vpn/selftest", nil, &r); err != nil {
		return err
	}
	if a.asJSON {
		b, _ := json.MarshalIndent(r, "", " ")
		fmt.Fprintln(a.env.Out, string(b))
	} else {
		for _, s := range r.Steps {
			mark := "✔"
			if !s.OK {
				mark = "✖"
			}
			fmt.Fprintf(a.env.Out, "%s %s: %s\n", mark, s.Name, s.Detail)
		}
	}
	if !r.OK {
		return errors.New("самотест не пройден")
	}
	if !a.asJSON {
		fmt.Fprintln(a.env.Out, "самотест пройден")
	}
	return nil
}

// warpRegister: регистрирует аккаунт WARP (сначала напрямую, затем через активный узел) и перезапускает ядро.
func (a *app) warpRegister(args []string) error {
	fs := flag.NewFlagSet("warp register", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	force := fs.Bool("force", false, "заменить уже настроенный аккаунт")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	var r map[string]any
	if err := a.call("POST", "/v1/warp/register", map[string]any{"replace": *force}, &r); err != nil {
		return err
	}
	if !a.show(r) {
		fmt.Fprintln(a.env.Out, "WARP: аккаунт зарегистрирован, ядро перезапущено (проверка: brodctl warp probe)")
	}
	return nil
}

// warpImport: загружает готовый файл аккаунта (warp-account.json) и перезапускает ядро.
func (a *app) warpImport(args []string) error {
	fs := flag.NewFlagSet("warp import", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	force := fs.Bool("force", false, "заменить уже настроенный аккаунт")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errUsage
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	var r map[string]any
	if err := a.call("POST", "/v1/warp/import", map[string]any{"account": json.RawMessage(raw), "replace": *force}, &r); err != nil {
		return err
	}
	if !a.show(r) {
		fmt.Fprintln(a.env.Out, "WARP: аккаунт загружен, ядро перезапущено (проверка: brodctl warp probe)")
	}
	return nil
}

func (a *app) simplePost(path, what string, describe func(map[string]any) string) error {
	var r map[string]any
	if err := a.call("POST", path, nil, &r); err != nil {
		return err
	}
	if a.show(r) {
		return nil
	}
	fmt.Fprintf(a.env.Out, "%s: %s\n", what, describe(r))
	return nil
}

func (a *app) alternatives(args []string) error {
	fs := flag.NewFlagSet("alternatives", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	n := fs.Int("n", 3, "сколько подобрать")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	var alts []map[string]any
	if err := a.call("GET", fmt.Sprintf("/v1/alternatives?n=%d", *n), nil, &alts); err != nil {
		return err
	}
	if a.show(alts) {
		return nil
	}
	if len(alts) == 0 {
		fmt.Fprintln(a.env.Out, "проверенных альтернатив сейчас нет")
		return nil
	}
	w := a.tw()
	fmt.Fprintln(w, "ID\tПРОТОКОЛ\tАДРЕС\tЗАДЕРЖКА\tGOOGLE")
	for _, x := range alts {
		nd, _ := x["node"].(map[string]any)
		fmt.Fprintf(w, "%s\t%s\t%s:%s\t%s мс\t%s\n", a.id(str(nd, "id")), str(nd, "class"), str(nd, "server"), str(nd, "port"), str(x, "latency_ms"), yn(x["google_api"]))
	}
	return w.Flush()
}

func (a *app) refresh() error {
	var r map[string]any
	if err := a.call("POST", "/v1/refresh", nil, &r); err != nil {
		return err
	}
	if a.show(r) {
		return nil
	}
	srcs, _ := r["sources"].([]any)
	bad := 0
	for _, s := range srcs {
		if m, _ := s.(map[string]any); str(m, "error") != "" {
			bad++
			fmt.Fprintf(a.env.Out, "  сбой %s: %s\n", str(m, "name"), str(m, "error"))
		}
	}
	fmt.Fprintf(a.env.Out, "источников %d (сбоев %d), новых узлов %s, всего %s, доступно %s, за %s мс\n", len(srcs), bad, str(r, "added"), str(r, "total"), str(r, "present"), str(r, "took_ms"))
	return nil
}

// candidates — автопоиск источников: список со статистикой карантина, добавление, скрытие, внеочередной проход.
func (a *app) candidates(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		var list []map[string]any
		if err := a.call("GET", "/v1/source-candidates", nil, &list); err != nil {
			return err
		}
		if a.show(list) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "ID\tИМЯ\tССЫЛОК\tСХЕМЫ\tДНЕЙ\tЖИВЫХ")
		for _, c := range list {
			live := "не проверялось"
			if str(c, "sampled") != "0" {
				live = fmt.Sprintf("%s из %s", str(c, "sample_ok"), str(c, "sampled"))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", str(c, "id"), str(c, "name"), str(c, "links"), schemesText(c["schemes"]), str(c, "days_seen"), live)
		}
		return w.Flush()
	}
	switch args[0] {
	case "groups":
		var list []map[string]any
		if err := a.call("GET", "/v1/source-candidate-groups", nil, &list); err != nil {
			return err
		}
		if a.show(list) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "ГРУППА\tПРЕДСТАВИТЕЛЬ\tПРЕДСТ. ID\tРАЗМЕР\tССЫЛОК\tЖИВЫХ")
		for _, g := range list {
			rep, _ := g["representative"].(map[string]any)
			live := "не проверялось"
			if str(rep, "sampled") != "0" {
				live = fmt.Sprintf("%s из %s", str(rep, "sample_ok"), str(rep, "sampled"))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", str(g, "id"), str(rep, "name"), str(rep, "id"), str(g, "size"), str(g, "total_links_max"), live)
		}
		return w.Flush()
	case "dismiss-group":
		if len(args) != 2 {
			return errUsage
		}
		return a.ok("POST", "/v1/source-candidate-groups/"+url.PathEscape(args[1])+"/dismiss", nil)
	case "add":
		if len(args) != 2 {
			return errUsage
		}
		var r map[string]any
		if err := a.call("POST", "/v1/source-candidates/"+url.PathEscape(args[1])+"/add", nil, &r); err != nil {
			return err
		}
		if !a.show(r) {
			fmt.Fprintf(a.env.Out, "добавлен источник %s\n", str(r, "name"))
		}
		return nil
	case "dismiss":
		if len(args) != 2 {
			return errUsage
		}
		return a.ok("POST", "/v1/source-candidates/"+url.PathEscape(args[1])+"/dismiss", nil)
	case "scan":
		fs := flag.NewFlagSet("candidates scan", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		metered := fs.Bool("metered", false, "разрешить проход в платной сети")
		if len(reorder(fs, args[1:])) != 0 {
			return errUsage
		}
		var r map[string]any
		if err := a.call("POST", "/v1/source-candidates/scan", map[string]bool{"allow_metered": *metered}, &r); err != nil {
			return err
		}
		if a.show(r) {
			return nil
		}
		fmt.Fprintf(a.env.Out, "репозиториев %s, файлов %s, кандидатов %s (новых %s), проверено ссылок %s, ответило %s, за %s мс\n",
			str(r, "repos"), str(r, "files"), str(r, "found"), str(r, "new"), str(r, "sampled"), str(r, "alive"), str(r, "took_ms"))
		return nil
	}
	return errUsage
}

// schemesText — «vless 90, trojan 10» по убыванию числа ссылок.
func schemesText(v any) string {
	m, _ := v.(map[string]any)
	type kv struct {
		k string
		n float64
	}
	var l []kv
	for k, n := range m {
		f, _ := n.(float64)
		l = append(l, kv{k, f})
	}
	sort.Slice(l, func(i, j int) bool {
		if l[i].n != l[j].n {
			return l[i].n > l[j].n
		}
		return l[i].k < l[j].k
	})
	var parts []string
	for _, e := range l {
		parts = append(parts, fmt.Sprintf("%s %d", e.k, int(e.n)))
	}
	return strings.Join(parts, ", ")
}

func (a *app) sources(args []string) error {
	if len(args) == 0 {
		var list []map[string]any
		if err := a.call("GET", "/v1/sources", nil, &list); err != nil {
			return err
		}
		if a.show(list) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "ИМЯ\tХОСТ\tВКЛ\tССЫЛОК\tОБНОВЛЁН\tКВОТА\tОШИБКА")
		for _, s := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", str(s, "name"), str(s, "host"), yn(s["enabled"]), str(s, "last_count"), ago(str(s, "last_ok")), quota(s), str(s, "last_error"))
		}
		return w.Flush()
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("sources add", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		prio := fs.Int("prio", 1, "приоритет (меньше = раньше)")
		mirror := fs.String("mirror", "", "запасной адрес")
		pos := reorder(fs, args[1:])
		if len(pos) != 2 {
			return errUsage
		}
		return a.ok("POST", "/v1/sources", map[string]any{"name": pos[0], "url": pos[1], "priority": *prio, "mirror": *mirror})
	case "enable", "disable":
		if len(args) != 2 {
			return errUsage
		}
		return a.ok("POST", "/v1/sources/"+url.PathEscape(args[1])+"/enable", map[string]bool{"on": args[0] == "enable"})
	case "rm":
		if len(args) != 2 {
			return errUsage
		}
		return a.ok("DELETE", "/v1/sources/"+url.PathEscape(args[1]), nil)
	}
	return errUsage
}

// reorder разбирает флаги, допуская их после позиционных аргументов; возвращает позиционные.
func reorder(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos
}

func (a *app) ok(method, path string, body any) error {
	if err := a.call(method, path, body, nil); err != nil {
		return err
	}
	if !a.asJSON {
		fmt.Fprintln(a.env.Out, "готово")
	}
	return nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func (a *app) targets(args []string) error {
	if len(args) == 0 {
		var list []map[string]any
		if err := a.call("GET", "/v1/targets", nil, &list); err != nil {
			return err
		}
		if a.show(list) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "ЦЕЛЬ\tВЛАДЕЛЕЦ\tПОЛИТИКА\tУЗЕЛ\tСОСТОЯНИЕ\tПРОВЕРКА")
		for _, t := range list {
			state := "ок"
			switch {
			case str(t, "node") == "":
				state = "узел не выбран"
			case t["degraded"] == true:
				state = "ДЕГРАДАЦИЯ"
			case t["last"] == nil:
				state = "не проверялась"
			}
			node := str(t, "node")
			if node == "" {
				node = "—"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", str(t, "id"), str(t, "owner"), str(t, "policy"), a.id(node), state, ago(str(t, "last_at")))
		}
		return w.Flush()
	}
	switch args[0] {
	case "show":
		if len(args) != 2 {
			return errUsage
		}
		var t map[string]any
		if err := a.call("GET", "/v1/targets/"+url.PathEscape(args[1]), nil, &t); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Fprintln(a.env.Out, string(b))
		a.printed = true // иначе --json напечатает ответ второй раз
		return nil
	case "add":
		return a.targetAdd(args[1:])
	case "rm":
		if len(args) != 2 {
			return errUsage
		}
		return a.ok("DELETE", "/v1/targets/"+url.PathEscape(args[1]), nil)
	case "switch":
		if len(args) != 3 {
			return errUsage
		}
		var r map[string]any
		if err := a.call("POST", "/v1/targets/"+url.PathEscape(args[1])+"/switch", map[string]string{"to": args[2]}, &r); err != nil {
			return err
		}
		if !a.asJSON {
			fmt.Fprintf(a.env.Out, "цель %s → %s\n", args[1], a.id(str(r, "node")))
		}
		return nil
	case "probe":
		if len(args) != 2 {
			return errUsage
		}
		return a.simplePost("/v1/targets/"+url.PathEscape(args[1])+"/probe", "цель "+args[1], func(m map[string]any) string {
			if m["ok"] == true {
				return fmt.Sprintf("работает, %v мс", m["latency_ms"])
			}
			return fmt.Sprintf("НЕ работает: %v %v", m["class"], m["detail"])
		})
	case "probe-url":
		fs := flag.NewFlagSet("probe-url", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		ttl := fs.Int("ttl", 3600, "срок жизни ссылки, секунд")
		pos := reorder(fs, args[1:])
		if len(pos) != 2 {
			return errUsage
		}
		return a.ok("POST", "/v1/targets/"+url.PathEscape(pos[0])+"/probe-url", map[string]any{"url": pos[1], "ttl_sec": *ttl})
	}
	return errUsage
}

func (a *app) targetAdd(args []string) error {
	fs := flag.NewFlagSet("targets add", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	title := fs.String("title", "", "название")
	var domains, suffixes, keywords multi
	fs.Var(&domains, "domain", "точный домен (можно несколько раз)")
	fs.Var(&suffixes, "suffix", "домен и его поддомены")
	fs.Var(&keywords, "keyword", "подстрока в домене")
	check := fs.String("check", "http", "вид проверки: http|youtube|google-api|telegram")
	u := fs.String("url", "", "URL проверки (для http)")
	expect := fs.String("expect", "", "допустимые коды, через запятую (по умолчанию 200)")
	policy := fs.String("policy", "sticky-manual", "sticky-manual | auto-failover")
	k := fs.Int("k", 3, "провалов подряд до переключения/деградации")
	noDirect := fs.Bool("no-direct", false, "не использовать direct как кандидата")
	pos := reorder(fs, args)
	if len(pos) != 1 {
		return errUsage
	}
	var codes []int
	for _, s := range strings.Split(*expect, ",") {
		if s = strings.TrimSpace(s); s != "" {
			var n int
			if _, err := fmt.Sscan(s, &n); err != nil {
				return fmt.Errorf("неверный код %q", s)
			}
			codes = append(codes, n)
		}
	}
	body := map[string]any{
		"title": *title, "policy": *policy, "failover_k": *k, "allow_direct": !*noDirect, "allow_warp": true,
		"rules": map[string]any{"domain": []string(domains), "domain_suffix": []string(suffixes), "domain_keyword": []string(keywords)},
		"check": map[string]any{"kind": *check, "url": *u, "expect_codes": codes},
	}
	var out map[string]any
	if err := a.call("PUT", "/v1/targets/"+url.PathEscape(pos[0]), body, &out); err != nil {
		return err
	}
	if !a.asJSON {
		fmt.Fprintf(a.env.Out, "цель %s зарегистрирована\n", pos[0])
	}
	return nil
}

func (a *app) events(args []string) error {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	follow := fs.Bool("f", false, "следить за новыми")
	after := fs.Int64("after", 0, "начиная после ID")
	last := fs.Int("n", 30, "сколько последних показать (без -f и --after)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	line := func(e map[string]any) {
		at, _ := time.Parse(time.RFC3339Nano, str(e, "at"))
		d := ""
		if e["detail"] != nil {
			if s, ok := e["detail"].(string); ok {
				d = s
			} else {
				b, _ := json.Marshal(e["detail"])
				d = string(b)
			}
		}
		fmt.Fprintf(a.env.Out, "%s  %-16s %-10s %s %s %s\n", at.Local().Format("15:04:05"), str(e, "kind"), str(e, "initiator"), a.id(str(e, "node")), str(e, "target"), d)
	}
	if !*follow {
		var evs []map[string]any
		// без --after — последние n (tail): запрос «после 0» отдаёт самые старые события, и при тысяче с лишним
		// в базе показывались устаревшие
		path := fmt.Sprintf("/v1/events?tail=%d", max(*last, 1))
		if *after != 0 {
			path = fmt.Sprintf("/v1/events?after=%d&limit=1000", *after)
		}
		if err := a.call("GET", path, nil, &evs); err != nil {
			return err
		}
		if a.show(evs) {
			return nil
		}
		for _, e := range evs {
			line(e)
		}
		return nil
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/v1/events?after=%d", a.base, *after), nil)
	req.Header.Set("Accept", "text/event-stream")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	hc := *a.hc
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("демон недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &apiError{resp.StatusCode, "поток событий отклонён"}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var e map[string]any
			if json.Unmarshal([]byte(data), &e) == nil {
				if a.asJSON {
					fmt.Fprintln(a.env.Out, data)
				} else {
					line(e)
				}
			}
		}
	}
	return sc.Err()
}

func (a *app) logs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(a.env.Err)
	n := fs.Int("n", 100, "строк")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	var r struct {
		Lines []string `json:"lines"`
	}
	if err := a.call("GET", fmt.Sprintf("/v1/logs?n=%d", *n), nil, &r); err != nil {
		return err
	}
	if a.show(r) {
		return nil
	}
	for _, l := range r.Lines {
		fmt.Fprintln(a.env.Out, l)
	}
	return nil
}

func (a *app) settings(args []string) error {
	if len(args) == 0 {
		var m map[string]string
		if err := a.call("GET", "/v1/settings", nil, &m); err != nil {
			return err
		}
		if a.show(m) {
			return nil
		}
		for k, v := range m {
			if v == "" {
				v = "(по умолчанию)"
			}
			fmt.Fprintf(a.env.Out, "%s = %s\n", k, v)
		}
		return nil
	}
	if args[0] == "set" && len(args) == 3 {
		return a.ok("PUT", "/v1/settings/"+url.PathEscape(args[1]), map[string]string{"value": args[2]})
	}
	return errUsage
}

func (a *app) appProxy() error {
	var m map[string]string
	if err := a.call("GET", "/v1/app-proxy", nil, &m); err != nil {
		return err
	}
	if a.show(m) {
		return nil
	}
	fmt.Fprintf(a.env.Out, "прокси (HTTP и SOCKS5): %s\nлогин:  %s\nпароль: %s\nURL: http://%s:%s@%s\n", m["addr"], m["username"], m["password"], m["username"], m["password"], m["addr"])
	return nil
}

func (a *app) tokenCmd(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "create":
		if len(args) != 2 {
			return errUsage
		}
		var m map[string]string
		if err := a.call("POST", "/v1/tokens", map[string]string{"name": args[1]}, &m); err != nil {
			return err
		}
		if !a.asJSON {
			fmt.Fprintf(a.env.Out, "токен приложения %s (показывается один раз):\n%s\n", m["name"], m["token"])
		}
		return nil
	case "list":
		var names []string
		if err := a.call("GET", "/v1/tokens", nil, &names); err != nil {
			return err
		}
		if a.show(names) {
			return nil
		}
		for _, n := range names {
			fmt.Fprintln(a.env.Out, n)
		}
		return nil
	case "revoke":
		if len(args) != 2 {
			return errUsage
		}
		return a.ok("DELETE", "/v1/tokens/"+url.PathEscape(args[1]), nil)
	}
	return errUsage
}

// quota: «осталось/квота до даты» из сведений источника (subscription-userinfo).
func quota(s map[string]any) string {
	total, _ := s["total_bytes"].(float64)
	if total <= 0 {
		return "—"
	}
	used, _ := s["used_bytes"].(float64)
	out := fmt.Sprintf("%.1f из %.1f ГБ", used/(1<<30), total/(1<<30))
	if e := str(s, "expire"); e != "" {
		if t, err := time.Parse(time.RFC3339Nano, e); err == nil {
			out += " до " + t.Local().Format("02.01.2006")
		}
	}
	return out
}

var vpnModeNames = map[string]string{"blocked": "только заблокированное", "all": "всё через выход", "all-but-ru": "всё, кроме РФ"}

// vpn: без аргументов — состояние; on/off — включить/выключить; --mode M (или `vpn mode M`) — выбрать режим (запоминается; при включённом VPN применяется сразу).
var autoconnectNames = map[string]string{
	"last":   "как было (включённый VPN включается снова)",
	"always": "всегда включать",
	"off":    "не включать",
}

func (a *app) vpn(args []string) error {
	body := map[string]any{}
	if len(args) >= 1 && args[0] == "autoconnect" { // vpn autoconnect last|always|off
		if len(args) != 2 {
			return errUsage
		}
		body["autoconnect"] = args[1]
		args = nil
	}
	for i := 0; i < len(args); i++ {
		switch w := args[i]; {
		case w == "on":
			body["on"] = true
		case w == "off":
			body["on"] = false
		case (w == "mode" || w == "--mode" || w == "-mode") && i+1 < len(args):
			i++
			body["mode"] = args[i]
		case strings.HasPrefix(w, "--mode="):
			body["mode"] = strings.TrimPrefix(w, "--mode=")
		default:
			return errUsage
		}
	}
	var st map[string]any
	var err error
	if len(body) == 0 {
		err = a.call("GET", "/v1/vpn", nil, &st)
	} else {
		err = a.call("POST", "/v1/vpn", body, &st)
	}
	if err != nil {
		return err
	}
	if a.show(st) {
		return nil
	}
	state := "выключен"
	if st["on"] == true {
		state = "ВКЛЮЧЁН (" + str(st, "interface") + ")"
	}
	mn := str(st, "mode")
	if n, ok := vpnModeNames[mn]; ok {
		mn += " — " + n
	}
	fmt.Fprintf(a.env.Out, "VPN %s; режим: %s\n", state, mn)
	if ac := str(st, "autoconnect"); ac != "" {
		fmt.Fprintf(a.env.Out, "автоподключение при запуске: %s\n", autoconnectNames[ac])
	}
	if e := str(st, "error"); e != "" {
		fmt.Fprintf(a.env.Out, "последняя ошибка: %s\n", e)
	}
	return nil
}
