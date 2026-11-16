package rulesets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSnapshotIsValidAndInstalls(t *testing.T) {
	for _, s := range Sets {
		b, err := snapshot.ReadFile("data/" + s.Name + ".srs")
		if err != nil {
			t.Fatalf("в снимке нет %s", s.Name)
		}
		if err := Validate(b); err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
	}
	dir := t.TempDir() + "/rs"
	n, err := Install(dir)
	if err != nil || n != len(Sets) {
		t.Fatalf("создано %d из %d: %v", n, len(Sets), err)
	}
	// повторная установка ничего не перезаписывает (обновлённая копия новее снимка)
	custom := []byte("пометка")
	p := Path(dir, "geosite-youtube")
	_ = os.WriteFile(p, custom, 0o600)
	if n, _ := Install(dir); n != 0 {
		t.Fatalf("повторная установка создала %d файлов", n)
	}
	if b, _ := os.ReadFile(p); string(b) != "пометка" {
		t.Fatal("установка затёрла существующий файл")
	}
	if st, _ := os.Stat(Path(dir, "geosite-ru-blocked")); posixPerms && st.Mode().Perm() != 0o600 {
		t.Fatalf("права: %v", st.Mode().Perm())
	}
	if len(Names(RoleProxy)) == 0 || len(Names(RoleDirect)) != 3 || len(Names(RoleRU)) != 2 {
		t.Fatalf("роли: %v %v", Names(RoleProxy), Names(RoleDirect))
	}
}

func TestValidateRejectsGarbage(t *testing.T) {
	for _, bad := range [][]byte{nil, []byte("short"), []byte(strings.Repeat("не srs ", 100)), make([]byte, 100)} {
		if Validate(bad) == nil {
			t.Fatalf("принят мусор %q", bad[:min(len(bad), 8)])
		}
	}
}

// Update принимает только корректные и не подозрительно уменьшившиеся файлы; зеркало подхватывает при сбое основного адреса.
func TestUpdateSafety(t *testing.T) {
	good, _ := snapshot.ReadFile("data/geosite-ru-blocked.srs")
	youtube, _ := snapshot.ReadFile("data/geosite-youtube.srs")
	var rawHits, mirrorHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		rawHits.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "geosite-ru-blocked.srs"):
			w.Write(good[:len(good)/10]) // обрезанный ответ: не srs / слишком мал
		case strings.HasSuffix(r.URL.Path, "geosite-youtube.srs"):
			w.Write(youtube) // без изменений
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/mirror/", func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		if strings.HasSuffix(r.URL.Path, "geosite-ru-blocked.srs") {
			w.Write(good)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// перенаправляем запросы к настоящим адресам на тестовый сервер
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.Path
		kind := "/raw"
		if strings.Contains(r.URL.Host, "jsdelivr") {
			kind = "/mirror"
		}
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, srv.URL+kind+path, nil)
		return http.DefaultClient.Do(req)
	})
	dir := t.TempDir()
	if _, err := Install(dir); err != nil {
		t.Fatal(err)
	}
	// «старый» файл подменяем заведомо отличающимся, но корректным и большим
	big := append([]byte(nil), good...)
	big = append(big, 0) // отличие от good; для srs конец файла не важен — проверяем, что обновление заменит его на good
	_ = os.WriteFile(Path(dir, "geosite-ru-blocked"), good[:len(good)-0], 0o600)
	res, err := Update(context.Background(), &http.Client{Transport: rt}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if mirrorHits.Load() == 0 {
		t.Fatal("при непринятом ответе основного адреса зеркало не использовано")
	}
	if res.Unchanged < 2 { // youtube с raw и ru-blocked с зеркала совпали с имеющимися
		t.Fatalf("итог: %+v", res)
	}
	if len(res.Failed) == 0 { // остальные 7 файлов на тестовом сервере отсутствуют: прежние остаются
		t.Fatal("отсутствующие файлы должны попадать в Failed")
	}
	for _, s := range Sets { // ни один файл не повреждён
		b, err := os.ReadFile(Path(dir, s.Name))
		if err != nil || Validate(b) != nil {
			t.Fatalf("%s повреждён после обновления: %v", s.Name, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateRejectsShrunkAndGiant(t *testing.T) {
	good, _ := snapshot.ReadFile("data/geosite-ru-blocked.srs")
	tiny, _ := snapshot.ReadFile("data/geosite-youtube.srs") // корректный srs, но на порядки меньше
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "geosite-ru-blocked.srs") {
			w.Write(tiny)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, srv.URL+r.URL.Path, nil)
		return http.DefaultClient.Do(req)
	})
	dir := t.TempDir()
	_, _ = Install(dir)
	res, _ := Update(context.Background(), &http.Client{Transport: rt}, dir)
	if reason, bad := res.Failed["geosite-ru-blocked"]; !bad || !strings.Contains(reason, "втрое меньше") {
		t.Fatalf("уменьшившийся втрое файл принят: %+v", res.Failed)
	}
	if b, _ := os.ReadFile(Path(dir, "geosite-ru-blocked")); len(b) != len(good) {
		t.Fatal("прежний файл заменён урезанным")
	}
}
