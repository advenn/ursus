# Step 38 — as built

**The memory number has an explanation now.** It was the one thing the project said
it could not account for; it turns out to be four separate things, and the largest of
them is the cost of a feature ursus does not have.

Authoritative where it disagrees with [`step-37-as-built.md`](./step-37-as-built.md),
the vision docs and [`design/`](./design/).

No `internal/` change: this step built an instrument and used it. **1529 test cases
still green** under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the
experiment off, and under `-race`. `make levels` and `go vet` clean in all three
modules. PDS-H SF=0.1 validates 22/22.

---

## 1. It was never "ursus uses 4.7x". It was one query.

Free, from results already in the repo:

| PDS-H SF=1 | ursus | polars | ratio |
| --- | --: | --: | --: |
| **q21** | **3.62 GB** | **0.23 GB** | **15.6x** |
| q9 | 1.51 | 0.70 | 2.2x |
| q8 | 1.10 | 0.18 | 6.2x |
| q18 | 0.10 | 0.61 | **0.2x** |
| q10 | 0.29 | 0.43 | **0.7x** |

The published figure is a MAX across queries, so q21 *is* the headline. ursus uses
**less** than polars on two of them, which rules out a systemic overhead before any
measurement. h2o agrees from the other side: the joins are at parity (j1 1.0x, j3
1.1x, j5 1.3x) and the group-bys are not (gb10 2.4x at 8.11 GB, gb8 3.1x).

---

## 2. Three numbers, because VmHWM cannot say why

The harness reported `VmHWM` alone. It now reports four things per run, and the gaps
between them are the answer:

```
q21, SF=1, GOGC=100

  accounted (ursus's own MemoryStats.Peak)   0.81 GB
  live heap at the sampled peak, post-GC     1.08 GB
  peak HeapInuse (live + uncollected)        3.82 GB
  VmHWM                                      3.89 GB

  total allocated over the query            12.68 GB   in 42 collections
```

**ursus's accounting is roughly honest** — 0.81 GB claimed against ~1.08 GB actually
live. The engine is not lying about what it holds.

**The peak is mostly garbage, not retention.** 3.82 GB of heap in use against 1.08 GB
live, and 12.68 GB allocated over a query whose working set is about a gigabyte. The
heap is large because allocation outruns collection, not because ursus is holding on
to anything.

### Getting this wrong first

The first version read `MemStats` once, at the end. It reported **0.00 GB live**
against 3.85 GB of `HeapSys` — by then the answer had been released, so the number
was true and meaningless. That is tooth #2 from the plan, walked into rather than
avoided: reading the heap at the wrong moment answers a different question and looks
identical. The instrument now samples every 5 ms and keeps the maximum.

---

## 3. The GC costs about 30%, not the gap

| GOGC | peak heap | VmHWM | total allocated | GCs | time |
| --: | --: | --: | --: | --: | --: |
| 100 | 3.82 GB | 3.89 GB | 12.68 GB | 42 | 5.29 s |
| 50 | 3.03 | 3.13 | 12.68 | 79 | 6.75 s |
| 25 | 2.66 | 2.77 | 12.68 | 157 | 6.48 s |

`TotalAlloc` is identical to the byte — the work does not change, only how often it is
swept. Collecting four times as often buys **1.4x** of peak RSS for roughly 20% more
time, and it asymptotes well above the live set.

So "set GOGC lower" is a real but small lever, and **the memory story is the
allocation rate**, not the GC's target. That matters for what to do next: tuning the
collector is not the fix.

---

## 4. What is actually live: one Go map per group

The heap profile at the peak, `inuse_space`:

```
  497.57MB 46.13%  kernel.(*nuniqueAcc).AddBatch
  301.55MB 27.95%  kernel.(*nuniqueAcc).Merge.func1
  101.07MB  9.37%  kernel.(*KeyTable).GetOrInsert
```

**`nuniqueAcc` is 74% of the live heap**, and its storage is

```go
type nuniqueAcc struct {
    sets []map[string]struct{}   // one map PER GROUP
}
```

q21 groups `lineitem` by `l_orderkey` — about 1.5 million groups at SF=1, so about
1.5 million Go maps. This is precisely the layout `joinTable`'s doc rejected for the
join, in an argument that was never carried across: *"A map to a slice costs a slice
header plus a backing array per distinct key: at 5M keys that is ~120 MB of headers,
5M allocations, and 5M pointers for the GC to trace on every cycle."* The join got
CSR; `n_unique` never did.

---

## 5. And the reason q21 uses `n_unique` at all is a missing feature

q21 is *"suppliers who kept orders waiting"* — a self semi join plus a self anti
join. polars expresses it that way and never materialises a distinct set. **ursus has
no `JoinWhere` (non-equi join)**, which its README lists under "not done", so
`ursusengine/pdsh.go` expresses the same condition as two `NUnique()` aggregations
over `l_suppkey` grouped by `l_orderkey`.

So the largest single memory number in the published results is **the cost of working
around a missing feature**, paid in a data structure that was already known to be the
wrong one. Both halves are fixable and neither is "ursus is a memory hog".

---

## 6. The honest sentence, replacing "I don't have a satisfying explanation"

> The peak is one query. On it, about a gigabyte is live and the rest is uncollected
> garbage — ursus allocates 12.7 GB over a query with a 1 GB working set. Of the live
> gigabyte, three quarters is `n_unique` keeping one Go map per group, and it is only
> running `n_unique` because ursus lacks the non-equi join the query wants.

---

## 7. Verification

The code lives entirely in `bench/engines/go`; `internal/` is untouched, so the root
gate is a formality that was run anyway. What needed checking is the instrument:

- **`MemoryStats` is read after `Collect` returns**, since it is written at the end of
  the query — and deliberately **not** stored on the `answer`, because `answer` reuses
  its option slice for a checksum aggregation whose far smaller Collect would
  overwrite the query's figure with its own. That trap is in the code as a comment.
- **The peak is sampled, not read once** (§2).
- **`accounted <= live` holds** — 0.81 against 1.08 — which is the sanity check that
  the accounting does not double-count.
- The new fields land in the raw JSON, which the Go engine writes directly, so the
  Python driver needed no change. `timings.csv` is gitignored; **`REPORT.md` was not
  regenerated** and `git status` confirms it.

---

## 8. What is still open

- **`nuniqueAcc`'s one-map-per-group.** The obvious next step, and now measured: 74%
  of q21's live heap. The join's CSR is the worked example of the alternative.
- **`JoinWhere`** — the non-equi join. It would remove q21's need for `n_unique`
  entirely, which makes it a memory fix as well as a feature.
- **The allocation rate itself** — 12.68 GB for a 1 GB working set. Worth a profile of
  `alloc_space` rather than `inuse_space`, which is a different question from this one.
- **Re-running the suite.** The published timings predate steps 36–37, which took the
  join from serial to ~3.7x on eight threads. A deliberate, heavy act and its own step.
- The standing list: the join build side (`Merge`, still never called), Right/Full
  parallel probe, `KeyTable.Get`'s memory latency, nested writing and `as_struct`,
  `.list` set operations, Map/Array, Pivot/Unpivot, SQL, cloud stores, join reordering.
