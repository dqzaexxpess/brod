package api

import (
	"net/http"

	"brod/core"
	"brod/prober"
)

// Свои списки маршрутизации по URL (только владелец): список, добавление (скачивается и проверяется сразу), удаление. Обновление — вместе со
// встроенными (`POST /v1/rulesets/update` и раз в 12 часов).

func userSetsView(l []core.UserSet) []core.UserSet {
	if l == nil {
		return []core.UserSet{}
	}
	for i := range l {
		l[i].Error = prober.Sanitize(l[i].Error)
	}
	return l
}

func (s *Server) userSetsList(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	writeJSON(w, 200, map[string]any{"sets": userSetsView(s.core().UserSets())})
}

func (s *Server) userSetsAdd(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if !decode(w, r, &b) {
		return
	}
	set, err := s.core().AddUserSet(r.Context(), b.URL, b.Title, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, set)
}

func (s *Server) userSetsDelete(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().RemoveUserSet(r.PathValue("id"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
