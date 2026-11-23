package updater

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LinuxApply — параметры установки на Linux. Это выполняет помощник (brodctl update-apply), запущенный ядром и пережидающий выход
// приложения и демона: подменять файлы под работающим приложением нельзя (библиотеки Flutter и ресурсы подгружаются во время работы).
type LinuxApply struct {
	Stage    string // распакованный и проверенный релиз (рядом с Target, одна файловая система)
	Target   string // установленный каталог (~/.local/opt/brod)
	From, To string
	WaitPIDs []int
	Result   string // куда записать итог
	Relaunch bool   // запустить приложение после установки

	// подписанный выпуск для помощника от root (brod-helper): скачанный архив, манифест и его подпись. Помощник сам сверяет архив с
	// манифестом и ставит brodd в DaemonDir; в stage на месте brodd остаётся ссылка на поставленный файл.
	Archive, Manifest, Sig string

	// подменяемое в тестах
	WaitTimeout     time.Duration
	HelperInstalled func() bool                               // поставлен ли помощник; nil — проверка HelperPath и DaemonDir
	Helper          func(archive, manifest, sig string) error // nil — pkexec HelperPath install …
	DaemonPath      string                                    // куда помощник ставит brodd; "" — DaemonDir/brodd
	Start           func(exe string) error                    // запуск приложения; nil — по умолчанию отсоединённый процесс
	Log             func(format string, a ...any)
}

// помощник от root и каталог, куда он ставит демон (ставятся `sudo deploy/install-helper.sh`).
const (
	HelperPath = "/usr/local/libexec/brod/brod-helper"
	DaemonDir  = "/usr/local/lib/brod"
)

// detectHelper — проверка по умолчанию; тесты пакета подменяют её, чтобы не зависеть от того, поставлен ли помощник на машине.
var detectHelper = helperInstalled

func (a *LinuxApply) logf(f string, v ...any) {
	if a.Log != nil {
		a.Log(f, v...)
	}
}

// Run выполняет установку. Порядок выбран так, чтобы до замены ничего не менялось: сначала дожидаемся выхода программ, затем выдаём права
// новому brodd прямо в каталоге stage (xattr переживает переименование), и только потом меняем каталоги. Отказ в пароле — установка не
// начиналась, прежняя версия на месте.
func (a *LinuxApply) Run() (err error) {
	waited, swapped, daemonDone := false, false, false // программы закрыты / каталоги заменены / brodd уже поставлен помощником
	defer func() {
		r := Result{At: time.Now().UTC(), From: a.From, To: a.To, OK: err == nil}
		if err != nil {
			r.Error = err.Error()
			_ = os.RemoveAll(a.Stage) // подготовленный релиз (десятки МБ) после неудачи не нужен: следующая попытка распакует заново
			// программы уже закрыты, а установка не состоялась (отказ в пароле и т. п.): вернуть прежнюю версию, иначе человек остаётся без VPN
			if a.Relaunch && waited && !swapped {
				exe := filepath.Join(a.Target, "brod")
				if _, e := os.Stat(exe); e == nil {
					start := a.Start
					if start == nil {
						start = startDetached
					}
					if serr := start(exe); serr != nil {
						a.logf("прежнее приложение не запущено: %v", serr)
					} else {
						a.logf("установка не состоялась, прежняя версия запущена снова")
					}
				}
			}
		}
		if a.Result != "" {
			_ = WriteResult(a.Result, r)
		}
	}()
	if err := CheckLayout(a.Stage, "linux-amd64", a.To); err != nil {
		return fmt.Errorf("проверка распакованного релиза: %w", err)
	}
	if err := a.wait(); err != nil {
		return err
	}
	waited = true
	installed := a.HelperInstalled
	if installed == nil {
		installed = detectHelper
	}
	if installed() {
		if err := a.installDaemon(); err != nil {
			return err
		}
	} else {
		return errors.New("помощник обновлений brod-helper не установлен: установка не начата (прежняя версия на месте). " +
			"Поставьте его: ./install.sh из архива выпуска (системный шаг) или sudo deploy/install-helper.sh")
	}
	a.logf("замена каталога %s", a.Target)
	if err := SwapDir(a.Stage, a.Target); err != nil {
		if daemonDone {
			// откат brodd потребовал бы ещё одной привилегированной команды помощника; повторная установка той же версии безопасна и завершит обновление
			return fmt.Errorf("%w (brodd в %s уже новой версии, приложение прежней: повторите обновление)", err, DaemonDir)
		}
		return err
	}
	swapped = true
	if a.Relaunch {
		start := a.Start
		if start == nil {
			start = startDetached
		}
		if serr := start(filepath.Join(a.Target, "brod")); serr != nil {
			a.logf("приложение не запущено: %v (установка выполнена)", serr)
		}
	}
	return nil
}

// installDaemon — brodd ставит помощник от root из подписанного выпуска (сам копирует архив к себе, сверяет SHA-256 с манифестом и
// выдаёт права своей копии). Отказ помощника — установка не начата; на прежний способ при отказе НЕ откатываемся, иначе помеха помощнику
// возвращала бы окно подмены. После успеха brodd в stage заменяется ссылкой на поставленный файл: приложение запускает brodd рядом с собой.
func (a *LinuxApply) installDaemon() error {
	if a.Archive == "" || a.Manifest == "" || a.Sig == "" {
		return errors.New("помощнику не переданы архив, манифест и подпись выпуска: установка не начата (прежняя версия на месте)")
	}
	run := a.Helper
	if run == nil {
		run = pkexecHelper
	}
	a.logf("установка brodd помощником %s", HelperPath)
	if err := run(a.Archive, a.Manifest, a.Sig); err != nil {
		return fmt.Errorf("помощник не поставил brodd, установка не начата (прежняя версия на месте): %w", err)
	}
	daemon := a.DaemonPath
	if daemon == "" {
		daemon = filepath.Join(DaemonDir, "brodd")
	}
	link := filepath.Join(a.Stage, "brodd")
	if err := os.Remove(link); err != nil {
		return err
	}
	if err := os.Symlink(daemon, link); err != nil {
		return fmt.Errorf("ссылка %s → %s: %w", link, daemon, err)
	}
	a.logf("brodd поставлен в %s", daemon)
	return nil
}

func (a *LinuxApply) wait() error {
	limit := a.WaitTimeout
	if limit == 0 {
		limit = 90 * time.Second
	}
	deadline := time.Now().Add(limit)
	for _, pid := range a.WaitPIDs {
		for pidAlive(pid) {
			if time.Now().After(deadline) {
				return fmt.Errorf("процесс %d не завершился за %s: установка не начата", pid, limit)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

func pkexecHelper(archive, manifest, sig string) error {
	pk, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("нет pkexec (пакет polkit)")
	}
	out, err := exec.Command(pk, HelperPath, "install", archive, manifest, sig).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", HelperPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}
