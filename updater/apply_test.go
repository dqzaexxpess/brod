package updater

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRelease(t *testing.T, dir, version, marker string) {
	t.Helper()
	files := map[string]string{releaseFile: ReleaseMark(version, "linux-amd64")}
	for _, f := range required["linux-amd64"] {
		files[f] = marker
	}
	for n, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func rig(t *testing.T) (root, target, stage string) {
	root = t.TempDir()
	target, stage = filepath.Join(root, "brod"), filepath.Join(root, "brod.new")
	writeRelease(t, target, "0.3.0", "old")
	writeRelease(t, stage, "0.4.0", "new")
	return
}

// withHelper подключает подставного помощника: он «ставит» brodd в каталог root-демона (файл с содержимым new) и запоминает вызов.
func withHelper(a *LinuxApply, root string, called *bool) *LinuxApply {
	daemon := filepath.Join(root, "lib", "brodd")
	a.Archive, a.Manifest, a.Sig = "/a", "/m", "/s"
	a.HelperInstalled = func() bool { return true }
	a.DaemonPath = daemon
	a.Helper = func(string, string, string) error {
		if called != nil {
			*called = true
		}
		if err := os.MkdirAll(filepath.Dir(daemon), 0o755); err != nil {
			return err
		}
		return os.WriteFile(daemon, []byte("new"), 0o755)
	}
	return a
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLinuxApplyReplacesAndKeepsPrev(t *testing.T) {
	skipNonLinux(t)
	root, target, stage := rig(t)
	res := filepath.Join(root, "last.json")
	var started string
	helperCalled := false
	a := withHelper(&LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res, Relaunch: true,
		Start: func(exe string) error { started = exe; return nil }}, root, &helperCalled)
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(target, "brodd")); got != "new" {
		t.Fatalf("brodd: %q", got)
	}
	if got := read(t, filepath.Join(target+".prev", "brodd")); got != "old" {
		t.Fatalf("prev: %q", got)
	}
	if !helperCalled {
		t.Fatal("демон должен ставить помощник до замены каталога")
	}
	if started != filepath.Join(target, "brod") {
		t.Fatalf("запуск: %q", started)
	}
	if _, err := os.Stat(stage); err == nil {
		t.Fatal("stage остался")
	}
	r, ok := ReadResult(res)
	if !ok || !r.OK || r.From != "0.3.0" || r.To != "0.4.0" {
		t.Fatalf("итог: %+v %v", r, ok)
	}
	// откат
	if err := Rollback(target); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(target, "brodd")); got != "old" {
		t.Fatalf("после отката: %q", got)
	}
}

func TestLinuxApplyAbortsBeforeChangingAnything(t *testing.T) {
	skipNonLinux(t)
	root, target, stage := rig(t)
	res := filepath.Join(root, "last.json")
	// отказ в пароле
	a := withHelper(&LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res}, root, nil)
	a.Helper = func(string, string, string) error { return errors.New("отказано") }
	err := a.Run()
	if err == nil || !strings.Contains(err.Error(), "установка не начата") {
		t.Fatalf("ошибка: %v", err)
	}
	if read(t, filepath.Join(target, "brodd")) != "old" {
		t.Fatal("прежняя версия изменена")
	}
	if _, e := os.Stat(stage); e == nil {
		t.Fatal("после неудачи подготовленный релиз должен быть удалён")
	}
	if r, ok := ReadResult(res); !ok || r.OK || r.Error == "" {
		t.Fatalf("итог: %+v", r)
	}
	writeRelease(t, stage, "0.4.0", "new") // дальше те же проверки на заново подготовленном релизе
	// процесс не завершился
	a2 := &LinuxApply{Stage: stage, Target: target, To: "0.4.0", WaitPIDs: []int{os.Getpid()}, WaitTimeout: 300 * time.Millisecond}
	if err := a2.Run(); err == nil || !strings.Contains(err.Error(), "не завершился") {
		t.Fatalf("ожидание: %v", err)
	}
	// stage другой версии
	writeRelease(t, stage, "0.4.0", "new")
	a3 := &LinuxApply{Stage: stage, Target: target, To: "0.9.9"}
	if err := a3.Run(); err == nil {
		t.Fatal("релиз другой версии принят")
	}
	// помощник не поставлен: установка не начинается и подсказывает, как его поставить (прежнего обходного пути нет)
	writeRelease(t, stage, "0.4.0", "new")
	a4 := &LinuxApply{Stage: stage, Target: target, To: "0.4.0", Archive: "/a", Manifest: "/m", Sig: "/s", HelperInstalled: func() bool { return false }}
	if err := a4.Run(); err == nil || !strings.Contains(err.Error(), "brod-helper не установлен") || !strings.Contains(err.Error(), "install.sh") {
		t.Fatalf("без помощника: %v", err)
	}
	if read(t, filepath.Join(target, "brodd")) != "old" {
		t.Fatal("без помощника прежняя версия изменена")
	}
}

func TestSwapDirRollsBackOnFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "brod")
	writeRelease(t, target, "0.3.0", "old")
	if err := SwapDir(filepath.Join(root, "нет-такого"), target); err == nil {
		t.Fatal("замена несуществующим каталогом удалась")
	}
	if read(t, filepath.Join(target, "brodd")) != "old" {
		t.Fatal("прежняя версия не возвращена")
	}
}

// Отказ в пароле после закрытия программ: прежняя версия не изменена, запускается снова (иначе человек остаётся без Brod и VPN), подготовка удалена.
// Если программы ещё не закрылись (ожидание не прошло), второй копии не запускаем.
func TestLinuxApplyRelaunchesOldVersionAfterRefusal(t *testing.T) {
	skipNonLinux(t)
	root, target, stage := rig(t)
	var started []string
	a := withHelper(&LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: filepath.Join(root, "last.json"), Relaunch: true,
		Start: func(exe string) error { started = append(started, exe); return nil }}, root, nil)
	a.Helper = func(string, string, string) error { return errors.New("отказано") }
	if err := a.Run(); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if len(started) != 1 || started[0] != filepath.Join(target, "brod") {
		t.Fatalf("прежнее приложение должно быть запущено один раз: %v", started)
	}
	if read(t, filepath.Join(target, "brodd")) != "old" {
		t.Fatal("прежняя версия изменена")
	}
	if _, e := os.Stat(stage); e == nil {
		t.Fatal("подготовка не удалена")
	}
	// приложение ещё живо (ожидание не прошло): запускать нечего
	writeRelease(t, stage, "0.4.0", "new")
	started = nil
	a2 := &LinuxApply{Stage: stage, Target: target, To: "0.4.0", Relaunch: true, WaitPIDs: []int{os.Getpid()}, WaitTimeout: 200 * time.Millisecond,
		Start: func(exe string) error { started = append(started, exe); return nil }}
	if err := a2.Run(); err == nil {
		t.Fatal("ожидалась ошибка ожидания")
	}
	if len(started) != 0 {
		t.Fatalf("запущено при живом приложении: %v", started)
	}
}
