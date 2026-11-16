package core

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func traceClient(body string, fail bool) *http.Client {
	return &http.Client{Transport: rt(func(*http.Request) (*http.Response, error) {
		if fail {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
}

func TestDiagnosticsFindings(t *testing.T) {
	c := startCore(t, t.TempDir())
	find := func(r DiagResult, level string) bool {
		for _, f := range r.Findings {
			if f.Level == level {
				return true
			}
		}
		return false
	}
	c.diagEnv = diagEnv{
		http4:  traceClient("ip=203.0.113.9\nloc=nl\ncolo=AMS\nwarp=off\n", false),
		http6:  traceClient("", true),
		txt:    func(context.Context, string) ([]string, error) { return []string{"172.68.1.1"}, nil },
		traceV: "https://x/trace", trace6: "https://x/trace6",
	}
	r := c.Diagnostics(context.Background())
	if r.IP != "203.0.113.9" || r.Country != "NL" || r.Colo != "AMS" || r.IPv6 != "" || r.Resolver != "172.68.1.1" || r.VPNOn {
		t.Fatalf("%+v", r)
	}
	if !find(r, "info") || !find(r, "ok") { // VPN выключен (info) и публичный резолвер Cloudflare (ok)
		t.Fatalf("выводы: %+v", r.Findings)
	}
	// интернета нет
	c.diagEnv.http4 = traceClient("", true)
	if r := c.Diagnostics(context.Background()); r.IP != "" || !find(r, "bad") {
		t.Fatalf("без IPv4 должен быть вывод bad: %+v", r)
	}
}

func TestResolverKind(t *testing.T) {
	for ip, want := range map[string]string{"1.1.1.1": "Cloudflare", "172.68.5.5": "Cloudflare", "8.8.8.8": "Google", "74.125.1.1": "Google", "9.9.9.9": "Quad9", "37.140.1.1": "", "2a00:1450:4001::1": "Google"} {
		if got := resolverKind(ip); got != want {
			t.Errorf("%s: %q, ждали %q", ip, got, want)
		}
	}
}
