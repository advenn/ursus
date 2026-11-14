# Step 508 — as built

**The fuzzing harness: `internal/fuzzcheck`, `internal/readfuzz`, and CI that
fuzzes.** Step 443 of [`v0.7.2-plan.md`](./v0.7.2-plan.md) (M3, H4.0), run on master's
code before v0.8.0, as step 506's plan says ("then": the bug hunt on master). It is
the one definition of an acceptable outcome that the hunt's later fuzz targets share.
The critic's correction to 443 is taken, with a different split (§3).

## 1. What

- **`internal/fuzzcheck`**, level 1: `Run(t, Limits{Wall, Alloc, Mem, API}, fn func()
  error)`; `TB`, the five methods of `testing.TB` it uses; the bounded `Allocator`,
  with `NewAllocator(n)`, `RequestLimit(n)`, `RequestBase`, `RequestPerByte` and
  `PoisonByte`. It imports `internal/uerr` alone of ursus, plus arrow-go's `memory`.
- **`internal/fuzzcheck/invariants`**, level 22: a blank import turns on
  `data.CheckNonNullable` and `data.CheckTimeRange`; `On()` says both are on.
- **`internal/readfuzz`**, level 80, tests only (its `doc.go` declares nothing, as
  `internal/memcheck`'s): `FuzzScanCSV`, 19 seeds, and `TestTheInvariantChecksAreOn`.
- **`internal/gen/fuzzlist`**: names every `func FuzzX(f *testing.F)` in the test
  files `go list` builds; `-json` gives fuzz.yml's matrix.
- **`make fuzz FUZZ=<target> PKG=<package> FUZZTIME=<d>`**: its defaults fuzz
  `FuzzEvery` in `./dtype/` for 60 s, as before. **`make fuzz-list`** lists the targets.
- **`.github/workflows/fuzz.yml`** (§6).
- `panic_test.go`'s `goroutineSites` names the watchdog's goroutine, with
  `TestAHangEndsTheProcess`. Every `go` statement in the module must be listed there,
  and this one panics on purpose.

## 2. What Run checks, and why

`Run` calls `fn` once, on the caller's goroutine, so a target's own `t.Fatal` inside
it stays legal. It fails the input, with `Errorf` and then `FailNow`, on any of these:

| outcome | how | why |
| --- | --- | --- |
| a hang | A watchdog goroutine waits `Wall` (10 s by default). Then it prints every goroutine's stack and panics on its own goroutine, which ends the process. | Go's fuzzer has no timeout for one input. A call cannot be stopped from outside. Under `go test -fuzz` a worker that dies records its input as a crasher (`internal/fuzz/worker.go`). |
| allocation past `Alloc` | The change in runtime/metrics' `/gc/heap/allocs:bytes` over the call, all goroutines' (1 GiB by default). The watchdog polls it every 10 ms and keeps every goroutine's stack from the moment it first passes the bound. | It counts what the call allocates. The live heap would also count the type-intern table, which grows during a run whatever the input. |
| runaway allocation | The watchdog ends the process at 4 × `Alloc`, as for a hang. | A worker that the kernel OOM-kills records no input: the fuzzer treats SIGKILL as "no crash will be recorded". |
| leaked goroutines | `runtime.NumGoroutine()` must be back at its count from before the call within 1 s. Otherwise every goroutine's stack is printed. | A scan that leaves a decoder's or a worker's goroutine running leaks in every service that runs it. |
| `ErrInternal` | `errors.Is(err, uerr.ErrInternal)`, through `Unwrap() error` and `Unwrap() []error`. | ursus's own "this is a bug in ursus". |
| a panic in ursus's code | A `*uerr.PanicError` anywhere in the error, or a panic that escaped `fn`. Either is judged by its frames from the panic outward, the runtime's left out. The standard library's frames are passed over: a `strconv` that panicked was called wrongly. So are other modules'. The first frame that is ursus's (test packages included) or a tolerated module's decides. | The plan says "whatever its Kind": arrowsrc's `callerCode` labelled ursus's own panics KindIO until step 428 judged them by their frames (`attributed`). |
| a tolerated panic | The deciding frame is `github.com/apache/arrow-go/`, `github.com/apache/thrift/` or `github.com/klauspost/`. It is logged (`fuzzcheck: tolerated: …`) and the input passes. | A decoder that panics on a corrupt input is the input's problem, as Parquet's `corrupt` and arrowsrc's `attributed` already rule. |
| a tolerated panic labelled `ErrInternal` | Fails, and the report says the recovery took another module's panic for ursus's bug. | The user is told to report an ursus bug about a corrupt file. The fix is attribution at the recovery, as `corrupt` does it. |
| a panic escaping the public API | `Limits.API` fails any panic that reaches `fn`'s caller, whoever raised it. | The public API recovers every panic (`TestEveryEntryPointRecovers`). A target of internal code, such as 449's twin of arrow-go's decoder, leaves `API` off. |
| a refused allocation | A `*Refusal` in the error or as the escaped panic, or in `Limits.Mem`'s record. The record also catches a refusal whose panic the code below swallowed. | §4. |

The invariant checks: `CheckNonNullable` has been on by default since step 98
(`batch.go`). `CheckTimeRange` is off outside tests for its O(rows) cost, and
`invariants` turns it on. Each violation is ErrInternal at the batch that holds it.

`Run`'s counts are the process's, so a target never runs beside a `t.Parallel` test.
That is why the public targets have a package of their own, apart from the root's
parallel tests. The allocation count lags by up to a P's cached spans, a few MiB. A
bound catches an allocation far past what the input needs; it is not an exact budget.

## 3. The levels

The critic's correction: 443's "level 1" is infeasible, because `internal/gen/levels`
allows strictly lower imports only and turning on data's flags imports
`internal/data` (level 20). The critic offered two fixes: fuzzcheck above data (level
26), or the flags moved out of it.

The second is taken, as a sub-package, for an import cycle the first would cause. An
internal test of a package imports the package's own code. If fuzzcheck imported
data, then dtype's, i128's, bitmap's, arrowx's, data's and uerr's internal tests
could not import fuzzcheck, since data imports all of them. 445's targets include
dtype's `FuzzParseFloat` and i128's `FuzzParse`, and dtype's one target today,
`FuzzEvery`, is an internal test. With uerr as its only ursus import, fuzzcheck keeps
level 1 and can be used everywhere above uerr. `invariants` sits at 22, above data.
Each batch-building target imports it: readfuzz's tests now, and the internal tests of
csv, ndjson and parquet later.

D20 asked whether a non-test internal package may import `testing`. It does not
arise: `Run` takes its own five-method `TB`, so fuzzcheck does not import `testing`,
and the teeth use a recording fake. On master `ursustest` already imports it.

## 4. The bounded allocator

D21 was never answered. The swarm proposed it in its decisions list, kept with the
probes (`~/ursus-scratch-archive/probes-0.7.2/swarm-result.json`): "a single
declared-length request may not exceed 16 MiB + 16x the input before the data is
seen, and total allocation is bounded by about 2x the answer + 64 MiB".

- **Per request:** `RequestLimit(n) = 16 MiB + 16 × n`, with `n` the input's size.
  `NewAllocator(n)` sets it. `Allocator{Limit: …}` sets any other limit, and its zero
  value is 16 MiB. A request over the limit, or of a negative size, is recorded with
  the asking goroutine's stack and panics with its `*Refusal`, as arrow-go's
  allocators panic when they cannot allocate. ursus's recovery wraps that panic, and
  `PanicError.Unwrap` exposes the Refusal to `Run`. A granted request is arrow-go's Go
  allocator's, 64-byte aligned.
- **Poisoned:** with `Poison`, each buffer is filled with 0xA5 as it is handed out
  and as it is freed. That is 448's probe of `pool.go`'s "not zeroed" assumption.
- **In total:** "2x the answer" cannot be known before the answer exists. readfuzz
  bounds a read at 64 MiB plus 1 KiB per byte of input, the input measured
  decompressed. While choosing that, the worst seen was about 250 bytes per byte: a
  header of commas, whose every column costs a name and a field. A small CSV costs
  1.2 MiB, the reader's buffers. An input that decompresses past 64 MiB is skipped:
  reading a gigabyte that a ratio of 1,000 makes from a megabyte is a question of
  capacity, not a bug.
- **D14** is still open: arrow-go's page buffer is sized by the page header. Under 448
  the bounded allocator refuses it (a page claiming 2^28 bytes is one of 448's
  seeds). The critic's fix is 448's choice: lower `MaxUncompressedPageSize` in
  `readProps`, or add a ratchet line naming the request.

## 5. Teeth

Each check was turned off in turn, and the teeth test named fails each time. These
are `internal/fuzzcheck`'s tests. The hang and the runaway run in a child process,
which must end with exit status 2 by the watchdog's panic.

| harness mutation | test | |
| --- | --- | --- |
| the watchdog's wall timer an hour | `TestAHangEndsTheProcess` | **bites** (the child times out at 30 s) |
| the allocation check off | `TestAnAllocationPastTheBoundFails` | **bites** |
| the runaway check off | `TestARunawayAllocationEndsTheProcess` | **bites** |
| the goroutine count off | `TestALeakedGoroutineFails` | **bites** |
| `ErrInternal` not checked | `TestAnInternalErrorFails` | **bites** |
| ursus's frames owned by others | `TestAPanicInUrsusCodeFails` (escaped, as KindIO, through `strings.Repeat`) | **bites** |
| the tolerated modules owned by ursus | `TestAPanicInAToleratedModuleIsLoggedNotFailed`, `TestThriftsPanicsAreTolerated` | **bites** |
| `API` ignored | `TestAPanicInAToleratedModuleIsLoggedNotFailed` (escaped the public API) | **bites** |
| the allocator refuses nothing | `TestTheBoundedAllocatorRefusesPastItsLimit` | **bites** |
| the allocator's record not read | `TestTheBoundedAllocatorRefusesPastItsLimit` (a refusal swallowed) | **bites** |
| no poison on Free | `TestAPoisonedAllocatorFillsWhatItHandsOutAndTakesBack` | **bites** |
| `CheckTimeRange` not set | `TestTheInvariantChecksAreOn` (readfuzz) | **bites** |

The tolerated panics are real ones: arrow-go's `bitutil.SetBit` past an empty bitmap,
and a read from klauspost's nil `gzip.Reader`, each recovered as KindIO, escaped, and
escaped from the API. Thrift's frames are a constructed `PanicError`, since raising a
real one would mean importing Thrift, which ursus does not do directly.

End to end, the CSV reader (`internal/source/csv`) was broken five ways, and
`FuzzScanCSV`'s seed corpus fails each:

| CSV reader mutation | outcome |
| --- | --- |
| a nil-map write for a record of three fields | **bites:** ErrInternal, the recovered panic's stack |
| `uerr.Internalf` for a batch of two rows | **bites:** ErrInternal |
| a goroutine left blocked per batch | **bites:** "1 goroutine(s) still running 1s after the call returned", on each seed with rows |
| 200 MiB allocated per batch | **bites:** 201.2 MiB against a bound of 64.0 MiB |
| a block forever for a record of three fields | **bites:** "fuzzcheck: FuzzScanCSV/seed#1: hang: no answer after 10s", the process ended |

## 6. fuzz.yml

- **Triggers:** `pull_request` touching `**.go`, `go.mod`, `go.sum`, a
  `testdata/fuzz` corpus or the workflow itself; `workflow_dispatch`, whose `fuzztime`
  input defaults to 30s; and a nightly schedule at 02:23 UTC.
  `GOEXPERIMENT=simd GOAMD64=v3` and Go 1.27.0, as ci.yml. `contents: read`, and one
  run at a time per PR or branch.
- **`list`:** `go run ./internal/gen/fuzzlist -json`. Today it lists
  `dtype-FuzzEvery`, `internal-readfuzz-FuzzScanCSV` and
  `internal-source-parquet-FuzzHybrid`.
- **`fuzz`:** a matrix of one job per target, `fail-fast: false`, each running
  `go test -run='^$' -fuzz="^$TARGET$" -fuzztime=30s $PKG`. The generated corpus
  (`$(go env GOCACHE)/fuzz`) is kept in actions/cache per target, so each run starts
  from the last one's corpus. On failure, `$PKG/testdata/fuzz` is uploaded as
  `fuzz-<id>`. A crasher committed there runs as a seed in every ci.yml leg.
- **Not a required check,** and master's protection is unchanged. A check that a path
  filter skips never reports. A fuzz finding is a bug for a PR of its own, not a
  block on the PR that met it.

Seeds also run in every ci.yml leg as ordinary tests. `FuzzScanCSV`'s 19 take 0.01 s;
fuzzcheck's teeth take 0.4 s, and 1.4 s under the race detector.

## 7. What the later targets plug in

- **A public reader's target (446–448, 450, 464):** a file in `internal/readfuzz`,
  `fuzzcheck.Run(t, limits(n), …)` with `n, ok := inflated(b)`, seeds by `f.Add`. 447
  adds `debug.SetMaxStack(8 MiB)` to readfuzz's init.
- **A parser's target inside its package (445, 465, 466; 446's and 447's internal
  ones):** `fuzzcheck.Run(t, fuzzcheck.Limits{…}, …)` from an internal or external
  test. Packages that build batches add
  `import _ "github.com/advenn/ursus/internal/fuzzcheck/invariants"`.
- **A reader under a bounded pool (448, 450, 451):** `mem := fuzzcheck.NewAllocator(len(b))`,
  with `Poison: true` for 448, passed to the reader. That needs an internal hook for
  Parquet's `pages` var, or `ipc.WithAllocator`, and `Limits{Mem: mem}`.
- **A twin of arrow-go (449):** `API: false`, so arrow-go's own panics are logged.
- **The sweep (468)** runs each file in a child process with its own limits. It can
  call `Run` inside the child, and a hang or a runaway then names the input in the
  child's dying output.
- **The nightly (474)** adds its long legs beside fuzz.yml's 30 s, or raises
  `fuzztime` through `workflow_dispatch`.

The two targets that predate the harness, `FuzzEvery` and `FuzzHybrid`, are left as
they are. fuzz.yml fuzzes them too.

## 8. Checks

- Locally, `make fuzz FUZZ=FuzzScanCSV PKG=./internal/readfuzz FUZZTIME=20s` ran
  137,107 inputs in 21 s on 4 workers, with 87 new interesting and none failing.
- `go test ./internal/fuzzcheck/... ./internal/readfuzz/ -race` and with
  `GOEXPERIMENT=` (scalar-only) both pass.
- The gate, after the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4
  GOEXPERIMENT=simd GOAMD64=v3`:
  - `go test ./internal/fuzzcheck/... ./internal/readfuzz/... ./internal/...`: ok;
  - `go test .` in full: ok, 312 s;
  - `go vet ./...`: clean;
  - `go run ./internal/gen/levels`: ok;
  - `go run ./internal/gen/api -check`: 2,481 features, 92 added and 0 gone since
    v0.7.2, as on master, since nothing public changed;
  - `go run ./internal/gen/testinventory -check`: 1,225 lines, as in the baseline;
  - `gofmt -l .`: empty.
