// Package testport выдаёт тестам свободные TCP-порты на 127.0.0.1, чтобы параллельные прогоны
// (пакеты go test, несколько worktree) не сталкивались на bind.
package testport

import (
	"net"
	"sync"
)

var (
	mu   sync.Mutex
	used = map[int]bool{}
)

// Next возвращает порт, свободный в момент вызова и не выданный ранее в этом процессе.
// Между закрытием пробного слушателя и реальным bind остаётся окно гонки, но оно на порядки
// уже, чем у фиксированных диапазонов.
func Next() int {
	mu.Lock()
	defer mu.Unlock()
	for {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic("testport: " + err.Error())
		}
		p := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if !used[p] {
			used[p] = true
			return p
		}
	}
}
