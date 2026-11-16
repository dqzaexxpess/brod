package translator

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Соответствие camelCase-ключей extra (Xray) полям transport sing-box-lx (snake_case); см. URL_PARSING.md lx.
var extraKeys = map[string]string{
	"xPaddingBytes": "x_padding_bytes", "scMaxEachPostBytes": "sc_max_each_post_bytes",
	"scMinPostsIntervalMs": "sc_min_posts_interval_ms", "scMaxConcurrentPosts": "sc_max_concurrent_posts",
	"scMaxBufferedPosts": "sc_max_buffered_posts", "scStreamUpServerSecs": "sc_stream_up_server_secs",
	"serverMaxHeaderBytes": "server_max_header_bytes", "noGRPCHeader": "no_grpc_header", "noSSEHeader": "no_sse_header",
	"xPaddingObfsMode": "x_padding_obfs_mode", "xPaddingKey": "x_padding_key", "xPaddingHeader": "x_padding_header",
	"xPaddingPlacement": "x_padding_placement", "xPaddingMethod": "x_padding_method",
	"sessionIDPlacement": "session_placement", "sessionPlacement": "session_placement",
	"sessionIDKey": "session_key", "sessionKey": "session_key", "seqPlacement": "seq_placement", "seqKey": "seq_key",
	"sessionIDTable": "session_table", "sessionIDLength": "session_length",
	"uplinkDataPlacement": "uplink_data_placement", "uplinkDataKey": "uplink_data_key",
	"uplinkChunkSize": "uplink_chunk_size", "uplinkHTTPMethod": "uplink_http_method",
}

// ключи-диапазоны: принимают число, "min-max", {from,to}, [min,max]
var rangeKeys = map[string]bool{
	"xPaddingBytes": true, "scMaxEachPostBytes": true, "scMinPostsIntervalMs": true, "scStreamUpServerSecs": true,
	"uplinkChunkSize": true, "sessionIDLength": true,
}

var boolKeys = map[string]bool{"noGRPCHeader": true, "noSSEHeader": true, "xPaddingObfsMode": true}
var intKeys = map[string]bool{"scMaxConcurrentPosts": true, "scMaxBufferedPosts": true, "serverMaxHeaderBytes": true}

var xmuxKeys = map[string]string{
	"maxConcurrency": "max_concurrency", "maxConnections": "max_connections", "cMaxReuseTimes": "c_max_reuse_times",
	"hMaxRequestTimes": "h_max_request_times", "hMaxReusableSecs": "h_max_reusable_secs", "hKeepAlivePeriod": "h_keep_alive_period",
}

// Верхние границы чисел. Движок не ограничивает их сам: x_padding_bytes даёт strings.Repeat("X", n) на каждый запрос,
// session_length — make([]byte, n) на каждый сеанс, sc_max_each_post_bytes — предел буфера выгрузки, xmux — число соединений.
// Границы взяты с запасом над реальными максимумами из публичных агрегатов: x_padding_bytes 4000, sc_max_each_post_bytes 20 000 000,
// uplink_chunk_size 614 400, session_length 32, max_concurrency 100, max_connections 16, c_max_reuse_times 2000, h_max_request_times 10 000,
// h_max_reusable_secs 86 400, h_keep_alive_period 600, sc_min_posts_interval_ms 200. Выше предела ссылка отклоняется целиком.
const (
	limitBytes = 64 << 20 // размеры буферов и порций: ~3,3x над реальным максимумом 20 000 000
)

var xhttpLimits = map[string]float64{
	"xPaddingBytes": 65536, "scMaxEachPostBytes": limitBytes, "uplinkChunkSize": limitBytes, "sessionIDLength": 256,
	"scMinPostsIntervalMs": 60000, "scStreamUpServerSecs": 86400,
	"scMaxConcurrentPosts": 1 << 20, "scMaxBufferedPosts": 1 << 20, "serverMaxHeaderBytes": limitBytes,
}

var xmuxLimits = map[string]float64{
	"maxConcurrency": 1024, "maxConnections": 256, "cMaxReuseTimes": 1e6, "hMaxRequestTimes": 1e6,
	"hMaxReusableSecs": 31 * 86400, "hKeepAlivePeriod": 86400,
}

// numMax возвращает наибольшее число в значении (число, "min-max", {from,to}, [min,max]); нечисловое не считается.
func numMax(v any) float64 {
	m := 0.0
	upd := func(f float64) {
		if f > m {
			m = f
		}
	}
	switch t := v.(type) {
	case float64:
		upd(t)
	case string:
		for _, p := range strings.Split(t, "-") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err == nil {
				upd(f)
			}
		}
	case map[string]any:
		for _, x := range t {
			if f, ok := x.(float64); ok {
				upd(f)
			}
		}
	case []any:
		for _, x := range t {
			if f, ok := x.(float64); ok {
				upd(f)
			}
		}
	}
	return m
}

func checkLimit(name string, v any, limit float64) error {
	if m := numMax(v); m > limit {
		return fail(ReasonOutOfRange, "xhttp %s: %.0f больше предела %.0f", name, m, limit)
	}
	return nil
}

var xhttpModes = map[string]bool{"auto": true, "packet-up": true, "stream-up": true, "stream-one": true}

// normRange приводит значение диапазона к виду "min-max" либо целому.
func normRange(v any) (any, error) {
	switch t := v.(type) {
	case float64:
		if t < 0 || t != float64(int64(t)) {
			return nil, fail(ReasonBadRange, "число %v", t)
		}
		return int64(t), nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil, fail(ReasonBadRange, "пусто")
		}
		parts := strings.SplitN(s, "-", 2)
		for _, p := range parts {
			if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err != nil || n < 0 {
				return nil, fail(ReasonBadRange, "строка %q", s)
			}
		}
		return s, nil
	case map[string]any:
		f, fo := t["from"].(float64)
		to, too := t["to"].(float64)
		if !fo && !too {
			return nil, fail(ReasonBadRange, "объект без from/to")
		}
		if !fo {
			f = to
		}
		if !too {
			to = f
		}
		if f < 0 || to < f {
			return nil, fail(ReasonBadRange, "from>to")
		}
		return fmt.Sprintf("%d-%d", int64(f), int64(to)), nil
	case []any:
		if len(t) == 2 {
			a, ok1 := t[0].(float64)
			b, ok2 := t[1].(float64)
			if ok1 && ok2 && a >= 0 && b >= a {
				return fmt.Sprintf("%d-%d", int64(a), int64(b)), nil
			}
		}
	}
	return nil, fail(ReasonBadRange, "тип %T", v)
}

func xhttp(q url.Values, o *Outbound) (map[string]any, error) {
	tr := map[string]any{"type": "xhttp"}
	path, host, mode := q.Get("path"), q.Get("host"), strings.ToLower(q.Get("mode"))
	var extra map[string]any
	if raw := q.Get("extra"); raw != "" {
		var parsed any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, fail(ReasonBadExtra, "невалидный JSON")
		}
		if m, ok := parsed.(map[string]any); ok {
			extra = m
		} else {
			// null, [] и т.п. валидный JSON, но не настройки: считаем, что extra нет
			o.Notes = append(o.Notes, "extra:не объект, проигнорирован")
		}
		// вложенный extra.extra встречается у части источников — поднимаем наверх, не перекрывая внешние ключи
		if in, ok := extra["extra"].(map[string]any); ok {
			for k, v := range in {
				if _, dup := extra[k]; !dup {
					extra[k] = v
				}
			}
			delete(extra, "extra")
		}
		// extra может переопределять mode/path/host
		if s, ok := extra["mode"].(string); ok && s != "" {
			mode = strings.ToLower(s)
		}
		if s, ok := extra["path"].(string); ok && s != "" {
			path = s
		}
		if s, ok := extra["host"].(string); ok && s != "" {
			host = s
		}
	}
	if mode == "" {
		mode = "auto"
	}
	if !xhttpModes[mode] {
		return nil, fail(ReasonBadTransport, "xhttp mode %q", mode)
	}
	tr["mode"] = mode
	if path == "" {
		path = "/"
	}
	tr["path"] = fixPath(path)
	if host != "" {
		tr["host"] = host
	}
	var dropped []string
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := extra[k]
		if lim, ok := xhttpLimits[k]; ok {
			if err := checkLimit(k, v, lim); err != nil {
				return nil, err
			}
		}
		switch {
		case k == "mode" || k == "path" || k == "host":
		case k == "downloadSettings":
			o.Flags.DownloadSplit = true
			o.Notes = append(o.Notes, "downloadSettings: не поддержан lx, отброшен")
		case k == "headers":
			m, ok := v.(map[string]any)
			if !ok {
				dropped = append(dropped, "headers:не объект")
				continue
			}
			h := map[string]any{}
			for hk, hv := range m {
				if s, ok := hv.(string); ok {
					h[hk] = s
				}
			}
			if len(h) > 0 {
				tr["headers"] = h
			}
		case k == "xmux":
			m, ok := v.(map[string]any)
			if !ok {
				dropped = append(dropped, "xmux:не объект")
				continue
			}
			xm := map[string]any{}
			for xk, xv := range m {
				name, known := xmuxKeys[xk]
				if !known {
					dropped = append(dropped, "xmux."+xk)
					continue
				}
				if err := checkLimit("xmux."+xk, xv, xmuxLimits[xk]); err != nil {
					return nil, err
				}
				if xk == "hKeepAlivePeriod" {
					if n, ok := xv.(float64); ok {
						xm[name] = int64(n)
					}
					continue
				}
				r, err := normRange(xv)
				if err != nil {
					dropped = append(dropped, "xmux."+xk+":значение")
					continue
				}
				xm[name] = r
			}
			if len(xm) > 0 {
				tr["xmux"] = xm
			}
		case extraKeys[k] != "":
			name := extraKeys[k]
			switch {
			case rangeKeys[k]:
				// необязательные настройки клиента с неверным значением отбрасываем с пометкой, узел не теряем:
				// если сервер реально требовал этот ключ, это покажет проверка L2
				r, err := normRange(v)
				if err != nil {
					dropped = append(dropped, k+":значение")
					continue
				}
				tr[name] = fmt.Sprint(r)
			case boolKeys[k]:
				b, ok := v.(bool)
				if !ok {
					dropped = append(dropped, k+":значение")
					continue
				}
				tr[name] = b
			case intKeys[k]:
				n, ok := v.(float64)
				if !ok {
					dropped = append(dropped, k+":значение")
					continue
				}
				tr[name] = int64(n)
			default:
				s, ok := v.(string)
				if !ok {
					dropped = append(dropped, k+":значение")
					continue
				}
				tr[name] = s
			}
		default:
			dropped = append(dropped, k) // белый список: неизвестное не пропускаем
		}
	}
	// пустая таблица / длина 0 означают «не задано» (так пишут часть панелей)
	if v, _ := tr["session_table"].(string); v == "" {
		delete(tr, "session_table")
	}
	if v, _ := tr["session_length"].(string); v == "0" || v == "0-0" {
		delete(tr, "session_length")
	}
	_, hasT := tr["session_table"]
	_, hasL := tr["session_length"]
	if hasT != hasL {
		delete(tr, "session_table")
		delete(tr, "session_length")
		dropped = append(dropped, "session_table/length:не парой")
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		o.Notes = append(o.Notes, "dropped:"+strings.Join(dropped, ","))
	}
	return tr, nil
}
