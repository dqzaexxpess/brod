package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSanitizeStripsHostsAndAddresses(t *testing.T) {
	e := errors.New(`Get "https://node.example.org/generate_204": read tcp 192.0.2.75:41972->203.0.113.7:443: x509: certificate is valid for secret.example.net, not www.cloudflare.com [2001:db8::1]:443`)
	got := short(e)
	for _, leak := range []string{"192.168", "203.0.113", "node.example", "secret.example", "2001:db8"} {
		if strings.Contains(got, leak) {
			t.Fatalf("утечка %q в %q", leak, got)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Class{
		"unexpected HTTP response status: 404": ClassAuth,
		"lookup x: no such host":               ClassDNS,
		"x509: certificate has expired":        ClassTLS,
		"context canceled":                     ClassCanceled,
		"dial tcp: connection refused":         ClassTCP,
	}
	for msg, want := range cases {
		if got := Classify(errors.New(msg), 0); got != want {
			t.Errorf("%q: got %s want %s", msg, got, want)
		}
	}
	// Windows: отказ и недоступность распознаются по коду Winsock, а не по тексту (он зависит от языка системы)
	for _, code := range []syscall.Errno{10061, 10051, 10065} {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: code}}
		if got := Classify(fmt.Errorf("get: %w", err), 0); got != ClassTCP {
			t.Errorf("winsock %d: got %s want %s", code, got, ClassTCP)
		}
	}
	if Classify(errors.New("i/o timeout"), 100) != ClassFreeze || Classify(errors.New("i/o timeout"), 0) != ClassTimeout {
		t.Error("таймаут после получения данных = freeze, без данных = timeout")
	}
}

func TestSanitizeStripsLookupHost(t *testing.T) {
	got := short(errors.New(`Get "https://x/y": lookup d56bb06d-production.up.railway.app: (exchange6: REFUSED)`))
	if strings.Contains(got, "railway") || strings.Contains(got, "d56bb06d") {
		t.Fatalf("имя хоста из lookup не вычищено: %q", got)
	}
}

// движок пишет «lookup failed for имя: …», «cache-only lookup missed for имя» и
// «open connection to имя:порт using outbound/…»; адреса IPv6 в журнале бывают без квадратных скобок.
func TestSanitizeStripsEngineLogNames(t *testing.T) {
	cases := map[string][]string{
		"ERROR dns: lookup failed for secret-site.example.org: context deadline exceeded":                {"secret-site", "example.org"},
		"DEBUG dns: cache-only lookup missed for hidden.example.net":                                     {"hidden", "example.net"},
		"ERROR connection: open connection to node7.vpn-host.example:8443 using outbound/vless[proxy-1]": {"node7", "vpn-host", "8443"},
		"ERROR connection: open connection to localhostish: dial tcp: refused":                           {"localhostish"},
		"ERROR dial tcp [2001:db8::5]:443: connect: refused":                                             {"2001:db8"},
		"ERROR dial tcp 2001:db8:85a3::8a2e:370:7334: connect: refused":                                  {"2001:db8", "8a2e"},
		"INFO inbound from fe80::1ff:fe23:4567:890a closed":                                              {"fe80", "890a"},
		"INFO peer 2001:db8:0:0:0:0:0:1 handshake":                                                       {"2001:db8"},
		"ERROR lookup failed for a.b.example: lookup a.b.example on 1.1.1.1:53: no such host":            {"a.b.example"},
	}
	for in, leaks := range cases {
		got := Sanitize(in)
		for _, leak := range leaks {
			if strings.Contains(got, leak) {
				t.Errorf("утечка %q в %q (вход %q)", leak, got, in)
			}
		}
	}
	// обычный текст не портится: время, имена файлов, версии
	for _, keep := range []string{
		"05:12:33 INFO core started", "see engine.log line 5", "std::fmt failed", "version 1.2.3 ready",
		"ERROR retry for 3 attempts",
	} {
		if got := Sanitize(keep); got != keep {
			t.Errorf("испорчено %q -> %q", keep, got)
		}
	}
}

func TestSanitizeStripsCertNames(t *testing.T) {
	for _, msg := range []string{
		"x509: certificate is valid for my-villa.ir, www.my-villa.ir, not melli2.example",
		"x509: certificate is valid for 100 names, but none matched secret.example.org",
		"x509: certificate is not valid for any names, but wanted to match secret.example.org",
	} {
		if got := short(errors.New(msg)); strings.Contains(got, "villa") || strings.Contains(got, "secret") || strings.Contains(got, "melli") {
			t.Fatalf("имя из сертификата не вычищено: %q", got)
		}
	}
}

func ytServer(t *testing.T, redirCode int, watchCode int, watchBody string) YouTubeURLs {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(redirCode) })
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "SOCS=") {
			t.Error("нужен cookie согласия, иначе европейским адресам показывают страницу согласия")
		}
		w.WriteHeader(watchCode)
		w.Write([]byte(watchBody))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return YouTubeURLs{Redirector: s.URL + "/204", Watch: s.URL + "/watch"}
}

func TestYouTubeViaWatchPage(t *testing.T) {
	cl := &http.Client{Timeout: 3 * time.Second}
	ok := youtubeVia(context.Background(), cl, ytServer(t, 204, 200, `var ytInitialPlayerResponse = {"responseContext":{},"playabilityStatus":{"status":"OK","playableInEmbed":true}};`))
	if !ok.OK {
		t.Fatalf("OK на странице — узел подходит: %+v", ok)
	}
	for name, tc := range map[string]struct {
		redir, code int
		body        string
		class       Class
	}{
		"бот":         {204, 200, `"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm you’re not a bot"}`, ClassBotCheck},
		"недоступно":  {204, 200, `"playabilityStatus":{"status":"UNPLAYABLE","reason":"Video unavailable"}`, ClassBlocked},
		"нет статуса": {204, 200, `<html>ничего</html>`, ClassBlocked},
		"429":         {204, 429, ``, ClassBotCheck},
		"403":         {204, 403, ``, ClassBlocked},
		"googlevideo": {403, 200, `"playabilityStatus":{"status":"OK"}`, ClassBlocked},
	} {
		r := youtubeVia(context.Background(), cl, ytServer(t, tc.redir, tc.code, tc.body))
		if r.OK || r.Class != tc.class {
			t.Errorf("%s: %+v, ждали провал класса %s", name, r, tc.class)
		}
	}
}

// Живая проверка с обычным сетевым выходом (LIVE_YOUTUBE=1): на выходе, где YouTube открывается, тест обязан сказать OK.
func TestLiveYouTube(t *testing.T) {
	if os.Getenv("LIVE_YOUTUBE") == "" {
		t.Skip("LIVE_YOUTUBE не задан")
	}
	r := youtubeVia(context.Background(), &http.Client{Timeout: 20 * time.Second}, DefaultYouTubeURLs)
	t.Logf("%+v", r)
	if !r.OK {
		t.Fatalf("на рабочем выходе тест должен проходить: %+v", r)
	}
}
