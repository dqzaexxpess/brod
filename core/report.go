package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"brod/buildinfo"
	"brod/dnsfallback"
	"brod/prober"
)

// Диагностический отчёт: один вызов собирает то, чего не хватало при разборе жалоб («живы только WireGuard», «страна не определена»):
// какие протоколы живы и почему падают остальные, как отвечает DNS (системный, DoH, через WARP), есть ли прямая связь, отвечает ли WARP,
// и выборочные пробы узлов напрямую и через WARP. Адреса, имена узлов и секреты в отчёт не попадают (prober.Sanitize, имена только как «узел-N»).

var reHexID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b[0-9a-f]{32,}\b`)

// reportMask чистит текст ошибки для отчёта: имена узлов заменяются метками, затем адреса и URL (prober.Sanitize), затем идентификаторы.
// В ошибках проб и соединений остаётся «host:443», которое Sanitize не знает, поэтому настоящие имена убираются по списку.
func reportMask(s string, names map[string]string) string {
	for real, label := range names {
		if real != "" {
			s = strings.ReplaceAll(s, real, label)
		}
	}
	return reHexID.ReplaceAllString(prober.Sanitize(s), "<id>")
}

// ReportTimeout — потолок всего отчёта.
const ReportTimeout = 100 * time.Second

// reportWellKnown — известные имена для проверки DNS.
var reportWellKnown = []string{"cloudflare.com", "www.google.com", "www.youtube.com", "telegram.org", "github.com", "wikipedia.org"}

type ReportDNS struct {
	Resolver string `json:"resolver"` // system | doh-1.1.1.1 | doh-8.8.8.8 | go-resolver | via-warp
	Name     string `json:"name"`     // известное имя или «узел-N» (имена узлов не раскрываются)
	OK       bool   `json:"ok"`
	Rcode    string `json:"rcode,omitempty"`  // NOERROR, NXDOMAIN, SERVFAIL, REFUSED…
	Answer   string `json:"answer,omitempty"` // public | private | loopback | bogon | none
	Ms       int64  `json:"ms"`
	Err      string `json:"err,omitempty"`
}

type ReportConn struct {
	Test string `json:"test"`
	OK   bool   `json:"ok"`
	Ms   int64  `json:"ms"`
	Err  string `json:"err,omitempty"`
}

type ReportSample struct {
	Proto  string `json:"proto"`
	Domain bool   `json:"domain"` // адрес узла — имя (нужен DNS)
	Mode   string `json:"mode"`   // direct | via-warp
	OK     bool   `json:"ok"`
	Class  string `json:"class,omitempty"`
	Ms     int64  `json:"ms"`
	Detail string `json:"detail,omitempty"`
}

type ReportNodes struct {
	Proto    string         `json:"proto"`
	Total    int            `json:"total"`
	Present  int            `json:"present"`
	Domain   int            `json:"domain"`
	Checked  map[string]int `json:"checked"`  // режим (direct|via-warp) → узлов с пробами L2 за 6 часов
	Alive    map[string]int `json:"alive"`    // режим → узлов, у которых хоть одна проба L2 удалась
	Failures map[string]int `json:"failures"` // «режим/класс» → число проб-провалов L2
}

type ReportSource struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	OK      bool   `json:"ok"`
	Count   int    `json:"count"`
	Alive   int    `json:"alive"`
	Err     string `json:"err,omitempty"`
	AgeMin  int64  `json:"age_min"` // минут с последней удачной загрузки; -1 — не было
}

type ReportWarp struct {
	Enabled    bool   `json:"enabled"`
	ViaWarp    bool   `json:"via_warp"`
	Down       bool   `json:"down"`
	LastOK     bool   `json:"last_ok"`
	Profile    string `json:"profile"`
	Remembered string `json:"remembered,omitempty"`
	ProbeOK    bool   `json:"probe_ok"`
	ProbeClass string `json:"probe_class,omitempty"`
	ProbeMs    int64  `json:"probe_ms"`
}

type Report struct {
	Generated time.Time         `json:"generated"`
	Version   string            `json:"version"`
	OS        string            `json:"os"`
	NetKind   string            `json:"net_kind"` // вид сети без адресов (wifi, cell…)
	TookMs    int64             `json:"took_ms"`
	Settings  map[string]string `json:"settings"`
	Warp      ReportWarp        `json:"warp"`
	Countries struct {
		Known int `json:"known"`
		Total int `json:"total"` // узлов с записью о выходе
	} `json:"countries"`
	Nodes    []ReportNodes  `json:"nodes"`
	Sources  []ReportSource `json:"sources"`
	DNS      []ReportDNS    `json:"dns"`
	Conn     []ReportConn   `json:"conn"`
	Samples  []ReportSample `json:"samples"`
	Findings []string       `json:"findings"`
	Partial  []string       `json:"partial,omitempty"` // этапы, не уложившиеся в потолок или упавшие

	mask   map[string]string // настоящие имена узлов → метки; наружу не выходит
	maskMu sync.Mutex
}

func (r *Report) addMask(real, label string) {
	r.maskMu.Lock()
	if r.mask == nil {
		r.mask = map[string]string{}
	}
	r.mask[real] = label
	r.maskMu.Unlock()
}

// scrub: последний проход по всем текстам ошибок. Имена, попавшие в сообщения (host:443 и т. п.), заменяются метками, адреса и идентификаторы чистятся.
func (r *Report) scrub() {
	r.maskMu.Lock()
	m := r.mask
	r.maskMu.Unlock()
	for i := range r.DNS {
		r.DNS[i].Err = reportMask(r.DNS[i].Err, m)
	}
	for i := range r.Conn {
		r.Conn[i].Err = reportMask(r.Conn[i].Err, m)
	}
	for i := range r.Samples {
		r.Samples[i].Detail = reportMask(r.Samples[i].Detail, m)
	}
	for i := range r.Sources {
		r.Sources[i].Err = reportMask(r.Sources[i].Err, m)
	}
	for i := range r.Partial {
		r.Partial[i] = reportMask(r.Partial[i], m)
	}
}

// ReportState — состояние асинхронного отчёта (запуск идёт десятки секунд, HTTP-запрос столько не держится).
type ReportState struct {
	State  string  `json:"state"` // idle | running | done | failed
	Stage  string  `json:"stage"` // что выполняется сейчас
	Error  string  `json:"error,omitempty"`
	Report *Report `json:"report,omitempty"`
}

var reportRun struct {
	sync.Mutex
	st ReportState
}

// StartReport запускает отчёт в фоне; повторный вызов во время работы возвращает ErrBusy.
func (c *Core) StartReport(parent context.Context) error {
	reportRun.Lock()
	if reportRun.st.State == "running" {
		reportRun.Unlock()
		return ErrBusy
	}
	reportRun.st = ReportState{State: "running", Stage: "начало"}
	reportRun.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(parent, ReportTimeout)
		defer cancel()
		r, err := c.BuildReport(ctx, func(s string) {
			reportRun.Lock()
			reportRun.st.Stage = s
			reportRun.Unlock()
		})
		reportRun.Lock()
		defer reportRun.Unlock()
		if err != nil {
			reportRun.st = ReportState{State: "failed", Error: err.Error()}
			return
		}
		reportRun.st = ReportState{State: "done", Report: r}
	}()
	return nil
}

// ReportStatus возвращает текущее состояние отчёта.
func (c *Core) ReportStatus() ReportState {
	reportRun.Lock()
	defer reportRun.Unlock()
	if reportRun.st.State == "" {
		return ReportState{State: "idle"}
	}
	return reportRun.st
}

// BuildReport собирает отчёт. Этапы независимы: сбой одного не отменяет остальные, он записывается в Partial.
func (c *Core) BuildReport(ctx context.Context, stage func(string)) (*Report, error) {
	if stage == nil {
		stage = func(string) {}
	}
	start := time.Now()
	r := &Report{Generated: start.UTC(), Version: buildinfo.Version, OS: runtime.GOOS + "/" + runtime.GOARCH, Settings: map[string]string{}}
	netID := c.cfg.NetID()
	r.NetKind = netKind(netID)
	for _, k := range []string{SetViaWarp, SetWarpAuto, SetWarpEndpoint, SetWarpPort, SetWarpMask, SetWarpMTU, "update.check", "ota.sources"} {
		if v, ok, _ := c.st.Setting(k); ok {
			r.Settings[k] = v
		}
	}

	stage("WARP")
	ws := c.WarpState()
	r.Warp = ReportWarp{Enabled: ws.Enabled, ViaWarp: c.viaWarp(), Down: ws.Down, LastOK: c.warpLastOK.Load(), Profile: ws.Current.String()}
	if ws.Remembered != nil {
		r.Warp.Remembered = ws.Remembered.String()
	}
	if ws.Enabled {
		if pr, err := c.ProbeWarp(ctx); err == nil {
			r.Warp.ProbeOK, r.Warp.ProbeClass, r.Warp.ProbeMs = pr.OK, string(pr.Class), pr.LatencyMs
		} else {
			r.Partial = append(r.Partial, "проба WARP: "+prober.Sanitize(err.Error()))
		}
	}

	stage("узлы и проверки")
	c.reportNodes(r, netID)
	c.reportSources(r)

	// у каждого этапа свой потолок: в сети, где всё молчит, DNS и связь иначе съели бы время пробам узлов, а они нужнее всего
	budget := func(d time.Duration, f func(context.Context)) {
		sctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		f(sctx)
	}
	stage("пробы узлов")
	budget(45*time.Second, func(x context.Context) { c.reportSamples(x, r) })
	stage("DNS")
	budget(25*time.Second, func(x context.Context) { c.reportDNS(x, r) })
	stage("связь")
	budget(15*time.Second, func(x context.Context) { c.reportConn(x, r) })

	r.scrub()
	r.Findings = reportFindings(r)
	r.TookMs = time.Since(start).Milliseconds()
	if ctx.Err() != nil {
		r.Partial = append(r.Partial, "время отчёта вышло: часть проб не выполнена")
	}
	return r, nil
}

// netKind: «wifi», «cell»… — часть идентификатора сети до первого двоеточия (дальше идёт адрес шлюза).
func netKind(netID string) string {
	if i := strings.IndexByte(netID, ':'); i > 0 {
		return netID[:i]
	}
	if netID == "" {
		return "unknown"
	}
	return netID
}

func protoOfClass(cls string) string {
	if i := strings.IndexByte(cls, '/'); i > 0 {
		return cls[:i]
	}
	return cls
}

func (c *Core) reportNodes(r *Report, netID string) {
	counts, err := c.st.NodeCounts()
	if err != nil {
		r.Partial = append(r.Partial, "число узлов: "+err.Error())
		return
	}
	by := map[string]*ReportNodes{}
	for _, p := range counts {
		n := &ReportNodes{Proto: p.Proto, Total: p.Total, Present: p.Present, Domain: p.Domain, Checked: map[string]int{}, Alive: map[string]int{}, Failures: map[string]int{}}
		by[p.Proto] = n
	}
	aggs, err := c.st.ProbeAggregates(c.cfg.Now().Add(-6 * time.Hour))
	if err != nil {
		r.Partial = append(r.Partial, "сводка проверок: "+err.Error())
	}
	for _, a := range aggs {
		mode := ""
		switch a.Net {
		case netID:
			mode = "direct"
		case netID + "|warp":
			mode = "via-warp"
		default:
			continue // другие сети в отчёт не идут
		}
		if a.Level != 2 {
			continue
		}
		n := by[protoOfClass(a.Cls)]
		if n == nil {
			continue
		}
		if a.OK {
			n.Alive[mode] += a.Nodes
		} else {
			n.Failures[mode+"/"+a.Class] += a.Probes
		}
		n.Checked[mode] += a.Nodes // узел с успехом и провалом посчитается дважды: это верхняя оценка
	}
	for _, p := range counts {
		r.Nodes = append(r.Nodes, *by[p.Proto])
	}
	// страны выходов
	if ex, err := c.st.ExitInfos(); err == nil {
		for _, e := range ex {
			r.Countries.Total++
			if len(e.Country) == 2 && e.Country != "XX" {
				r.Countries.Known++
			}
		}
	}
}

func (c *Core) reportSources(r *Report) {
	srcs, err := c.Sources()
	if err != nil {
		r.Partial = append(r.Partial, "источники: "+err.Error())
		return
	}
	now := c.cfg.Now()
	for i, s := range srcs {
		name := s.Name
		if s.UserAdded {
			name = fmt.Sprintf("свой-%d", i+1) // имя своего источника может быть личным
		}
		age := int64(-1)
		if !s.LastOK.IsZero() {
			age = int64(now.Sub(s.LastOK).Minutes())
		}
		r.Sources = append(r.Sources, ReportSource{Name: name, Enabled: s.Enabled, OK: s.LastErr == "" && !s.LastOK.IsZero(), Count: s.LastCount, Alive: s.Alive, Err: prober.Sanitize(s.LastErr), AgeMin: age})
	}
}

// ---- DNS ----

func rcodeName(rc dnsmessage.RCode) string {
	switch rc {
	case dnsmessage.RCodeSuccess:
		return "NOERROR"
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	}
	return fmt.Sprintf("RCODE%d", rc)
}

// ipClass: к какому диапазону относится адрес из ответа DNS. Подменённый DNS часто отдаёт частные, loopback или нулевые адреса.
func ipClass(ip net.IP) string {
	switch {
	case ip == nil:
		return "none"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsUnspecified():
		return "bogon"
	case ip.IsPrivate() || ip.IsLinkLocalUnicast():
		return "private"
	case ip.To4() != nil && (ip.To4()[0] >= 240 || ip.To4()[0] == 0 || (ip.To4()[0] == 100 && ip.To4()[1]&0xC0 == 64)):
		return "bogon"
	}
	return "public"
}

func dnsQuery(name string) ([]byte, error) {
	n, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, err
	}
	m := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: uint16(time.Now().UnixNano()), RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	return m.Pack()
}

// parseDNS разбирает ответ: код возврата и класс первого адреса A.
func parseDNS(b []byte) (rcode, answer string, err error) {
	var m dnsmessage.Message
	if err = m.Unpack(b); err != nil {
		return "", "", err
	}
	rcode, answer = rcodeName(m.Header.RCode), "none"
	for _, a := range m.Answers {
		if a.Header.Type == dnsmessage.TypeA {
			if ar, ok := a.Body.(*dnsmessage.AResource); ok {
				answer = ipClass(net.IP(ar.A[:]))
				break
			}
		}
	}
	return rcode, answer, nil
}

func udpDNS(ctx context.Context, server, name string) ReportDNS {
	out := ReportDNS{Resolver: "system", Name: name}
	t0 := time.Now()
	q, err := dnsQuery(name)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	var d net.Dialer
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := d.DialContext(cctx, "udp", net.JoinHostPort(server, "53"))
	if err != nil {
		out.Err = prober.Sanitize(err.Error())
		return out
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write(q); err != nil {
		out.Err = prober.Sanitize(err.Error())
		return out
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	out.Ms = time.Since(t0).Milliseconds()
	if err != nil {
		out.Err = "нет ответа за 3 с"
		return out
	}
	out.Rcode, out.Answer, err = parseDNS(buf[:n])
	if err != nil {
		out.Err = "ответ не разобран"
		return out
	}
	out.OK = out.Rcode == "NOERROR" && out.Answer != "none"
	return out
}

func dohDNS(ctx context.Context, url, label, name string) ReportDNS {
	out := ReportDNS{Resolver: label, Name: name}
	t0 := time.Now()
	q, err := dnsQuery(name)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(q))
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	hc := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := hc.Do(req)
	out.Ms = time.Since(t0).Milliseconds()
	if err != nil {
		out.Err = prober.Sanitize(err.Error())
		return out
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != 200 {
		out.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return out
	}
	if out.Rcode, out.Answer, err = parseDNS(b); err != nil {
		out.Err = "ответ не разобран"
		return out
	}
	out.OK = out.Rcode == "NOERROR" && out.Answer != "none"
	return out
}

func (c *Core) reportDNS(ctx context.Context, r *Report) {
	names := append([]string(nil), reportWellKnown...)
	nodeNames := map[string]string{} // реальное имя → метка «узел-N»
	if ds, err := c.st.SampleDomains(6); err == nil {
		for i, d := range ds {
			l := fmt.Sprintf("узел-%d", i+1)
			nodeNames[l] = d
			r.addMask(d, l)
		}
	}
	type job struct {
		label, name string
	}
	var jobs []job
	for _, n := range names {
		jobs = append(jobs, job{n, n})
	}
	labels := make([]string, 0, len(nodeNames))
	for l := range nodeNames {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		jobs = append(jobs, job{l, nodeNames[l]})
	}
	sys := ""
	if SystemDNS != nil {
		sys = SystemDNS()
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	add := func(x ReportDNS) { mu.Lock(); r.DNS = append(r.DNS, x); mu.Unlock() }
	for _, j := range jobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if ctx.Err() != nil {
				return
			}
			if sys != "" {
				x := udpDNS(ctx, sys, j.name)
				x.Name = j.label
				add(x)
			}
			for _, d := range []struct{ url, label string }{{"https://1.1.1.1/dns-query", "doh-1.1.1.1"}, {"https://8.8.8.8/dns-query", "doh-8.8.8.8"}} {
				x := dohDNS(ctx, d.url, d.label, j.name)
				x.Name = j.label
				add(x)
			}
			// резолвер Go (на Android — с запасным путём через DoH): показывает, что видит остальная часть программы
			t0 := time.Now()
			rc, cancel := context.WithTimeout(ctx, 4*time.Second)
			ips, err := net.DefaultResolver.LookupHost(rc, j.name)
			cancel()
			g := ReportDNS{Resolver: "go-resolver", Name: j.label, Ms: time.Since(t0).Milliseconds(), OK: err == nil && len(ips) > 0}
			if err != nil {
				g.Err = prober.Sanitize(err.Error())
			} else if len(ips) > 0 {
				g.Answer = ipClass(net.ParseIP(ips[0]))
			}
			add(g)
			// имя через WARP: резолвится на его стороне
			if c.warpOn && !c.warpDown.Load() {
				if p, err := c.eng.Existing(OutWarp); err == nil {
					t1 := time.Now()
					dc, dcancel := context.WithTimeout(ctx, 5*time.Second)
					cn, derr := p.DialContext(dc, "tcp", net.JoinHostPort(j.name, "443"))
					dcancel()
					w := ReportDNS{Resolver: "via-warp", Name: j.label, Ms: time.Since(t1).Milliseconds(), OK: derr == nil}
					if derr != nil {
						w.Err = prober.Sanitize(derr.Error())
					} else {
						cn.Close()
					}
					add(w)
				}
			}
		}()
	}
	wg.Wait()
	sort.SliceStable(r.DNS, func(i, j int) bool {
		if r.DNS[i].Resolver != r.DNS[j].Resolver {
			return r.DNS[i].Resolver < r.DNS[j].Resolver
		}
		return r.DNS[i].Name < r.DNS[j].Name
	})
	if sys == "" {
		r.Partial = append(r.Partial, "системный DNS не определён: этот этап пропущен")
	}
	_ = dnsfallback.DefaultDoH // DoH-серверы отчёта те же, что запасные у программы
}

// ---- связь ----

func (c *Core) reportConn(ctx context.Context, r *Report) {
	type tcp struct{ label, addr string }
	tests := []tcp{{"tcp 1.1.1.1:443", "1.1.1.1:443"}, {"tcp 8.8.8.8:443", "8.8.8.8:443"}, {"tcp 9.9.9.9:443", "9.9.9.9:443"}, {"tcp ipv6 cloudflare", "[2606:4700:4700::1111]:443"}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	add := func(x ReportConn) { mu.Lock(); r.Conn = append(r.Conn, x); mu.Unlock() }
	for _, t := range tests {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			var d net.Dialer
			cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			cn, err := d.DialContext(cctx, "tcp", t.addr)
			x := ReportConn{Test: t.label, OK: err == nil, Ms: time.Since(t0).Milliseconds()}
			if err != nil {
				x.Err = prober.Sanitize(err.Error())
			} else {
				cn.Close()
			}
			add(x)
		}()
	}
	for _, u := range []string{"https://www.gstatic.com/generate_204", "https://www.google.com/generate_204", "https://cloudflare.com/cdn-cgi/trace", "https://api.telegram.org"} {
		u := u
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
			hc := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
			resp, err := hc.Do(req)
			x := ReportConn{Test: "https " + strings.TrimPrefix(strings.SplitN(u, "/", 4)[2], "www."), Ms: time.Since(t0).Milliseconds()}
			if err != nil {
				x.Err = prober.Sanitize(err.Error())
			} else {
				x.OK = true
				x.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
				resp.Body.Close()
			}
			add(x)
		}()
	}
	wg.Wait()
	sort.SliceStable(r.Conn, func(i, j int) bool { return r.Conn[i].Test < r.Conn[j].Test })
}

// ---- выборочные пробы узлов ----

func (c *Core) reportSamples(ctx context.Context, r *Report) {
	nodes, err := c.st.SampleNodes(3, 6)
	if err != nil {
		r.Partial = append(r.Partial, "выборка узлов: "+err.Error())
		return
	}
	modes := []string{"direct"}
	if c.warpOn && !c.warpDown.Load() {
		modes = append(modes, "via-warp")
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, n := range nodes {
		for _, mode := range modes {
			n, mode := n, mode
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				if ctx.Err() != nil {
					return
				}
				res, server := c.reportProbe(ctx, n.ID, mode == "via-warp")
				if server != "" {
					r.addMask(server, "<узел>")
				}
				s := ReportSample{Proto: n.Proto, Domain: n.Domain, Mode: mode, OK: res.OK, Class: string(res.Class), Ms: res.LatencyMs, Detail: res.Detail}
				mu.Lock()
				r.Samples = append(r.Samples, s)
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	sort.SliceStable(r.Samples, func(i, j int) bool {
		a, b := r.Samples[i], r.Samples[j]
		if a.Proto != b.Proto {
			return a.Proto < b.Proto
		}
		return a.Mode < b.Mode
	})
}

// reportProbe — проба L2 одного узла тем же кодом, что и обычная проверка (outboundJSON, пробный outbound движка). Вторым значением идёт адрес
// сервера узла: отчёт вырезает его из текстов ошибок.
func (c *Core) reportProbe(ctx context.Context, id string, viaWarp bool) (prober.Result, string) {
	full, err := c.st.Node(id)
	if err != nil {
		return prober.Result{Level: 2, Class: prober.ClassOther, Detail: "узел исчез из базы"}, ""
	}
	opts := full.Opts
	if viaWarp {
		if opts, err = outboundJSON(full, "probe", true); err != nil {
			return prober.Result{Level: 2, Class: prober.ClassOther, Detail: "опции узла"}, full.Server
		}
	}
	p, err := c.eng.NewProbe(opts)
	if err != nil {
		return prober.Result{Level: 2, Class: prober.ClassOther, Detail: "не создаётся: " + err.Error()}, full.Server
	}
	defer p.Close()
	cctx, cancel := context.WithTimeout(ctx, c.cfg.CheckTimeout)
	defer cancel()
	res := prober.L2(cctx, p)
	if ctx.Err() != nil && !res.OK { // этап вышел за потолок: проба не доведена до конца, провалом она не считается
		res.Class = prober.ClassCanceled
		res.Detail = "не успела за время отчёта"
	}
	return res, full.Server
}

// ---- выводы ----

func ratio(ok, total int) float64 {
	if total == 0 {
		return -1
	}
	return float64(ok) / float64(total)
}

// reportFindings: короткие выводы по правилам. Это подсказки для разбора, а не вердикт: исходные данные идут рядом.
func reportFindings(r *Report) []string {
	var out []string
	count := func(resolver string, node bool) (ok, total int, bad int) {
		for _, d := range r.DNS {
			if d.Resolver != resolver || strings.HasPrefix(d.Name, "узел-") != node {
				continue
			}
			total++
			if d.OK {
				ok++
			}
			if d.Answer == "private" || d.Answer == "loopback" || d.Answer == "bogon" {
				bad++
			}
		}
		return
	}
	sysOK, sysTotal, sysBad := count("system", false)
	dohOK, dohTotal, _ := count("doh-1.1.1.1", false)
	nodeOK, nodeTotal, _ := count("system", true)
	switch {
	case sysTotal > 0 && sysBad > 0:
		out = append(out, "Системный DNS отдаёт подменённые адреса (частные, loopback или нулевые) на известные имена: DNS подменяется.")
	case sysTotal > 0 && ratio(sysOK, sysTotal) < 0.5 && ratio(dohOK, dohTotal) >= 0.5:
		out = append(out, fmt.Sprintf("Системный DNS не отвечает нормально на известные имена (%d из %d), а DoH работает: DNS сети режется или ломается.", sysOK, sysTotal))
	}
	if nodeTotal > 0 && ratio(nodeOK, nodeTotal) < 0.5 && ratio(sysOK, sysTotal) >= 0.5 {
		out = append(out, fmt.Sprintf("Имена узлов системным DNS не резолвятся (%d из %d), а известные имена резолвятся: узлы с доменом будут мёртвы, живы только узлы с IP.", nodeOK, nodeTotal))
	}
	if wOK, wTotal, _ := count("via-warp", false); wTotal > 0 && ratio(wOK, wTotal) < 0.5 {
		out = append(out, "Через WARP известные имена не открываются: цепочка через WARP не работает.")
	}
	tcpOK, tcpTotal := 0, 0
	for _, c := range r.Conn {
		if strings.HasPrefix(c.Test, "tcp ") && !strings.Contains(c.Test, "ipv6") {
			tcpTotal++
			if c.OK {
				tcpOK++
			}
		}
	}
	if tcpTotal > 0 && tcpOK == 0 {
		out = append(out, "Нет прямого TCP до публичных адресов (1.1.1.1, 8.8.8.8, 9.9.9.9 на 443): прямой интернет в этой сети ограничен или программе закрыт доступ к сети.")
	}
	if r.Warp.Enabled && !r.Warp.ProbeOK {
		out = append(out, fmt.Sprintf("WARP не ответил на пробу (%s): узлы будут проверяться без цепочки, живых будет очень мало.", r.Warp.ProbeClass))
	}
	if !r.Warp.Enabled {
		out = append(out, "Аккаунт WARP не настроен: проверки узлов идут напрямую.")
	}
	// выборочные пробы: кто жив
	alive, dead := map[string]bool{}, map[string]bool{}
	for _, s := range r.Samples {
		if s.Class == string(prober.ClassCanceled) {
			continue // не доведена до конца: не доказательство ни за, ни против
		}
		if s.OK {
			alive[s.Proto] = true
		} else {
			dead[s.Proto] = true
		}
	}
	var onlyAlive []string
	for p := range alive {
		onlyAlive = append(onlyAlive, p)
	}
	sort.Strings(onlyAlive)
	if len(alive) > 0 && len(alive) <= 1 && len(dead) >= 2 {
		out = append(out, fmt.Sprintf("В выборочных пробах отвечает только протокол %s, остальные (%d) нет.", strings.Join(onlyAlive, ", "), len(dead)))
	}
	if len(r.Samples) > 0 && len(alive) == 0 && len(dead) > 0 {
		out = append(out, "В выборочных пробах не ответил ни один узел.")
	}
	// главный класс провала
	total := map[string]int{}
	sum := 0
	for _, n := range r.Nodes {
		for k, v := range n.Failures {
			total[k[strings.IndexByte(k, '/')+1:]] += v
			sum += v
		}
	}
	top, topN := "", 0
	for k, v := range total {
		if v > topN {
			top, topN = k, v
		}
	}
	if sum > 0 && top != "" && float64(topN)/float64(sum) >= 0.5 {
		out = append(out, fmt.Sprintf("Основная причина провалов проверок за 6 часов: «%s» (%d%% проб).", top, topN*100/sum))
	}
	if r.Countries.Total > 0 && ratio(r.Countries.Known, r.Countries.Total) < 0.5 {
		out = append(out, fmt.Sprintf("Страна выхода известна лишь у %d из %d узлов с проверкой выхода.", r.Countries.Known, r.Countries.Total))
	}
	if len(out) == 0 {
		out = append(out, "Явных проблем правила не нашли: смотрите исходные данные ниже.")
	}
	return out
}

// Text — отчёт в виде читаемого текста (его читает человек и присылает вместе с JSON).
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Brod %s, %s, сеть: %s, отчёт от %s UTC (%d с)\n", r.Version, r.OS, r.NetKind, r.Generated.Format("2006-01-02 15:04:05"), r.TookMs/1000)
	b.WriteString("\n== Выводы ==\n")
	for _, f := range r.Findings {
		b.WriteString("- " + f + "\n")
	}
	if len(r.Partial) > 0 {
		b.WriteString("\n== Не выполнено ==\n")
		for _, p := range r.Partial {
			b.WriteString("- " + p + "\n")
		}
	}
	fmt.Fprintf(&b, "\n== WARP ==\nаккаунт: %v, цепочка включена: %v, не отвечает: %v, последняя проверка ок: %v\nпрофиль: %s, запомненный для сети: %s\nпроба: ok=%v класс=%s %d мс\n",
		r.Warp.Enabled, r.Warp.ViaWarp, r.Warp.Down, r.Warp.LastOK, r.Warp.Profile, orDash(r.Warp.Remembered), r.Warp.ProbeOK, orDash(r.Warp.ProbeClass), r.Warp.ProbeMs)
	b.WriteString("\n== Настройки ==\n")
	keys := make([]string, 0, len(r.Settings))
	for k := range r.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, r.Settings[k])
	}
	b.WriteString("\n== Узлы по протоколам (проверки L2 за 6 часов) ==\n")
	for _, n := range r.Nodes {
		fmt.Fprintf(&b, "%s: всего %d, в источниках %d, с доменом %d | напрямую: проверено %d, живо %d | через WARP: проверено %d, живо %d\n", n.Proto, n.Total, n.Present, n.Domain,
			n.Checked["direct"], n.Alive["direct"], n.Checked["via-warp"], n.Alive["via-warp"])
		if len(n.Failures) > 0 {
			ks := make([]string, 0, len(n.Failures))
			for k := range n.Failures {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			var parts []string
			for _, k := range ks {
				parts = append(parts, fmt.Sprintf("%s=%d", k, n.Failures[k]))
			}
			b.WriteString("    провалы: " + strings.Join(parts, ", ") + "\n")
		}
	}
	fmt.Fprintf(&b, "страна выхода известна: %d из %d\n", r.Countries.Known, r.Countries.Total)
	b.WriteString("\n== Источники ==\n")
	for _, s := range r.Sources {
		fmt.Fprintf(&b, "%s: включён=%v ок=%v узлов=%d живых=%d последняя удачная загрузка %d мин назад %s\n", s.Name, s.Enabled, s.OK, s.Count, s.Alive, s.AgeMin, s.Err)
	}
	b.WriteString("\n== DNS ==\n")
	for _, d := range r.DNS {
		fmt.Fprintf(&b, "%-13s %-14s ok=%-5v %-9s %-8s %4d мс %s\n", d.Resolver, d.Name, d.OK, d.Rcode, d.Answer, d.Ms, d.Err)
	}
	b.WriteString("\n== Связь ==\n")
	for _, c := range r.Conn {
		fmt.Fprintf(&b, "%-28s ok=%-5v %4d мс %s\n", c.Test, c.OK, c.Ms, c.Err)
	}
	b.WriteString("\n== Выборочные пробы узлов ==\n")
	for _, s := range r.Samples {
		fmt.Fprintf(&b, "%-10s домен=%-5v %-8s ok=%-5v %-14s %5d мс %s\n", s.Proto, s.Domain, s.Mode, s.OK, s.Class, s.Ms, s.Detail)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// JSON — отчёт в машинном виде.
func (r *Report) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}
