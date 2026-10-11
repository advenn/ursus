//go:build !darwin && !windows

package execopt

// physicalMemory has nothing to say here: Linux reads /proc/meminfo, and other
// platforms are left unbudgeted.
func physicalMemory() (int64, bool) { return 0, false }
