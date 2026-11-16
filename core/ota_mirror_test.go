package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"brod/ota"
)

// pubServer отдаёт документ doc по /d и подпись по /d.sig.
func pubServer(doc, sig []byte) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/d":
			w.Write(doc)
		case "/d.sig":
			w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
}

func signDoc(priv ed25519.PrivateKey, doc []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, doc)))
}

func releasesDoc(t *testing.T, version int) []byte {
	t.Helper()
	doc, err := json.Marshal(ota.ReleasesDoc{Version: version, Release: ota.Release{App: "0.4.0", Notes: "n", Assets: map[string]ota.Asset{
		"linux-amd64": {URL: "https://example.org/brod.tar.gz", SHA256: strings.Repeat("a", 64), Size: 10}}}})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// устаревшее зеркало (публикация старее виденной) и зеркало с чужой подписью не блокируют свежее, стоящее дальше в списке.
func TestCheckUpdateSkipsStaleAndBadMirrors(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	stale := releasesDoc(t, 4)
	forged := releasesDoc(t, 9)
	fresh := releasesDoc(t, 6)
	s1 := pubServer(stale, signDoc(priv, stale))
	defer s1.Close()
	s2 := pubServer(forged, signDoc(other, forged))
	defer s2.Close()
	s3 := pubServer(fresh, signDoc(priv, fresh))
	defer s3.Close()
	c := cfgCore(t, func(cfg *Config) {
		cfg.UpdateURLs = []string{s1.URL + "/d", s2.URL + "/d", s3.URL + "/d"}
		cfg.ReleasesKey, cfg.OTAClient, cfg.AppVersion, cfg.UpdatePlatform = pub, s1.Client(), "0.3.0", "linux-amd64"
	})
	if err := c.st.SetSetting(settingReleasesVersion, "5"); err != nil {
		t.Fatal(err)
	}
	u, err := c.CheckUpdate(t.Context(), "test")
	if err != nil || u.Error != "" || !u.Available || u.App != "0.4.0" {
		t.Fatalf("свежее зеркало не принято: %v, %+v", err, u)
	}
	if v, _, _ := c.st.Setting(settingReleasesVersion); v != "6" {
		t.Fatalf("виденная публикация %q, нужна 6", v)
	}
}

// Все зеркала плохие: ошибка про откат остаётся видимой (её не заслоняет последняя 404).
func TestCheckUpdateAllMirrorsStaleKeepsRollbackError(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	stale := releasesDoc(t, 4)
	s1 := pubServer(stale, signDoc(priv, stale))
	defer s1.Close()
	s2 := pubServer(nil, nil) // документа нет: 404
	defer s2.Close()
	c := cfgCore(t, func(cfg *Config) {
		cfg.UpdateURLs = []string{s1.URL + "/d", s2.URL + "/nope"}
		cfg.ReleasesKey, cfg.OTAClient, cfg.AppVersion, cfg.UpdatePlatform = pub, s1.Client(), "0.3.0", "linux-amd64"
	})
	_ = c.st.SetSetting(settingReleasesVersion, "5")
	_, err := c.CheckUpdate(t.Context(), "test")
	if err == nil || !strings.Contains(err.Error(), "откат") {
		t.Fatalf("ожидалась ошибка отката, получено: %v", err)
	}
}

// Для списка источников: старое зеркало первым, свежее вторым.
func TestOTASourcesSkipsStaleMirror(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mk := func(v int) []byte {
		return []byte(`{"version":` + strconv.Itoa(v) + `,"sources":[{"name":"managed/m","url":"https://m.example/a"}]}`)
	}
	old, fresh := mk(4), mk(6)
	s1 := pubServer(old, signDoc(priv, old))
	defer s1.Close()
	s2 := pubServer(fresh, signDoc(priv, fresh))
	defer s2.Close()
	c := cfgCore(t, func(cfg *Config) {
		cfg.OTASourcesURLs = []string{s1.URL + "/d", s2.URL + "/d"}
		cfg.OTAKey, cfg.OTAClient = pub, s1.Client()
	})
	_ = c.st.SetSetting(settingOTAVersion, "5")
	res, err := c.UpdateOTASources(t.Context(), "test")
	if err != nil || !res.Applied || res.Version != 6 {
		t.Fatalf("итог %+v, ошибка %v", res, err)
	}
}

// перенаправление с https на http не выполняется ни для документа, ни для файла обновления.
func TestRedirectToHTTPIsRefused(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		w.Write([]byte("данные"))
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer tls.Close()
	c := cfgCore(t, func(cfg *Config) { cfg.OTAClient = tls.Client() })

	if _, _, err := c.fetchOTA(t.Context(), []string{tls.URL + "/d"}, false, nil); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("документ: перенаправление на http принято или не та ошибка: %v", err)
	}
	a := ota.Asset{URL: tls.URL + "/f", SHA256: strings.Repeat("a", 64), Size: 100}
	err := c.fetchAssetOnce(t.Context(), tls.Client(), tls.URL+"/f", a, t.TempDir()+"/f")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("файл: перенаправление на http принято или не та ошибка: %v", err)
	}
	if n := plainHits.Load(); n != 0 {
		t.Fatalf("на http-сервер дошло запросов: %d", n)
	}
}

// номер публикации тестового манифеста с update.url не попадает в счётчик боевого адреса и не вызывает «откат» у первого настоящего релиза.
func TestUpdateOverrideHasOwnReleaseCounter(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte("x")
	if err := r.c.SetSetting(SetUpdateURL, "http://127.0.0.1:9/releases.json", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.apply(100, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	if _, err := r.apply(50, "0.4.0", data); err == nil {
		t.Fatal("внутри одного адреса откат должен отвергаться")
	}
	if err := r.c.SetSetting(SetUpdateURL, "", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.apply(1, "0.4.0", data); err != nil {
		t.Fatalf("первый боевой релиз отвергнут из-за тестового манифеста: %v", err)
	}
}
