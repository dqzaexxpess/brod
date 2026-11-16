package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"brod/aggregator"
	"brod/store"
)

// порционный разбор даёт те же записи в том же порядке, что разбор целым срезом, а дубликаты схлопываются по всему
// источнику, в том числе между порциями.
func TestParseLinksChunkedMatchesWhole(t *testing.T) {
	var links []string
	for i := 0; i < 40; i++ {
		links = append(links, vlessLink(i%25), "мусор", vlessLink(1000+i)) // дубликаты разнесены по порциям
	}
	links = append(links, "vless://5b0e5c2e-0000-4000-8000-000000000001@127.0.0.1:443?security=tls#local") // локальный адрес отбрасывается
	whole := parseLinks(links)
	for _, chunk := range []int{1, 3, 7, 1000} {
		var got []store.NodeRec
		sizes := []int{}
		n, ok := parseLinksChunked(context.Background(), links, chunk, func(r []store.NodeRec) bool {
			got = append(got, r...)
			sizes = append(sizes, len(r))
			return true
		})
		if !ok || n != len(whole) || len(got) != len(whole) {
			t.Fatalf("chunk %d: n=%d ok=%v записей %d, ждали %d", chunk, n, ok, len(got), len(whole))
		}
		for i := range whole {
			if got[i].ID != whole[i].ID || string(got[i].Opts) != string(whole[i].Opts) {
				t.Fatalf("chunk %d: запись %d отличается", chunk, i)
			}
		}
		for i, sz := range sizes {
			if sz > chunk || (i < len(sizes)-1 && sz != chunk) {
				t.Fatalf("chunk %d: размеры порций %v", chunk, sizes)
			}
		}
	}
	// emit вернул false — разбор останавливается
	calls := 0
	if _, ok := parseLinksChunked(context.Background(), links, 3, func([]store.NodeRec) bool { calls++; return false }); ok || calls != 1 {
		t.Fatalf("остановка: ok=%v calls=%d", ok, calls)
	}
}

// источник из нескольких порций с дубликатами записывается полностью, отчёт и присутствие узлов те же, а пропавший
// из источника узел помечается пропавшим (EndRefresh после последней порции).
func TestRefreshStreamsChunksAndEndsRefresh(t *testing.T) {
	old := ingestChunk
	ingestChunk = 4
	t.Cleanup(func() { ingestChunk = old })
	var links []string
	for i := 0; i < 30; i++ {
		links = append(links, vlessLink(i), vlessLink(i/2)) // каждый узел повторён, дубликаты в соседних порциях
	}
	f := newFeed()
	f.set("A", links...)
	c := refreshCore(t, f, "A")
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if a := rep.Sources[0]; a.Links != 60 || a.Nodes != 30 || a.Lost != 0 || a.Err != "" || rep.Total != 30 || rep.Present != 30 {
		t.Fatalf("первое обновление: %+v / %+v", rep.Sources[0], rep)
	}
	var rest []string
	for _, l := range links {
		if l != vlessLink(0) {
			rest = append(rest, l)
		}
	}
	f.set("A", rest...) // узел 0 пропал из источника
	rep, err = c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if a := rep.Sources[0]; a.Nodes != 29 || a.Lost != 1 || rep.Total != 30 || rep.Present != 29 {
		t.Fatalf("второе обновление: %+v / %+v", rep.Sources[0], rep)
	}
}

// настоящая загрузка (aggregator.Fetch, кэш на диске) с Lazy: ссылки читаются из файла кэша, итоги совпадают с обычным путём,
// а второе обновление (304) идёт быстрым путём.
func TestRefreshLazyFetchEndToEnd(t *testing.T) {
	old := ingestChunk
	ingestChunk = 10
	t.Cleanup(func() { ingestChunk = old })
	var body strings.Builder
	for i := 0; i < 55; i++ {
		fmt.Fprintln(&body, vlessLink(i))
	}
	fmt.Fprintln(&body, vlessLink(3)) // дубликат
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"e1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("Profile-Title", "Тест")
		fmt.Fprint(w, body.String())
	}))
	defer srv.Close()
	c, err := New(Config{StateDir: t.TempDir(), AppPort: nextPort(), LogLevel: "error",
		SeedSources: []aggregator.Source{{Name: "L", URL: srv.URL, Priority: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if s := rep.Sources[0]; s.Err != "" || s.Links != 56 || s.Nodes != 55 || rep.Total != 55 || rep.Present != 55 {
		t.Fatalf("первое: %+v / %+v", rep.Sources[0], rep)
	}
	recs, _ := c.st.Sources()
	if recs[0].Title != "Тест" || recs[0].LastCount != 56 {
		t.Fatalf("сведения источника: %+v", recs[0])
	}
	rep, err = c.Refresh(context.Background(), "user") // Known: 304 без чтения тела
	if err != nil {
		t.Fatal(err)
	}
	if s := rep.Sources[0]; s.Err != "" || !s.NotModified || s.Nodes != 55 || rep.Present != 55 {
		t.Fatalf("второе: %+v / %+v", rep.Sources[0], rep)
	}
}
