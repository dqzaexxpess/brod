package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"unicode/utf16"
)

// Правила маршрутизации пользователя и профили (API /v1/route-rules, /v1/route-profiles).

// matchSummary — условия правила одной строкой («domain_suffix: a.com, b.com · port: 443»).
func matchSummary(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		switch v := m[k].(type) {
		case []any:
			var vs []string
			for i, e := range v {
				if i == 3 {
					vs = append(vs, "…")
					break
				}
				vs = append(vs, fmt.Sprint(e))
			}
			parts = append(parts, k+": "+strings.Join(vs, ", "))
		case bool:
			if v {
				parts = append(parts, k)
			}
		default:
			parts = append(parts, k+": "+fmt.Sprint(v))
		}
	}
	return strings.Join(parts, " · ")
}

// viaText — выход правила («страна NL», «узел 1a2b3c4d», «WARP»); пусто, если у правила общий выход.
func viaText(r map[string]any) string {
	v, _ := r["via"].(map[string]any)
	if v == nil {
		return ""
	}
	s := ""
	switch str(v, "kind") {
	case "country":
		s = "страна " + str(v, "country")
	case "node":
		n := str(v, "node")
		if len(n) > 8 {
			n = n[:8]
		}
		s = "узел " + n
	case "warp":
		s = "WARP"
	}
	if f := str(v, "fallback"); f != "" && s != "" {
		s += " (иначе " + f + ")"
	}
	return s
}

func (a *app) rules(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		var d map[string]any
		if err := a.call("GET", "/v1/route-rules", nil, &d); err != nil {
			return err
		}
		if a.show(d["rules"]) {
			return nil
		}
		rs, _ := d["rules"].([]any)
		if len(rs) == 0 {
			fmt.Fprintln(a.env.Out, "правил нет")
			return nil
		}
		states := map[string]map[string]any{}
		for _, e := range asList(d["via"]) {
			states[str(e, "rule")] = e
		}
		w := a.tw()
		fmt.Fprintln(w, "№\tВКЛ\tДЕЙСТВИЕ\tВЫХОД\tСЕЙЧАС\tПРАВИЛО")
		for i, e := range rs {
			r, _ := e.(map[string]any)
			act := str(r, "action")
			if bm := str(r, "block_method"); bm != "" {
				act += "/" + bm
			}
			now := ""
			if st := states[str(r, "id")]; st != nil {
				now = str(st, "target")
				if n := str(st, "node"); n != "" {
					now += " " + a.id(n)
				}
				if st["fallback"] == true {
					now += " (запасной)"
				}
			}
			name := str(r, "title")
			m, _ := r["match"].(map[string]any)
			if name != "" {
				name += " — "
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s%s\n", i+1, yn(r["enabled"]), act, viaText(r), now, name, matchSummary(m))
		}
		return w.Flush()
	case "export":
		var d map[string]any
		if err := a.call("GET", "/v1/route-rules", nil, &d); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(d["rules"], "", "  ")
		fmt.Fprintln(a.env.Out, string(b))
		a.printed = true
		return nil
	case "import":
		if len(args) != 1 {
			return errUsage
		}
		raw, err := readArg(args[0])
		if err != nil {
			return err
		}
		var body struct {
			Rules []any `json:"rules"`
		}
		if err := json.Unmarshal(raw, &body); err != nil { // допускаем и голый список
			var bare []any
			if err2 := json.Unmarshal(raw, &bare); err2 != nil {
				return fmt.Errorf("не список правил: %v", err)
			}
			body.Rules = bare
		}
		if body.Rules == nil {
			body.Rules = []any{}
		}
		var out map[string]any
		if err := a.call("PUT", "/v1/route-rules", body, &out); err != nil {
			return err
		}
		return a.applied(out, len(body.Rules))
	case "via":
		var d map[string]any
		if err := a.call("GET", "/v1/route-rules", nil, &d); err != nil {
			return err
		}
		if a.show(d["via"]) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "ПРАВИЛО\tВИД\tТЕПЕРЬ\tУЗЕЛ\tЗАПАСНОЙ\tКАНДИДАТОВ")
		for _, st := range asList(d["via"]) {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", str(st, "rule"), str(st, "kind"), str(st, "target"), a.id(str(st, "node")), yn(st["fallback"]), str(st, "candidates"))
		}
		return w.Flush()
	case "countries":
		var d map[string]any
		if err := a.call("GET", "/v1/route-rules", nil, &d); err != nil {
			return err
		}
		if a.show(d["countries"]) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "СТРАНА\tЖИВЫХ\tВСЕГО")
		for _, c := range asList(d["countries"]) {
			fmt.Fprintf(w, "%s\t%s\t%s\n", str(c, "code"), str(c, "alive"), str(c, "total"))
		}
		return w.Flush()
	}
	return errUsage
}

func asList(v any) []map[string]any {
	var out []map[string]any
	l, _ := v.([]any)
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// readArg читает файл или стандартный ввод («-»).
func readArg(p string) ([]byte, error) {
	var raw []byte
	var err error
	if p == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(p)
	}
	if err != nil {
		return nil, err
	}
	return decodeText(raw), nil
}

// decodeText приводит содержимое файла к UTF-8 без метки порядка байт: Windows PowerShell 5.1 (`Out-File`, `-Encoding utf8`) и Блокнот ставят
// метку UTF-8 (EF BB BF) или пишут UTF-16 (FF FE / FE FF), а разбор JSON такого начала не принимает.
func decodeText(raw []byte) []byte {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return raw[3:]
	case (bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) || bytes.HasPrefix(raw, []byte{0xFE, 0xFF})) && len(raw)%2 == 0:
		le := raw[0] == 0xFF
		u := make([]uint16, 0, len(raw)/2-1)
		for i := 2; i+1 < len(raw); i += 2 {
			if le {
				u = append(u, uint16(raw[i])|uint16(raw[i+1])<<8)
			} else {
				u = append(u, uint16(raw[i+1])|uint16(raw[i])<<8)
			}
		}
		return []byte(string(utf16.Decode(u)))
	}
	return raw
}

// applied сообщает итог применения правил или профиля: перезапуск VPN мог не удаться.
func (a *app) applied(out map[string]any, n int) error {
	if a.show(out) {
		return nil
	}
	if e := str(out, "reload_error"); e != "" {
		fmt.Fprintf(a.env.Out, "сохранено (правил: %d), но VPN не перезапустился: %s\n", n, e)
		return nil
	}
	if vpn, _ := out["vpn"].(map[string]any); vpn != nil && vpn["on"] == true {
		fmt.Fprintf(a.env.Out, "применено (правил: %d), VPN перезапущен\n", n)
	} else {
		fmt.Fprintf(a.env.Out, "сохранено (правил: %d); применится при включении VPN\n", n)
	}
	return nil
}

// profileID находит профиль по точному id, однозначному префиксу id или названию (без учёта регистра).
func (a *app) profileID(ref string) (string, error) {
	var d map[string]any
	if err := a.call("GET", "/v1/route-profiles", nil, &d); err != nil {
		return "", err
	}
	var byName, byPrefix []string
	for _, p := range asList(d["profiles"]) {
		id := str(p, "id")
		if id == ref {
			return id, nil
		}
		if strings.EqualFold(str(p, "name"), ref) {
			byName = append(byName, id)
		}
		if len(ref) >= 2 && strings.HasPrefix(id, ref) {
			byPrefix = append(byPrefix, id)
		}
	}
	switch {
	case len(byName) == 1:
		return byName[0], nil
	case len(byName) == 0 && len(byPrefix) == 1:
		return byPrefix[0], nil
	case len(byName)+len(byPrefix) == 0:
		return "", fmt.Errorf("профиль %q не найден", ref)
	}
	return "", fmt.Errorf("профиль %q неоднозначен", ref)
}

func (a *app) profiles(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		var d map[string]any
		if err := a.call("GET", "/v1/route-profiles", nil, &d); err != nil {
			return err
		}
		if a.show(d["profiles"]) {
			return nil
		}
		w := a.tw()
		fmt.Fprintln(w, "ID\tНАЗВАНИЕ\tПРАВИЛ\tАКТИВНЫЙ")
		for _, p := range asList(d["profiles"]) {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", str(p, "id"), str(p, "name"), str(p, "rules"), yn(p["active"]))
		}
		return w.Flush()
	case "new":
		fs := flag.NewFlagSet("profiles new", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		from := fs.String("from", "", "скопировать правила профиля (id, префикс или название)")
		pos := reorder(fs, args)
		if len(pos) != 1 {
			return errUsage
		}
		body := map[string]any{"name": pos[0]}
		if *from != "" {
			id, err := a.profileID(*from)
			if err != nil {
				return err
			}
			body["from"] = id
		}
		return a.ok("POST", "/v1/route-profiles", body)
	case "use":
		if len(args) != 1 {
			return errUsage
		}
		id, err := a.profileID(args[0])
		if err != nil {
			return err
		}
		var out map[string]any
		if err := a.call("POST", "/v1/route-profiles/"+url.PathEscape(id)+"/activate", nil, &out); err != nil {
			return err
		}
		n := 0
		if rs, ok := out["rules"].([]any); ok {
			n = len(rs)
		}
		return a.applied(out, n)
	case "rename":
		if len(args) != 2 {
			return errUsage
		}
		id, err := a.profileID(args[0])
		if err != nil {
			return err
		}
		return a.ok("PUT", "/v1/route-profiles/"+url.PathEscape(id), map[string]string{"name": args[1]})
	case "rm":
		if len(args) != 1 {
			return errUsage
		}
		id, err := a.profileID(args[0])
		if err != nil {
			return err
		}
		return a.ok("DELETE", "/v1/route-profiles/"+url.PathEscape(id), nil)
	case "share":
		if len(args) != 1 {
			return errUsage
		}
		id, err := a.profileID(args[0])
		if err != nil {
			return err
		}
		var out map[string]any
		if err := a.call("GET", "/v1/route-profiles/"+url.PathEscape(id)+"/share", nil, &out); err != nil {
			return err
		}
		if a.show(out) {
			return nil
		}
		fmt.Fprintln(a.env.Out, str(out, "link"))
		return nil
	case "import":
		fs := flag.NewFlagSet("profiles import", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		name := fs.String("name", "", "название нового профиля (по умолчанию из ссылки)")
		pos := reorder(fs, args)
		if len(pos) != 1 {
			return errUsage
		}
		text := pos[0]
		if !strings.HasPrefix(text, "brod://") { // не ссылка: файл или «-» (ссылка или JSON)
			raw, err := readArg(text)
			if err != nil {
				return err
			}
			text = strings.TrimSpace(string(raw))
		}
		var out map[string]any
		if err := a.call("POST", "/v1/route-profiles/import", map[string]string{"text": text, "name": *name}, &out); err != nil {
			return err
		}
		if a.show(out) {
			return nil
		}
		fmt.Fprintf(a.env.Out, "профиль «%s» добавлен (правил: %s), не активирован: `brodctl profiles use` включит его\n", str(out, "name"), str(out, "rules"))
		return nil
	}
	return errUsage
}
