package prober

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"brod/engine"
)

func directProbe(t *testing.T) *engine.Probe {
	t.Helper()
	e, err := engine.New("error")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	p, err := e.NewProbe([]byte(`{"type":"direct"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestCheckHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "hello")
		case "/anubis":
			w.WriteHeader(403)
			fmt.Fprint(w, "not a bot?")
		case "/video": // честный Range-ответ
			w.Header().Set("Content-Range", "bytes 0-1023/9999")
			w.WriteHeader(206)
			fmt.Fprint(w, strings.Repeat("v", 1024))
		case "/redirect": // подписанная ссылка CDN → его сервер
			http.Redirect(w, r, "/video-range", http.StatusFound)
		case "/video-range": // сервер CDN отдаёт поток только на Range-запрос
			if r.Header.Get("Range") == "" {
				http.Error(w, "range required", http.StatusBadRequest)
				return
			}
			w.WriteHeader(206)
			fmt.Fprint(w, strings.Repeat("v", 1024))
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/slow": // всё приходит, но медленно: 1 КиБ за ~0,5 с
			w.WriteHeader(206)
			for range 4 {
				fmt.Fprint(w, strings.Repeat("v", 256))
				w.(http.Flusher).Flush()
				time.Sleep(125 * time.Millisecond)
			}
		case "/short": // недобор
			w.WriteHeader(206)
			fmt.Fprint(w, "tiny")
		case "/stall": // данные пошли и встали
			w.WriteHeader(206)
			fmt.Fprint(w, strings.Repeat("v", 100))
			w.(http.Flusher).Flush()
			time.Sleep(8 * time.Second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := directProbe(t)
	ctx := context.Background()
	cases := []struct {
		name string
		spec HTTPSpec
		ok   bool
		cls  Class
	}{
		{"200", HTTPSpec{ID: "t", URL: srv.URL + "/ok"}, true, ""},
		{"404 по умолчанию провал", HTTPSpec{ID: "t", URL: srv.URL + "/nope"}, false, ClassBlocked},
		{"403 допустим (Anubis)", HTTPSpec{ID: "t", URL: srv.URL + "/anubis", ExpectCodes: []int{200, 403}}, true, ""},
		{"403 без разрешения", HTTPSpec{ID: "t", URL: srv.URL + "/anubis"}, false, ClassBlocked},
		{"range ок", HTTPSpec{ID: "t", RangeURL: srv.URL + "/video", MinBytes: 1024}, true, ""},
		{"range недобор", HTTPSpec{ID: "t", RangeURL: srv.URL + "/short", MinBytes: 1024}, false, ClassFreeze},
		{"range простой", HTTPSpec{ID: "t", RangeURL: srv.URL + "/stall", MinBytes: 1024}, false, ClassFreeze},
		{"range выше порога скорости", HTTPSpec{ID: "t", RangeURL: srv.URL + "/video", MinBytes: 1024, MinKBps: 1}, true, ""},
		{"range ниже порога скорости", HTTPSpec{ID: "t", RangeURL: srv.URL + "/slow", MinBytes: 1024, MinKBps: 100}, false, ClassFreeze},
		{"range 404", HTTPSpec{ID: "t", RangeURL: srv.URL + "/nope", MinBytes: 1024}, false, ClassBlocked},
		{"range через редирект (Range сохраняется)", HTTPSpec{ID: "t", RangeURL: srv.URL + "/redirect", MinBytes: 1024}, true, ""},
		{"range: бесконечные редиректы — провал", HTTPSpec{ID: "t", RangeURL: srv.URL + "/loop", MinBytes: 1024}, false, ClassBlocked},
		{"без Range-ссылки редирект не проходится", HTTPSpec{ID: "t", URL: srv.URL + "/redirect"}, false, ClassBlocked},
		{"недоступный адрес", HTTPSpec{ID: "t", URL: "http://127.0.0.1:1/"}, false, ClassTCP},
	}
	for _, c := range cases {
		r := CheckHTTP(ctx, p, c.spec)
		if r.OK != c.ok || (!c.ok && r.Class != c.cls) || r.Target != "t" || r.Level != 4 {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}
