//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SHELLEXECUTEINFOW: в x/sys/windows структуры нет.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	createNoWindow        = 0x08000000
)

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	o := parseArgs(args)
	if o.silent {
		return silent(o, args)
	}
	dir, err := unpack()
	if err != nil {
		msgBox("Установка Brod", "Не удалось подготовить установку: "+err.Error())
		return exitPayload
	}
	defer os.RemoveAll(dir)
	ui := exec.Command(filepath.Join(dir, "Brod.exe"), "--setup")
	ui.Dir = dir
	if err := ui.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		msgBox("Установка Brod", "Не удалось открыть окно установки: "+err.Error())
		return exitPayload
	}
	return exitOK
}

func silent(o options, args []string) int {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return relaunchElevated(args)
	}
	dir, err := unpack()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return exitPayload
	}
	defer os.RemoveAll(dir)
	user := o.user
	if user == "" {
		user = defaultUser(os.Getenv)
	}
	if user == "" {
		fmt.Fprintln(os.Stderr, "ошибка: не удалось определить учётную запись, укажите /USER=ДОМЕН\\Имя")
		return exitUsage
	}
	psArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(dir, "install.ps1"), "-User", user}
	if o.dir != "" {
		psArgs = append(psArgs, "-Dir", o.dir)
	}
	cmd := exec.Command("powershell.exe", psArgs...)
	cmd.Dir = dir
	// загрузчик — оконное приложение без консоли: без этого флага PowerShell открывает своё окно и в тихом режиме
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 0 {
			return exitInstall
		}
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return exitInstall
	}
	return exitOK
}

// relaunchElevated запускает этот же файл с теми же аргументами через запрос UAC и возвращает его код возврата.
func relaunchElevated(args []string) int {
	exe, err := os.Executable()
	if err != nil {
		return exitInstall
	}
	var quoted []string
	for _, a := range args {
		quoted = append(quoted, syscall.EscapeArg(a))
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess, lpVerb: verb, lpFile: file, lpParameters: params, nShow: windows.SW_HIDE}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, _ := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return exitElevation
	}
	defer windows.CloseHandle(info.hProcess)
	if ev, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil || ev != windows.WAIT_OBJECT_0 {
		return exitInstall
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return exitInstall
	}
	return int(code)
}

func msgBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	x, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, x, t, windows.MB_OK|windows.MB_ICONERROR)
}
