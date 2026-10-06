package execopt

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
)

// DefaultLimit is the memory budget a query gets when its caller sets none: half of
// the smaller of the process's cgroup memory limit and the machine's RAM, or 0 —
// unlimited — where neither can be read.
//
// # Why a default at all
//
// Spilling is decided by the budget, and with no budget nothing spills. A cgroup's
// limit is enforced by the kernel from outside, and ursus never saw it: the v0.3.0
// benchmark ran each query under an 8 GB cgroup, and ursus's h2o gb10 over CSV —
// a group-by that spills perfectly well under WithMemoryLimit — grew past it and was
// OOM-killed instead. Every container with a memory limit had the same exposure.
// DuckDB defaults its limit to 80% of RAM for the same reason.
//
// # Why half, and not DuckDB's 80%
//
// The budget counts live bytes, and ursus's buffers live on the Go heap, which with
// GOGC=100 grows to about twice its live size before collecting. Half the ceiling
// keeps the process's true peak under it. Set GOMEMLIMIT, or WithMemoryLimit, to
// choose differently; WithMemoryLimit(0) turns the budget off.
//
// Detected once per process. Linux only: elsewhere nothing is readable and the
// default stays unlimited, as it always was.
func DefaultLimit() int64 { return Ceiling() / 2 }

// Ceiling is the memory this process may use: the smaller of its cgroup's limit and
// the machine's RAM, or 0 where neither can be read. Detected once per process.
func Ceiling() int64 {
	ceilingOnce.Do(func() { ceiling = detectCeiling(os.ReadFile) })
	return ceiling
}

var (
	ceilingOnce sync.Once
	ceiling     int64
)

// detectLimit is DefaultLimit over a file reader, so a test can hand it fixtures.
func detectLimit(read func(string) ([]byte, error)) int64 {
	return detectCeiling(read) / 2
}

// detectCeiling is Ceiling over a file reader.
func detectCeiling(read func(string) ([]byte, error)) int64 {
	c, ok := memTotal(read)
	if cg, cok := cgroupLimit(read); cok && (!ok || cg < c) {
		c, ok = cg, true
	}
	if !ok {
		return 0
	}
	return c
}

// memTotal is MemTotal from /proc/meminfo, in bytes.
func memTotal(read func(string) ([]byte, error)) (int64, bool) {
	b, err := read("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil || kb <= 0 {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}

// cgroupLimit is the smallest memory limit on the process's cgroup or any of its
// ancestors. A limit can sit on any of them — systemd puts one on a slice, and a
// container runtime on the container's root — so the process's own cgroup alone
// is not enough. cgroup v2's memory.max is read, and v1's memory.limit_in_bytes,
// whose "unlimited" is a number near 2^63.
func cgroupLimit(read func(string) ([]byte, error)) (int64, bool) {
	b, err := read("/proc/self/cgroup")
	if err != nil {
		return 0, false
	}
	best, found := int64(0), false
	take := func(v int64) {
		if v > 0 && (!found || v < best) {
			best, found = v, true
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		// hierarchy-ID:controllers:path
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		var root, file string
		switch {
		case parts[0] == "0" && parts[1] == "":
			root, file = "/sys/fs/cgroup", "memory.max"
		case hasController(parts[1], "memory"):
			root, file = "/sys/fs/cgroup/memory", "memory.limit_in_bytes"
		default:
			continue
		}
		for p := path.Clean("/" + parts[2]); ; p = path.Dir(p) {
			if v, ok := readLimit(read, path.Join(root, p, file)); ok {
				take(v)
			}
			if p == "/" {
				break
			}
		}
	}
	return best, found
}

func hasController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// readLimit parses one limit file: a byte count, or "max" for none. v1 writes its
// "none" as a number past any real machine, which is treated as none too.
func readLimit(read func(string) ([]byte, error), file string) (int64, bool) {
	b, err := read(file)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || v >= 1<<62 {
		return 0, false
	}
	return v, true
}
