//go:build !linux

package updater

// CloexecAll: дескрипторы наследуются иначе (Windows: bInheritHandles у CreateProcess); ничего не делаем.
func CloexecAll() int { return 0 }
