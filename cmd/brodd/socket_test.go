package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveSocketAfterListenerClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brodd.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skip("unix-сокеты недоступны:", err)
	}
	ln.Close()
	removeSocket(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл сокета остался: %v", err)
	}
	removeSocket(path) // повторное удаление и отсутствующий файл не ошибка
}
