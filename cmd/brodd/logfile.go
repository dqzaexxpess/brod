package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
)

// Журнал самого демона (не движка): старт, ошибки блокировки, смены состояния VPN, причина выхода. Файл нужен, потому что на Windows
// у демона нет консоли (собран как GUI-приложение, чтобы задача планировщика не открывала окно) и stderr после сбоя не прочитать.

const (
	logMaxSize = 1 << 20 // 1 МБ на файл
	logKeep    = 3       // brodd.log, brodd.log.1, brodd.log.2
)

// rotatingFile — файл журнала с ротацией по размеру.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openRotating(path string) (*rotatingFile, error) {
	w := &rotatingFile{path: path}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *rotatingFile) rotate() {
	w.f.Close()
	for i := logKeep - 1; i >= 1; i-- {
		from := w.path
		if i > 1 {
			from = w.path + "." + strconv.Itoa(i-1)
		}
		_ = os.Rename(from, w.path+"."+strconv.Itoa(i))
	}
	_ = w.open()
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil
	}
	if w.size+int64(len(p)) > logMaxSize {
		w.rotate()
		if w.f == nil {
			return len(p), nil
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// tee пишет в файл и в stderr; ошибка stderr (у GUI-приложения Windows его нет) не мешает записи в файл.
type tee struct{ file, console io.Writer }

func (t tee) Write(p []byte) (int, error) {
	_, _ = t.file.Write(p)
	_, _ = t.console.Write(p)
	return len(p), nil
}

// setupLog направляет журнал демона в brodd.log (с ротацией) и в stderr; паники рантайма — в brodd.crash.log. Не получилось открыть файл —
// журнал остаётся только в stderr.
func setupLog(stateDir string) (logPath string, err error) {
	logPath = filepath.Join(stateDir, "brodd.log")
	w, err := openRotating(logPath)
	if err != nil {
		return logPath, err
	}
	log.SetOutput(tee{file: w, console: os.Stderr})
	if cf, err := os.OpenFile(filepath.Join(stateDir, "brodd.crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_ = debug.SetCrashOutput(cf, debug.CrashOptions{})
	}
	return logPath, nil
}
