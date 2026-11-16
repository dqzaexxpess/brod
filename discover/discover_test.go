package discover

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func vlessList(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "vless://uuid-%s-%d@h%d.example:443?security=tls#n%d\n", tag, i, i, i)
	}
	return b.String()
}

// fakeGitHub — локальная замена raw.githubusercontent.com: README двух репозиториев и агрегатора, файлы подписок.
func fakeGitHub(t *testing.T, hits *atomic.Int64) (*httptest.Server, string) {
	t.Helper()
	var base string
	files := map[string]string{
		"/agg/list/main/README.md": "Репозитории: https://github.com/o2/r2 и ещё https://github.com/o1/r1/ и https://github.com/x/readme.md",
		"/o1/r1/main/README.md": "# r1\n" +
			"[подписка](BASE/o1/r1/main/sub.txt)\n" +
			"[b64](BASE/o1/r1/main/b64.txt)\n" +
			"[мало](BASE/o1/r1/main/tiny.txt)\n" +
			"[огромный](BASE/o1/r1/main/huge.txt)\n" +
			"![badge](BASE/o1/r1/main/badge.svg) [doc](BASE/o1/r1/main/doc.md) [чужой](https://evil.example/o1/r1/main/x.txt)\n",
		"/o2/r2/master/readme.md":     "см. [файл](BASE/o2/r2/master/sub/vless.txt) и повтор [тот же](BASE/o1/r1/main/sub.txt)",
		"/o1/r1/main/sub.txt":         vlessList(30, "a") + "trojan://p@t.example:443#t\nhttp://notproxy.example/\n# comment\n",
		"/o1/r1/main/b64.txt":         base64.StdEncoding.EncodeToString([]byte(vlessList(25, "b"))),
		"/o1/r1/main/tiny.txt":        vlessList(3, "c"),
		"/o1/r1/main/huge.txt":        strings.Repeat("x", 5000),
		"/o2/r2/master/sub/vless.txt": vlessList(40, "d"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(body, "BASE", strings.TrimSuffix(base, "/")))
	}))
	t.Cleanup(srv.Close)
	base = srv.URL + "/"
	return srv, base
}

func TestRunFindsSubscriptionLinks(t *testing.T) {
	var hits atomic.Int64
	_, base := fakeGitHub(t, &hits)
	opt := Options{Base: base, Aggregator: "agg/list", FileMax: 4000, Rand: rand.New(rand.NewSource(1))}
	found, st, err := Run(context.Background(), opt, []string{"o1/r1"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Found{}
	for _, f := range found {
		byName[f.Name] = f
	}
	if len(found) != 3 {
		t.Fatalf("ждали 3 кандидата (sub, b64, vless), получили %d: %+v", len(found), found)
	}
	sub := byName["o1/r1/sub"]
	if sub.Links != 31 || sub.Schemes["vless"] != 30 || sub.Schemes["trojan"] != 1 || len(sub.Sample) != 20 || sub.Repo != "o1/r1" {
		t.Errorf("sub: %+v", sub)
	}
	if b := byName["o1/r1/b64"]; b.Links != 25 || b.Schemes["vless"] != 25 {
		t.Errorf("base64-подписка: %+v", b)
	}
	if d := byName["o2/r2/vless"]; d.Links != 40 || !strings.HasPrefix(d.URL, base) {
		t.Errorf("репозиторий из README агрегатора: %+v", d)
	}
	if found[0].Links < found[1].Links || found[1].Links < found[2].Links {
		t.Errorf("не по убыванию числа ссылок: %v %v %v", found[0].Links, found[1].Links, found[2].Links)
	}
	if st.TooLarge != 1 || st.Repos != 2 || st.Readmes != 2 {
		t.Errorf("статистика: %+v", st)
	}
	for _, f := range found {
		if strings.Contains(f.URL, "evil.example") || strings.HasSuffix(f.URL, ".svg") || strings.HasSuffix(f.URL, ".md") {
			t.Errorf("лишняя ссылка: %s", f.URL)
		}
	}
}

func TestRunSkipsKnownAndLimits(t *testing.T) {
	var hits atomic.Int64
	_, base := fakeGitHub(t, &hits)
	known := base + "o1/r1/main/sub.txt"
	opt := Options{Base: base, Aggregator: "-", Skip: func(u string) bool { return u == known }}
	found, _, err := Run(context.Background(), opt, []string{"o1/r1", "o2/r2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.URL == known {
			t.Errorf("известный адрес предложен снова")
		}
	}
	// предел числа репозиториев: второй не читается
	hits.Store(0)
	_, st, _ := Run(context.Background(), Options{Base: base, Aggregator: "-", MaxRepos: 1}, []string{"o1/r1", "o2/r2"})
	if st.Repos != 1 {
		t.Errorf("MaxRepos не соблюдён: %+v", st)
	}
	// предел числа файлов
	_, st, _ = Run(context.Background(), Options{Base: base, Aggregator: "-", MaxFiles: 2}, []string{"o1/r1"})
	if st.Files+st.Failed+st.TooLarge > 2 {
		t.Errorf("MaxFiles не соблюдён: %+v", st)
	}
	// невалидные репозитории из зерна отбрасываются
	_, st, _ = Run(context.Background(), Options{Base: base, Aggregator: "-"}, []string{"../etc", "a b/c", "o1/r1"})
	if st.Repos != 1 {
		t.Errorf("зерно не проверено: %+v", st)
	}
}

func TestRunCancel(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done() // зависший сервер
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := Run(ctx, Options{Base: srv.URL + "/", Aggregator: "-", Timeout: time.Minute}, []string{"o1/r1", "o2/r2"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("отмена не вернула ошибку")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("проход не прервался по отмене контекста")
	}
}

func TestReadmeLinksAndHelpers(t *testing.T) {
	text := "https://github.com/a/b/blob/main/sub.txt https://github.com/a/b/raw/refs/heads/main/x.list. https://raw.githubusercontent.com/a/b/main/img.png " +
		"https://raw.githubusercontent.com/a/b/main/LICENSE.txt http://raw.githubusercontent.com/a/b/main/plain.txt"
	got := ReadmeLinks("", text, 0)
	want := []string{"https://raw.githubusercontent.com/a/b/main/sub.txt", "https://raw.githubusercontent.com/a/b/main/x.list"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ссылки: %v", got)
	}
	if r, ok := RepoOfURL("", "https://raw.githubusercontent.com/igareck/vpn-configs-for-russia/main/X.txt"); !ok || r != "igareck/vpn-configs-for-russia" {
		t.Errorf("RepoOfURL: %q %v", r, ok)
	}
	if _, ok := RepoOfURL("", "https://example.com/a/b/c"); ok {
		t.Error("чужой хост принят")
	}
	if n := CandidateName("o/r", "https://raw.githubusercontent.com/o/r/main/dir/my sub!.txt"); n != "o/r/my_sub_" {
		t.Errorf("имя %q", n)
	}
}
