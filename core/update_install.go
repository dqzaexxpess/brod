package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"brod/ota"
	"brod/store"
	"brod/updater"
)

// Установка обновления на Linux и Windows. Ядро только готовит и проверяет: копирует скачанный архив в каталог установки (на Windows он
// доступен лишь администраторам: пользователь не успеет подменить файл между проверкой и установкой), заново сверяет размер и SHA-256 с
// подписанным манифестом уже у копии, распаковывает безопасным распаковщиком и проверяет раскладку. Заменять файлы работающей программы оно
// не может, поэтому запускает отдельного помощника (Linux: brodctl update-apply из копии прежней версии; Windows: install.ps1 из нового
// релиза) и завершается, а помощник дожидается выхода приложения и демона, ставит новое и запускает приложение снова.

// InstallSpec — задание помощнику.
type InstallSpec struct {
	Platform   string
	From, To   string
	Target     string // установленный каталог программы
	Stage      string // распакованный и проверенный релиз
	WaitPIDs   []int  // процессы, выхода которых нужно дождаться (демон и приложение)
	ResultFile string // куда помощник записывает итог
	LogFile    string
	// Linux: скачанный архив и подписанный манифест с подписью — для помощника от root, который сам ставит brodd.
	Archive, Manifest, Sig string
}

// Платформы, на которых ядро ставит обновление само (Android ставит система: диалог «Установить» в приложении).
func installablePlatform(p string) bool { return p == "linux-amd64" || p == "windows-amd64" }

func (c *Core) updatesDir() string { return filepath.Join(c.cfg.StateDir, "updates") }

// InstallUpdate готовит и запускает установку скачанного обновления. appPID — процесс приложения, выхода которого нужно дождаться (0 — нет).
// Возвращает сразу; демон после этого завершается (Config.OnUpdateInstall), итог — в UpdateInfo.Last после следующего запуска.
func (c *Core) InstallUpdate(initiator string, appPID int) (UpdateInfo, error) {
	plat := c.updatePlatform()
	if !installablePlatform(plat) {
		return c.UpdateInfo(), fmt.Errorf("%w: на этой платформе обновление устанавливает система", ErrInvalid)
	}
	c.updMu.Lock()
	if !c.upd.Available || c.upd.Download.State != DLReady || c.upd.Download.Path == "" {
		c.updMu.Unlock()
		return c.UpdateInfo(), fmt.Errorf("%w: сначала нужно скачать и проверить обновление", ErrInvalid)
	}
	if c.updInstalling {
		c.updMu.Unlock()
		return c.UpdateInfo(), fmt.Errorf("%w: установка уже идёт", ErrInvalid)
	}
	c.updInstalling = true
	app, asset, archive := c.upd.App, c.updAsset, c.upd.Download.Path
	c.updMu.Unlock()
	fail := func(err error) (UpdateInfo, error) {
		c.updMu.Lock()
		c.updInstalling = false
		c.updMu.Unlock()
		c.bus.Publish(store.Event{Kind: EvError, Initiator: initiator, Detail: "установка обновления: " + err.Error()})
		return c.UpdateInfo(), err
	}

	target, err := c.installDir()
	if err != nil {
		return fail(err)
	}
	stage, pkg, err := c.stageRelease(target, plat, app, archive, asset)
	if err != nil {
		return fail(err)
	}
	spec := InstallSpec{
		Platform: plat, From: c.appVersion(), To: app, Target: target, Stage: stage,
		WaitPIDs:   []int{os.Getpid()},
		ResultFile: updater.ResultFile(c.cfg.StateDir),
		LogFile:    filepath.Join(c.updatesDir(), "apply.log"),
	}
	if plat == "linux-amd64" {
		spec.Archive = archive
		spec.Manifest, spec.Sig = c.releaseManifestPaths()
	}
	if appPID > 0 {
		spec.WaitPIDs = append(spec.WaitPIDs, appPID)
	}
	spawn := c.cfg.UpdateSpawn
	if spawn == nil {
		spawn = c.spawnInstaller
	}
	if err := spawn(spec); err != nil {
		_ = os.RemoveAll(stage)
		_ = os.Remove(pkg)
		return fail(fmt.Errorf("запуск установщика: %w", err))
	}
	c.bus.Publish(store.Event{Kind: EvUpdate, Initiator: initiator, Detail: detail(map[string]any{"installing": app, "current": c.appVersion()})})
	if c.cfg.OnUpdateInstall != nil {
		time.AfterFunc(1500*time.Millisecond, c.cfg.OnUpdateInstall) // ответ API успевает уйти
	}
	return c.UpdateInfo(), nil
}

// installDir — каталог установленной программы: тот, где лежит запущенный brodd; приложение лежит рядом. Запуск из каталога сборки
// (разработка) установкой не считается: заменять там нечего.
func (c *Core) installDir() (string, error) {
	if c.cfg.InstallDir != "" {
		return c.cfg.InstallDir, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	dir := filepath.Dir(exe)
	if dir == updater.DaemonDir {
		// демон поставлен помощником в каталог root, приложение — в домашнем каталоге (там же, куда его ставит deploy/install-linux.sh)
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(h, ".local", "opt", "brod")
	}
	for _, f := range []string{"brod", "Brod.exe"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil && st.Mode().IsRegular() {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w: установка из приложения доступна только для установленной копии (рядом с демоном нет приложения: %s)", ErrInvalid, dir)
}

// stageRelease копирует архив рядом с каталогом установки, проверяет копию по манифесту и распаковывает с проверкой раскладки.
// Linux: stage — соседний каталог (та же файловая система: замена переименованием). Windows: подкаталог каталога установки.
func (c *Core) stageRelease(target, plat, app, archive string, a ota.Asset) (_, _ string, err error) {
	var stage, pkg string
	ext := filepath.Ext(archive)
	if filepath.Ext(archive[:len(archive)-len(ext)]) == ".tar" {
		ext = ".tar.gz"
	}
	if plat == "windows-amd64" {
		stage, pkg = filepath.Join(target, ".update-stage"), filepath.Join(target, ".update-pkg"+ext)
	} else {
		stage, pkg = target+".new", target+".new-pkg"+ext
	}
	_ = os.RemoveAll(stage)
	_ = os.Remove(pkg)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", "", err
	}
	defer func() {
		_ = os.Remove(pkg)
		if err != nil {
			_ = os.RemoveAll(stage)
		}
	}()
	src, err := os.Open(archive)
	if err != nil {
		return "", "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(pkg, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	_, err = io.Copy(dst, io.LimitReader(src, a.Size+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(pkg)
	if err != nil {
		return "", "", err
	}
	err = ota.VerifyAsset(f, a)
	f.Close()
	if err != nil {
		return "", "", fmt.Errorf("копия архива не прошла проверку: %w", err)
	}
	if err := updater.Extract(pkg, stage); err != nil {
		return "", "", fmt.Errorf("распаковка: %w", err)
	}
	if err := updater.CheckLayout(stage, plat, app); err != nil {
		return "", "", fmt.Errorf("раскладка релиза: %w", err)
	}
	return stage, pkg, nil
}

var errInstallUnsupported = errors.New("не поддерживается на этой платформе")
