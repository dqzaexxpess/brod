package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Тесты пакета не должны зависеть от того, поставлен ли помощник на машине (иначе на рабочих машинах они звали бы настоящий pkexec):
// по умолчанию «не поставлен», тесты помощника задают HelperInstalled явно.
func init() { detectHelper = func() bool { return false } }

func helperRig(t *testing.T) (root, target, stage, daemon string) {
	root, target, stage = rig(t)
	lib := filepath.Join(root, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	daemon = filepath.Join(lib, "brodd")
	return
}

func TestLinuxApplyInstallsDaemonViaHelper(t *testing.T) {
	skipNonLinux(t)
	root, target, stage, daemon := helperRig(t)
	res := filepath.Join(root, "last.json")
	var got []string
	setcap := false
	var log strings.Builder
	a := &LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res,
		Archive: "/home/u/a.tar.gz", Manifest: "/home/u/releases.json", Sig: "/home/u/releases.json.sig",
		HelperInstalled: func() bool { return true }, DaemonPath: daemon,
		Helper: func(a, m, s string) error {
			got = []string{a, m, s}
			return os.WriteFile(daemon, []byte("root-daemon"), 0o755)
		},
		NeedCaps: func(string) bool { return true },
		Setcap:   func(string) error { setcap = true; return nil },
		Log:      func(f string, v ...any) { fmt.Fprintf(&log, f+"\n", v...) }}
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "/home/u/a.tar.gz /home/u/releases.json /home/u/releases.json.sig" {
		t.Fatalf("помощнику переданы %v", got)
	}
	if setcap {
		t.Fatal("при поставленном помощнике прежний setcap не нужен")
	}
	// приложение запускает brodd рядом с собой: это ссылка на файл, поставленный помощником
	l, err := os.Readlink(filepath.Join(target, "brodd"))
	if err != nil || l != daemon {
		t.Fatalf("brodd в каталоге программы: %q, %v", l, err)
	}
	if read(t, filepath.Join(target, "brodd")) != "root-daemon" {
		t.Fatal("ссылка ведёт не туда")
	}
	if read(t, filepath.Join(target, "brod")) != "new" {
		t.Fatal("приложение не обновлено")
	}
	r, ok := ReadResult(res)
	if !ok || !r.OK || r.Warning != "" {
		t.Fatalf("итог: %+v", r)
	}
	if strings.Contains(log.String(), "ВНИМАНИЕ") {
		t.Fatalf("предупреждение переходного режима при поставленном помощнике: %s", log.String())
	}
}

// Отказ помощника (подпись, хэш, отказ в polkit) не откатывается на прежний setcap: иначе помеха помощнику возвращала бы окно подмены.
func TestLinuxApplyHelperFailureDoesNotFallBack(t *testing.T) {
	skipNonLinux(t)
	root, target, stage, daemon := helperRig(t)
	res := filepath.Join(root, "last.json")
	setcap := false
	a := &LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res,
		Archive: "/a", Manifest: "/m", Sig: "/s", HelperInstalled: func() bool { return true }, DaemonPath: daemon,
		Helper:   func(string, string, string) error { return errors.New("exit status 5: манифест отвергнут") },
		NeedCaps: func(string) bool { return true }, Setcap: func(string) error { setcap = true; return nil }}
	err := a.Run()
	if err == nil || !strings.Contains(err.Error(), "установка не начата") || !strings.Contains(err.Error(), "манифест отвергнут") {
		t.Fatalf("ошибка: %v", err)
	}
	if setcap {
		t.Fatal("после отказа помощника вызван прежний setcap")
	}
	if read(t, filepath.Join(target, "brodd")) != "old" {
		t.Fatal("прежняя версия изменена")
	}
	if r, ok := ReadResult(res); !ok || r.OK {
		t.Fatalf("итог: %+v", r)
	}
	// без файлов выпуска помощник не зовётся вовсе
	writeRelease(t, stage, "0.4.0", "new")
	called := false
	a2 := &LinuxApply{Stage: stage, Target: target, To: "0.4.0", HelperInstalled: func() bool { return true },
		Helper: func(string, string, string) error { called = true; return nil }, Setcap: func(string) error { setcap = true; return nil }}
	if err := a2.Run(); err == nil || called || setcap {
		t.Fatalf("без архива и манифеста: %v, помощник=%v, setcap=%v", err, called, setcap)
	}
}

// Переходный режим: помощник не поставлен — прежнее поведение с заметным предупреждением в журнале и в итоге установки.
func TestLinuxApplyTransitionWarns(t *testing.T) {
	skipNonLinux(t)
	root, target, stage := rig(t)
	res := filepath.Join(root, "last.json")
	var log strings.Builder
	capsFor := ""
	a := &LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res, Archive: "/a", Manifest: "/m", Sig: "/s",
		HelperInstalled: func() bool { return false },
		Helper:          func(string, string, string) error { t.Fatal("помощник не поставлен, но вызван"); return nil },
		NeedCaps:        func(string) bool { return true }, Setcap: func(f string) error { capsFor = f; return nil },
		Log: func(f string, v ...any) { fmt.Fprintf(&log, f+"\n", v...) }}
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if capsFor != filepath.Join(stage, "brodd") {
		t.Fatalf("прежний setcap: %q", capsFor)
	}
	if !strings.Contains(log.String(), "ВНИМАНИЕ: переходный режим") {
		t.Fatalf("журнал: %s", log.String())
	}
	if r, ok := ReadResult(res); !ok || !r.OK || r.Warning != TransitionWarning {
		t.Fatalf("итог: %+v", r)
	}
	if l, err := os.Lstat(filepath.Join(target, "brodd")); err != nil || !l.Mode().IsRegular() {
		t.Fatal("в переходном режиме brodd остаётся обычным файлом")
	}
}
