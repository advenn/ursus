# Step 181 — as built

**R3 of `v0.6-scope.md`: a default memory budget off Linux.**

## 1. Evidence

- **`execopt.Ceiling`** read only `/proc/meminfo` and the cgroup files. On macOS and
  Windows neither exists, so the ceiling was 0.
- So, by `DefaultLimit`, **a query was unbudgeted:** nothing spilled unless the
  caller set `WithMemoryLimit`. And by `SetProcessMemoryLimit`, **no Go soft limit
  was set.**
- The v0.5.0 changelog's limitations gained that line only after the tag, at step
  156.

## 2. What changed

`detectCeiling` takes a second source, `physicalMemory`. It is asked for the
machine's RAM where `/proc/meminfo` is not there, and the smaller cgroup limit still
wins where one is readable. Three files, by build constraint:

- **`physmem_darwin.go`:** `syscall.Sysctl("hw.memsize")`. Sysctl returns the value's
  eight little-endian bytes as a string less one trailing zero byte, which it drops
  as though it ended a C string. So 16 GiB arrives as seven bytes. `sysctlUint64`,
  in the shared file so Linux can test it, pads them back.
- **`physmem_windows.go`:** `GlobalMemoryStatusEx` through `syscall`'s lazy
  `kernel32.dll`, with `MEMORYSTATUSEX`'s layout.
- **`physmem_other.go`:** nothing: Linux reads `/proc`, and other platforms stay
  unbudgeted.

Both use the standard library alone, which keeps the README's one dependency.
`golang.org/x/sys` has helpers for both, but is not a direct dependency.

What follows on macOS and Windows:

- the default budget is half the RAM;
- under it, `SetProcessMemoryLimit` sets the Go soft limit to nine tenths, as on
  Linux, unless `GOMEMLIMIT` or `LeaveProcessMemoryLimit` says otherwise.

## 3. Tests

- **`TestSysctlUint64ReadsDarwinsBytes`:**
  - 16 GiB and 8 GiB arriving as seven bytes;
  - all eight bytes, and one byte;
  - refused: empty, nine bytes, zero, and past int64.
- **`TestCeilingUsesThePlatformsRAMWithoutProc`:**
  - the platform's RAM alone;
  - nothing readable, so unbudgeted;
  - a smaller cgroup limit still wins;
  - `/proc/meminfo` is still read first where it exists.
- **Checked:**
  - `GOOS=darwin` and `GOOS=windows` vet the package, and `freebsd` too;
  - the module builds for darwin/arm64 and windows/amd64;
  - the package's test binaries compile for both.
- **Not run here,** with no such machine: the real `sysctl` and
  `GlobalMemoryStatusEx` calls.

| tooth | result |
| --- | --- |
| Darwin's dropped zero not restored | **bites:** `TestSysctlUint64ReadsDarwinsBytes` |
| the platform's RAM not asked | **bites:** `TestCeilingUsesThePlatformsRAMWithoutProc` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
