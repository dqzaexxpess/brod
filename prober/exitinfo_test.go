package prober

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func exitServer(t *testing.T, googleCode int, googleLoc string, siteCode int, siteHeader string) ExitURLs {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/trace", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fl=1\nip=203.0.113.9\ncolo=FRA\nloc=de\nwarp=off\n"))
	})
	mux.HandleFunc("/google", func(w http.ResponseWriter, r *http.Request) {
		if googleLoc != "" {
			w.Header().Set("Location", googleLoc)
		}
		w.WriteHeader(googleCode)
	})
	mux.HandleFunc("/site", func(w http.ResponseWriter, r *http.Request) {
		if siteHeader != "" {
			w.Header().Set("cf-mitigated", siteHeader)
		}
		w.WriteHeader(siteCode)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return ExitURLs{Trace: s.URL + "/trace", Google: s.URL + "/google", Site: s.URL + "/site"}
}

func TestExitClean(t *testing.T) {
	u := exitServer(t, 200, "", 200, "")
	got := exitVia(context.Background(), &http.Client{Timeout: 3 * time.Second}, u)
	if got.IP != "203.0.113.9" || got.Country != "DE" || got.Colo != "FRA" {
		t.Fatalf("trace: %+v", got)
	}
	if got.GoogleOK == nil || !*got.GoogleOK || got.SiteOK == nil || !*got.SiteOK || got.Detail != "" {
		t.Fatalf("чистый выход: %+v", got)
	}
}

func TestExitDirty(t *testing.T) {
	u := exitServer(t, 302, "https://www.google.com/sorry/index?continue=x", 403, "challenge")
	got := exitVia(context.Background(), &http.Client{Timeout: 3 * time.Second}, u)
	if got.GoogleOK == nil || *got.GoogleOK {
		t.Fatalf("капча Google должна считаться провалом: %+v", got)
	}
	if got.SiteOK == nil || *got.SiteOK {
		t.Fatalf("испытание Cloudflare должно считаться провалом: %+v", got)
	}
	if got.Detail == "" {
		t.Fatal("нужна причина")
	}
}

func TestExitConsentIsNotCaptcha(t *testing.T) {
	u := exitServer(t, 302, "https://consent.google.com/ml?continue=x", 301, "")
	got := exitVia(context.Background(), &http.Client{Timeout: 3 * time.Second}, u)
	if got.GoogleOK == nil || !*got.GoogleOK || got.SiteOK == nil || !*got.SiteOK {
		t.Fatalf("согласие на куки и редирект — не провал: %+v", got)
	}
}

func TestExitNoNetwork(t *testing.T) {
	got := exitVia(context.Background(), &http.Client{Timeout: 500 * time.Millisecond}, ExitURLs{Trace: "http://127.0.0.1:1/x", Google: "http://127.0.0.1:1/g", Site: "http://127.0.0.1:1/s"})
	if got.IP != "" || got.GoogleOK != nil || got.SiteOK != nil || got.Detail == "" {
		t.Fatalf("без сети ничего не известно: %+v", got)
	}
}
