package ursus

import (
	"math"
	"os"
	"runtime/debug"

	"github.com/advenn/ursus/internal/execopt"
)

// SetProcessMemoryLimit sets the Go runtime's soft memory limit — the one GOMEMLIMIT
// sets — to nine tenths of the memory this process may use: the smaller of its
// cgroup's limit and the machine's RAM, as the default query budget reads them. It
// returns the limit in effect afterwards, in bytes; math.MaxInt64 means none.
//
// Call it once, at start-up, in a program that runs ursus under a memory limit — a
// container, a systemd unit — and that does not set GOMEMLIMIT itself.
//
// # Why
//
// The default budget is half the ceiling (WithMemoryLimit says why): ursus's
// buffers live on the Go heap, and with GOGC's default the heap grows to about
// twice what is live before the collector runs. Half leaves room for that, and it is
// still a race — a query holding its whole budget can take the heap to the ceiling.
// A soft limit near the ceiling makes the collector run harder as the heap
// approaches it, instead of letting the kernel kill the process. Measured (step
// 97): a 300 MB live set, with batches built and dropped around it, peaked at about
// 650 MB of runtime memory under the defaults and at about 440 MB under a 450 MB
// soft limit.
//
// # Why it is not automatic
//
// The limit is the whole process's, not ursus's: it changes how the program
// around ursus collects garbage too. A library should not decide that on import.
//
// # What it leaves alone
//
// A limit already chosen — GOMEMLIMIT in the environment, "off" included, or an
// earlier debug.SetMemoryLimit — is kept, and returned. Where no ceiling can be read
// (anywhere but Linux) nothing is set.
func SetProcessMemoryLimit() int64 {
	cur := debug.SetMemoryLimit(-1)
	if _, set := os.LookupEnv("GOMEMLIMIT"); set || cur != math.MaxInt64 {
		return cur
	}
	ceiling := processCeiling()
	if ceiling <= 0 {
		return cur
	}
	lim := ceiling / 10 * 9
	debug.SetMemoryLimit(lim)
	return lim
}

// processCeiling is execopt.Ceiling, as a variable so a test can set it.
var processCeiling = execopt.Ceiling
