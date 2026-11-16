// Package api — локальный JSON API демона: unix-сокет (права 0600, вызывающий — владелец) и HTTP на 127.0.0.1 с токеном.
//
// Токены: главный (владелец, всё разрешено) и токены приложений (`api:<имя>`): приложение видит и меняет только свои цели,
// читает статус без деталей активного узла и события своих целей. Ошибки — {"error": "..."} с кодами 400/401/403/404/409.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"brod/core"
	"brod/notify"
	"brod/store"
	"brod/supervisor"
)

// Server — HTTP-обвязка над ядром.
type Server struct {
	c       *core.Core
	version string
	// NotifyTest показывает проверочное уведомление (nil — уведомления отключены).
	NotifyTest func() error
	// Notices — уведомления, которые показывает приложение (Windows: служба в сеансе 0 показывать их не может); nil — нет.
	Notices *notify.Relay
	// Core возвращает текущее ядро: при переключении VPN демон пересоздаёт ядро целиком (nil — всегда ядро из New).
	Core func() *core.Core
	// VPN управляет режимом VPN (TUN); nil — переключатель недоступен.
	VPN VPNControl
}

// VPNControl — переключатель VPN: on == nil — только сменить режим (если VPN включён — переключается сразу), mode == "" — режим не менять.
type VPNControl interface {
	State() supervisor.State
	Set(on *bool, mode string) (supervisor.State, error)
	// Reload применяет изменённые настройки движка (правила маршрутизации): включённый VPN перезапускается.
	Reload() (supervisor.State, error)
	// Restart пересоздаёт ядро даже при выключенном VPN (после смены аккаунта WARP).
	Restart() (supervisor.State, error)
	// RefreshKillswitch применяет изменённую настройку киллсвитча без перезапуска ядра.
	RefreshKillswitch() supervisor.State
	// ReleaseStaleKillswitch снимает правила киллсвитча, оставшиеся после упавшего демона (при включённом VPN отказывает).
	ReleaseStaleKillswitch() error
	// SetAutoconnect меняет автоподключение при старте демона (last | always | off).
	SetAutoconnect(v string) (supervisor.State, error)
}

// New создаёт сервер API.
func New(c *core.Core, version string) *Server { return &Server{c: c, version: version} }

func (s *Server) core() *core.Core {
	if s.Core != nil {
		return s.Core()
	}
	return s.c
}

// uiVisible — интерфейс приложения на экране (сообщает сам интерфейс, `POST /v1/ui`). Пока false, а VPN выключен, на Android фоновые
// проходы ядра спят. Ядро и интерфейс живут в одном процессе, так что флаг не может «застрять» после гибели приложения.
var uiVisible atomic.Bool

// UIVisible — приложение сейчас на экране.
func UIVisible() bool { return uiVisible.Load() }

type ctxKey struct{}

// caller возвращает идентификатор вызывающего: "user" либо "api:<приложение>".
func caller(r *http.Request) string {
	v, _ := r.Context().Value(ctxKey{}).(string)
	return v
}

// Handler возвращает обработчик. trusted=true — транспорт уже защищён правами файла (unix-сокет): токен не нужен, вызывающий — user.
// Иначе нужен заголовок Authorization: Bearer <токен>, запрос с Origin (из браузера) и с чужим Host отклоняется.
func (s *Server) Handler(trusted bool, allowedHosts ...string) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		who := "user"
		if !trusted {
			if r.Header.Get("Origin") != "" {
				writeErr(w, http.StatusForbidden, "запросы из браузера не принимаются")
				return
			}
			if len(allowedHosts) > 0 && !hostAllowed(r.Host, allowedHosts) {
				writeErr(w, http.StatusForbidden, "недопустимый Host")
				return
			}
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			var ok bool
			if who, ok = s.core().Authenticate(tok); !ok {
				time.Sleep(200 * time.Millisecond) // не даём перебирать токены быстро
				writeErr(w, http.StatusUnauthorized, "нужен токен")
				return
			}
		}
		sw := &statusWriter{ResponseWriter: w}
		mux.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, who)))
		logAPIError(r.Method, r.URL.Path, sw.code, sw.body.String())
		countHit(r.Method, r.URL.Path, time.Now())
	})
}

func hostAllowed(host string, allowed []string) bool {
	for _, a := range allowed {
		if host == a {
			return true
		}
	}
	return false
}

// ListenUnix создаёт сокет с правами 0600 (прежний файл сокета удаляется).
func ListenUnix(path string) (net.Listener, error) {
	_ = os.Remove(path)
	old := setUmask(0o177)
	ln, err := net.Listen("unix", path)
	setUmask(old)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

// Serve обслуживает слушатель до отмены контекста.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = srv.Close() // SSE-потоки сами не закрываются
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---- вспомогательное ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONTagged — writeJSON с меткой версии ответа (ETag от хэша тела): если клиент прислал ту же метку в If-None-Match,
// тело не отправляется (304). Приложение опрашивает список узлов каждые пару секунд, а он весит полмегабайта и почти не меняется.
// Если метка (pre) уже посчитана до сборки ответа, она и отдаётся; пустая — метка считается от тела.
func writeJSONTagged(w http.ResponseWriter, r *http.Request, v any, pre string) {
	body, err := json.Marshal(v)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tag := pre
	if tag == "" {
		sum := sha256.Sum256(body)
		tag = `"` + hex.EncodeToString(sum[:8]) + `"`
	}
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(append(body, '\n'))
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": utf8Text(msg)})
}

// utf8Text приводит текст ошибки к UTF-8. SQLite на Windows берёт описание системной ошибки через FormatMessageA, то есть в ANSI-кодовой
// странице (cp1251), и такая строка, прочитанная клиентом как UTF-8, превращалась в «кракозябры». Недопустимые последовательности
// трактуем как Windows-1251; если не получилось, заменяем символом подстановки.
func utf8Text(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	if out, err := charmap.Windows1251.NewDecoder().String(s); err == nil && utf8.ValidString(out) {
		return out
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// fail переводит ошибку ядра в код ответа.
func fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, core.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, core.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound), errors.Is(err, core.ErrNoNode):
		code = http.StatusNotFound
	case errors.Is(err, core.ErrBusy), errors.Is(err, core.ErrNoCandidates), errors.Is(err, core.ErrMetered), errors.Is(err, core.ErrConflict):
		code = http.StatusConflict
	case strings.Contains(strings.ToLower(err.Error()), "database is closed"):
		// ядро пересоздаётся (смена режима, перезапуск после настроек): не «ошибка базы», а просьба повторить
		writeErr(w, http.StatusServiceUnavailable, "демон перезапускает ядро, повторите через секунду")
		return
	}
	writeErr(w, code, err.Error())
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "неверное тело запроса: "+err.Error())
		return false
	}
	return true
}

// onlyUser пропускает только владельца.
func onlyUser(w http.ResponseWriter, r *http.Request) bool {
	if caller(r) != "user" {
		writeErr(w, http.StatusForbidden, "только для владельца")
		return false
	}
	return true
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func qInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && v >= 0 {
		return v
	}
	return def
}

func qBool(r *http.Request, key string) bool {
	v := r.URL.Query().Get(key)
	return v == "1" || v == "true"
}

var _ = fmt.Sprintf

// statusWriter запоминает код ответа и начало тела ошибки, чтобы записать её в журнал демона.
type statusWriter struct {
	http.ResponseWriter
	code int
	body strings.Builder
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	if w.code >= 400 && w.body.Len() < 300 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var apiLogged = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// logAPIError пишет в журнал демона каждую ошибку, которую API вернул клиенту (метод, путь, код, текст): по журналу можно
// восстановить, что именно видел пользователь в окне. Повтор одной и той же ошибки — не чаще раза в 30 с (приложение опрашивает API).
func logAPIError(method, path string, code int, body string) {
	if code < 400 || code == http.StatusUnauthorized {
		return
	}
	key := method + " " + path + " " + strconv.Itoa(code)
	now := time.Now()
	apiLogged.Lock()
	if t, ok := apiLogged.at[key]; ok && now.Sub(t) < 30*time.Second {
		apiLogged.Unlock()
		return
	}
	if len(apiLogged.at) > 500 {
		apiLogged.at = map[string]time.Time{}
	}
	apiLogged.at[key] = now
	apiLogged.Unlock()
	log.Printf("API %s %s → %d: %s", method, path, code, strings.TrimSpace(utf8Text(body)))
}
