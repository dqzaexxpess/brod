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
	var log strings.Builder
	a := &LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res,
		Archive: "/home/u/a.tar.gz", Manifest: "/home/u/releases.json", Sig: "/home/u/releases.json.sig",
		HelperInstalled: func() bool { return true }, DaemonPath: daemon,
		Helper: func(a, m, s string) error {
			got = []string{a, m, s}
			return os.WriteFile(daemon, []byte("root-daemon"), 0o755)
		},
		Log: func(f string, v ...any) { fmt.Fprintf(&log, f+"\n", v...) }}
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "/home/u/a.tar.gz /home/u/releases.json /home/u/releases.json.sig" {
		t.Fatalf("помощнику переданы %v", got)
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
	if !ok || !r.OK {
		t.Fatalf("итог: %+v", r)
	}
}

// Отказ помощника (подпись, хэш, отказ в polkit) — установка не начата, обходного пути нет: иначе помеха помощнику возвращала бы окно подмены.
func TestLinuxApplyHelperFailureDoesNotFallBack(t *testing.T) {
	skipNonLinux(t)
	root, target, stage, daemon := helperRig(t)
	res := filepath.Join(root, "last.json")
	a := &LinuxApply{Stage: stage, Target: target, From: "0.3.0", To: "0.4.0", Result: res,
		Archive: "/a", Manifest: "/m", Sig: "/s", HelperInstalled: func() bool { return true }, DaemonPath: daemon,
		Helper: func(string, string, string) error {
			return errors.New("exit status 5: манифест отвергнут")
		}}
	err := a.Run()
	if err == nil || !strings.Contains(err.Error(), "установка не начата") || !strings.Contains(err.Error(), "манифест отвергнут") {
		t.Fatalf("ошибка: %v", err)
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
		Helper: func(string, string, string) error { called = true; return nil }}
	if err := a2.Run(); err == nil || called {
		t.Fatalf("без архива и манифеста: %v, помощник=%v", err, called)
	}
}
