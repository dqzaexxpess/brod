package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"brod/core"
	"brod/notify"
	"brod/prober"
	"brod/store"
	"brod/supervisor"
)

// ---- DTO ----

type nodeDTO struct {
	ID         string        `json:"id"`
	Proto      string        `json:"proto"`
	Class      string        `json:"class"`
	Server     string        `json:"server"`
	Port       int           `json:"port"`
	Favorite   bool          `json:"favorite"`
	Pinned     bool          `json:"pinned"`
	Insecure   bool          `json:"insecure"`
	NoSecurity bool          `json:"no_security"`
	Present    bool          `json:"present"`
	L2At       *time.Time    `json:"l2_at,omitempty"`
	GoogleAPI  bool          `json:"google_api"`
	YouTube    bool          `json:"youtube"`
	LastFail   *time.Time    `json:"last_fail,omitempty"`
	Notes      []string      `json:"notes,omitempty"`
	Exit       *exitDTO      `json:"exit,omitempty"`       // что видят сайты на выходе узла; нет, пока не собрано
	Label      string        `json:"label,omitempty"`      // пользовательское название
	Own        bool          `json:"own,omitempty"`        // собственный сервер пользователя
	LatencyMs  int64         `json:"latency_ms,omitempty"` // пинг: задержка последней успешной проверки
	SpeedKBps  float64       `json:"speed_kbps,omitempty"` // скорость скачивания через узел, КБ/с
	SpeedAt    *time.Time    `json:"speed_at,omitempty"`
	UDP        string        `json:"udp,omitempty"`    // good | unstable | none: годен ли узел для звонков (замер UDP в текущей сети)
	UDPMs      int64         `json:"udp_ms,omitempty"` // медиана задержки STUN-ответов
	UDPAt      *time.Time    `json:"udp_at,omitempty"`
	Chain      []chainHopDTO `json:"chain,omitempty"` // звенья, если это цепочка
}

type chainHopDTO struct {
	ID     string `json:"id"`
	Proto  string `json:"proto"`
	Server string `json:"server"`
	Port   int    `json:"port"`
}

// exitDTO — страна и «чистота» выхода (Cloudflare trace через узел + поведенческие проверки).
type exitDTO struct {
	IP       string    `json:"ip"`
	Country  string    `json:"country"`
	Colo     string    `json:"colo,omitempty"`
	GoogleOK *bool     `json:"google_ok"` // null — не проверялось
	SiteOK   *bool     `json:"site_ok"`
	Clean    string    `json:"clean"` // clean | mixed | dirty | unknown
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
}

func exitFrom(e *core.ExitView) *exitDTO {
	if e == nil {
		return nil
	}
	return &exitDTO{IP: e.IP, Country: e.Country, Colo: e.Colo, GoogleOK: e.GoogleOK, SiteOK: e.SiteOK, Clean: e.Clean, Detail: e.Detail, At: e.At}
}

func nodeFromInfo(n core.NodeInfo) nodeDTO {
	return nodeDTO{ID: n.ID, Proto: n.Proto, Class: n.Class, Server: n.Server, Port: n.Port, Favorite: n.Favorite, Pinned: n.Pinned, Insecure: n.Insecure, Present: n.Present}
}

func nodeFromView(v core.NodeView) nodeDTO {
	d := nodeFromInfo(v.NodeInfo)
	d.NoSecurity, d.L2At, d.GoogleAPI, d.YouTube, d.LastFail, d.Notes = v.NoSecurity, tp(v.L2At), v.GoogleAPI, v.YouTube, tp(v.LastFail), v.Notes
	d.Exit = exitFrom(v.Exit)
	d.LatencyMs, d.SpeedKBps, d.SpeedAt, d.Own = v.LatencyMs, v.SpeedKBps, tp(v.SpeedAt), v.Own
	d.Label = v.Label
	d.UDP, d.UDPMs, d.UDPAt = v.UDP, v.UDPMs, tp(v.UDPAt)
	for _, h := range v.Chain {
		d.Chain = append(d.Chain, chainHopDTO{ID: h.ID, Proto: h.Proto, Server: h.Server, Port: h.Port})
	}
	return d
}

type resultDTO struct {
	OK        bool   `json:"ok"`
	Class     string `json:"class,omitempty"`
	LatencyMs int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

func resultFrom(r prober.Result) resultDTO {
	return resultDTO{OK: r.OK, Class: string(r.Class), LatencyMs: r.LatencyMs, Detail: r.Detail}
}

type targetDTO struct {
	ID          string            `json:"id"`
	Owner       string            `json:"owner"`
	Title       string            `json:"title"`
	Rules       store.TargetRules `json:"rules"`
	Check       store.TargetCheck `json:"check"`
	Policy      string            `json:"policy"`
	FailoverK   int               `json:"failover_k"`
	AllowDirect bool              `json:"allow_direct"`
	AllowWarp   bool              `json:"allow_warp"`
	Node        string            `json:"node"`
	NodeInfo    *nodeDTO          `json:"node_info,omitempty"`
	Fallbacks   []string          `json:"fallbacks"`
	SetBy       string            `json:"set_by,omitempty"`
	Last        *resultDTO        `json:"last,omitempty"`
	LastAt      *time.Time        `json:"last_at,omitempty"`
	Degraded    bool              `json:"degraded"`
	Fails       int               `json:"fails"`
	HasProbeURL bool              `json:"has_probe_url"`
}

func targetFrom(t core.TargetStatus) targetDTO {
	d := targetDTO{ID: t.ID, Owner: t.Owner, Title: t.Title, Rules: t.Rules, Check: t.Check, Policy: t.Policy, FailoverK: t.FailoverK,
		AllowDirect: t.AllowDirect, AllowWarp: t.AllowWarp, Node: t.Node, Fallbacks: t.Fallbacks, SetBy: t.SetBy,
		Degraded: t.Degraded, Fails: t.Fails, HasProbeURL: t.HasProbeURL, LastAt: tp(t.LastAt)}
	if d.Fallbacks == nil {
		d.Fallbacks = []string{}
	}
	if t.NodeInfo != nil {
		n := nodeFromInfo(*t.NodeInfo)
		d.NodeInfo = &n
	}
	if !t.LastAt.IsZero() {
		r := resultFrom(t.Last)
		d.Last = &r
	}
	return d
}

type eventDTO struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Initiator string    `json:"initiator,omitempty"`
	Node      string    `json:"node,omitempty"`
	Target    string    `json:"target,omitempty"`
	Detail    any       `json:"detail,omitempty"` // JSON-объект, если деталь — JSON, иначе строка
}

// ---- маршруты ----

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/status", s.status)
	m.HandleFunc("GET /v1/nodes", s.nodes)
	m.HandleFunc("GET /v1/nodes/{id}", s.node)
	m.HandleFunc("GET /v1/nodes/{id}/link", s.nodeLink)
	m.HandleFunc("POST /v1/nodes/{id}/connect", s.connect)
	m.HandleFunc("GET /v1/connections", s.connections)
	m.HandleFunc("DELETE /v1/connections/{id}", s.closeConnection)
	m.HandleFunc("GET /v1/backup", s.backupGet)
	m.HandleFunc("POST /v1/backup", s.backupPost)
	m.HandleFunc("POST /v1/diagnostics", s.diagnostics)
	m.HandleFunc("POST /v1/report", s.reportStart)
	m.HandleFunc("GET /v1/report", s.reportGet)
	m.HandleFunc("GET /v1/traffic", s.traffic)
	m.HandleFunc("GET /v1/traffic/daily", s.trafficDaily)
	m.HandleFunc("POST /v1/connect-best", s.connectBest)
	m.HandleFunc("GET /v1/tg-proxies", s.tgList)
	m.HandleFunc("POST /v1/tg-proxies", s.tgAdd)
	m.HandleFunc("POST /v1/tg-proxies/check", s.tgCheck)
	m.HandleFunc("DELETE /v1/tg-proxies/{id}", s.tgDelete)
	m.HandleFunc("PUT /v1/tg-proxies/{id}/favorite", s.tgFavorite)
	m.HandleFunc("POST /v1/own-nodes", s.ownAdd)
	m.HandleFunc("POST /v1/chains", s.chainAdd)
	m.HandleFunc("DELETE /v1/own-nodes/{id}", s.ownDelete)
	m.HandleFunc("PATCH /v1/own-nodes/{id}", s.ownEdit)
	m.HandleFunc("PUT /v1/nodes/{id}/label", s.nodeLabel)
	m.HandleFunc("POST /v1/measure", s.measureStart)
	m.HandleFunc("POST /v1/ui", s.uiState)
	m.HandleFunc("GET /v1/measure", s.measureGet)
	m.HandleFunc("DELETE /v1/measure", s.measureCancel)
	m.HandleFunc("POST /v1/nodes/{id}/exit-info", s.exitInfo)
	m.HandleFunc("POST /v1/nodes/{id}/speed", s.speed)
	m.HandleFunc("POST /v1/nodes/{id}/favorite", s.flag(true))
	m.HandleFunc("POST /v1/nodes/{id}/pin", s.flag(false))
	m.HandleFunc("POST /v1/refresh", s.refresh)
	m.HandleFunc("POST /v1/check", s.check)
	m.HandleFunc("POST /v1/probe-active", s.probeActive)
	m.HandleFunc("GET /v1/alternatives", s.alternatives)
	m.HandleFunc("POST /v1/warp/probe", s.probeWarp)
	m.HandleFunc("GET /v1/warp", s.warpGet)
	m.HandleFunc("POST /v1/warp/scan", s.warpScan)
	m.HandleFunc("POST /v1/warp/reset", s.warpReset)
	m.HandleFunc("PUT /v1/warp/params", s.warpParams)
	m.HandleFunc("POST /v1/warp/register", s.registerWarp)
	m.HandleFunc("POST /v1/ota/sources", s.otaSources)
	m.HandleFunc("POST /v1/warp/import", s.importWarp)
	m.HandleFunc("POST /v1/notify-test", s.notifyTest)
	m.HandleFunc("GET /v1/notifications", s.notifications)
	m.HandleFunc("POST /v1/notifications/{id}/action", s.notificationAction)
	m.HandleFunc("POST /v1/rulesets/update", s.updateRuleSets)
	m.HandleFunc("GET /v1/route-sets", s.userSetsList)
	m.HandleFunc("POST /v1/route-sets", s.userSetsAdd)
	m.HandleFunc("DELETE /v1/route-sets/{id}", s.userSetsDelete)
	m.HandleFunc("GET /v1/route-rules", s.routeRulesGet)
	m.HandleFunc("PUT /v1/route-rules", s.routeRulesPut)
	m.HandleFunc("GET /v1/update", s.updateGet)
	m.HandleFunc("POST /v1/update/check", s.updateCheck)
	m.HandleFunc("POST /v1/update/download", s.updateDownload)
	m.HandleFunc("POST /v1/update/install", s.updateInstall)
	m.HandleFunc("GET /v1/route-profiles", s.profilesGet)
	m.HandleFunc("POST /v1/route-profiles", s.profilesCreate)
	m.HandleFunc("POST /v1/route-profiles/import", s.profilesImport)
	m.HandleFunc("PUT /v1/route-profiles/{id}", s.profilesRename)
	m.HandleFunc("DELETE /v1/route-profiles/{id}", s.profilesDelete)
	m.HandleFunc("POST /v1/route-profiles/{id}/activate", s.profilesActivate)
	m.HandleFunc("GET /v1/route-profiles/{id}/share", s.profilesShare)
	m.HandleFunc("POST /v1/killswitch/release", s.killswitchRelease)
	m.HandleFunc("GET /v1/vpn", s.vpnGet)
	m.HandleFunc("POST /v1/vpn", s.vpnSet)
	m.HandleFunc("POST /v1/vpn/selftest", s.vpnSelfTest)
	m.HandleFunc("POST /v1/nodes/{id}/check", s.nodeCheck)
	m.HandleFunc("POST /v1/active/check", s.activeCheck)
	m.HandleFunc("POST /v1/nodes/{id}/udp", s.nodeUDP)
	m.HandleFunc("POST /v1/active/udp", s.activeUDP)
	m.HandleFunc("GET /v1/sources", s.sources)
	m.HandleFunc("POST /v1/sources", s.addSource)
	m.HandleFunc("POST /v1/sources/{name}/enable", s.enableSource)
	m.HandleFunc("DELETE /v1/sources/{name}", s.deleteSource)
	m.HandleFunc("GET /v1/source-candidates", s.candidates)
	m.HandleFunc("POST /v1/source-candidates/scan", s.candidatesScan)
	m.HandleFunc("POST /v1/source-candidates/{id}/add", s.candidateAdd)
	m.HandleFunc("POST /v1/source-candidates/{id}/dismiss", s.candidateDismiss)
	m.HandleFunc("GET /v1/source-candidate-groups", s.candidateGroups)
	m.HandleFunc("POST /v1/source-candidate-groups/{id}/dismiss", s.candidateGroupDismiss)
	m.HandleFunc("GET /v1/targets", s.targets)
	m.HandleFunc("GET /v1/targets/{id}", s.target)
	m.HandleFunc("PUT /v1/targets/{id}", s.putTarget)
	m.HandleFunc("DELETE /v1/targets/{id}", s.deleteTarget)
	m.HandleFunc("POST /v1/targets/{id}/probe-url", s.probeURL)
	m.HandleFunc("POST /v1/targets/{id}/probe", s.probeTarget)
	m.HandleFunc("POST /v1/targets/{id}/switch", s.switchTarget)
	m.HandleFunc("GET /v1/events", s.events)
	m.HandleFunc("GET /v1/settings", s.settings)
	m.HandleFunc("PUT /v1/settings/{key}", s.putSetting)
	m.HandleFunc("PUT /v1/tun/apps", s.putTunApps)
	m.HandleFunc("GET /v1/logs", s.logs)
	m.HandleFunc("GET /v1/app-proxy", s.appProxy)
	m.HandleFunc("GET /v1/tokens", s.tokens)
	m.HandleFunc("POST /v1/tokens", s.createToken)
	m.HandleFunc("DELETE /v1/tokens/{name}", s.revokeToken)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.core().Status()
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{
		"version": s.version, "started_at": st.StartedAt, "nodes": st.Nodes, "present": st.Present,
		"degraded": s.core().Degraded(), "activity": s.core().Activity(),
	}
	if caller(r) == "user" { // приложению детали активного узла не нужны
		if st.Active != nil {
			a := nodeFromInfo(*st.Active)
			if st.Active.Proto != "warp" { // страна, чистота, пинг и скорость активного узла — для главного экрана
				if v, err := s.core().NodeByID(st.Active.ID); err == nil {
					a = nodeFromView(v)
				}
			}
			out["active"], out["active_since"], out["active_by"] = a, st.ActiveSince, st.ActiveBy
		}
		if s.VPN != nil { // состояние VPN в том же ответе, опросу не нужен второй запрос
			out["vpn"] = s.VPN.State()
		}
		addr, _, _ := s.core().AppProxy()
		out["app_proxy"] = addr
		out["warp"] = map[string]any{"enabled": st.WarpEnabled, "via_warp": st.ViaWarp, "down_min": st.WarpDownMin}
		if st.NetMode != "" {
			out["net_mode"] = st.NetMode
		}
		if u := s.core().UpdateInfo(); u.Available { // краткая сводка для баннера на главном экране
			out["update"] = map[string]any{"available": true, "app": u.App, "download": u.Download.State}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) nodes(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	q := r.URL.Query()
	nq := core.NodesQuery{Limit: qInt(r, "limit", 200), FavoritesOnly: qBool(r, "favorites"), Source: q.Get("source"),
		Class: q.Get("class"), IncludeNoSec: qBool(r, "include_no_security"), Alive: qBool(r, "alive")}
	// метка до сборки списка: список не менялся — 304 без работы ядра; для фильтра «живые» метки заранее нет (от времени), её даст тело
	tag, pre := s.core().NodesTag(nq)
	if pre && r.Header.Get("If-None-Match") == tag {
		w.Header().Set("ETag", tag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	list, err := s.core().Nodes(nq)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]nodeDTO, len(list))
	for i, v := range list {
		out[i] = nodeFromView(v)
	}
	writeJSONTagged(w, r, out, tag)
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	id, err := s.core().ResolveNode(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	v, err := s.core().NodeByID(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, nodeFromView(v))
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	id := r.PathValue("id")
	if id != store.SelWarp { // «warp» — не узел подписки, а встроенный выход
		var err error
		if id, err = s.core().ResolveNode(id); err != nil {
			fail(w, err)
			return
		}
	}
	if err := s.core().Connect(id, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) flag(favorite bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !onlyUser(w, r) {
			return
		}
		var body struct {
			On bool `json:"on"`
		}
		if !decode(w, r, &body) {
			return
		}
		id, err := s.core().ResolveNode(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		if favorite {
			err = s.core().SetFavorite(id, body.On, caller(r))
		} else {
			err = s.core().SetPinned(id, body.On, caller(r))
		}
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	rep, err := s.core().Refresh(r.Context(), caller(r))
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	type src struct {
		Name  string `json:"name"`
		Links int    `json:"links"`
		Nodes int    `json:"nodes"`
		Lost  int    `json:"lost"`
		Err   string `json:"error,omitempty"`
	}
	srcs := make([]src, len(rep.Sources))
	for i, x := range rep.Sources {
		srcs[i] = src{x.Name, x.Links, x.Nodes, x.Lost, prober.Sanitize(x.Err)}
	}
	writeJSON(w, 200, map[string]any{"sources": srcs, "added": rep.Added, "total": rep.Total, "present": rep.Present, "took_ms": rep.Took.Milliseconds()})
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var body struct {
		Limit int      `json:"limit"`
		Force bool     `json:"force"`
		IDs   []string `json:"ids"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	rep, err := s.core().CheckNow(r.Context(), core.CheckOptions{Limit: body.Limit, Force: body.Force, IDs: body.IDs}, caller(r))
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"picked": rep.Picked, "checked": rep.Checked, "ok": rep.OK, "google_ok": rep.GoogleOK, "took_ms": rep.Took.Milliseconds(), "fails": rep.Fails})
}

func (s *Server) probeActive(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	probe := s.core().ProbeActive
	if r.URL.Query().Get("quick") == "1" { // быстрая проверка «выход поднялся» для момента подключения
		probe = s.core().ProbeActiveQuick
	}
	res, err := probe(r.Context())
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, 200, resultFrom(res))
}

func (s *Server) alternatives(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	alts, err := s.core().Alternatives(r.Context(), qInt(r, "n", 3))
	if err != nil && r.Context().Err() == nil {
		fail(w, err)
		return
	}
	type alt struct {
		Node      nodeDTO `json:"node"`
		GoogleAPI bool    `json:"google_api"`
		LatencyMs int64   `json:"latency_ms"`
	}
	out := make([]alt, len(alts))
	for i, a := range alts {
		out[i] = alt{nodeFromInfo(a.Node), a.GoogleAPI, a.LatencyMs}
	}
	writeJSON(w, 200, out)
}

func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	list, err := s.core().Sources()
	if err != nil {
		fail(w, err)
		return
	}
	type src struct {
		Name        string     `json:"name"`
		Host        string     `json:"host"`
		Priority    int        `json:"priority"`
		Enabled     bool       `json:"enabled"`
		UserAdded   bool       `json:"user_added"`
		LastFetch   *time.Time `json:"last_fetch,omitempty"`
		LastOK      *time.Time `json:"last_ok,omitempty"`
		LastErr     string     `json:"last_error,omitempty"`
		LastCount   int        `json:"last_count"`
		Title       string     `json:"title,omitempty"`
		UpdateHours int        `json:"update_hours,omitempty"`
		UsedBytes   int64      `json:"used_bytes,omitempty"`
		TotalBytes  int64      `json:"total_bytes,omitempty"`
		Expire      *time.Time `json:"expire,omitempty"`
		Present     int        `json:"present"` // присутствующих узлов источника
		Checked     int        `json:"checked"` // из них проверялось за неделю
		Alive       int        `json:"alive"`   // из проверенных ответило хотя бы раз
		Rarely      bool       `json:"rarely,omitempty"`
	}
	out := make([]src, len(list))
	for i, x := range list {
		out[i] = src{x.Name, x.Host, x.Priority, x.Enabled, x.UserAdded, tp(x.LastFetch), tp(x.LastOK), x.LastErr, x.LastCount,
			x.Title, x.UpdateHours, x.UsedBytes, x.TotalBytes, tp(x.Expire), x.Present, x.Checked, x.Alive, x.Rarely}
	}
	writeJSON(w, 200, out)
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Mirror   string `json:"mirror"`
		Priority int    `json:"priority"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := s.core().AddSource(b.Name, b.URL, b.Mirror, b.Priority, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true})
}

func (s *Server) enableSource(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := s.core().SetSourceEnabled(r.PathValue("name"), b.On, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().RemoveSource(r.PathValue("name"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- цели ----

func (s *Server) targets(w http.ResponseWriter, r *http.Request) {
	list, err := s.core().Targets()
	if err != nil {
		fail(w, err)
		return
	}
	out := []targetDTO{}
	for _, t := range list {
		out = append(out, targetFrom(t))
	}
	writeJSON(w, 200, out)
}

func (s *Server) target(w http.ResponseWriter, r *http.Request) {
	t, err := s.core().TargetStatus(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, targetFrom(t))
}

func (s *Server) putTarget(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title       string            `json:"title"`
		Rules       store.TargetRules `json:"rules"`
		Check       store.TargetCheck `json:"check"`
		Policy      string            `json:"policy"`
		FailoverK   int               `json:"failover_k"`
		AllowDirect bool              `json:"allow_direct"`
		AllowWarp   bool              `json:"allow_warp"`
	}
	if !decode(w, r, &b) {
		return
	}
	t := store.Target{ID: r.PathValue("id"), Title: b.Title, Rules: b.Rules, Check: b.Check, Policy: b.Policy, FailoverK: b.FailoverK,
		AllowDirect: b.AllowDirect, AllowWarp: b.AllowWarp}
	if err := s.core().PutTarget(t, caller(r)); err != nil {
		fail(w, err)
		return
	}
	st, err := s.core().TargetStatus(t.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, targetFrom(st))
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.core().DeleteTarget(r.PathValue("id"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) probeURL(w http.ResponseWriter, r *http.Request) {
	var b struct {
		URL    string `json:"url"`
		TTLSec int    `json:"ttl_sec"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := s.core().SetProbeURL(r.PathValue("id"), b.URL, time.Duration(b.TTLSec)*time.Second, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) probeTarget(w http.ResponseWriter, r *http.Request) {
	t, err := s.core().TargetStatus(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if caller(r) != "user" && t.Owner != caller(r) {
		writeErr(w, http.StatusForbidden, "чужая цель")
		return
	}
	res, err := s.core().ProbeTarget(r.Context(), t.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, 200, resultFrom(res))
}

func (s *Server) switchTarget(w http.ResponseWriter, r *http.Request) {
	var b struct {
		To string `json:"to"` // NodeID, direct, warp или next
	}
	if !decode(w, r, &b) {
		return
	}
	if b.To == "" {
		writeErr(w, http.StatusBadRequest, "не указан узел (to)")
		return
	}
	if b.To != "next" && b.To != store.SelDirect && b.To != store.SelWarp {
		id, err := s.core().ResolveNode(b.To)
		if err != nil {
			fail(w, err)
			return
		}
		b.To = id
	}
	to, err := s.core().SwitchTarget(r.Context(), r.PathValue("id"), b.To, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "node": to})
}

// ---- настройки, лог, служебное ----

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	v, err := s.core().Settings()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// putTunApps: раздельное туннелирование (Android) одной записью: режим off | include | exclude и список пакетов. VPN перезапускается один
// раз; если перезапуск не удался, настройка всё равно сохранена, а причина приходит в поле restart_error.
func (s *Server) putTunApps(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		Mode string   `json:"mode"`
		Apps []string `json:"apps"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := s.core().SetTunApps(b.Mode, b.Apps, caller(r)); err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": true}
	if s.VPN != nil {
		if _, err := s.VPN.Reload(); err != nil {
			out["restart_error"] = err.Error()
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) putSetting(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := s.core().SetSetting(r.PathValue("key"), b.Value, caller(r)); err != nil {
		fail(w, err)
		return
	}
	if (strings.HasPrefix(r.PathValue("key"), "tun.") || r.PathValue("key") == core.SetFakeIP) && s.VPN != nil {
		_, _ = s.VPN.Reload() // список приложений читается при создании ядра; включённый VPN перезапускается, выключенный подхватит при включении
	}
	if r.PathValue("key") == core.SetProxyLAN && s.VPN != nil {
		_, _ = s.VPN.Restart() // адрес прослушивания читается при создании ядра
	}
	if strings.HasPrefix(r.PathValue("key"), "warp.") && s.VPN != nil {
		_, _ = s.VPN.Restart() // параметры WARP читаются при создании ядра, а WARP работает и при выключенном VPN (проверки узлов идут через него)
	}
	if r.PathValue("key") == core.SetKillswitch && s.VPN != nil {
		s.VPN.RefreshKillswitch() // правила появляются или исчезают сразу, без перезапуска ядра
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// killswitchRelease снимает правила киллсвитча, оставшиеся после падения демона: приложению без прав администратора (Windows) остаётся
// перезапустить службу и попросить демона. Как и остальные действия над настройками, доступно только пользователю с токеном.
func (s *Server) killswitchRelease(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if s.VPN == nil {
		fail(w, fmt.Errorf("%w: VPN недоступен", core.ErrInvalid))
		return
	}
	if err := s.VPN.ReleaseStaleKillswitch(); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	lines, err := s.core().Logs(qInt(r, "n", 200))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}

// appProxy отдаёт адрес и учётные данные app-входа (секрет: только владельцу).
func (s *Server) appProxy(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	addr, user, pass := s.core().AppProxy()
	share := s.core().ProxyShare()
	addrs := share.Addrs
	if addrs == nil {
		addrs = []string{}
	}
	writeJSON(w, 200, map[string]any{"addr": addr, "username": user, "password": pass, "lan": share.Enabled, "lan_addrs": addrs, "port": share.Port})
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	names, err := s.core().AppTokens()
	if err != nil {
		fail(w, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, 200, names)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	tok, err := s.core().CreateAppToken(strings.TrimSpace(b.Name), caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"name": b.Name, "token": tok})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().RevokeAppToken(r.PathValue("name"), caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) probeWarp(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().ProbeWarp(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, resultFrom(res))
}

// warpGet — состояние окна «Cloudflare WARP»: аккаунт, действующий и запомненный для сети профиль, ручные параметры.
func (s *Server) warpGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	writeJSON(w, 200, s.core().WarpState())
}

// warpScan подбирает профиль WARP под текущую сеть (долго: до минуты). apply — лучший сохранить и применить (ядро пересоздаётся).
func (s *Server) warpScan(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		Apply      bool `json:"apply"`
		Limit      int  `json:"limit"`
		StopAtGood int  `json:"stop_at_good"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	res, err := s.core().WarpScan(r.Context(), core.WarpScanOptions{Apply: b.Apply, Limit: b.Limit, StopAtGood: b.StopAtGood})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}

// warpParams задаёт ручные параметры WARP одной записью (тело: {"warp.endpoint": "...", "warp.port": "..."}), ядро пересоздаётся один раз.
func (s *Server) warpParams(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var vals map[string]string
	if !decode(w, r, &vals) {
		return
	}
	if err := s.core().SetWarpParams(vals); err != nil {
		fail(w, err)
		return
	}
	if s.VPN != nil {
		_, _ = s.VPN.Restart()
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// warpReset убирает ручные параметры WARP (умолчание) и пересоздаёт ядро.
func (s *Server) warpReset(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	s.core().ResetWarpProfile()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// otaSources забирает подписанный список источников у издателя и применяет его (то же, что раз в сутки делает ядро само).
func (s *Server) otaSources(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().UpdateOTASources(r.Context(), "user")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// registerWarp создаёт аккаунт WARP (прямо или через активный узел) и пересоздаёт ядро, чтобы WARP заработал. replace — заменить настроенный.
func (s *Server) registerWarp(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Replace bool `json:"replace"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in) // пустое тело — значения по умолчанию
	if err := s.core().RegisterWarp(r.Context(), in.Replace); err != nil {
		fail(w, err)
		return
	}
	s.finishWarpChange(w)
}

// importWarp принимает готовый аккаунт WARP (тело: {"account": {...}, "replace": bool}) и пересоздаёт ядро.
func (s *Server) importWarp(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Account json.RawMessage `json:"account"`
		Replace bool            `json:"replace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Account) == 0 {
		writeErr(w, http.StatusBadRequest, "нужно тело {\"account\": {...}}")
		return
	}
	if err := s.core().ImportWarp(in.Account, in.Replace); err != nil {
		fail(w, err)
		return
	}
	s.finishWarpChange(w)
}

// finishWarpChange: аккаунт записан — ядро пересоздаётся (даже при выключенном VPN: аккаунт читается при создании ядра).
func (s *Server) finishWarpChange(w http.ResponseWriter) {
	if s.VPN == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "restarted": false})
		return
	}
	if _, err := s.VPN.Restart(); err != nil {
		writeErr(w, http.StatusConflict, "аккаунт сохранён, но ядро не перезапустилось: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restarted": true})
}

func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if s.NotifyTest == nil {
		writeErr(w, http.StatusConflict, "уведомления отключены (флаг --no-notify)")
		return
	}
	if err := s.NotifyTest(); err != nil {
		writeErr(w, http.StatusInternalServerError, "уведомление не показано: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifications — долгий опрос уведомлений для приложения: ?after=N (последний полученный seq, 0 — с начала), ?wait=секунды (до 30).
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if s.Notices == nil {
		writeErr(w, http.StatusNotFound, "уведомления через приложение не используются на этой платформе")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	wait := time.Duration(min(qInt(r, "wait", 25), 30)) * time.Second
	next, ops := s.Notices.Poll(r.Context(), after, wait)
	if ops == nil {
		ops = []notify.Op{}
	}
	writeJSON(w, 200, map[string]any{"next": next, "ops": ops})
}

// notificationAction — нажатие кнопки уведомления в приложении.
func (s *Server) notificationAction(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if s.Notices == nil {
		writeErr(w, http.StatusNotFound, "уведомления через приложение не используются на этой платформе")
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 32)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "неверный id уведомления")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.Notices.Action(uint32(id), body.Key) {
		writeErr(w, http.StatusNotFound, "уведомление уже закрыто")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) updateRuleSets(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().UpdateRuleSets(r.Context(), caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	failed := map[string]string{}
	for k, v := range res.Failed {
		failed[k] = prober.Sanitize(v)
	}
	updated := res.Updated
	if updated == nil {
		updated = []string{}
	}
	writeJSON(w, 200, map[string]any{"updated": updated, "unchanged": res.Unchanged, "failed": failed})
}

func (s *Server) vpnGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if s.VPN == nil {
		writeErr(w, http.StatusConflict, "переключатель VPN недоступен")
		return
	}
	writeJSON(w, 200, s.VPN.State())
}

// vpnSet: {"on": true|false, "mode": "blocked|all|all-but-ru", "autoconnect": "last|always|off"}; любое из полей можно опустить.
// vpnSelfTest проверяет работающий туннель целиком (узел насквозь, DNS, внешний запрос через туннель, заблокированный сайт); ничего не меняет.
func (s *Server) vpnSelfTest(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	writeJSON(w, 200, s.core().VPNSelfTest(r.Context()))
}

// nodeCheck — сквозная проверка узла: запрос через сам узел до внешнего сайта, ответ с IP, страной и причиной провала (не просто «готово»).
func (s *Server) nodeCheck(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().CheckNodeE2E(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// activeCheck — то же для активного выхода: проверяется тот самый outbound, по которому идёт трафик.
func (s *Server) activeCheck(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().CheckNodeE2E(r.Context(), "active")
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// nodeUDP — замер UDP через узел (потери и задержка STUN-запросов): годится ли узел для звонков.
func (s *Server) nodeUDP(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().ProbeUDP(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// activeUDP — то же для активного выхода.
func (s *Server) activeUDP(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	res, err := s.core().ProbeUDP(r.Context(), "active")
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) vpnSet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if s.VPN == nil {
		writeErr(w, http.StatusConflict, "переключатель VPN недоступен")
		return
	}
	var in struct {
		On          *bool   `json:"on"`
		Mode        string  `json:"mode"`
		Autoconnect *string `json:"autoconnect"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !core.ValidMode(in.Mode) {
		writeErr(w, http.StatusBadRequest, "неизвестный режим (blocked | all | all-but-ru)")
		return
	}
	if in.Autoconnect != nil {
		if !supervisor.ValidAutoconnect(*in.Autoconnect) {
			writeErr(w, http.StatusBadRequest, "неизвестное автоподключение (last | always | off)")
			return
		}
		st, err := s.VPN.SetAutoconnect(*in.Autoconnect)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "state": st})
			return
		}
		if in.On == nil && in.Mode == "" {
			writeJSON(w, 200, st)
			return
		}
	}
	st, err := s.VPN.Set(in.On, in.Mode)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "state": st})
		return
	}
	writeJSON(w, 200, st)
}

// exitInfo: собрать сведения о выходе узла сейчас (страна, адрес, чистота); ответ — узел с заполненным exit.
func (s *Server) exitInfo(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	id, err := s.core().ResolveNode(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	n, err := s.core().ExitInfoNow(r.Context(), []string{id})
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	if n == 0 {
		writeErr(w, http.StatusConflict, "выход узла не определился (узел не отвечает)")
		return
	}
	v, err := s.core().NodeByID(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, nodeFromView(v))
}

// routeRulesGet: правила маршрутизации пользователя и список встроенных списков, доступных в условиях.
func (s *Server) routeRulesGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	rs, err := s.core().RouteRules()
	if err != nil {
		fail(w, err)
		return
	}
	if rs == nil {
		rs = []core.RouteRule{}
	}
	type setDTO struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	var sets []setDTO
	for _, x := range core.BuiltinRuleSets() {
		sets = append(sets, setDTO{x.Name, x.Role})
	}
	for _, x := range s.core().UserSets() { // свои списки по URL: выбираются в правилах так же, как встроенные
		sets = append(sets, setDTO{x.ID, "custom"})
	}
	// выходы правил (текущая привязка) и страны с проверенными узлами: для выбора в редакторе правила
	countries, _ := s.core().RouteCountries()
	if countries == nil {
		countries = []core.CountryInfo{}
	}
	writeJSON(w, 200, map[string]any{"rules": rs, "sets": sets, "via": s.core().RouteViaStates(), "countries": countries})
}

// routeRulesPut заменяет весь список правил (порядок = приоритет) и применяет: включённый VPN перезапускается.
func (s *Server) routeRulesPut(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Rules []core.RouteRule `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.core().PutRouteRules(in.Rules, caller(r)); err != nil {
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
	out["rules"] = rs
	writeJSON(w, 200, out)
}

// speed: замерить скорость скачивания через узел сейчас; ответ — узел с заполненной скоростью.
func (s *Server) speed(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	id, err := s.core().ResolveNode(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	n, err := s.core().SpeedNow(r.Context(), []string{id})
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fail(w, err)
		return
	}
	if n == 0 {
		writeErr(w, http.StatusConflict, "скорость не замерилась (узел не отвечает или не отдаёт данные)")
		return
	}
	v, err := s.core().NodeByID(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, nodeFromView(v))
}

type measureDTO struct {
	Running  bool       `json:"running"`
	Total    int        `json:"total"`
	Done     int        `json:"done"`
	Stage    string     `json:"stage,omitempty"` // ping | exit | speed
	OK       int        `json:"ok"`
	Canceled bool       `json:"canceled,omitempty"`
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
}

func measureFrom(st core.MeasureState) measureDTO {
	return measureDTO{Running: st.Running, Total: st.Total, Done: st.Done, Stage: st.Stage, OK: st.OK, Canceled: st.Canceled, Started: tp(st.Started), Finished: tp(st.Finished)}
}

// measureStart: пакетный замер выбранных узлов в фоне: {"ids":[…], "ping":bool, "exit":bool, "speed":bool}.
func (s *Server) measureStart(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		IDs   []string `json:"ids"`
		Ping  bool     `json:"ping"`
		Exit  bool     `json:"exit"`
		Speed bool     `json:"speed"`
	}
	if !decode(w, r, &in) {
		return
	}
	ids := make([]string, 0, len(in.IDs))
	for _, id := range in.IDs {
		full, err := s.core().ResolveNode(id)
		if err != nil {
			fail(w, err)
			return
		}
		ids = append(ids, full)
	}
	if err := s.core().StartMeasure(core.MeasureOptions{IDs: ids, Ping: in.Ping, Exit: in.Exit, Speed: in.Speed}); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, measureFrom(s.core().MeasureState()))
}

func (s *Server) measureGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	writeJSON(w, 200, measureFrom(s.core().MeasureState()))
}

func (s *Server) measureCancel(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	s.core().CancelMeasure()
	writeJSON(w, 200, measureFrom(s.core().MeasureState()))
}

// ownAdd: добавить свои серверы: {"text": "ссылки по одной в строке или готовый конфиг"}.
func (s *Server) ownAdd(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	rep, err := s.core().AddOwn(in.Text, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"added": rep.Added, "duplicates": rep.Duplicates, "failed": rep.Failed, "ids": rep.IDs})
}

func (s *Server) ownDelete(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	id, err := s.core().ResolveNode(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.core().RemoveOwn(id, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

type connDTO struct {
	ID        string     `json:"id"`
	Network   string     `json:"network"`
	Inbound   string     `json:"inbound"`
	Source    string     `json:"source"`
	Dest      string     `json:"dest"`
	Process   string     `json:"process,omitempty"`
	Route     string     `json:"route"` // direct | proxy | target | block | other
	RouteName string     `json:"route_name,omitempty"`
	Node      string     `json:"node"`
	Up        int64      `json:"up"`
	Down      int64      `json:"down"`
	Started   time.Time  `json:"started"`
	Closed    *time.Time `json:"closed,omitempty"`
}

// connections: открытые соединения (новые первыми); ?closed=1 добавляет недавно закрытые, ?limit=N.
func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	list := s.core().Connections(qBool(r, "closed"), qInt(r, "limit", 300))
	out := make([]connDTO, len(list))
	for i, c := range list {
		out[i] = connDTO{ID: c.ID, Network: c.Network, Inbound: c.Inbound, Source: c.Source, Dest: c.Dest, Process: c.Process, Route: c.Route,
			RouteName: c.RouteName, Node: c.Node, Up: c.Up, Down: c.Down, Started: c.Started, Closed: tp(c.Closed)}
	}
	writeJSON(w, 200, out)
}

func (s *Server) closeConnection(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if !s.core().CloseConnection(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "соединение не найдено (уже закрыто)")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// traffic: скорость по секундам за последние n секунд (по умолчанию 120) и суммарные счётчики.
func (s *Server) traffic(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	st := s.core().Traffic(qInt(r, "n", 120))
	type sample struct {
		At   int64 `json:"t"` // unix-секунды
		Up   int64 `json:"up"`
		Down int64 `json:"down"`
	}
	samples := make([]sample, len(st.Samples))
	for i, x := range st.Samples {
		samples[i] = sample{x.At.Unix(), x.Up, x.Down}
	}
	writeJSON(w, 200, map[string]any{"samples": samples, "up_now": st.UpNow, "down_now": st.DownNow, "up_total": st.UpTotal, "down_total": st.DownTotal, "conns": st.Conns})
}

// nodeLink: ссылка «поделиться» для узла (с секретами — только владельцу).
func (s *Server) nodeLink(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	link, err := s.core().NodeLink(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"link": link})
}

// ownEdit: изменить собственный сервер: {"label", "server", "port"} (любое поле можно опустить) или {"link": "новая ссылка"} — замена целиком.
func (s *Server) ownEdit(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Label  *string `json:"label"`
		Server *string `json:"server"`
		Port   *int    `json:"port"`
		Link   *string `json:"link"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := s.core().ResolveNode(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	newID := id
	if in.Link != nil && strings.TrimSpace(*in.Link) != "" {
		if newID, err = s.core().ReplaceOwn(id, *in.Link, caller(r)); err != nil {
			fail(w, err)
			return
		}
	}
	if err := s.core().EditOwn(newID, core.EditOwnReq{Label: in.Label, Server: in.Server, Port: in.Port}, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": newID})
}

// nodeLabel: пользовательское название любого узла: {"label": "…"} (пустое — снять).
func (s *Server) nodeLabel(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := s.core().ResolveNode(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.core().SetNodeLabel(id, in.Label, caller(r)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// connectBest: сделать активным лучший живой узел (чистота, YouTube, скорость, пинг; свои — с бонусом).
func (s *Server) connectBest(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	v, err := s.core().ConnectBest(caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, nodeFromView(v))
}

// trafficDaily: трафик по дням (всего и через VPN), ?days=N (по умолчанию 30).
func (s *Server) trafficDaily(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	days, err := s.core().DailyTraffic(qInt(r, "days", 30))
	if err != nil {
		fail(w, err)
		return
	}
	type day struct {
		Day     string `json:"day"`
		Up      int64  `json:"up"`
		Down    int64  `json:"down"`
		VPNUp   int64  `json:"vpn_up"`
		VPNDown int64  `json:"vpn_down"`
	}
	out := make([]day, len(days))
	for i, d := range days {
		out[i] = day{d.Day, d.Up, d.Down, d.VPNUp, d.VPNDown}
	}
	writeJSON(w, 200, out)
}

// diagnostics: «что видят сайты» — внешний адрес, страна, IPv6, DNS-резолвер и выводы; выполняется по запросу (несколько секунд).
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	d := s.core().Diagnostics(r.Context())
	type finding struct {
		Level  string `json:"level"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	fs := make([]finding, len(d.Findings))
	for i, f := range d.Findings {
		fs[i] = finding{f.Level, f.Title, f.Detail}
	}
	type geo struct {
		Name     string `json:"name"`
		Country  string `json:"country,omitempty"`
		City     string `json:"city,omitempty"`
		Timezone string `json:"timezone,omitempty"`
		Org      string `json:"org,omitempty"`
		Hosting  *bool  `json:"hosting,omitempty"`
		Proxy    *bool  `json:"proxy,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	gs := make([]geo, len(d.Geo))
	for i, g := range d.Geo {
		gs[i] = geo{g.Name, g.Country, g.City, g.Timezone, g.Org, g.Hosting, g.Proxy, g.Err}
	}
	writeJSON(w, 200, map[string]any{"at": d.At, "vpn_on": d.VPNOn, "mode": d.Mode, "ip": d.IP, "country": d.Country, "colo": d.Colo, "warp": d.WarpOn,
		"ipv6": d.IPv6, "resolver": d.Resolver, "resolver_country": d.ResolverCountry, "exit_ip": d.ExitIP, "geo": gs, "findings": fs, "took_ms": d.TookMs})
}

// backupGet: резервная копия (правила, настройки, собственные серверы с параметрами подключения — секрет, только владельцу).
func (s *Server) backupGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	b, err := s.core().ExportBackup()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, b)
}

// backupPost: восстановить из копии (тело — то, что отдал GET /v1/backup), с перезапуском VPN, если он включён (правила применяются при запуске движка).
func (s *Server) backupPost(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b core.Backup
	if !decode(w, r, &b) {
		return
	}
	rep, err := s.core().ImportBackup(b, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"settings": rep.Settings, "rules": rep.Rules, "own": rep.Own, "labels": rep.Labels, "skipped": rep.Skipped}
	if rep.Rules > 0 && s.VPN != nil {
		if _, err := s.VPN.Reload(); err != nil {
			out["reload_error"] = err.Error()
		}
	}
	writeJSON(w, 200, out)
}

// ---- прокси для Telegram (вкладка «Прокси») ----

// tgList: GET /v1/tg-proxies?alive=1&fav=1&kind=mtproto|socks5|webproxy&limit=N
func (s *Server) tgList(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "mtproto" && kind != "socks5" && kind != "webproxy" {
		writeErr(w, http.StatusBadRequest, "kind: mtproto, socks5 или webproxy")
		return
	}
	l, err := s.core().TGProxies(qBool(r, "alive"), qBool(r, "fav"), kind, qInt(r, "limit", 300))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, l)
}

// tgAdd: POST /v1/tg-proxies {"text": "...ссылки tg://proxy… / t.me/proxy… / tg://socks…..."}
func (s *Server) tgAdd(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	n, err := s.core().AddTGProxies(in.Text, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"added": n})
}

// tgCheck: POST /v1/tg-proxies/check {"limit": N} — проверить пачку сейчас (ждёт окончания).
func (s *Server) tgCheck(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Limit int `json:"limit"`
	}
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	rep, err := s.core().CheckTGProxies(r.Context(), in.Limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) tgDelete(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().DeleteTGProxy(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) tgFavorite(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.core().SetTGFavorite(r.PathValue("id"), in.On); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// uiState принимает от интерфейса, виден ли он на экране. Только владелец: чужое приложение не должно будить и усыплять ядро.
func (s *Server) uiState(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Visible bool `json:"visible"`
	}
	if !decode(w, r, &in) {
		return
	}
	uiVisible.Store(in.Visible)
	w.WriteHeader(http.StatusNoContent)
}

// reportStart запускает диагностический отчёт в фоне (десятки секунд): состояние и итог — GET /v1/report.
func (s *Server) reportStart(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	if err := s.core().StartReport(context.Background()); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"state": "running"})
}

// reportGet отдаёт состояние отчёта; готовый приходит в report (структура) и text (то же читаемым текстом).
func (s *Server) reportGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	st := s.core().ReportStatus()
	out := map[string]any{"state": st.State, "stage": st.Stage}
	if st.Error != "" {
		out["error"] = st.Error
	}
	if st.Report != nil {
		out["report"] = st.Report
		out["text"] = st.Report.Text()
	}
	writeJSON(w, 200, out)
}

// chainAdd создаёт цепочку прокси из узлов (тело: {"name": "…", "hops": [id, id, …]}, от первого звена к выходу); возвращает её id.
func (s *Server) chainAdd(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var in struct {
		Name string   `json:"name"`
		Hops []string `json:"hops"`
	}
	if !decode(w, r, &in) {
		return
	}
	hops := make([]string, len(in.Hops))
	for i, h := range in.Hops {
		id, err := s.core().ResolveNode(h) // допускается короткий префикс идентификатора
		if err != nil {
			fail(w, err)
			return
		}
		hops[i] = id
	}
	id, err := s.core().AddChain(in.Name, hops, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}
