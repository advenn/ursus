package ursus

import (
	"math"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/advenn/ursus/internal/execopt"
)

// SetProcessMemoryLimit sets the Go runtime's soft memory limit — the one GOMEMLIMIT
// sets — to nine tenths of the memory this process may use: the smaller of its
// cgroup's limit and the machine's RAM, as the default query budget reads them. It
// returns the limit in effect afterwards, in bytes; math.MaxInt64 means none.
//
// ursus calls it itself, once, the first time a query runs under the default budget
// (step 128). Calling it at start-up does the same thing earlier.
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
// # Why it is automatic
//
// The limit is the whole process's, not ursus's: it changes how the program around
// ursus collects garbage too, which is why step 97 left it to the caller. Measured
// since, that was the wrong trade. The budget counts what the operators hold, and
// the heap ran to about twice it: under an 8 GB cgroup, h2o's six-key group-by peaked
// at 7.6 GB against a 4 GB budget, and under a 4 GB cgroup a join of two ten-million-
// row tables was killed at the cap. A program that never called this, which is most
// of them, had a default budget that did not hold.
//
// So it is set where ursus already decides how much memory the process may use: when
// a query runs under the default budget, which ursus derives from that same ceiling.
// Not on import, and not under a limit the caller gave WithMemoryLimit.
//
// To keep ursus from setting it, set GOMEMLIMIT, "off" included, or call
// LeaveProcessMemoryLimit before the first query.
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

// LeaveProcessMemoryLimit keeps ursus from setting the Go runtime's soft memory limit
// when a query first runs under the default budget. Call it before the first query,
// in a program that manages the limit itself, or wants none and cannot set
// GOMEMLIMIT=off.
func LeaveProcessMemoryLimit() { autoLimitOff.Store(true) }

var (
	autoLimitOnce sync.Once
	autoLimitOff  atomic.Bool
)

// applyAutoMemoryLimit is SetProcessMemoryLimit, once per process, unless
// LeaveProcessMemoryLimit came first. The query's own budget is not affected: it is
// half the ceiling either way.
func applyAutoMemoryLimit() {
	if autoLimitOff.Load() {
		return
	}
	autoLimitOnce.Do(func() {
		if !autoLimitOff.Load() {
			SetProcessMemoryLimit()
		}
	})
}
