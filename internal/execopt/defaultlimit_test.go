package execopt

import (
	"errors"
	"testing"
)

// fakeFS serves fixed file contents and fails every other path.
type fakeFS map[string]string

func (f fakeFS) read(p string) ([]byte, error) {
	if s, ok := f[p]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("no such file")
}

const meminfo16G = "MemTotal:       16000000 kB\nMemFree:  1 kB\n"

// TestDefaultLimitIsHalfTheSmallerCeiling: the default budget is half the smaller
// of the cgroup limit — the least of the process's cgroup and its ancestors — and
// RAM, and nothing where nothing can be read.
func TestDefaultLimitIsHalfTheSmallerCeiling(t *testing.T) {
	ram := int64(16000000) * 1024
	cases := []struct {
		name string
		fs   fakeFS
		want int64
	}{
		{"no cgroup limit: half of RAM", fakeFS{
			"/proc/meminfo":     meminfo16G,
			"/proc/self/cgroup": "0::/user.slice/app.scope\n",
			"/sys/fs/cgroup/user.slice/app.scope/memory.max": "max\n",
		}, ram / 2},
		{"a limit on the process's own cgroup", fakeFS{
			"/proc/meminfo":     meminfo16G,
			"/proc/self/cgroup": "0::/user.slice/bench.scope\n",
			"/sys/fs/cgroup/user.slice/bench.scope/memory.max": "8589934592\n",
		}, 4 << 30},
		{"a tighter limit on an ancestor wins", fakeFS{
			"/proc/meminfo":     meminfo16G,
			"/proc/self/cgroup": "0::/kubepods/pod1/ctr\n",
			"/sys/fs/cgroup/kubepods/pod1/ctr/memory.max": "8589934592\n",
			"/sys/fs/cgroup/kubepods/pod1/memory.max":     "2147483648\n",
			"/sys/fs/cgroup/kubepods/memory.max":          "max\n",
		}, 1 << 30},
		{"a container's root cgroup", fakeFS{
			"/proc/meminfo":             meminfo16G,
			"/proc/self/cgroup":         "0::/\n",
			"/sys/fs/cgroup/memory.max": "1073741824\n",
		}, 512 << 20},
		{"a cgroup limit above RAM: RAM wins", fakeFS{
			"/proc/meminfo":             meminfo16G,
			"/proc/self/cgroup":         "0::/\n",
			"/sys/fs/cgroup/memory.max": "68719476736\n",
		}, ram / 2},
		{"cgroup v1, with its huge 'unlimited' on the root", fakeFS{
			"/proc/meminfo":     meminfo16G,
			"/proc/self/cgroup": "12:cpu,cpuacct:/x\n4:memory:/docker/abc\n",
			"/sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": "4294967296\n",
			"/sys/fs/cgroup/memory/memory.limit_in_bytes":            "9223372036854771712\n",
		}, 2 << 30},
		{"nothing readable: unlimited", fakeFS{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectLimit(tc.fs.read); got != tc.want {
				t.Errorf("detectLimit = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSysctlUint64ReadsDarwinsBytes: syscall.Sysctl hands back hw.memsize's eight
// little-endian bytes less one trailing zero, so most sizes arrive as seven.
func TestSysctlUint64ReadsDarwinsBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want int64
		ok   bool
	}{
		{"16 GiB, its last zero dropped", "\x00\x00\x00\x00\x04\x00\x00", 16 << 30, true},
		{"8 GiB", "\x00\x00\x00\x00\x02\x00\x00", 8 << 30, true},
		{"all eight bytes", "\x01\x00\x00\x00\x00\x00\x00\x01", 1<<56 + 1, true},
		{"one byte", "\x40", 64, true},
		{"empty", "", 0, false},
		{"too long", "\x01\x00\x00\x00\x00\x00\x00\x00\x00", 0, false},
		{"zero", "\x00", 0, false},
		{"past int64", "\x00\x00\x00\x00\x00\x00\x00\x80", 0, false},
	} {
		got, ok := sysctlUint64(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %d %v, want %d %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestCeilingUsesThePlatformsRAMWithoutProc: on macOS and Windows there is no
// /proc/meminfo, and the platform's own answer is the ceiling; a cgroup limit, where
// one is readable, still wins when it is smaller.
func TestCeilingUsesThePlatformsRAMWithoutProc(t *testing.T) {
	ram := func() (int64, bool) { return 16 << 30, true }
	if got := detectCeiling(fakeFS{}.read, ram); got != 16<<30 {
		t.Errorf("RAM alone: %d", got)
	}
	if got := detectCeiling(fakeFS{}.read, noMemory); got != 0 {
		t.Errorf("nothing readable: %d, want 0, unbudgeted", got)
	}
	withCgroup := fakeFS{"/proc/self/cgroup": "0::/\n", "/sys/fs/cgroup/memory.max": "1073741824\n"}
	if got := detectCeiling(withCgroup.read, ram); got != 1<<30 {
		t.Errorf("a smaller cgroup limit: %d", got)
	}
	// /proc/meminfo, where there is one, is still read first.
	if got := detectCeiling(fakeFS{"/proc/meminfo": meminfo16G}.read, func() (int64, bool) { return 1, true }); got != 16000000*1024 {
		t.Errorf("/proc/meminfo first: %d", got)
	}
}
