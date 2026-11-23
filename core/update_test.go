package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"brod/ota"
	"brod/updater"
)

type updRig struct {
	t    *testing.T
	c    *Core
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newUpdRig(t *testing.T, app string) *updRig {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := &updRig{t: t, pub: pub, priv: priv}
	r.c = cfgCore(t, func(cfg *Config) {
		cfg.AppVersion, cfg.UpdatePlatform, cfg.ReleasesKey, cfg.UpdateAllowHTTP = app, "linux-amd64", pub, true
	})
	return r
}

// manifest собирает и подписывает манифест: версия публикации pub, приложение app, файл linux-amd64 с данными data.
func (r *updRig) manifest(pubVer int, app string, data []byte, platforms ...string) (doc, sig []byte) {
	sum := sha256.Sum256(data)
	assets := map[string]ota.Asset{}
	if len(platforms) == 0 {
		platforms = []string{"linux-amd64"}
	}
	for _, p := range platforms {
		assets[p] = ota.Asset{URL: "https://example.org/brod-" + app + ".tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	}
	doc, _ = json.Marshal(ota.ReleasesDoc{Version: pubVer, Release: ota.Release{App: app, Notes: "что нового", Assets: assets}})
	sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, doc)))
	return doc, sig
}

func (r *updRig) apply(pubVer int, app string, data []byte, platforms ...string) (UpdateInfo, error) {
	doc, sig := r.manifest(pubVer, app, data, platforms...)
	return r.c.applyUpdateManifest(doc, sig, []ed25519.PublicKey{r.pub}, "test")
}

func TestUpdateManifestAvailabilityAndAnnounce(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte("release-bytes")
	if u, err := r.apply(1, "0.3.0", data); err != nil || u.Available {
		t.Fatalf("та же версия: %v, %+v", err, u)
	}
	if u, err := r.apply(2, "0.4.0", data, "windows-amd64"); err != nil || u.Available {
		t.Fatalf("нет файла для платформы: %v, %+v", err, u)
	}
	u, err := r.apply(3, "0.4.0", data)
	if err != nil || !u.Available || u.App != "0.4.0" || u.Notes != "что нового" || u.Size != int64(len(data)) || u.Download.State != DLNone || u.Current != "0.3.0" || u.Platform != "linux-amd64" {
		t.Fatalf("новая версия: %v, %+v", err, u)
	}
	// о версии сообщается один раз, сколько бы раз ни проверяли
	if _, err := r.apply(4, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	if n := len(eventsOf(r.c, EvUpdate)); n != 1 {
		t.Fatalf("событий update %d, ожидалось 1", n)
	}
	if _, err := r.apply(5, "0.5.0", data); err != nil {
		t.Fatal(err)
	}
	if n := len(eventsOf(r.c, EvUpdate)); n != 2 {
		t.Fatalf("о новой версии 0.5.0 тоже должно сообщиться: событий %d", n)
	}
}

func TestUpdateManifestRejections(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte("x")
	doc, sig := r.manifest(5, "0.4.0", data)
	// чужая подпись
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	badSig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, doc)))
	if u, err := r.c.applyUpdateManifest(doc, badSig, []ed25519.PublicKey{r.pub}, "test"); err == nil || u.Available || !strings.Contains(u.Error, "отвергнут") {
		t.Fatalf("чужая подпись принята: %v, %+v", err, u)
	}
	// подпись другого документа
	other, _ := r.manifest(5, "0.9.0", data)
	if _, err := r.c.applyUpdateManifest(other, sig, []ed25519.PublicKey{r.pub}, "test"); err == nil {
		t.Fatal("подпись не от этого документа принята")
	}
	// разбор: неизвестное поле
	evil := []byte(strings.Replace(string(doc), `"version":5`, `"version":5,"evil":1`, 1))
	evilSig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, evil)))
	if _, err := r.c.applyUpdateManifest(evil, evilSig, []ed25519.PublicKey{r.pub}, "test"); err == nil {
		t.Fatal("манифест с неизвестным полем принят")
	}
	// принять честный, затем старее виденного — откат
	if u, err := r.c.applyUpdateManifest(doc, sig, []ed25519.PublicKey{r.pub}, "test"); err != nil || !u.Available {
		t.Fatalf("честный манифест: %v, %+v", err, u)
	}
	if _, err := r.apply(4, "0.4.0", data); err == nil || !strings.Contains(err.Error(), "откат") {
		t.Fatalf("публикация 4 после 5 должна отвергаться как откат: %v", err)
	}
	if u := r.c.UpdateInfo(); !strings.Contains(u.Error, "откат") {
		t.Fatalf("причина не записана в состояние: %+v", u)
	}
	// та же публикация допустима (повторная проверка)
	if _, err := r.apply(5, "0.4.0", data); err != nil {
		t.Fatalf("повторная проверка той же публикации: %v", err)
	}
}

func waitDownload(t *testing.T, c *Core, want string) UpdateInfo {
	t.Helper()
	var u UpdateInfo
	waitFor(t, "скачивание: "+want, func() bool { u = c.UpdateInfo(); return u.Download.State == want })
	return u
}

func TestUpdateDownloadVerifiesAndCleans(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte(strings.Repeat("brod-release-", 50_000)) // ~650 КБ: несколько отчётов о ходе
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write(data) }))
	defer srv.Close()
	if _, err := r.apply(1, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	r.c.updMu.Lock()
	r.c.updAsset.URL = srv.URL + "/brod-0.4.0.tar.gz"
	r.c.updMu.Unlock()
	// старое и недокачанное в каталоге исчезнет
	dir := filepath.Join(r.c.cfg.StateDir, "updates")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "brod-0.2.0-linux-amd64.tar.gz"), []byte("старое"), 0o600)
	if u, err := r.c.StartUpdateDownload("test", false); err != nil || (u.Download.State != DLDownloading && u.Download.State != DLReady) {
		t.Fatalf("старт: %v, %+v", err, u.Download)
	}
	u := waitDownload(t, r.c, DLReady)
	got, err := os.ReadFile(u.Download.Path)
	if err != nil || string(got) != string(data) || u.Download.Bytes != int64(len(data)) {
		t.Fatalf("файл: %v, %d байт, состояние %+v", err, len(got), u.Download)
	}
	if filepath.Base(u.Download.Path) != "brod-0.4.0-linux-amd64.tar.gz" {
		t.Fatalf("имя файла строится из версии и платформы: %s", u.Download.Path)
	}
	if st, _ := os.Stat(u.Download.Path); posixPerms && st.Mode().Perm() != 0o600 {
		t.Fatalf("права файла %v", st.Mode().Perm())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("в каталоге обновлений должен остаться один файл: %v", ents)
	}
	if n := len(eventsOf(r.c, EvUpdate)); n != 2 { // «доступна» и «скачан»
		t.Fatalf("событий update %d, ожидалось 2", n)
	}
	// повторный запуск при готовом файле ничего не качает
	before := hits.Load()
	if u2, err := r.c.StartUpdateDownload("test", false); err != nil || u2.Download.State != DLReady || hits.Load() != before {
		t.Fatalf("повторный запуск: %v, %+v, запросов %d→%d", err, u2.Download, before, hits.Load())
	}
	// новая проверка той же версии не сбрасывает готовое скачивание
	if _, err := r.apply(2, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	if u3 := r.c.UpdateInfo(); u3.Download.State != DLReady || u3.Download.Path == "" {
		t.Fatalf("после повторной проверки: %+v", u3.Download)
	}
}

func TestUpdateDownloadRejectsBadFiles(t *testing.T) {
	data := []byte(strings.Repeat("A", 100_000))
	cases := map[string]http.HandlerFunc{
		"подмена того же размера": func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(strings.Repeat("B", len(data)))) },
		"обрезанный файл":         func(w http.ResponseWriter, _ *http.Request) { w.Write(data[:len(data)/2]) },
		"файл больше заявленного": func(w http.ResponseWriter, _ *http.Request) { w.Write(append(append([]byte(nil), data...), 'x')) },
		"заявленный размер больше": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "999999999")
			w.Write(data[:10])
		},
		"HTTP 404": func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			r := newUpdRig(t, "0.3.0")
			srv := httptest.NewServer(h)
			defer srv.Close()
			if _, err := r.apply(1, "0.4.0", data); err != nil {
				t.Fatal(err)
			}
			r.c.updMu.Lock()
			r.c.updAsset.URL = srv.URL + "/f.tar.gz"
			r.c.updMu.Unlock()
			if _, err := r.c.StartUpdateDownload("test", false); err != nil {
				t.Fatal(err)
			}
			u := waitDownload(t, r.c, DLFailed)
			if u.Download.Error == "" || u.Download.Path != "" {
				t.Fatalf("состояние: %+v", u.Download)
			}
			// ни проверенного файла, ни недокачанного не остаётся
			dir := filepath.Join(r.c.cfg.StateDir, "updates")
			if ents, _ := os.ReadDir(dir); len(ents) != 0 {
				t.Fatalf("после отказа в каталоге остались файлы: %v", ents)
			}
		})
	}
}

func TestUpdateDownloadMirrorAndExistingFile(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte(strings.Repeat("Z", 70_000))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "нет", 503) }))
	defer bad.Close()
	var good atomic.Int32
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { good.Add(1); w.Write(data) }))
	defer ok.Close()
	if _, err := r.apply(1, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	r.c.updMu.Lock()
	r.c.updAsset.URL, r.c.updAsset.Mirror = bad.URL+"/a.apk", ok.URL+"/a.apk"
	r.c.updMu.Unlock()
	if _, err := r.c.StartUpdateDownload("test", false); err != nil {
		t.Fatal(err)
	}
	u := waitDownload(t, r.c, DLReady)
	if good.Load() != 1 || !strings.HasSuffix(u.Download.Path, ".tar.gz") && !strings.HasSuffix(u.Download.Path, ".apk") {
		t.Fatalf("зеркало: запросов %d, путь %s", good.Load(), u.Download.Path)
	}
	// новое ядро (перезапуск) нашло уже проверенный файл на диске и ничего не качает
	r2 := newUpdRig(t, "0.3.0")
	if _, err := r2.apply(1, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	_, final := r2.c.updateFile("0.4.0", r2.c.updAsset)
	_ = os.MkdirAll(filepath.Dir(final), 0o700)
	if err := os.WriteFile(final, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r2.c.updMu.Lock()
	r2.c.updAsset.URL = bad.URL + "/never.tar.gz" // скачивать не придётся (расширение то же, значит и имя файла то же)
	r2.c.updMu.Unlock()
	if _, err := r2.c.StartUpdateDownload("test", false); err != nil {
		t.Fatal(err)
	}
	waitDownload(t, r2.c, DLReady)
	// повреждённый файл на диске не принимается и перекачивается
	r3 := newUpdRig(t, "0.3.0")
	if _, err := r3.apply(1, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	_, final3 := r3.c.updateFile("0.4.0", r3.c.updAsset)
	_ = os.MkdirAll(filepath.Dir(final3), 0o700)
	_ = os.WriteFile(final3, []byte("повреждён"), 0o600)
	r3.c.updMu.Lock()
	r3.c.updAsset.URL = ok.URL + "/a.tar.gz"
	r3.c.updMu.Unlock()
	if _, err := r3.c.StartUpdateDownload("test", false); err != nil {
		t.Fatal(err)
	}
	u3 := waitDownload(t, r3.c, DLReady)
	if b, _ := os.ReadFile(u3.Download.Path); string(b) != string(data) {
		t.Fatal("повреждённый файл не заменён")
	}
}

func TestStartUpdateDownloadWithoutUpdate(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	if _, err := r.c.StartUpdateDownload("test", false); err == nil {
		t.Fatal("без доступного обновления скачивание запускаться не должно")
	}
	if _, err := r.apply(1, "0.3.0", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.StartUpdateDownload("test", false); err == nil {
		t.Fatal("та же версия: скачивать нечего")
	}
}

// Репозиторий достижим, но манифеста в нём нет (релизов ещё не публиковали): не ошибка, а «обновлений нет»; через узел второй раз не спрашиваем.
func TestCheckUpdateNoManifestIsNotAnError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) }))
	defer srv.Close()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c := cfgCore(t, func(cfg *Config) {
		cfg.UpdateURLs, cfg.ReleasesKey, cfg.OTAClient, cfg.AppVersion, cfg.UpdatePlatform = []string{srv.URL + "/releases.json"}, pub, srv.Client(), "0.3.0", "linux-amd64"
	})
	u, err := c.CheckUpdate(t.Context(), "test")
	if err != nil || u.Error != "" || u.Available || u.Checked.IsZero() {
		t.Fatalf("404 на манифест: %v, %+v", err, u)
	}
	if n := len(eventsOf(c, EvError)); n != 0 {
		t.Fatalf("в журнал записано ошибок: %d", n)
	}
	// а недоступный сервер остаётся ошибкой
	c2 := cfgCore(t, func(cfg *Config) {
		cfg.UpdateURLs, cfg.ReleasesKey, cfg.OTAClient, cfg.AppVersion = []string{"https://127.0.0.1:9/releases.json"}, pub, srv.Client(), "0.3.0"
	})
	if u2, err := c2.CheckUpdate(t.Context(), "test"); err == nil || u2.Error == "" {
		t.Fatalf("недоступный сервер должен быть ошибкой: %v, %+v", err, u2)
	}
}

// update.url: манифест и файл с локального http-сервера. Подпись обязательна, а http допускается только на адрес этой машины.
func TestUpdateURLLoopbackHTTP(t *testing.T) {
	data := []byte("apk-bytes-0.4.0")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256(data)
	var base string
	signed := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases.json", "/releases.json.sig":
			doc, _ := json.Marshal(ota.ReleasesDoc{Version: 1, Release: ota.Release{App: "0.4.0", Notes: "тест", Assets: map[string]ota.Asset{
				"android-arm64": {URL: base + "/brod.apk", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}}}})
			if r.URL.Path == "/releases.json" {
				w.Write(doc)
				return
			}
			key := priv
			if !signed {
				_, key, _ = ed25519.GenerateKey(rand.Reader)
			}
			fmt.Fprint(w, base64.StdEncoding.EncodeToString(ed25519.Sign(key, doc)))
		case "/brod.apk":
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base = srv.URL
	c := cfgCore(t, func(cfg *Config) {
		cfg.AppVersion, cfg.UpdatePlatform, cfg.ReleasesKey = "0.3.0-android", "android-arm64", pub
	})
	// без update.url http не принимается, даже на loopback
	if _, _, err := c.fetchOTA(t.Context(), []string{srv.URL + "/releases.json"}, false, nil); err == nil {
		t.Fatal("http без update.url должен отвергаться")
	}
	for _, bad := range []string{"http://example.org/r.json", "ftp://127.0.0.1/r.json", "http://u:p@127.0.0.1/r.json", "https://example.org/r.json?x=1", "127.0.0.1/r.json"} {
		if err := c.SetSetting(SetUpdateURL, bad, "test"); err == nil {
			t.Errorf("адрес %q принят", bad)
		}
	}
	if err := c.SetSetting(SetUpdateURL, srv.URL+"/releases.json", "test"); err != nil {
		t.Fatal(err)
	}
	signed = false
	if u, err := c.CheckUpdate(t.Context(), "test"); err == nil || u.Available {
		t.Fatalf("чужая подпись принята: %v, %+v", err, u)
	}
	signed = true
	u, err := c.CheckUpdate(t.Context(), "test")
	if err != nil || !u.Available || u.App != "0.4.0" {
		t.Fatalf("проверка с локального сервера: %v, %+v", err, u)
	}
	if _, err := c.StartUpdateDownload("test", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.UpdateInfo().Download.State == DLDownloading && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if d := c.UpdateInfo().Download; d.State != DLReady {
		t.Fatalf("скачивание: %+v", d)
	}
	// сняли update.url — loopback-http снова не принимается
	if err := c.SetSetting(SetUpdateURL, "", "test"); err != nil {
		t.Fatal(err)
	}
	if c.updateOverride() != "" {
		t.Fatal("update.url не снят")
	}
}

// ---- установка на Linux и Windows ----

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func linuxReleaseFiles(version string) map[string]string {
	f := map[string]string{"BROD-RELEASE": version + " linux-amd64\n"}
	for _, n := range []string{"brod", "brodd", "brodctl", "data/flutter_assets/AssetManifest.bin", "lib/libflutter_linux_gtk.so"} {
		f[n] = "x"
	}
	return f
}

type instRig struct {
	c       *Core
	root    string
	archive string
	specs   chan InstallSpec
	quit    chan struct{}
}

func newInstRig(t *testing.T, platform string, files map[string]string) *instRig {
	t.Helper()
	data := tarGz(t, files)
	sum := sha256.Sum256(data)
	r := &instRig{root: t.TempDir(), specs: make(chan InstallSpec, 1), quit: make(chan struct{}, 1)}
	target := filepath.Join(r.root, "opt", "brod")
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "brod"), []byte("old"), 0o755)
	r.c = cfgCore(t, func(cfg *Config) {
		cfg.AppVersion, cfg.UpdatePlatform, cfg.InstallDir = "0.3.0", platform, target
		cfg.UpdateSpawn = func(s InstallSpec) error { r.specs <- s; return nil }
		cfg.OnUpdateInstall = func() { r.quit <- struct{}{} }
	})
	r.archive = filepath.Join(r.c.updatesDir(), "brod-0.4.0-"+platform+".tar.gz")
	os.MkdirAll(r.c.updatesDir(), 0o700)
	os.WriteFile(r.archive, data, 0o600)
	asset := ota.Asset{URL: "https://example.org/brod-0.4.0.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	r.c.updMu.Lock()
	r.c.updAsset = asset
	r.c.upd = UpdateInfo{Available: true, App: "0.4.0", Download: UpdateDownload{State: DLReady, Path: r.archive, Total: asset.Size, Bytes: asset.Size}}
	r.c.updMu.Unlock()
	return r
}

func TestInstallUpdateStagesVerifiedReleaseAndStartsHelper(t *testing.T) {
	r := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.4.0"))
	info, err := r.c.InstallUpdate("test", 4242)
	if err != nil || !info.Installing {
		t.Fatalf("установка: %v %+v", err, info)
	}
	s := <-r.specs
	if s.Platform != "linux-amd64" || s.From != "0.3.0" || s.To != "0.4.0" || s.Target != filepath.Join(r.root, "opt", "brod") || s.Stage != s.Target+".new" {
		t.Fatalf("задание: %+v", s)
	}
	if len(s.WaitPIDs) != 2 || s.WaitPIDs[0] != os.Getpid() || s.WaitPIDs[1] != 4242 {
		t.Fatalf("ждать: %v", s.WaitPIDs)
	}
	if err := updater.CheckLayout(s.Stage, "linux-amd64", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Stage + "-pkg.tar.gz"); err == nil {
		t.Fatal("копия архива осталась")
	}
	select {
	case <-r.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("демону не сказали завершиться")
	}
	if _, err := r.c.InstallUpdate("test", 0); err == nil {
		t.Fatal("повторная установка запущена")
	}
}

func TestInstallUpdateRefusals(t *testing.T) {
	// файл подменён после скачивания
	r := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.4.0"))
	os.WriteFile(r.archive, tarGz(t, linuxReleaseFiles("0.4.0")), 0o600)
	b, _ := os.ReadFile(r.archive)
	b[len(b)/2] ^= 0xff
	os.WriteFile(r.archive, b, 0o600)
	if _, err := r.c.InstallUpdate("test", 0); err == nil || !strings.Contains(err.Error(), "проверку") {
		t.Fatalf("подменённый архив: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "opt", "brod.new")); err == nil {
		t.Fatal("stage остался после отказа")
	}
	if len(r.specs) != 0 || r.c.UpdateInfo().Installing {
		t.Fatal("помощник запущен или флаг установки остался")
	}
	// в архиве другая версия, чем в манифесте
	r2 := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.9.9"))
	if _, err := r2.c.InstallUpdate("test", 0); err == nil || !strings.Contains(err.Error(), "раскладка") {
		t.Fatalf("чужая версия: %v", err)
	}
	// Android ставит система
	r3 := newInstRig(t, "android-arm64", linuxReleaseFiles("0.4.0"))
	if _, err := r3.c.InstallUpdate("test", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("android: %v", err)
	}
	// не скачано
	r4 := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.4.0"))
	r4.c.updMu.Lock()
	r4.c.upd.Download = UpdateDownload{State: DLNone}
	r4.c.updMu.Unlock()
	if _, err := r4.c.InstallUpdate("test", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("не скачано: %v", err)
	}
	// запуск из каталога сборки: рядом нет приложения
	r5 := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.4.0"))
	r5.c.cfg.InstallDir = ""
	if _, err := r5.c.InstallUpdate("test", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("не установленная копия: %v", err)
	}
}

// После проверки, показавшей «обновлений нет», скачанные файлы чистятся, итог установки и журнал остаются.
func TestCleanUpdatesKeepsResult(t *testing.T) {
	r := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.4.0"))
	res := updater.ResultFile(r.c.cfg.StateDir)
	updater.WriteResult(res, updater.Result{To: "0.4.0", OK: true})
	r.c.updMu.Lock()
	r.c.upd = UpdateInfo{}
	r.c.updMu.Unlock()
	r.c.cleanUpdates()
	if _, err := os.Stat(r.archive); err == nil {
		t.Fatal("скачанный файл остался")
	}
	if _, ok := updater.ReadResult(res); !ok {
		t.Fatal("итог установки удалён")
	}
}

// В платной сети файл не качается без согласия: ни одного запроса, состояние скачивания не меняется; с согласием работает как обычно.
func TestUpdateDownloadNeedsConsentOnMeteredNetwork(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte(strings.Repeat("brod-release-", 1000))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write(data) }))
	defer srv.Close()
	var metered atomic.Bool
	metered.Store(true)
	r.c.cfg.Metered = metered.Load
	if _, err := r.apply(1, "0.4.0", data); err != nil {
		t.Fatal(err)
	}
	r.c.updMu.Lock()
	r.c.updAsset.URL = srv.URL + "/brod-0.4.0.tar.gz"
	r.c.updMu.Unlock()
	u, err := r.c.StartUpdateDownload("test", false)
	if !errors.Is(err, ErrMetered) || !u.Metered || u.Download.State != DLNone || hits.Load() != 0 {
		t.Fatalf("без согласия: %v, %+v, запросов %d", err, u, hits.Load())
	}
	if _, err := r.c.StartUpdateDownload("test", true); err != nil {
		t.Fatalf("с согласием: %v", err)
	}
	waitDownload(t, r.c, DLReady)
	// безлимитная сеть: согласие не нужно
	metered.Store(false)
	if u := r.c.UpdateInfo(); u.Metered {
		t.Fatalf("metered в безлимитной сети: %+v", u)
	}
}
