package core

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"brod/buildinfo"
	"brod/ota"
	"brod/store"
	"brod/updater"
)

// Обновление приложения (OTA, этап 2a и 2b): раз в сутки ядро забирает подписанный манифест релизов (пакет ota), сравнивает версию с
// buildinfo.Version, сообщает о новой и по команде скачивает файл своей платформы с проверкой размера и SHA-256 по манифесту. Установку
// ядро не делает: на Android она требует подтверждения системы, на Linux и Windows — прав.

// SetUpdateCheck — проверять ли обновления приложения (1 по умолчанию или 0).
const SetUpdateCheck = "update.check"

// SetUpdateURL — свой адрес манифеста релизов вместо общего (для проверки обновления с локального сервера и закрытых сборок): https или
// http на адрес самой машины. Подпись манифеста проверяется так же, ключом релизов; пусто — общий адрес.
const SetUpdateURL = "update.url"

func updateURLSetting(v string) error {
	if len(v) > 512 {
		return fmt.Errorf("%w: адрес длиннее 512 знаков", ErrInvalid)
	}
	if v == "" {
		return nil
	}
	pu, err := url.Parse(v)
	if err != nil || pu.Host == "" || pu.User != nil || pu.RawQuery != "" || pu.Fragment != "" ||
		!(pu.Scheme == "https" || (pu.Scheme == "http" && ota.IsLoopbackHost(pu.Hostname()))) {
		return fmt.Errorf("%w: нужен https-адрес (http — только на этот же компьютер)", ErrInvalid)
	}
	return nil
}

const (
	settingReleasesVersion = "update.releases.version" // наибольшая виденная версия публикации манифеста (защита от отката)
	settingUpdateAnnounced = "update.announced"        // версия приложения, о которой уже сообщили
)

// Где издатель публикует манифест (releases.json и releases.json.sig): тот же публичный репозиторий, что у списка источников.
var defaultUpdateURLs = []string{
	"https://raw.githubusercontent.com/dqzaexxpess/brod-releases/main/" + ota.ReleasesFile,
	"https://cdn.jsdelivr.net/gh/dqzaexxpess/brod-releases@main/" + ota.ReleasesFile,
	// второй хостинг, независимый от GitHub: тот же подписанный документ в репозитории GitLab
	"https://gitlab.com/misavladimirov120/brod-releases/-/raw/main/" + ota.ReleasesFile,
}

// Состояния скачивания.
const (
	DLNone        = "none"
	DLDownloading = "downloading"
	DLReady       = "ready"
	DLFailed      = "failed"
)

// UpdateDownload — состояние скачивания файла обновления.
type UpdateDownload struct {
	State string `json:"state"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
	Path  string `json:"path,omitempty"`  // файл проверен и лежит здесь (state=ready)
	Error string `json:"error,omitempty"` // state=failed
}

// UpdateInfo — что известно об обновлении.
type UpdateInfo struct {
	Current    string          `json:"current"`  // версия запущенного ядра
	Platform   string          `json:"platform"` // ключ платформы в манифесте
	Checked    time.Time       `json:"checked,omitzero"`
	Error      string          `json:"error,omitempty"` // последняя проверка не удалась
	Available  bool            `json:"available"`       // в манифесте есть более новая версия с файлом для этой платформы
	App        string          `json:"app,omitempty"`
	Notes      string          `json:"notes,omitempty"`
	Size       int64           `json:"size,omitempty"`
	SHA256     string          `json:"sha256,omitempty"`
	URL        string          `json:"url,omitempty"`
	Download   UpdateDownload  `json:"download"`
	Metered    bool            `json:"metered,omitempty"`    // текущая сеть платная: скачивание файла требует согласия
	Installing bool            `json:"installing,omitempty"` // установка запущена: демон сейчас завершится
	Last       *updater.Result `json:"last,omitempty"`       // итог последней установки (после перезапуска)
}

var fileExtRe = regexp.MustCompile(`^(\.[a-z0-9]{1,8}){1,2}$`)

func (c *Core) appVersion() string {
	if c.cfg.AppVersion != "" {
		return c.cfg.AppVersion
	}
	return buildinfo.Version
}

func (c *Core) updatePlatform() string {
	if c.cfg.UpdatePlatform != "" {
		return c.cfg.UpdatePlatform
	}
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "android-arm64", "linux-amd64", "windows-amd64":
		return runtime.GOOS + "-" + runtime.GOARCH
	}
	return ""
}

// updateOverride — адрес манифеста из настройки update.url ("" — не задан).
func (c *Core) updateOverride() string {
	v, _, _ := c.st.Setting(SetUpdateURL)
	if updateURLSetting(v) != nil {
		return ""
	}
	return v
}

func (c *Core) updateEnabled() bool {
	v, ok, _ := c.st.Setting(SetUpdateCheck)
	return !ok || v != "0"
}

// UpdateInfo возвращает текущее состояние (из памяти; сеть не трогает).
func (c *Core) UpdateInfo() UpdateInfo {
	c.updMu.Lock()
	defer c.updMu.Unlock()
	u := c.upd
	u.Current, u.Platform = c.appVersion(), c.updatePlatform()
	u.Installing, u.Last = c.updInstalling, c.updLast
	u.Metered = c.meteredNow()
	if u.Download.State == "" {
		u.Download.State = DLNone
	}
	return u
}

// CheckUpdate забирает манифест и обновляет состояние. Сначала прямой путь, при сбое — через вход приложений (активный узел).
func (c *Core) CheckUpdate(ctx context.Context, initiator string) (UpdateInfo, error) {
	urls := c.cfg.UpdateURLs
	if len(urls) == 0 {
		urls = defaultUpdateURLs
	}
	if o := c.updateOverride(); o != "" {
		urls = []string{o}
	}
	key := []ed25519.PublicKey{c.cfg.ReleasesKey}
	if len(c.cfg.ReleasesKey) == 0 {
		key = ota.ReleasesKeys()
	}
	doc, sig, err := c.fetchOTA(ctx, urls, c.updateOverride() != "", c.releasesCheck(key))
	if errors.Is(err, errHTTPNotFound) {
		// релизов ещё не публиковали: это нормальное состояние, а не сбой (ни ошибки в журнале, ни красной строки в диалоге)
		c.updMu.Lock()
		c.upd.Checked, c.upd.Error = c.cfg.Now(), ""
		c.updMu.Unlock()
		return c.UpdateInfo(), nil
	}
	if err != nil {
		err = fmt.Errorf("манифест релизов: %w", err)
		c.setUpdateError(err)
		return c.UpdateInfo(), err
	}
	return c.applyUpdateManifest(doc, sig, key, initiator)
}

// releasesVersionKey — ключ счётчика виденной публикации: у адреса-переопределения update.url он свой, чтобы номер тестового манифеста
// не считался «откатом» для первого настоящего релиза.
func (c *Core) releasesVersionKey() string {
	if c.updateOverride() != "" {
		return settingReleasesVersion + ".override"
	}
	return settingReleasesVersion
}

// releasesCheck — проверка манифеста для fetchOTA: подпись, строгий разбор и публикация не старее виденной; негодное зеркало пропускается.
func (c *Core) releasesCheck(key []ed25519.PublicKey) otaCheck {
	return func(doc, sig []byte) error {
		if err := ota.VerifyAny(key, doc, sig); err != nil {
			return err
		}
		d, err := ota.ParseReleasesWith(doc, c.updateOverride() != "")
		if err != nil {
			return err
		}
		seen := 0
		if v, ok, _ := c.st.Setting(c.releasesVersionKey()); ok {
			seen, _ = strconv.Atoi(v)
		}
		if d.Version < seen {
			return fmt.Errorf("публикация %d старее уже виденной %d (откат)", d.Version, seen)
		}
		return nil
	}
}

func (c *Core) setUpdateError(err error) {
	c.updMu.Lock()
	c.upd.Checked, c.upd.Error = c.cfg.Now(), err.Error()
	c.updMu.Unlock()
}

// applyUpdateManifest принимает манифест только с верной подписью, строгим разбором и версией публикации не ниже уже виденной.
func (c *Core) applyUpdateManifest(doc, sig []byte, key []ed25519.PublicKey, initiator string) (UpdateInfo, error) {
	fail := func(err error) (UpdateInfo, error) {
		c.setUpdateError(err)
		return c.UpdateInfo(), err
	}
	if err := ota.VerifyAny(key, doc, sig); err != nil {
		return fail(fmt.Errorf("манифест релизов отвергнут: %w", err))
	}
	d, err := ota.ParseReleasesWith(doc, c.updateOverride() != "")
	if err != nil {
		return fail(fmt.Errorf("манифест релизов отвергнут: %w", err))
	}
	seen := 0
	if v, ok, _ := c.st.Setting(c.releasesVersionKey()); ok {
		seen, _ = strconv.Atoi(v)
	}
	if d.Version < seen {
		return fail(fmt.Errorf("манифест релизов отвергнут: публикация %d старее уже виденной %d (откат)", d.Version, seen))
	}
	if d.Version > seen {
		if err := c.st.SetSetting(c.releasesVersionKey(), strconv.Itoa(d.Version)); err != nil {
			return fail(err)
		}
	}
	asset, has := d.Release.Assets[c.updatePlatform()]
	avail := has && d.Release.NewerThan(c.appVersion())
	if !avail && has {
		c.cleanUpdates() // установленная версия уже не старше манифеста: скачанный раньше файл больше не нужен
	}
	c.updMu.Lock()
	prev := c.upd
	c.upd = UpdateInfo{Checked: c.cfg.Now(), Available: avail, App: d.Release.App, Notes: d.Release.Notes}
	if avail {
		c.upd.Size, c.upd.SHA256, c.upd.URL = asset.Size, asset.SHA256, asset.URL
		c.updAsset = asset
		if prev.App == d.Release.App && prev.SHA256 == asset.SHA256 { // та же версия: скачанное и идущее скачивание сохраняются
			c.upd.Download = prev.Download
		}
	}
	c.updMu.Unlock()
	if avail && c.updatePlatform() == "linux-amd64" {
		c.saveReleaseManifest(doc, sig)
	}
	if avail {
		if v, _, _ := c.st.Setting(settingUpdateAnnounced); v != d.Release.App {
			_ = c.st.SetSetting(settingUpdateAnnounced, d.Release.App)
			c.bus.Publish(store.Event{Kind: EvUpdate, Initiator: initiator, Detail: detail(map[string]any{"app": d.Release.App, "current": c.appVersion()})})
		}
	}
	return c.UpdateInfo(), nil
}

// Подписанный манифест выпуска рядом со скачанным файлом: его вместе с архивом получает помощник от root (brod-helper), который сам
// проверяет подпись вшитым ключом и сверяет архив с манифестом. Доверия к этим файлам не требуется: без верной подписи помощник откажет.
// Отдельный каталог рядом с каталогом обновлений, а не в нём: finishDownload чистит каталог обновлений целиком, кроме скачанного файла.
// cleanUpdates убирает его вместе со скачанным, когда обновлений больше нет.
func (c *Core) releaseManifestDir() string { return filepath.Join(c.cfg.StateDir, "update-manifest") }

func (c *Core) releaseManifestPaths() (doc, sig string) {
	d := c.releaseManifestDir()
	return filepath.Join(d, ota.ReleasesFile), filepath.Join(d, ota.ReleasesSigFile)
}

// saveReleaseManifest записывает манифест и подпись (временный файл и переименование). Сбой записи не мешает проверке обновления:
// без этих файлов откажет только установка через помощника, с понятной ошибкой.
func (c *Core) saveReleaseManifest(doc, sig []byte) {
	if err := os.MkdirAll(c.releaseManifestDir(), 0o700); err != nil {
		return
	}
	dp, sp := c.releaseManifestPaths()
	for _, f := range []struct {
		path string
		b    []byte
	}{{dp, doc}, {sp, sig}} {
		tmp := f.path + ".tmp"
		if os.WriteFile(tmp, f.b, 0o600) != nil || os.Rename(tmp, f.path) != nil {
			_ = os.Remove(tmp)
			return
		}
	}
}

// cleanUpdates удаляет скачанные файлы обновлений (brod-*), рабочий каталог помощника и сохранённый манифест; итог установки и журнал остаются.
func (c *Core) cleanUpdates() {
	c.updMu.Lock()
	busy := c.upd.Download.State == DLDownloading || c.updInstalling
	c.updMu.Unlock()
	if busy {
		return
	}
	_ = os.RemoveAll(c.releaseManifestDir())
	dir := c.updatesDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "brod-") || e.Name() == "apply" {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// updateFile — имя и путь файла обновления: имя строится из версии и платформы, а не берётся из ответа сервера; расширение — из адреса и только безопасное.
func (c *Core) updateFile(app string, a ota.Asset) (dir, file string) {
	dir = filepath.Join(c.cfg.StateDir, "updates")
	ext := ".bin"
	if pu, err := url.Parse(a.URL); err == nil {
		b := strings.ToLower(path.Base(pu.Path))
		for _, e := range []string{".tar.gz", ".tar.xz", ".zip", ".apk", ".exe", ".msi", ".deb", ".tgz"} {
			if strings.HasSuffix(b, e) {
				ext = e
				break
			}
		}
	}
	if !fileExtRe.MatchString(ext) {
		ext = ".bin"
	}
	return dir, filepath.Join(dir, "brod-"+app+"-"+c.updatePlatform()+ext)
}

func (c *Core) meteredNow() bool { return c.cfg.Metered != nil && c.cfg.Metered() }

// StartUpdateDownload запускает скачивание файла обновления своей платформы с проверкой по манифесту. Возвращает сразу: ход — в UpdateInfo.
// В платной сети (Config.Metered) без allowMetered ничего не качает: файл большой (десятки МБ), трафик пользователя не тратится молча.
func (c *Core) StartUpdateDownload(initiator string, allowMetered bool) (UpdateInfo, error) {
	c.updMu.Lock()
	if !c.upd.Available {
		c.updMu.Unlock()
		return c.UpdateInfo(), fmt.Errorf("%w: нет доступного обновления", ErrInvalid)
	}
	if c.upd.Download.State == DLDownloading || c.upd.Download.State == DLReady {
		c.updMu.Unlock()
		return c.UpdateInfo(), nil
	}
	if !allowMetered && c.meteredNow() {
		c.updMu.Unlock()
		return c.UpdateInfo(), ErrMetered
	}
	app, asset := c.upd.App, c.updAsset
	c.upd.Download = UpdateDownload{State: DLDownloading, Total: asset.Size}
	c.updMu.Unlock()
	ctx := c.baseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	c.goLoop("update-download", func() { c.downloadUpdate(ctx, app, asset, initiator) })
	return c.UpdateInfo(), nil
}

func (c *Core) setDownload(f func(*UpdateDownload)) {
	c.updMu.Lock()
	f(&c.upd.Download)
	c.updMu.Unlock()
}

func (c *Core) downloadUpdate(ctx context.Context, app string, a ota.Asset, initiator string) {
	dir, final := c.updateFile(app, a)
	failWith := func(err error) {
		c.setDownload(func(d *UpdateDownload) { *d = UpdateDownload{State: DLFailed, Total: a.Size, Error: err.Error()} })
		c.bus.Publish(store.Event{Kind: EvError, Initiator: initiator, Detail: "скачивание обновления: " + err.Error()})
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		failWith(err)
		return
	}
	// файл мог быть скачан раньше: проверка по манифесту вместо повторной загрузки
	if f, err := os.Open(final); err == nil {
		ok := ota.VerifyAsset(f, a) == nil
		f.Close()
		if ok {
			c.finishDownload(dir, final, a, initiator)
			return
		}
		_ = os.Remove(final)
	}
	part := final + ".part"
	_ = os.Remove(part)
	urls := []string{a.URL}
	if a.Mirror != "" {
		urls = append(urls, a.Mirror)
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	err := c.fetchAssetTo(dctx, urls, a, part)
	if err != nil {
		_ = os.Remove(part)
		if ctx.Err() != nil {
			c.setDownload(func(d *UpdateDownload) { *d = UpdateDownload{State: DLNone} })
			return
		}
		failWith(err)
		return
	}
	// ещё раз целиком по манифесту: размер и SHA-256 проверяет тот же код, что и в тестах пакета ota
	f, err := os.Open(part)
	if err == nil {
		err = ota.VerifyAsset(f, a)
		f.Close()
	}
	if err != nil {
		_ = os.Remove(part)
		failWith(fmt.Errorf("файл не прошёл проверку: %w", err))
		return
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		failWith(err)
		return
	}
	c.finishDownload(dir, final, a, initiator)
}

func (c *Core) finishDownload(dir, final string, a ota.Asset, initiator string) {
	if ents, err := os.ReadDir(dir); err == nil { // старые версии и недокачанное не копятся
		for _, e := range ents {
			if p := filepath.Join(dir, e.Name()); p != final {
				_ = os.Remove(p)
			}
		}
	}
	_ = os.Chmod(final, 0o600)
	c.setDownload(func(d *UpdateDownload) {
		*d = UpdateDownload{State: DLReady, Bytes: a.Size, Total: a.Size, Path: final}
	})
	c.bus.Publish(store.Event{Kind: EvUpdate, Initiator: initiator, Detail: detail(map[string]any{"downloaded": filepath.Base(final), "sha256": a.SHA256})})
}

// fetchAssetTo скачивает файл в dest: по очереди адреса (основной, зеркало), каждый сначала напрямую, затем через вход приложений.
func (c *Core) fetchAssetTo(ctx context.Context, urls []string, a ota.Asset, dest string) error {
	clients := []*http.Client{{Timeout: 0, Transport: &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second}}}
	if c.cfg.OTAClient != nil {
		clients[0] = c.cfg.OTAClient
	}
	if st, err := c.Status(); err == nil && st.Active != nil {
		addr, user, pass := c.AppProxy()
		u := &url.URL{Scheme: "http", User: url.UserPassword(user, pass), Host: addr}
		clients = append(clients, &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true, ResponseHeaderTimeout: 60 * time.Second}})
	}
	var last error
	for _, u := range urls {
		for i, hc := range clients {
			err := c.fetchAssetOnce(ctx, hc, u, a, dest)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if i == 1 {
				err = fmt.Errorf("через активный узел: %w", err)
			}
			last = err
		}
	}
	return last
}

func (c *Core) fetchAssetOnce(ctx context.Context, hc *http.Client, u string, a ota.Asset, dest string) error {
	pu, err := url.Parse(u)
	loopOK := c.cfg.UpdateAllowHTTP || (pu != nil && pu.Scheme == "http" && ota.IsLoopbackHost(pu.Hostname()) && c.updateOverride() != "")
	if err != nil || (pu.Scheme != "https" && !loopOK) {
		return fmt.Errorf("%s: нужен https-адрес", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := httpsOnlyRedirects(hc, loopOK).Do(req) // перенаправление на http отклоняется
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", pu.Host, resp.StatusCode)
	}
	if resp.ContentLength > a.Size {
		return fmt.Errorf("%s: файл больше заявленного в манифесте (%d > %d)", pu.Host, resp.ContentLength, a.Size)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	c.setDownload(func(d *UpdateDownload) { d.Bytes = 0 })
	_, err = io.Copy(&progressWriter{w: f, c: c}, io.LimitReader(resp.Body, a.Size+1))
	return err
}

// progressWriter считает записанное и обновляет состояние скачивания не чаще раза на 256 КиБ.
type progressWriter struct {
	w    io.Writer
	c    *Core
	n    int64
	last int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if p.n-p.last >= 256<<10 {
		p.last = p.n
		p.c.setDownload(func(d *UpdateDownload) { d.Bytes = p.n })
	}
	return n, err
}

// updateLoop раз в сутки проверяет обновления (первый раз через несколько минут после старта). Сбой пишется в журнал один раз на причину.
func (c *Core) updateLoop(ctx context.Context) {
	timer := time.NewTimer(c.cfg.UpdateFirstDelay)
	defer timer.Stop()
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if c.updateEnabled() {
			if _, err := c.CheckUpdate(ctx, "auto"); err != nil && ctx.Err() == nil {
				if msg := err.Error(); msg != lastErr {
					lastErr = msg
					c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: msg})
				}
			} else if err == nil {
				lastErr = ""
			}
		}
		timer.Reset(c.cfg.UpdateInterval)
	}
}
