# Step 27 — as built

**The optimisation did not land.** What shipped is the instrument that measured
it, and two findings that scope the next step precisely: **the join ignores
threads entirely**, and 72% of it is one function that is memory-latency-bound.

Authoritative where it disagrees with [`step-26-as-built.md`](./step-26-as-built.md),
the vision docs and [`design/`](./design/).

No change to `internal/`. 1415 test cases still green; the root module is
untouched.

```
opbench join, 5M rows joined to 100k, NO file IO

  -threads 1     1,097.7 ms
  -threads 8     1,253.4 ms      <- more threads is SLOWER
```

---

## 1. A new instrument, because the old ones cannot see the join

`bench/micro` reaches every operation through a Parquet file, and step 24 found
`BenchmarkJoinInner` spends 87% of its samples outside the join. The suite proper
has the same property deliberately — it measures what a real query does, IO
included. Neither can answer "where does the JOIN go".

`bench/engines/go/cmd/opbench` already existed for exactly this reason — "times
ursus operations with no file IO anywhere", 5M-row left against a 100k-row right,
materialised outside the timed region, with a polars twin running the identical
query. It had no way to be profiled. It now takes:

| flag | why |
| --- | --- |
| `-cpuprofile` / `-memprofile` | the profile starts AFTER the fixture is built, so materialising 5M rows never appears in it |
| `-only <op>` | profile one operation without six others' samples in the file |
| `-threads N` | the one-line experiment that produced the headline above |

This is the same move as `radix_bench_test.go` in step 24: when the existing
benchmarks cannot resolve the question, build the one that can before changing
anything.

---

## 2. Two findings

### The join is serial, and threads make it worse

1,097.7 ms on one thread, 1,253.4 ms on eight. Not "scales poorly" — *negative*.
The pipeline below the join parallelises, then everything funnels into one probe
goroutine, and the extra workers only add dispatch cost.

The cause is structural and was found by reading before measuring: **`join.go`
contains no `parallelSink`, no worker count, and no reference to
`Options.Threads` at all.** The hash aggregate has been parallel since step 17;
the join was never wired up.

Two assets for fixing it already exist and are unused:

- **`joinBuildSink.Merge` is implemented and tested but never called** — its test
  is titled *"runs a method that has never executed"*, the same state
  `hashAggSink.Merge` was in for sixteen steps before step 17. Step 23 also made
  it deterministic.
- **`joinTable` was designed for this**: "Nothing mutates it after freeze, which
  is what would let a future morsel scheduler share one table across N probe
  workers with no lock." Step 23 preserved it by giving `KeyTable.Get` no writes.

And the h2o queries are the tractable case: j1/j2/j4/j5 are Inner, j3 is Left, and
the only cross-batch mutable probe state — `matched`, `seen` — is allocated *only*
for Right and Full (`join.go:447`).

### 72% of the join is `KeyTable.Get`, and it is waiting on memory

```
joinProbeOp.probeStep                    4.66s  95.5%
  kernel.(*KeyTable).Get                 3.47s  71.1%   flat 2.17s
    memeqbody (via bytes.Equal)          0.73s  15.0%
    keyAt                                0.39s   8.0%
  kernel.Take (materialising output)     0.66s  13.5%
```

Roughly 173 ns a lookup, which is not work — it is four dependent random
accesses, each waiting on the one before: `slots[i]` → `hashes[got]` →
`offs[got]` → `arena[…]`. `slots` is indexed by SLOT and `hashes` by **ID**, so
the second access lands at an index uncorrelated with the first.

---

## 3. The fix I tried, why it looked right, and why it is reverted

Merge `slots` and `hashes` into one slot-indexed array so the occupancy test and
the hash filter arrive in one cache line:

```go
type entry struct { hash uint64; id int32; _ int32 }   // 16B, four per line
```

It worked as designed on its own terms — `Get`'s **flat** time fell 2.17s → 1.78s
(−18%) — and every test passed. But `Get`'s cumulative time did not move
(3.47s → 3.42s): the cost relocated into `memeqbody` (0.73s → 0.96s), which is
the arena fetch on every hit.

Then matched five-sample runs, cleanly separated with no overlap:

```
before (slots + id-indexed hashes)   892  979  1043  1203   892     mean 1002.5 ms
after  (merged slot-indexed entries) 1400 1499 1617  1630  1726     mean 1574.3 ms
```

**And there is a mechanism, which I should have seen when designing it.** For
100,000 keys at 3/4 load — 262,144 slots:

| | index bytes |
| --- | --: |
| `slots []int32` + `hashes []uint64` per **id** | 1.05 MB + 0.80 MB = **1.85 MB** |
| `entries []entry`, 16 B per **slot** | **4.20 MB** |

Slot-indexing makes every EMPTY slot carry a hash, and the padding I called
"load-bearing" wasted four more bytes each. The trade was one indirection against
**2.3x more randomly-accessed memory** — enough to move the index out of L2 on a
machine whose L2 is 1.25 MB a core. Saving an indirection is worthless if it
costs the working set.

So the original design is not the mistake it looked like: indexing `hashes` by id
is what keeps it compact, because only occupied entries have one. Reverted whole.

---

## 4. What this step did not do, and why

The plan was to parallelise. The measurement supports it — an 8x-idle machine and
a 95% serial probe — but the implementation is a genuinely new operator, not a
tweak. `parallelOp` exists and preserves order structurally (round-robin dispatch,
round-robin collection), but it composes `BatchOp`s, and `BatchOp` is contractually
"a stateless, order-independent transform of **one batch into one batch**". The
probe is one-in-many-out: j5 turns a 10M-row probe side into 9M output rows split
at `spec.batchSize`. Its `parResult` protocol already tolerates zero-or-one output
per input (`r.b == nil` means "filtered away, not end of stream"), so the
extension is a per-input-batch `last` marker plus a worker that owns probe state
over the shared frozen table — but that is a new operator with its own teeth and
its own race surface.

Starting it with the context left would have meant leaving it half-done. It is
now scoped by measurement rather than by guesswork, which is what this step was
for.

---

## 5. Verification

- `go test ./...` clean; `internal/` untouched after the revert.
- `go vet` clean in all three modules.
- `opbench` with no flags still runs all six operations, unchanged — the `-only`
  filter defaults to running everything.
- The revert verified by `git checkout HEAD -- internal/kernel/keytable.go`
  followed by a full test run.

`bench/results/REPORT.md` in this commit is a **full multi-engine re-run at
`cd1d78a`**, not mine — and it confirms step 25's provenance fix works, since the
generator stamped its own commit and date into the file. It supersedes the
hand-written staleness note, which is exactly what should happen once the numbers
are current.

Its fresh numbers sharpen the case for the next step: group-bys are 1.4x–9.6x
polars, **every join is 6.5x–13.8x** — j3 13.8x, j2 9.9x, j5 7.9x, j1 7.2x,
j4 6.5x. The worst group-by is better than the best join.

---

## 6. What is still open

- **Parallelise the join** (§4), now the best-evidenced item in the backlog.
- **`KeyTable.Get` is memory-bound** (§2) and the obvious layout fix is refuted
  (§3). What is left is reducing the arena access, not the indirection: an inline
  key for short keys would remove `keyAt` + `memeqbody` (30% of the join) but
  doubles the entry, which is the trade that just failed — so it needs measuring
  first, on a table that does not fit cache as well as one that does.
- **gb10's +40% peak memory** from step 26.
- **The arrow-go Parquet floor**, **`windowSink` parallelism**, the **top-k
  rewrite** for gb8, **unaccounted kernel scratch**, **`hashAggSink`'s unaccounted
  group table**, **`argExtremumAcc`/`positionAcc`**, **`nuniqueAcc`'s per-group
  sets**, **string kernels**, **CSE**, **`JoinWhere`**, **nested types**.
