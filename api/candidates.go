package api

import (
	"errors"
	"net/http"
	"time"

	"brod/core"
)

// Автопоиск источников (core/discover.go). Все маршруты только для владельца: кандидаты заводятся в источники лишь его командой.

type candJSON struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Repo       string         `json:"repo"`
	URL        string         `json:"url"`
	Links      int            `json:"links"`
	Schemes    map[string]int `json:"schemes"`
	FirstSeen  *time.Time     `json:"first_seen,omitempty"`
	LastSeen   *time.Time     `json:"last_seen,omitempty"`
	DaysSeen   int            `json:"days_seen"`
	Sampled    int            `json:"sampled"`     // проверено ссылок из выборок (накопительно)
	SampleOK   int            `json:"sample_ok"`   // из них ответило
	SampleDays int            `json:"sample_days"` // дней, в которые брали выборку
	LastSample *time.Time     `json:"last_sample,omitempty"`
	Group      string         `json:"group"`      // группа похожих по содержимому кандидатов (зеркал)
	GroupSize  int            `json:"group_size"` // сколько кандидатов в группе, включая этого
}

func candOf(v core.CandidateView) candJSON {
	return candJSON{v.ID, v.Name, v.Repo, v.URL, v.Links, v.Schemes, tp(v.FirstSeen), tp(v.LastSeen), v.DaysSeen, v.Sampled, v.SampleOK, v.SampleDays,
		tp(v.LastSample), v.Group, v.GroupSize}
}

func (s *Server) candidates(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	list, err := s.core().Candidates()
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]candJSON, len(list))
	for i, v := range list {
		out[i] = candOf(v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) candidateGroups(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	list, err := s.core().CandidateGroups()
	if err != nil {
		fail(w, err)
		return
	}
	type group struct {
		ID             string     `json:"id"`
		Representative candJSON   `json:"representative"`
		Members        []candJSON `json:"members"`
		Size           int        `json:"size"`
		TotalLinksMax  int        `json:"total_links_max"`
	}
	out := make([]group, len(list))
	for i, g := range list {
		m := make([]candJSON, len(g.Members))
		for j, v := range g.Members {
			m[j] = candOf(v)
		}
		out[i] = group{g.ID, candOf(g.Representative), m, g.Size, g.TotalLinksMax}
	}
	writeJSON(w, 200, out)
}

func (s *Server) candidateGroupDismiss(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	n, err := s.core().DismissGroup(r.PathValue("id"), caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "dismissed": n})
}

func (s *Server) candidatesScan(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		AllowMetered bool `json:"allow_metered"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	rep, err := s.core().ScanCandidates(r.Context(), caller(r), b.AllowMetered)
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"repos": rep.Repos, "readmes": rep.Readmes, "files": rep.Files, "found": rep.Found, "new": rep.New,
		"sampled": rep.Sampled, "alive": rep.Alive, "probed": rep.Probed, "took_ms": rep.Took.Milliseconds()})
}

func (s *Server) candidateAdd(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	name, err := s.core().AddCandidate(r.PathValue("id"), caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "name": name})
}

func (s *Server) candidateDismiss(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().DismissCandidate(r.PathValue("id"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
