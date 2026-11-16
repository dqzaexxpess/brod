package memwatch

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters — PROCESS_MEMORY_COUNTERS (psapi.h).
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// Proc на Windows возвращает рабочий набор процесса (аналог RSS) в КиБ; «лениво освобождённых» страниц в смысле Linux здесь нет — 0.
func Proc() (rss, lazy int) {
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&c)), uintptr(c.cb)); r == 0 {
		return 0, 0
	}
	return int(c.WorkingSetSize >> 10), 0
}
