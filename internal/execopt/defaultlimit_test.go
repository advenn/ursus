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
