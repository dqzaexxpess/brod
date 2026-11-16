package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"brod/store"
)

func toEventDTO(e store.Event) eventDTO {
	d := eventDTO{ID: e.ID, At: e.At, Kind: e.Kind, Initiator: e.Initiator, Node: e.Node, Target: e.Target}
	if e.Detail != "" {
		var raw json.RawMessage
		if json.Unmarshal([]byte(e.Detail), &raw) == nil && strings.HasPrefix(strings.TrimSpace(e.Detail), "{") {
			d.Detail = raw
		} else {
			d.Detail = e.Detail
		}
	}
	return d
}

// visible: владелец видит всё; приложение — события только своих целей.
func (s *Server) visible(who string, e store.Event) bool {
	if who == "user" {
		return true
	}
	if e.Target == "" {
		return false
	}
	t, err := s.core().Store().Target(strings.TrimPrefix(e.Target, "target:"))
	return err == nil && t.Owner == who
}

// events: JSON-список (после ?after=ID) либо поток SSE при Accept: text/event-stream. SSE продолжается с Last-Event-ID.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	who := caller(r)
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if id, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil {
		after = id
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		var evs []store.Event
		var err error
		if n := qInt(r, "tail", 0); n > 0 { // последние n событий (для окна журнала)
			evs, err = s.core().LastEvents(min(n, 1000))
		} else {
			evs, err = s.core().EventsAfter(after, qInt(r, "limit", 200))
		}
		if err != nil {
			fail(w, err)
			return
		}
		out := []eventDTO{}
		for _, e := range evs {
			if s.visible(who, e) {
				out = append(out, toEventDTO(e))
			}
		}
		writeJSON(w, 200, out)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "потоковая передача недоступна")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	c0 := s.core()
	sub := c0.Bus().Subscribe(256)
	defer sub.Close()
	send := func(e store.Event) bool {
		if e.ID != 0 && e.ID <= after {
			return true
		}
		if e.ID != 0 {
			after = e.ID
		}
		if !s.visible(who, e) {
			return true
		}
		b, _ := json.Marshal(toEventDTO(e))
		if e.ID != 0 {
			fmt.Fprintf(w, "id: %d\n", e.ID)
		}
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, b)
		fl.Flush()
		return err == nil
	}
	// пропущенное: из журнала после after (в том числе всё, что опубликовано между запросом и подпиской)
	backfill := func() bool {
		evs, err := s.core().EventsAfter(after, 1000)
		if err != nil {
			return true
		}
		for _, e := range evs {
			if !send(e) {
				return false
			}
		}
		return true
	}
	if !backfill() {
		return
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	swap := time.NewTicker(500 * time.Millisecond) // ядро пересоздали (переключение VPN): поток закрываем, клиент переподключится с Last-Event-ID
	defer swap.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-swap.C:
			if s.core() != c0 || !c0.CallerActive(who) { // и отозванный токен: Authenticate срабатывает только при входе запроса
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case e := <-sub.C:
			if sub.Lagged() || (e.ID != 0 && e.ID > after+1) {
				if !backfill() { // дыра в нумерации: догоняем из журнала, затем текущее
					return
				}
			}
			if !send(e) {
				return
			}
		}
	}
}
