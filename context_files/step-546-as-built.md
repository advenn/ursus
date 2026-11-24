# Step 546 — fuzzcheck counts a leak only where ursus's code runs it or started it

**A false alarm in the fuzz harness.** #96's CI fuzzing of `i128`'s FuzzArith failed
with "1 goroutine(s) still running 1s after the call returned". The goroutines its
dump listed were all the fuzzing engine's own: its worker server, which runs the
fuzz target; `os/signal`'s loop; and the `NotifyContext` watcher, started during
the call. `i128` starts none.

## The fix

`fuzzcheck.Run` compared `runtime.NumGoroutine()` before and after each call, so it
counted any goroutine started meanwhile, whoever started it. The count stays as the
fast path. When it is up, `leaks` reads the stacks and counts only a goroutine whose
frames, or whose "created by" line, name `github.com/advenn/ursus/`. A goroutine
that the testing package, the fuzzing engine or `os/signal` runs or started is
theirs. The caller's own goroutine, the dump's first, is set aside.

## Tests

- **`TestLeaksCountsOnlyUrsusGoroutines`** reads the recorded dump,
  `internal/fuzzcheck/testdata/fuzzarith-stacks.txt`, taken from that CI log, and
  expects 0 leaks. With an ursus goroutine added it expects 1. With a
  standard-library goroutine that ursus started added as well, 2.
- **`TestALeakedGoroutineFails`**, the harness's teeth since step 508, still holds:
  its `leakyWait` is ursus's code.

## Checks

On CI only: the maintainer is away, and the laptop is kept quiet. Test harness
only, so not in the CHANGELOG, as step 540 was not.
