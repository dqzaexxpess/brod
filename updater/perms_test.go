package updater

import (
	"runtime"
	"testing"
)

// posixPerms — права файлов в стиле POSIX проверяемы: на Windows доступ задаётся ACL, а Mode().Perm() их не отражает.
var posixPerms = runtime.GOOS != "windows"

// skipNonLinux — помощник установки на месте (apply_linux.go) есть только на Linux; на Windows установку делает install.ps1 -Update.
func skipNonLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("помощник установки Linux")
	}
}
