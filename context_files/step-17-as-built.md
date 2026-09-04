# Step 17 — as built

Parallel hash aggregation. **`Sink.Merge` runs in production for the first time**,
sixteen steps after it was written.

Authoritative where it disagrees with [`step-16-as-built.md`](./step-16-as-built.md),
the vision docs and [`design/`](./design/).

**1321 test cases green** — 476 top-level and 845 subtests (1299 before this step) —
under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment off,
and under `-race`. `make levels` and `go vet` clean.

194 files, ~58.6k lines (excluding `bench/`, which is its own module). No new
`LazyFrame` operations: this step is entirely behind `WithThreads`.

```
GroupBy(k).Agg(Sum(v))   over 2,000,000 rows, in memory

  100 groups     59.3 ms  ->  12.4 ms     4.76x on 8 threads
  10,000 groups  78.2 ms  ->  22.3 ms     3.50x
```

---

## 1. This is the first step driven by a measurement

`bench/` turns out to hold a complete harness — PDS-H (TPC-H, 22 queries) and
h2o.ai (10 groupby + 5 join) against duckdb, polars, pandas, DataFusion, chDB,
Arrow-Go, Gota and QFrame, with generated data, duckdb reference answers and a
validator. `bench/results/REPORT.md` claimed `2/2 queries passed` and listed only
q1 and q6, because it had been generated from a two-query smoke run and never
refreshed.

Regenerated from the timings already on disk, it says something much better:
**ursus runs and VALIDATES all 22 TPC-H queries**, and 14 of 15 h2o queries. What
it also says is that ursus was 6.2× slower than polars on TPC-H geomean, and the
reason was structural — `parallelise` wraps only *pure pipelines*, so every
pipeline breaker ran single-threaded against polars' eight.

`sink.go` had described the fix since step 1:

> *"Consume/Merge/Finish is the shape a parallel scheduler needs. N workers each
> get their own Sink, consume morsels independently, and the partial results are
> merged."*

and `hashAggSink.Merge` named the one missing piece — *"a real parallel merge must
remap the other sink's group ids… that remapping is the piece parallel aggregation
needs"*. This step is that remap and that scheduler.

---

## 2. Two gates, both checked rather than hoped

`Sink.Merge`'s contract is that `other` consumed a **LATER portion** of the input.
Round-robin dispatch does not satisfy it — worker 0 holds batches 0, N, 2N…, so
worker 1's batch 1 precedes worker 0's batch N and neither sink holds anything
contiguous. Rather than weaken the contract, `aggWorkers` returns 1 unless the
merge is order-insensitive:

| Falls back to serial when | Because |
| --- | --- |
| `MaintainOrder` | `firstSeen` holds per-sink input ordinals; two sinks that saw interleaved portions numbered their rows independently |
| any of `First`, `Last`, `ArgMin`, `ArgMax` | the first two name a position; the second two report an INDEX that `argExtremumAcc.Merge` shifts by a row count it assumes came earlier |
| `budget.Limit() > 0` | all three spilling sinks refuse to Merge once frozen, and N workers splitting one budget makes spilling *likelier* |

The memory gate is not theoretical. Removed, with a 16 KB limit, the query fails
outright with *"cannot merge hashAggSinks that have spilled"* — measured, along
with the fact that at 64 KB the workers stay resident and it succeeds, which is why
the test uses the tighter limit.

New: `expr.AggOp.IsOrderDependent()`, beside `IsCounting()`. The whole aggregate is
declined rather than the individual expression, because one `Agg` list is one hash
table.

---

## 3. The remap, and the half a join never needs

`Accumulator.Merge(other)` becomes `Merge(other, remap []int32)`, where `remap[i]`
is this accumulator's group id for other's group i and nil means the numbering
already agrees. Ten implementations; each loop goes from `a.x[i] op= o.x[i]` to
`a.x[remap[i]] op= o.x[i]` through two shared helpers, `mergeCap` and `mergeEach`,
so ten copies of the nil-remap branch do not become ten slightly different ones.

`hashAggSink.Merge` then follows `joinBuildSink.Merge`'s shape — except for one
piece the join genuinely does not need. A join's per-key state is a row COUNT, so
its keys never have to be materialised again. Here `Finish` concatenates `keyParts`
and pairs the result **positionally** with one aggregate row per group, so a group
merged in without its key row shifts every key after it. Reintroduced, that gives
`batch column "__agg0" has 679 rows, but column "k" has 278`.

**One detail is load-bearing and looks like tidiness.** The merge visits the other
sink's keys in ITS id order — via an inverse array — rather than by ranging its
map. Ranging would still produce correct totals; it would make the merged group
ORDER vary run to run, turning a deterministic output into a random one.
`TestParallelAggregationIsDeterministic` runs the same query thirteen times.

---

## 4. Two contracts had to be narrowed, and both are real

**Floating-point sums reassociate.** A parallel sum adds the same values in a
different order, so `1047` becomes `1047.0000000000002`. Every parallel engine does
this. `WithThreads` used to promise "byte-identical output at any thread count";
that is still true of the pipeline and is now false of a float group-by, and the
doc says so.

**Group order changes.** Group ids are assigned by first appearance, so the serial
path emits groups in first-appearance order *as a side effect*. Under N workers
each sink numbers only what it saw and the merge appends the groups a later worker
introduced. This is within the existing contract — `plan.Aggregate.MaintainOrder`
exists to pin exactly this and its doc already calls the order *"genuinely
unspecified"* without it — but it is a visible change, and `MaintainOrder` is one
of the things that now forces the serial path.

---

## 5. What it bought, measured

End to end, h2o at 1e7 rows from Parquet, and TPC-H q1 at sf=0.1:

| query | 1 thread | 8 threads | speedup |
| --- | --: | --: | --: |
| gb1 — `sum(v1) by id1` | 1035 ms | 703 ms | 1.47× |
| gb3 — `sum(v1), mean(v3) by id3` | 3420 ms | 1796 ms | 1.90× |
| gb5 — `sum(v1..v3) by id6` | 2433 ms | 1234 ms | 1.97× |
| gb7 — `max(v1), min(v2) by id3` | 77,751 ms | 27,827 ms | 2.79× |
| **gb10 — `sum(v3), count by id1..id6`** | **10,875 ms** | **10,866 ms** | **1.00×** |
| TPC-H q1 | 207 ms | 131 ms | 1.58× |

The in-memory microbenchmark reaches 4.76×; end to end it does not, because the
Parquet scan is a large share of the wall clock and was already parallel.

**gb10 gains nothing at all, and that is the interesting number.** It groups by six
columns and emits 10,000,000 rows — one group per input row. The merge is
O(groups) per worker, so when the group count approaches the row count each worker
builds a full-size table and the fold costs exactly what the parallelism saved.
That is the shape radix partitioning exists for, and `sink.go` already records why
the current Merge cannot express it: *"it is incompatible with a radix-partitioned
aggregation, whose partials are disjoint by hash rather than ordered by input
position"*.

---

## 6. `ursustest.WithTolerance` was decorative

Comparing a parallel float sum against a serial one needs a tolerance, and
`ursustest` had one — with **zero callers**, and unreachable in the case it was
written for. `AssertFrameEqual` compares rendered rows exactly FIRST, so a one-ulp
difference fails there and the numeric pass below never runs. The option, its
config fields and `assertFloatsClose` were all written and none of it could fire.

Fixed by moving float columns off the exact-rendering path when a tolerance is
configured. Combining it with `IgnoreRowOrder` now fails loudly rather than
silently comparing unrelated rows: `assertFloatsClose` walks positionally and
cannot align frames that were sorted into agreement by their rendered form.

---

## 7. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean. **`-race` is the load-bearing one this step**: it is the first
time anything in the module runs a Sink concurrently.

Five teeth checks fired, each by reintroducing the defect:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| Pass `nil` instead of the remap | `TestHashAggSinkMergeRemapsGroupIds`, `TestParallelAggregationMatchesSerial` | `1,100;2,200;3,300` where `1,300;2,200;3,100` belongs — the reversal, exactly |
| Drop the `keyParts` append | `TestHashAggSinkMergeAppendsNewGroupKeys` | `column "__agg0" has 679 rows, but column "k" has 278` |
| Allow merging ordered sinks | `TestHashAggSinkMergeRefusesMaintainOrder` | an invented first-appearance ordering |
| Remove the order gate | `TestOrderDependentAggregatesStaySerial` | `Last` returned 140.857 where 141.857 belongs |
| Remove the memory gate | `TestMemoryLimitDisablesParallelAggregation` | *"cannot merge hashAggSinks that have spilled"* — a working query turned into a failing one |

**One did not fire, and it is recorded rather than dressed up.** Removing the
`n == 1` special case — so a one-worker `parallelSink` replaces the plain `breaker`
— breaks nothing at all. That branch is there for clarity and to keep the serial
operator tree free of a goroutine and two channels, not for correctness.

**And one test had to be rebuilt before it had teeth.** The first fixture used
`ursus.Values`, which produces a single 4000-row batch — so the dispatcher handed
everything to worker 0, every other sink stayed empty, the merge folded nothing,
and the teeth check for the remap **passed with the remap deleted**. `WithBatchSize`
does not help: `memsrc` serves the batches it was built from and never re-chunks.
The fixture now builds 32 explicit batches, after which the same check fails
loudly.

---

## 8. A pre-existing wrong answer, found by refreshing the report

**h2o gb7 returns 96 spurious NULLs out of 100,000 groups, and has done since
before this step.** It is the one h2o query that fails validation:
`sum_range_v1_v2: 399495 != 399879`, and 399879 − 399495 = 384 = 96 × 4.

Confirmed against the raw data rather than inferred. Four of the groups ursus
reports as null — `id0000040297`, `id0000041005`, `id0000016397`, `id0000092467` —
have **no nulls at all** in `v1` or `v2` and a true range of 4.

It is not a regression from this step: the SERIAL path produces the same 96 nulls
and the same checksum. Parallelism only shuffles *which* 96 groups are affected,
which is why per-key output differs between one thread and eight while the
checksum does not — a permutation a checksum cannot see, and worth remembering
before trusting one.

The lead: `kernel.assembleRows` builds a validity bitmap from `c.IsValid(0)` and
then **never uses it**, relying on `concatColumn` to carry validity across up to
100,000 single-row parts instead. That is the same `extremumAcc` whose per-group
`*data.Column` storage makes gb7 76× slower than polars, so one step can plausibly
fix both.

---

## 9. Honest gaps

- **gb7's spurious nulls** (§8). A wrong answer outranks everything below.
- **`extremumAcc` is still 76× slower than polars** — one heap `*data.Column` per
  group, plus a `map[int32]int`, a `Take` and a `concatColumn` per group per batch,
  where `sumAcc` uses flat typed slices. 2.79× of that is now paid back by
  parallelism; the rest is an allocation defect.
- **Parallel join build and parallel sort are not done**, deliberately (§2 of the
  plan). Sort's work is in `Finish`, not `Consume`, and its Merge concatenates in
  input order to keep the sort stable; the join's Merge is already correct but
  round-robin would move intra-key match order, and the build side is the smaller
  input.
- **The join PROBE is serial**, and it is what actually dominates a join when the
  left side is large — j1–j5 sit at 6–15× and this step does not touch them.
- **Peak memory rises to ~N× the group table** without a limit, because each worker
  builds its own. The memory gate makes that trade explicit rather than silent.
- **Spilling and parallelism remain mutually exclusive**, and making partials
  re-aggregatable is what would unlock both that and radix partitioning.
- **gb8's `Rank().Over()`** at 20×, **CSE**, **`JoinWhere`** and
  **`reverseSink.Merge`'s missing test** are unchanged from step 16.

---

## 10. Files

**New:** `internal/physical/parallelsink.go` (181), `parallelagg_test.go`.

| File | Change |
| --- | --- |
| `internal/kernel/agg.go`, `aggstat.go` | `Accumulator.Merge` gains `remap`; `mergeCap`/`mergeEach`; ten implementations |
| `internal/physical/agg.go` | `hashAggSink.Merge` does the remap and the key rows; `aggWorkers` and `aggBreaker`; `planAggregate` builds sinks through one factory |
| `internal/expr/agg.go` | `AggOp.IsOrderDependent()` |
| `internal/physical/sink_test.go` | `sameNumbering` and its test replaced by three that exercise the remap, the key rows and the refusal |
| `lazy.go` | `optimizer()` unchanged; `WithThreads`' contract narrowed (§4) |
| `ursustest/ursustest.go` | `WithTolerance` made reachable (§6) |
| `bench/results/` | regenerated from the existing timings — 22/22 TPC-H, not 2/2 |
