package execopt

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx is Windows' MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

var globalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// physicalMemory is the machine's RAM, from GlobalMemoryStatusEx.
func physicalMemory() (int64, bool) {
	if globalMemoryStatusEx.Find() != nil {
		return 0, false
	}
	m := memoryStatusEx{}
	m.length = uint32(unsafe.Sizeof(m))
	if ok, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); ok == 0 || m.totalPhys == 0 {
		return 0, false
	}
	if m.totalPhys > 1<<62 {
		return 0, false
	}
	return int64(m.totalPhys), true
}
