package core

import (
	"brod/store"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestIPClass(t *testing.T) {
	for ip, want := range map[string]string{"1.1.1.1": "public", "192.168.1.1": "private", "10.0.0.1": "private", "127.0.0.1": "loopback", "0.0.0.0": "bogon", "100.64.1.1": "bogon", "169.254.1.1": "private", "2606:4700::1111": "public"} {
		if got := ipClass(net.ParseIP(ip)); got != want {
			t.Errorf("%s: %s, ждали %s", ip, got, want)
		}
	}
}

func TestParseDNS(t *testing.T) {
	name := dnsmessage.MustNewName("a.example.")
	pack := func(rc dnsmessage.RCode, ip [4]byte, withAnswer bool) []byte {
		m := dnsmessage.Message{Header: dnsmessage.Header{Response: true, RCode: rc}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
		if withAnswer {
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: ip}}}
		}
		b, err := m.Pack()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, c := range []struct {
		b            []byte
		rcode, class string
	}{
		{pack(dnsmessage.RCodeSuccess, [4]byte{93, 184, 216, 34}, true), "NOERROR", "public"},
		{pack(dnsmessage.RCodeSuccess, [4]byte{127, 0, 0, 1}, true), "NOERROR", "loopback"},
		{pack(dnsmessage.RCodeNameError, [4]byte{}, false), "NXDOMAIN", "none"},
		{pack(dnsmessage.RCodeServerFailure, [4]byte{}, false), "SERVFAIL", "none"},
	} {
		rc, cl, err := parseDNS(c.b)
		if err != nil || rc != c.rcode || cl != c.class {
			t.Errorf("%s/%s/%v, ждали %s/%s", rc, cl, err, c.rcode, c.class)
		}
	}
	if _, _, err := parseDNS([]byte{1, 2, 3}); err == nil {
		t.Error("мусор принят за ответ DNS")
	}
}

func dnsRows(resolver string, names []string, ok bool, answer string) []ReportDNS {
	var out []ReportDNS
	for _, n := range names {
		out = append(out, ReportDNS{Resolver: resolver, Name: n, OK: ok, Answer: answer})
	}
	return out
}

func reportHas(fs []string, sub string) bool {
	for _, f := range fs {
		if strings.Contains(f, sub) {
			return true
		}
	}
	return false
}

func TestReportFindings(t *testing.T) {
	known := []string{"a", "b", "c", "d"}
	nodes := []string{"узел-1", "узел-2", "узел-3"}
	var r Report
	r.Warp = ReportWarp{Enabled: true, ProbeOK: true}
	r.DNS = append(r.DNS, dnsRows("system", known, true, "public")...)
	r.DNS = append(r.DNS, dnsRows("system", nodes, false, "")...)
	r.DNS = append(r.DNS, dnsRows("doh-1.1.1.1", known, true, "public")...)
	r.Samples = []ReportSample{{Proto: "wireguard", OK: true}, {Proto: "vless"}, {Proto: "vmess"}, {Proto: "trojan"}}
	f := reportFindings(&r)
	if !reportHas(f, "Имена узлов системным DNS не резолвятся") {
		t.Errorf("нет вывода про имена узлов: %v", f)
	}
	if !reportHas(f, "отвечает только протокол wireguard") {
		t.Errorf("нет вывода про единственный живой протокол: %v", f)
	}

	r = Report{Warp: ReportWarp{Enabled: true, ProbeOK: true}}
	r.DNS = append(r.DNS, dnsRows("system", known, true, "loopback")...)
	if f = reportFindings(&r); !reportHas(f, "подменяется") {
		t.Errorf("подмена DNS не замечена: %v", f)
	}

	r = Report{Warp: ReportWarp{Enabled: true, ProbeOK: true}}
	r.DNS = append(r.DNS, dnsRows("system", known, false, "")...)
	r.DNS = append(r.DNS, dnsRows("doh-1.1.1.1", known, true, "public")...)
	if f = reportFindings(&r); !reportHas(f, "DNS сети режется или ломается") {
		t.Errorf("отказ системного DNS при рабочем DoH не замечен: %v", f)
	}

	r = Report{Warp: ReportWarp{Enabled: true, ProbeOK: false, ProbeClass: "timeout"}, Conn: []ReportConn{{Test: "tcp 1.1.1.1:443"}, {Test: "tcp 8.8.8.8:443"}}}
	f = reportFindings(&r)
	if !reportHas(f, "WARP не ответил") || !reportHas(f, "Нет прямого TCP") {
		t.Errorf("нет выводов про WARP и TCP: %v", f)
	}

	r = Report{Warp: ReportWarp{Enabled: true, ProbeOK: true}, Nodes: []ReportNodes{{Proto: "vless", Failures: map[string]int{"direct/dns": 80, "direct/timeout": 20}}}}
	if f = reportFindings(&r); !reportHas(f, "«dns» (80% проб)") {
		t.Errorf("главная причина провалов: %v", f)
	}

	if f = reportFindings(&Report{Warp: ReportWarp{Enabled: true, ProbeOK: true}}); !reportHas(f, "Явных проблем") {
		t.Errorf("пустой отчёт должен говорить, что проблем не найдено: %v", f)
	}
}

// Имена узлов в сообщениях об ошибках заменяются метками, идентификаторы и адреса вычищаются.
func TestReportScrubMasksNamesInErrors(t *testing.T) {
	var r Report
	r.addMask("secret-host.example.org", "узел-1")
	r.addMask("node-b.example.net", "<узел>")
	r.DNS = []ReportDNS{{Resolver: "via-warp", Name: "узел-1", Err: "dial tcp secret-host.example.org:443: i/o timeout"}}
	r.Samples = []ReportSample{{Proto: "vless", Detail: "connect node-b.example.net:8443 uuid 11111111-2222-3333-4444-555555555555 via 198.51.100.7"}}
	r.Partial = []string{"проба: secret-host.example.org недоступен"}
	r.scrub()
	all := r.Text() + string(r.JSON())
	for _, leak := range []string{"secret-host.example.org", "node-b.example.net", "11111111-2222-3333-4444-555555555555", "198.51.100.7"} {
		if strings.Contains(all, leak) {
			t.Errorf("в отчёте осталось %q:\n%s", leak, all)
		}
	}
	if !strings.Contains(all, "узел-1") {
		t.Error("метка узла пропала")
	}
}

// Целиком: узлы с доменом и секретом в настройках, отчёт собирается, ни имя, ни пароль, ни uuid в итог не попадают.
func TestBuildReportDoesNotLeakNodeSecrets(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	nodes := []store.NodeRec{
		{ID: "n-1", Class: "vless/tcp/tls", Proto: "vless", Server: "secret-host.example.org", Port: 443,
			Opts: []byte(`{"type":"vless","server":"secret-host.example.org","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}`)},
		{ID: "n-2", Class: "trojan/tcp/tls", Proto: "trojan", Server: "secret-host2.example.org", Port: 443,
			Opts: []byte(`{"type":"trojan","server":"secret-host2.example.org","server_port":443,"password":"s3cr3t-pass-123"}`)},
	}
	if err := c.st.UpsertNodes("src", 1, nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r, err := c.BuildReport(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	all := r.Text() + string(r.JSON())
	for _, leak := range []string{"secret-host", "s3cr3t-pass-123", "11111111-2222-3333-4444-555555555555"} {
		if strings.Contains(all, leak) {
			t.Errorf("в отчёте %q", leak)
		}
	}
	if len(r.Samples) == 0 {
		t.Error("выборочные пробы не выполнялись: утечку в них проверить нечем")
	}
}

// Отчёт на пустой базе собирается без паники, этапы не падают друг от друга, итог читается как текст и JSON.
func TestBuildReportSmoke(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r, err := c.BuildReport(ctx, nil)
	if err != nil || r == nil {
		t.Fatalf("отчёт: %v", err)
	}
	if len(r.Findings) == 0 || r.Text() == "" || len(r.JSON()) == 0 {
		t.Fatalf("пустой итог: %+v", r)
	}
}

func TestStartReportBusy(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	reportRun.Lock()
	reportRun.st = ReportState{State: "running"}
	reportRun.Unlock()
	t.Cleanup(func() { reportRun.Lock(); reportRun.st = ReportState{}; reportRun.Unlock() })
	if err := c.StartReport(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("повторный запуск: %v", err)
	}
}

// Сводка по протоколам собирается из тех же записей, что пишет обычная проверка: узлы с разными названиями протокола в nodes.proto и в классе
// (ss и shadowsocks), проверки напрямую и через WARP, чужая сеть не примешивается.
func TestReportNodesFromRecordedProbes(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	netID := c.cfg.NetID()
	now := time.Now()
	nodes := []store.NodeRec{
		{ID: "n-vless-1", Class: "vless/tcp/reality", Proto: "vless", Server: "a.example.org", Port: 443, Opts: []byte(`{"type":"direct"}`)},
		{ID: "n-vless-2", Class: "vless/tcp/reality", Proto: "vless", Server: "198.51.100.7", Port: 443, Opts: []byte(`{"type":"direct"}`)},
		{ID: "n-ss-1", Class: "shadowsocks/tcp/none", Proto: "ss", Server: "b.example.org", Port: 8388, Opts: []byte(`{"type":"direct"}`)},
		{ID: "n-wg-1", Class: "wireguard/tcp/none", Proto: "wireguard", Endpoint: true, Server: "203.0.113.5", Port: 51820, Opts: []byte(`{"type":"direct"}`)},
	}
	if err := c.st.UpsertNodes("src", 1, nodes, now); err != nil {
		t.Fatal(err)
	}
	row := func(id, cls, net string, ok bool, class string) store.Row {
		return store.Row{Node: id, NodeClass: cls, Net: net, Level: 2, OK: ok, Class: class, At: now}
	}
	if err := c.st.Record([]store.Row{
		row("n-vless-1", "vless/tcp/reality", netID, false, "dns"),
		row("n-vless-2", "vless/tcp/reality", netID, false, "timeout"),
		row("n-vless-1", "vless/tcp/reality", netID+"|warp", true, ""),
		row("n-ss-1", "shadowsocks/tcp/none", netID, false, "dns"),
		row("n-wg-1", "wireguard/tcp/none", netID, true, ""),
		row("n-wg-1", "wireguard/tcp/none", "чужая-сеть", true, ""),
	}); err != nil {
		t.Fatal(err)
	}
	var r Report
	c.reportNodes(&r, netID)
	by := map[string]ReportNodes{}
	for _, n := range r.Nodes {
		by[n.Proto] = n
	}
	v, ss, wg := by["vless"], by["shadowsocks"], by["wireguard"]
	if v.Total != 2 || v.Domain != 1 || v.Present != 2 {
		t.Errorf("vless: %+v", v)
	}
	if v.Checked["direct"] != 2 || v.Alive["direct"] != 0 || v.Failures["direct/dns"] != 1 || v.Failures["direct/timeout"] != 1 {
		t.Errorf("vless напрямую: %+v", v)
	}
	if v.Alive["via-warp"] != 1 || v.Checked["via-warp"] != 1 {
		t.Errorf("vless через WARP: %+v", v)
	}
	if ss.Total != 1 || ss.Failures["direct/dns"] != 1 {
		t.Errorf("shadowsocks (nodes.proto=ss): %+v", ss)
	}
	if wg.Alive["direct"] != 1 || wg.Checked["direct"] != 1 {
		t.Errorf("wireguard: %+v (чужая сеть не должна считаться)", wg)
	}
	if _, ok := by["ss"]; ok {
		t.Error("протокол назван по nodes.proto, а не по классу")
	}
}
