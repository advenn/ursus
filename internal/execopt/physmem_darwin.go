package execopt

import "syscall"

// physicalMemory is the machine's RAM, from sysctl's hw.memsize.
func physicalMemory() (int64, bool) {
	s, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0, false
	}
	return sysctlUint64(s)
}
