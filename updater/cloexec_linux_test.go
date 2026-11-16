//go:build linux

package updater

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func cloexec(fd int) bool {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	return errno == 0 && flags&syscall.FD_CLOEXEC != 0
}

// Дескриптор без O_CLOEXEC (как /dev/net/tun у движка) после CloexecAll не доходит до запущенного процесса.
func TestCloexecAllStopsFDLeakIntoChild(t *testing.T) {
	fd, err := syscall.Open(os.DevNull, syscall.O_RDWR, 0) // намеренно без O_CLOEXEC
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if cloexec(fd) {
		t.Fatal("тест неверен: дескриптор уже с CLOEXEC")
	}
	list := func() string {
		out, err := exec.Command("ls", "/proc/self/fd").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(out)), " ")
	}
	if !strings.Contains(" "+list()+" ", " "+itoa(fd)+" ") {
		t.Fatalf("до CloexecAll дескриптор %d должен был дойти до потомка: %s", fd, list())
	}
	if CloexecAll() == 0 || !cloexec(fd) {
		t.Fatal("CloexecAll не поставил флаг")
	}
	if strings.Contains(" "+list()+" ", " "+itoa(fd)+" ") {
		t.Fatalf("после CloexecAll дескриптор %d всё ещё доходит до потомка: %s", fd, list())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
