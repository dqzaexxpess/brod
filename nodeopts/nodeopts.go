// Package nodeopts — белый список «тип узла → разрешённые поля» для всего, что из хранилища или из чужих данных
// (резервная копия, подписка, «свой сервер») уходит в движок.
//
// Разрешено только перечисленное; всё остальное отклоняется. Движок получает не исходные байты, а заново собранный JSON
// из проверенного дерева, поэтому расхождение разборщиков (повторяющиеся ключи, регистр имён полей) ничего не даёт.
// Список полей выведен из того, что выдают translator и warp; любое новое поле узла сначала добавляется сюда.
package nodeopts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

const (
	maxRaw    = 256 << 10 // предел размера параметров одного узла
	maxDepth  = 8
	maxString = 8 << 10
	maxItems  = 64
)

// Flags — что разрешено сверх параметров узла.
type Flags struct {
	Tag    bool // поле tag (имя в движке; ставит ядро)
	Detour bool // поле detour (цепочка через WARP; ставит ядро)
}

type kind int

const (
	kStr       kind = iota // строка
	kInt                   // целое число
	kBool                  // логическое
	kScalar                // строка, число или логическое (но не структура)
	kStrList               // список строк
	kIntList               // список целых
	kStrOrList             // строка или список строк
	kObj                   // вложенный объект
	kObjList               // список вложенных объектов
	kStrMap                // объект «строка → строка» с произвольными ключами (заголовки)
)

type field struct {
	k   kind
	sub schema
}

type schema map[string]field

var (
	fStr      = field{k: kStr}
	fInt      = field{k: kInt}
	fBool     = field{k: kBool}
	fScalar   = field{k: kScalar}
	fStrList  = field{k: kStrList}
	fStrOrLst = field{k: kStrOrList}
)

func obj(s schema) field { return field{k: kObj, sub: s} }

var tlsSchema = schema{
	"enabled": fBool, "server_name": fStr, "insecure": fBool, "alpn": fStrList,
	"utls":    obj(schema{"enabled": fBool, "fingerprint": fStr}),
	"reality": obj(schema{"enabled": fBool, "public_key": fStr, "short_id": fStr}),
}

var xmuxSchema = schema{
	"max_concurrency": fScalar, "max_connections": fScalar, "c_max_reuse_times": fScalar,
	"h_max_request_times": fScalar, "h_max_reusable_secs": fScalar, "h_keep_alive_period": fScalar,
}

var transportSchema = schema{
	"type": fStr, "path": fStrOrLst, "host": fStrOrLst, "headers": {k: kStrMap}, "max_early_data": fInt,
	"early_data_header_name": fStr, "service_name": fStr, "header_type": fStr, "mode": fStr, "xmux": obj(xmuxSchema),
	// XHTTP (translator/xhttp.go, extraKeys)
	"x_padding_bytes": fScalar, "sc_max_each_post_bytes": fScalar, "sc_min_posts_interval_ms": fScalar,
	"sc_max_concurrent_posts": fScalar, "sc_max_buffered_posts": fScalar, "sc_stream_up_server_secs": fScalar,
	"server_max_header_bytes": fScalar, "no_grpc_header": fScalar, "no_sse_header": fScalar, "x_padding_obfs_mode": fScalar,
	"x_padding_key": fScalar, "x_padding_header": fScalar, "x_padding_placement": fScalar, "x_padding_method": fScalar,
	"session_placement": fScalar, "session_key": fScalar, "seq_placement": fScalar, "seq_key": fScalar,
	"session_table": fScalar, "session_length": fScalar, "uplink_data_placement": fScalar, "uplink_data_key": fScalar,
	"uplink_chunk_size": fScalar, "uplink_http_method": fScalar,
}

var transportTypes = map[string]bool{"ws": true, "grpc": true, "httpupgrade": true, "http": true, "raw": true, "xhttp": true}

var peerSchema = schema{
	"address": fStr, "port": fInt, "public_key": fStr, "allowed_ips": fStrList, "pre_shared_key": fStr,
	"reserved": {k: kIntList} /* список чисел либо base64: []uint8 из translator маршалится строкой */, "persistent_keepalive_interval": fInt,
}

func with(base schema, more schema) schema {
	out := schema{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

var serverBase = schema{"type": fStr, "server": fStr, "server_port": fInt}

// types — разрешённые типы узла и их поля верхнего уровня. Выдаёт их только translator (и warp для wireguard).
var types = map[string]schema{
	"vless":  with(serverBase, schema{"uuid": fStr, "flow": fStr, "tls": obj(tlsSchema), "transport": obj(transportSchema)}),
	"vmess":  with(serverBase, schema{"uuid": fStr, "security": fStr, "alter_id": fInt, "tls": obj(tlsSchema), "transport": obj(transportSchema)}),
	"trojan": with(serverBase, schema{"password": fStr, "tls": obj(tlsSchema), "transport": obj(transportSchema)}),
	"hysteria2": with(serverBase, schema{"password": fStr, "tls": obj(tlsSchema),
		"obfs": obj(schema{"type": fStr, "password": fStr})}),
	"hysteria": with(serverBase, schema{"auth_str": fStr, "up_mbps": fInt, "down_mbps": fInt, "obfs": fStr, "tls": obj(tlsSchema)}),
	"tuic": with(serverBase, schema{"uuid": fStr, "password": fStr, "congestion_control": fStr, "udp_relay_mode": fStr,
		"tls": obj(tlsSchema)}),
	"anytls":      with(serverBase, schema{"password": fStr, "tls": obj(tlsSchema)}),
	"socks":       with(serverBase, schema{"version": fStr, "username": fStr, "password": fStr}),
	"shadowsocks": with(serverBase, schema{"method": fStr, "password": fStr, "plugin": fStr, "plugin_opts": fStr}),
	"shadowsocksr": with(serverBase, schema{"method": fStr, "password": fStr, "protocol": fStr, "obfs": fStr,
		"protocol_param": fStr, "obfs_param": fStr}),
	"wireguard": {
		"type": fStr, "address": fStrList, "private_key": fStr, "mtu": fInt, "peers": {k: kObjList, sub: peerSchema},
		// AmneziaWG (translator awgKeys, warp.Endpoint)
		"jc": fScalar, "jmin": fScalar, "jmax": fScalar, "s1": fScalar, "s2": fScalar, "s3": fScalar, "s4": fScalar,
		"h1": fScalar, "h2": fScalar, "h3": fScalar, "h4": fScalar,
		"i1": fScalar, "i2": fScalar, "i3": fScalar, "i4": fScalar, "i5": fScalar,
		"ip": fScalar, "id": fScalar, "ib": fScalar,
	},
}

var testTypes sync.RWMutex

// AllowTypeForTests разрешает на время теста тип узла-заглушки (direct): тесты ядра и API кладут такие узлы в базу, чтобы не
// поднимать настоящие серверы. Принимает только значение с Cleanup (testing.TB), в продуктовом коде вызвать её нечем.
func AllowTypeForTests(tb interface{ Cleanup(func()) }, typ string) {
	testTypes.Lock()
	types[typ] = serverBase
	testTypes.Unlock()
	tb.Cleanup(func() {
		testTypes.Lock()
		delete(types, typ)
		testTypes.Unlock()
	})
}

// ErrDenied — параметры узла не прошли белый список.
var ErrDenied = errors.New("параметры узла не разрешены")

func deny(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrDenied, fmt.Sprintf(format, a...))
}

// Sanitize проверяет параметры узла по белому списку и возвращает заново собранный JSON. Значения в сообщениях об ошибках
// не приводятся (там могут быть пароли), только имена полей и типов.
func Sanitize(raw []byte, fl Flags) ([]byte, error) {
	if len(raw) > maxRaw {
		return nil, deny("слишком большой размер")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tree, err := parseStrict(dec, 0)
	if err != nil {
		return nil, deny("%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, deny("лишние данные после объекта")
	}
	m, ok := tree.(map[string]any)
	if !ok {
		return nil, deny("ожидался объект")
	}
	typ, _ := m["type"].(string)
	testTypes.RLock()
	sch, ok := types[typ]
	testTypes.RUnlock()
	if !ok {
		return nil, deny("тип %q не разрешён", clip(typ))
	}
	if fl.Tag {
		sch = with(sch, schema{"tag": fStr})
	}
	if fl.Detour {
		sch = with(sch, schema{"detour": fStr})
	}
	clean, err := check(m, sch, typ)
	if err != nil {
		return nil, err
	}
	if t, _ := clean["transport"].(map[string]any); t != nil {
		if tt, _ := t["type"].(string); !transportTypes[tt] {
			return nil, deny("транспорт %q не разрешён", clip(tt))
		}
	}
	if typ == "shadowsocks" {
		if err := cleanPlugin(clean); err != nil {
			return nil, err
		}
	}
	return json.Marshal(clean)
}

// Check — то же без результата: для проверки при приёме данных.
func Check(raw []byte, fl Flags) error {
	_, err := Sanitize(raw, fl)
	return err
}

func clip(s string) string {
	if len(s) > 32 {
		return s[:32] + "…"
	}
	return s
}

// parseStrict разбирает JSON в дерево, отклоняя повторяющиеся ключи на любом уровне.
func parseStrict(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("слишком глубокая вложенность")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				if _, dup := m[k]; dup {
					return nil, fmt.Errorf("повторяющееся поле %q", clip(k))
				}
				if len(m) >= 4*maxItems {
					return nil, errors.New("слишком много полей")
				}
				v, err := parseStrict(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			var l []any
			for dec.More() {
				if len(l) >= maxItems {
					return nil, errors.New("слишком длинный список")
				}
				v, err := parseStrict(dec, depth+1)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			if l == nil {
				l = []any{}
			}
			return l, nil
		}
		return nil, errors.New("неожиданный разделитель")
	default:
		return tok, nil
	}
}

func isInt(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	_, err := n.Int64()
	return err == nil
}

func isStr(v any) bool {
	s, ok := v.(string)
	return ok && len(s) <= maxString
}

func check(m map[string]any, sch schema, path string) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		f, ok := sch[k]
		if !ok {
			return nil, deny("поле %s.%s не разрешено", path, clip(k))
		}
		cv, err := checkValue(v, f, path+"."+k)
		if err != nil {
			return nil, err
		}
		out[k] = cv
	}
	return out, nil
}

func checkValue(v any, f field, path string) (any, error) {
	bad := func() (any, error) { return nil, deny("поле %s: неподходящее значение", path) }
	switch f.k {
	case kStr:
		if !isStr(v) {
			return bad()
		}
	case kInt:
		if !isInt(v) {
			return bad()
		}
	case kBool:
		if _, ok := v.(bool); !ok {
			return bad()
		}
	case kScalar:
		switch v.(type) {
		case bool, json.Number:
		case string:
			if !isStr(v) {
				return bad()
			}
		default:
			return bad()
		}
	case kStrList, kStrOrList:
		if f.k == kStrOrList && isStr(v) {
			return v, nil
		}
		l, ok := v.([]any)
		if !ok {
			return bad()
		}
		for _, x := range l {
			if !isStr(x) {
				return bad()
			}
		}
	case kIntList:
		if s, ok := v.(string); ok && len(s) <= 16 {
			return v, nil
		}
		l, ok := v.([]any)
		if !ok {
			return bad()
		}
		for _, x := range l {
			if !isInt(x) {
				return bad()
			}
		}
	case kObj:
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		return check(m, f.sub, path)
	case kObjList:
		l, ok := v.([]any)
		if !ok {
			return bad()
		}
		out := make([]any, 0, len(l))
		for i, x := range l {
			m, ok := x.(map[string]any)
			if !ok {
				return bad()
			}
			c, err := check(m, f.sub, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	case kStrMap:
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		for _, x := range m {
			if !isStr(x) {
				return bad()
			}
		}
	}
	return v, nil
}

// pluginKeys — опции плагинов Shadowsocks, которые пропускаются. Остальное (в первую очередь cert, key, certRaw,
// пути к файлам у v2ray-plugin) отбрасывается: оно читает файлы на диске демона.
var pluginKeys = map[string]bool{"tls": true, "host": true, "path": true, "mode": true, "mux": true, "obfs": true, "obfs-host": true}

var plugins = map[string]bool{"obfs-local": true, "v2ray-plugin": true}

// PluginOpts оставляет в строке опций плагина Shadowsocks (`k=v;k;k=v`, `\;` и `\=` экранируют разделители)
// только разрешённые ключи; dropped — имена отброшенных.
func PluginOpts(s string) (clean string, dropped []string) {
	var keep []string
	for _, part := range splitEscaped(s, ';') {
		if part == "" {
			continue
		}
		name := part
		if i := indexUnescaped(part, '='); i >= 0 {
			name = part[:i]
		}
		if pluginKeys[name] {
			keep = append(keep, part)
		} else {
			dropped = append(dropped, name)
		}
	}
	sort.Strings(dropped)
	return strings.Join(keep, ";"), dropped
}

func splitEscaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func indexUnescaped(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case c:
			return i
		}
	}
	return -1
}

func cleanPlugin(m map[string]any) error {
	p, _ := m["plugin"].(string)
	if p != "" && !plugins[p] {
		return deny("плагин %q не разрешён", clip(p))
	}
	if o, ok := m["plugin_opts"].(string); ok {
		if p == "" {
			return deny("plugin_opts без плагина")
		}
		c, _ := PluginOpts(o)
		m["plugin_opts"] = c
	}
	return nil
}
