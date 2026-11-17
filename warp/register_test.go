package warp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Регистрация аккаунта без сети: подставной http.Client отвечает за api.cloudflareclient.com.

type fakeRT struct {
	mu   sync.Mutex
	reqs []*http.Request
	body [][]byte
	fn   func(r *http.Request) (int, string)
}

func (f *fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.body = append(f.body, b)
	f.mu.Unlock()
	if err := r.Context().Err(); err != nil { // как настоящий транспорт
		return nil, err
	}
	code, body := f.fn(r)
	if code == 0 {
		return nil, errors.New("сеть недоступна (тест)")
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

const regOK = `{"id":"dev-1","token":"tok-1","config":{"client_id":"AQID","peers":[{"public_key":"peerpub","endpoint":{"host":"engage.cloudflareclient.com:2408"}}],"interface":{"addresses":{"v4":"172.16.0.2","v6":"fd01::2"}}}}`

func TestRegisterBuildsAccountAndEnablesWarp(t *testing.T) {
	f := &fakeRT{fn: func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return 200, regOK
		}
		return 200, "{}"
	}}
	a, err := Register(context.Background(), &http.Client{Transport: f})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 2 || f.reqs[0].Method != http.MethodPost || f.reqs[0].URL.String() != apiBase+"/reg" {
		t.Fatalf("запросы: %v", f.reqs)
	}
	if f.reqs[1].Method != http.MethodPatch || f.reqs[1].URL.String() != apiBase+"/reg/dev-1" || f.reqs[1].Header.Get("Authorization") != "Bearer tok-1" {
		t.Fatalf("включение WARP: %v %v", f.reqs[1].URL, f.reqs[1].Header)
	}
	if f.reqs[0].Header.Get("Authorization") != "" || f.reqs[0].Header.Get("CF-Client-Version") != clientVersion || f.reqs[0].Header.Get("User-Agent") != userAgent {
		t.Fatalf("заголовки регистрации: %v", f.reqs[0].Header)
	}
	var sent map[string]any
	if err := json.Unmarshal(f.body[0], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["key"] != a.PublicKey || sent["key"] == "" {
		t.Fatalf("в запросе не открытый ключ аккаунта: %v", sent["key"])
	}
	if string(f.body[1]) != `{"warp_enabled":true}` {
		t.Fatalf("тело PATCH: %s", f.body[1])
	}
	if a.ID != "dev-1" || a.Token != "tok-1" || a.PeerPublicKey != "peerpub" || a.AddrV4 != "172.16.0.2" || a.AddrV6 != "fd01::2" ||
		a.ClientID != "AQID" || a.EndpointHost != "engage.cloudflareclient.com:2408" || a.PrivateKey == "" || a.CreatedAt.IsZero() {
		t.Fatalf("аккаунт: %+v", a)
	}
	if r := a.Reserved(); len(r) != 3 || r[0] != 1 || r[1] != 2 || r[2] != 3 {
		t.Fatalf("reserved: %v", r)
	}
}

func TestRegisterErrors(t *testing.T) {
	cases := []struct {
		name string
		fn   func(r *http.Request) (int, string)
		want string
	}{
		{"HTTP 500 при регистрации", func(*http.Request) (int, string) { return 500, "boom" }, "регистрация"},
		{"сеть недоступна", func(*http.Request) (int, string) { return 0, "" }, "регистрация"},
		{"мусор вместо JSON", func(*http.Request) (int, string) { return 200, "не json" }, "неожиданный ответ"},
		{"нет пиров", func(*http.Request) (int, string) { return 200, `{"id":"x","token":"t","config":{"peers":[]}}` }, "неожиданный ответ"},
		{"нет id", func(*http.Request) (int, string) { return 200, `{"config":{"peers":[{"public_key":"p"}]}}` }, "неожиданный ответ"},
		{"отказ при включении WARP", func(r *http.Request) (int, string) {
			if r.Method == http.MethodPost {
				return 200, regOK
			}
			return 403, "no"
		}, "включение WARP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := Register(context.Background(), &http.Client{Transport: &fakeRT{fn: c.fn}})
			if err == nil || a != nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ждали ошибку с %q: %v %v", c.want, a, err)
			}
		})
	}
}

func TestRegisterCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Register(ctx, &http.Client{Transport: &fakeRT{fn: func(*http.Request) (int, string) { return 200, regOK }}}); err == nil {
		t.Fatal("отменённый контекст дал успех")
	}
}

func TestGenerateKeyIsUniqueBase64(t *testing.T) {
	p1, u1, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	p2, _, _ := GenerateKey()
	if p1 == p2 || len(p1) != 44 || len(u1) != 44 {
		t.Fatalf("ключи: %q %q %q", p1, p2, u1)
	}
}
