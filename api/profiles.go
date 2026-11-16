package api

import (
	"net/http"
	"strings"

	"brod/core"
)

// Профили маршрутизации (только владелец): список, создание, переименование, удаление, переключение, ссылка и импорт.

func (s *Server) profilesList(w http.ResponseWriter) {
	ps, err := s.core().RouteProfiles()
	if err != nil {
		fail(w, err)
		return
	}
	active := ""
	for _, p := range ps {
		if p.Active {
			active = p.ID
		}
	}
	writeJSON(w, 200, map[string]any{"active": active, "profiles": ps})
}

func (s *Server) profilesGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	s.profilesList(w)
}

func (s *Server) profilesCreate(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
		From string `json:"from"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, err := s.core().CreateRouteProfile(in.Name, in.From, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) profilesImport(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Text string `json:"text"`
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if t := strings.TrimSpace(in.Text); strings.HasPrefix(t, "https://") { // профиль по адресу: скачивается ядром, дальше тот же строгий разбор
		text, err := s.core().FetchProfileText(r.Context(), t)
		if err != nil {
			fail(w, err)
			return
		}
		in.Text = text
	}
	p, err := s.core().ImportRouteProfile(in.Text, in.Name, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) profilesRename(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.core().RenameRouteProfile(r.PathValue("id"), in.Name, caller(r)); err != nil {
		fail(w, err)
		return
	}
	s.profilesList(w)
}

func (s *Server) profilesDelete(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().DeleteRouteProfile(r.PathValue("id"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	s.profilesList(w)
}

func (s *Server) profilesShare(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	link, err := s.core().ShareRouteProfile(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"link": link})
}

// profilesActivate переключает профиль и применяет правила: включённый VPN перезапускается (как после PUT /v1/route-rules).
func (s *Server) profilesActivate(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().ActivateRouteProfile(r.PathValue("id"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{}
	if s.VPN != nil {
		st, err := s.VPN.Reload()
		out["vpn"] = st
		if err != nil {
			out["reload_error"] = err.Error()
		}
	}
	rs, err := s.core().RouteRules()
	if err != nil {
		fail(w, err)
		return
	}
	if rs == nil {
		rs = []core.RouteRule{}
	}
	ps, _ := s.core().RouteProfiles()
	out["rules"], out["profiles"] = rs, ps
	writeJSON(w, 200, out)
}
