# Step 10 — as built

Bounded memory and external sort. Authoritative where it disagrees with
[`step-9-as-built.md`](./step-9-as-built.md) and the vision docs.

**782 tests green** (741 before this step) under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean.

This is the first step whose deliverable is a **property** rather than an API: a
sort over more data than memory, feeding a streaming sink, with the peak measured
rather than assumed.

```go
var mem ursus.MemoryStats

err := ursus.ScanParquet("120gb/*.parquet").
    Sort(ursus.Desc(ursus.Col("ts"))).
    SinkParquet(ctx, "sorted.parquet",
        ursus.WithMemoryLimit(512<<20),
        ursus.WithSpillDir("/tmp/ursus"),
        ursus.WithMemoryStats(&mem))

// mem.Peak is what it actually held; mem.Spills is how many runs it wrote.
```

Measured on the test fixture (40,000 rows, 512-row batches, sort feeding
`SinkParquet`):

| | peak retained | spills |
| --- | --- | --- |
| no limit | 2,615,168 B | 0 |
| `WithMemoryLimit(256KiB)` | 267,776 B | 9 |

A 9.8× reduction, overshooting the limit by 5,632 bytes — exactly one batch, for
the reason given in §2.

---

## 1. Why this step

Nine steps of capability had produced **seven operators that buffer**, and every
step added at least one. None of them could bound its memory, because there was
nothing to bound it with: no byte count on `data.Column` or `data.Batch`, no
budget, no `WithMemoryLimit`, and no hook at the allocation site — `arrowx` pins
`memory.NewGoAllocator`, whose `Free` is a no-op and which reports nothing.

So `ScanParquet → Filter → Project → SinkParquet` streamed beautifully and a
single `Sort` materialised the whole input. That was the v0.2 headline and the
promise furthest from being true.

`internal/execopt` had been a reserved-but-empty level in the layering map since
step 1. It exists now.

---

## 2. Accounting: identity, not arithmetic

`data.Column` holds three `*memory.Buffer` slots plus two `bitmap.View`s. Summing
them per column is easy and wrong, because **columns share payloads**: `Rename`
shares one ("O(1): the payload is shared"), so does `WithDType`, so does the
character buffer of a sliced String column, and every pipeline breaker retains the
source's own batches rather than copies.

A budget that double-counts fires early and unpredictably — worse for a user than
no budget at all — so accounting is by **allocation identity**:

```go
// BufferID identifies one allocation by the address of its first byte.
type BufferID = *byte
```

A `*byte` rather than a `uintptr` on purpose: a real pointer stays meaningful to
the garbage collector, so an identity held in a ledger can never be recycled by a
later allocation while the entry is live. It also needs no `unsafe` — `&b.Bytes()[0]`
is an ordinary address-of.

`execopt.Budget` is a **refcounted ledger** over those ids, shared by every
operator in one query. Two sinks holding one batch is one entry, released when the
last account lets go. Sizes are `Cap()`, not `Len()`: the allocation is what is
held.

### A batch walker would not have been enough

Three of the largest retentions in the engine are not batches:

| State | Shape |
| --- | --- |
| `windowSink.perRow` | one `int32` per input row, **per distinct partitioning** — two windows over different keys hold two of them |
| `joinBuildSink.rowKey` | one `int32` per build row, always, whether or not the rows are kept |
| `quantileAcc.vals` | a `[]float64` per group, so the only aggregate whose state is O(rows) rather than O(groups) |

So `Account.RetainBytes` exists beside `Account.Retain`, and
`kernel.Accumulator` grew an **`NBytes() int64` method**. That is an interface
change touching ten accumulators, and it was the right call: per-group state is
where an aggregation's memory goes, and an accumulator added later with an
unbounded shape would otherwise be silently unaccounted. Every implementation is
O(groups) so it can be called once per batch, and the sinks track it as a *delta*
rather than releasing and re-retaining.

### What the peak does and does not include

It counts what buffering operators **hold across batches**. It does not count a
kernel's transient output — there is no allocation-site hook, and
`design/physical.md`'s proposed `kernel.Ctx` carrying a reservation was never
built, so inventing one would have touched every kernel.

Two consequences, stated rather than hidden:

- The limit is checked **after** a batch is retained, which is the first moment
  its size is known. So the peak overshoots by up to one batch. That is the
  5,632 bytes in the table above.
- `spill()` concatenates the **key** columns to sort them. That allocation is
  transient and unaccounted. It is bounded by the key columns only, not by the
  data, which is why the data is never concatenated — see §5.

---

## 3. A spill format, because none of the existing ones will do

Parquet is the only structured writer in the engine, and it **refuses `Int128`** —
the accumulator and output type of every integer `Sum`, and therefore the engine's
most common intermediate. It also refuses `Time`, `Datetime`, `Duration` and
`Enum` on write. Arrow IPC is not implemented at all.

`internal/spill` is a length-prefixed dump of `{schema, rows, validity, payload}`,
~230 lines each way. It is internal, written and read by one process within one
query, with no compatibility obligation — which is exactly what made writing one
cheaper than extending Parquet. `TestSpillRoundTripsEveryDtype` covers every
constructible column type including the six Parquet refuses.

Two things it does that a naive dump would not:

- **Offsets are rebased.** A sliced String column keeps the *original* character
  buffer and re-windows its offsets, so writing the buffer as it stands would
  spill every character the column no longer refers to — and still read back
  correctly. `TestSpillRebasesSlicedStrings` asserts the file **size**, not just
  the values, because that is the only way the bug is visible.
- **A payload-free column round-trips as one.** `data.NewNull` carries no payload
  buffer, and reconstructing it as a real all-null column would silently make a
  typed null literal expensive.

It required three new accessors on `data.Column` — `RawFixed`, `RawOffsets`,
`RawChars` — documented as existing for this one caller.

---

## 4. The cross-run comparator

`kernel.NewComparator` builds a `func(i, j int) int` over **one** column set:
`withNulls` closes over a single column's validity and `valueComparator` over a
single column's accessor, and both index the same columns. A merge has to compare
row *i* of run A against row *j* of run B, which that shape cannot express at all.

`kernel.RunSet` is the sibling. It is a **type rather than a closure** for one
reason: a run's resident batch is *replaced* when it is exhausted, and a
comparator that had captured its columns would keep comparing the old batch.
`TestRunSetReplacesRunColumns` pins exactly that.

The rules the two share are shared in code, not copied:

```go
// nullRule resolves a spec's null placement into the two answers a comparator
// returns when exactly one side is null.
func nullRule(spec SortSpec) (nullFirst, nullSecond int)

// direct applies the ordering direction to a value comparison.
func direct(spec SortSpec, r int) int
```

Both are called by `withNulls` and by `RunSet.Compare`. The rule they encode —
placement applied **before** direction, so `NullsLast` means the same thing
ascending and descending — is precisely the kind that drifts between two copies
and is invisible on data with no nulls.

`TestCrossComparatorMatchesWithinRun` is the differential that licenses a second
comparator existing: every pair of rows, compared through the cross comparator,
must agree with `NewComparator` over the two runs concatenated — across seven
dtypes × two directions × two null placements, with NaN and `-0.0` in the corpus
because those are where the total order deliberately differs from IEEE.

### Note on the group-key encoder

`NewGroupKeyEncoder`'s doc claims its encoding is order-preserving "which makes
the same encoder reusable … for spilling to disk without re-deriving it". It is
not sufficient on its own: it encodes neither direction nor null placement, both
of which `SortSpec` carries. Nothing was changed there; the claim is just narrower
than it reads.

---

## 5. External sort

Three changes to `sortSink`, and one thing that did **not** change.

**`Consume` spills past the budget.** It sorts what it holds, writes it as a run,
and lets go. Runs are created in input order, which the merge's tie-break depends
on.

**Only the KEY batches are ever concatenated.** `order()` concatenates the keys,
sorts them, and returns `[]rowRef` — `(batch, row)` pairs into the retained input
batches. The data is then gathered *chunk at a time* through `gatherRows`, so
writing a run costs one output batch rather than a second copy of the buffer. The
old `Finish` concatenated the data, sorted, gathered, and returned one batch: two
full copies at the peak.

**`Finish` returns a streaming operator.** Not one batch — the merge emits as it
advances, which is the whole point. The non-spilling path streams too, through
`sortedBufferRun`, which gathers a batch at a time out of the retained input. So
*every* sort now emits at the configured batch size, spilling or not.
`TestSortStreamsItsOutput` is the only thing in the suite that would notice a
regression: a single giant batch is a perfectly correct answer.

**Keys are not spilled with the data.** A run is re-keyed when it is read back,
which halves what a run costs on disk and removes the possibility of two files
drifting out of step. Sound because a sort key is a row-local expression — one
that were not would already produce batch-size-dependent answers in `Consume`,
which the batch-size invariance tests would have caught long ago.

`gatherRows` is the primitive both halves need: rows selected out of *several*
source batches, in the order given. `kernel.Take` gathers within one column, so
this gathers each source independently, concatenates, and permutes back. Two
passes instead of a new multi-source gather kernel per physical type, on a path
that runs once per batch rather than once per row.

### Spilling does NOT go through `Sink.Merge`, and that corrects a documented claim

`sink.go` said: *"Merge is also the spilling seam: a Sink that runs out of memory
writes its partial state out, starts fresh, and merges at the end."*

Read against the interface, every verb in that sentence except "merges" is
missing: there is no spill, no reset, and `Merge` takes a live `Sink` rather than
anything on disk. Worse for the sort specifically, `Merge`'s contract is that
`other` consumed a **later portion of the input**, and `sortSink.Merge` honours it
by *concatenating in input order* — neither sink has sorted anything at that
point. The operator whose doc named `Merge` as the external-sort seam had a
`Merge` that does the opposite of a merge.

The doc is corrected rather than the code contorted. Two supporting facts are now
recorded there:

- **`Sink.Merge` is dead code kept alive by tests.** The only non-test `.Merge(`
  call anywhere is `Accumulator.Merge` inside `hashAggSink.Merge`, itself only
  reached from `sink_test.go`.
- **Five implementations, five readings** of the "LATER portion" clause: append
  (sort), *prepend* (reverse — reversal turns later into earlier), remap-and-append
  (join), refuse-unless-identical (group_by), refuse-always (window). That is a
  per-operator rule, not a universal one — and it is incompatible with the
  radix-partitioned aggregation §17 proposes, whose partials are disjoint by hash
  rather than ordered by input position. Which is why the spilling hash-agg will
  not simply reuse this seam either.

### Stability is the hard part

`ArgSort` is stable and the sort's doc calls that load-bearing rather than a
nicety. A k-way merge is stable only if a tie between runs takes from the run
holding the **earlier** input rows, so `Merger.less` breaks ties on the run index,
ascending. Within a run, rows are already in stable order and the caller always
advances a run's head by one, so intra-run order is free.

`ArgTopK` needed two independent tie fixes, both invisible on data without ties.
This is the same hazard in a new setting, so:

- `TestMergerMatchesArgSort` compares the merge to `ArgSort` **exactly**, not to
  "is it sorted" — a merge that broke ties towards the later run still produces
  sorted output.
- `TestMergerBreaksTiesByRun` makes every key identical, so nothing but the
  tie-break decides.
- End to end, the fixture's sort key is **mostly ties** (40 distinct values over
  20,000 rows), with a `seq` column carrying the input position so tie order is
  observable.

---

## 6. The top-k guard is a memory guard, not a correctness guard

The plan predicted that spilling a bounded sort would break
`ArgTopK ≡ ArgSort[:k]`, because `ArgTopK`'s totality trick breaks ties by
**original row index**, which no longer fits within a run once rows span runs.

**That prediction was wrong, and the teeth check proved it.** Removing the guard —
so a bounded sort spills like any other — left `TestTopKSurvivesSpilling`'s answer
assertion passing; only the `Spills != 0` line failed. The reason is that limit
pushdown leaves a `Limit` node **above** the `Sort`, so a merge that returns more
rows than asked is simply trimmed, and the merge's own stability makes the prefix
correct.

So what the guard actually buys is that **a bounded sort never touches the disk**.
Past the budget it *compacts* instead: sorts what it holds and keeps the best
`limit` rows.

```go
// Pruning a prefix to its own top k cannot drop a row that the whole input's top
// k would have kept — adding rows only pushes rows out — and it preserves
// stability: the retained rows keep their relative order, and they all precede
// every row consumed afterwards, which is exactly the order their input
// positions had.
func (s *sortSink) compact(ctx context.Context) error
```

`BenchmarkTopK` is what step 9 turned on and never measured. Beside `BenchmarkSort`
on the same data:

```
BenchmarkSort-8           27,831,715 ns/op   16,160,237 B/op   3,612 allocs/op
BenchmarkTopK-8            1,265,150 ns/op      605,880 B/op     545 allocs/op
BenchmarkSortSpilling-8   35,161,296 ns/op   12,993,138 B/op   3,748 allocs/op
```

22× for the O(n log k) path. And bounded memory costs **1.26×** when it is not
needed — which is the number that decides whether anyone will turn the limit on.

---

## 7. Two API changes the feature forced

### `SinkParquet` and `SinkCSV` could not take execution options at all

Both called `newCollectCfg(nil)`. Not just the memory limit — no batch size, no
thread count, no optimizer flags could reach the one consumer that streams. The
headline example in the plan did not compile.

The fix is a compile-time union rather than `...any`:

```go
type ParquetSinkOption interface{ applyParquetSink(*parquetSinkCfg) }

func (o ParquetWriteOption) applyParquetSink(c *parquetSinkCfg) { o(&c.write) }
func (o CollectOption) applyParquetSink(c *parquetSinkCfg)      { o(&c.collect) }
```

Named function types can have methods, so both existing option types satisfy it
and every existing call site still compiles. A library that spends generic methods
and the `Operand` constraint to stop `Col("x").Gt(struct{}{})` compiling should
not then accept `SinkParquet(ctx, path, "oops")`.

### `MemoryStats`

Boundedness is only demonstrable if the peak is readable:

```go
type MemoryStats struct{ Peak, Limit, Spills int64 }
func WithMemoryStats(out *MemoryStats) CollectOption
```

`Spills` is not decoration. Without it, "external sort matches in-memory sort"
passes trivially when the budget turned out to be large enough that nothing
spilled — the same failure mode as an all-ASCII corpus in a UTF-8 test. Four tests
assert on it, including one that requires a *bounded* sort to spill **zero** times.

`uerr` gained `KindResource` / `ErrResource`: the one class of failure where the
right response is to change a knob and retry, and a caller should be able to
detect that without matching on a message.

---

## 8. The three things step 9 left behind

All found by re-reading my own work, not by any probe.

| | |
| --- | --- |
| `plan.unionTargetSchema` | Dead code I wrote and never called. Go does not flag an unused unexported function, so `go vet` was silent. Deleted. |
| `plan.ReconcileSchemas` | Exported on the argument that the physical planner would need the same answer the plan reports. It never did — `resolveUnion` adapts every child inside the plan. Unexported. |
| `ursus.Concat(frames ...any)` | `ursus.Concat(a, "oops")` compiled and failed at run time. Now `Concat(frames []*LazyFrame, opts ...ConcatOption)`, as the spec always said. The ergonomic case keeps both properties through the method form: `jan.Concat(feb, mar)` is variadic *and* type-safe. |

---

## 9. One thing the tests found

`data.NewNull`'s doc says it "carries no payload buffer, which makes a typed null
literal free". The payload really is absent — but `bitmap.Zeros(n)` **materialises
n bits**. For the length-1 literal that motivated the comment that is one byte;
for a thousand rows it is 128, and a reader of that sentence could reasonably
expect zero. `TestNBytesCountsEveryPayloadSlot` now pins the actual figure rather
than the implied one.

---

## 10. Honest gaps

**Three of the seven buffering operators are outside this step, and outside the
three things v0.2 names.**

- **`windowSink` is O(rows) by definition** — its output is as large as its input,
  so "spilling" it means spilling the *output*, a different problem.
- **`reverseSink`** (step 9) is O(input) for the same reason.
- **`hstackOp`** (step 9) is O(total rows) × inputs, and its own doc argues the
  bounded alternative is a net loss: re-chunking would slice every child at every
  boundary, and `Column.Slice` **copies for fixed-width types** because the
  representation carries no offset.

Bounding those needs a chunked `Column` — `ursus-api.md` §17 item 4, still open.
They are **accounted and refused**, not spilled: past the limit they fail with an
error naming the operator, which is a better outcome than being killed by the OS.

So the honest claim is **"a sort over more data than RAM"**, not "bounded memory
everywhere". Accounting covers all seven; spilling covers one.

**Still out, with reasons:**

- **Spilling hash-agg.** `hashAggSink.Merge` refuses divergent group numbering, and
  its own comment says a partitioned aggregation is exactly the case it refuses. It
  needs a group-id remap across ~20 accumulators, or a radix-partition-before-
  accumulate path — and §5 records why the `Sink.Merge` seam will not serve either.
- **Spilling hash-join**, for the same reason, though `joinBuildSink.Merge` already
  contains a working id remap.
- **The morsel scheduler.** It would replace `parallelOp`'s round-robin dispatch,
  which is what currently *buys* ordering structurally, and five things depend on
  input order.
- **`exec.Collect` is 2× the result and is not a spill target.** Asking for a frame
  in memory is a request to hold it. The larger-than-RAM story runs through
  `SinkParquet`, `SinkCSV` and `CollectBatches`, which is why
  `TestSortedSinkIsBounded` deliberately does not use `Collect`.
- **`nuniqueAcc.NBytes` under-reports long keys.** It counts a per-entry constant
  rather than the key bytes; counting exactly would mean walking every key on every
  batch. Documented at the method.

---

## 11. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean. 782 test cases, up from 741.

| Test | What it catches |
| --- | --- |
| `TestNBytesDoesNotDoubleCount` | a renamed column and a shared batch counted twice — the failure that makes a budget fire early |
| `TestNBytesCountsEveryPayloadSlot` | every payload slot, the all-set validity form costing nothing, and `NewNull`'s real size |
| `TestBudgetCountsSharedAllocationsOnce` | refcounting: held while any account holds it, released when the last lets go |
| `TestBudgetTracksPlainState` | `RetainBytes`, including a negative delta, and that the error is `ErrResource` and names the operator |
| `TestNilBudgetIsAWorkingNoOp` | `Options` built by hand, which every physical-package test does |
| `TestSpillRoundTripsEveryDtype` | every constructible dtype, including the six Parquet refuses |
| `TestSpillRoundTripsNullsAndEmpty` | all-null columns, zero-row batches, and a zero-**column** batch that still has rows |
| `TestSpillRebasesSlicedStrings` | the file **size**, which is the only way the sliced-string waste is visible |
| `TestCrossComparatorMatchesWithinRun` | differentially against `NewComparator`, over 7 dtypes × direction × null placement |
| `TestMergerMatchesArgSort` | the merge equals `ArgSort` exactly, ties included, over 1/2/5 runs |
| `TestMergerBreaksTiesByRun` | the stability tie-break with every key identical |
| `TestRunSetReplacesRunColumns` | a run's columns being swapped mid-merge, which is what makes it bounded |
| `TestExternalSortMatchesInMemory` | same answer at three limits, each asserted to have actually spilled |
| `TestExternalSortIsStable` | ties keeping input order across run boundaries, against the definition |
| `TestTopKSurvivesSpilling` | `ArgTopK ≡ ArgSort[:k]` under a budget, and that a bounded sort spills zero times |
| `TestSortStreamsItsOutput` | the sort emits batches, not one allocation the size of its input |
| `TestSortedSinkIsBounded` | the deliverable, as a peak measurement — with the unbounded run asserted to exceed the limit, so the fixture cannot be too small |
| `TestSpillFilesAreCleanedUp` | success, early `break` out of `CollectBatches`, and cancellation |
| `TestBudgetErrorNamesTheOperator` | all six operators that cannot spill |
| `TestMemoryLimitIsNotASemanticKnob` | eight query shapes × four limits, with spilling asserted for the seven that should |
| `BenchmarkTopK`, `BenchmarkSortSpilling` | step 9's unmeasured O(n log k) path, and what bounded memory costs |

**Teeth.** Five, each by reintroduction, each confirmed to fail the named test:

| Reintroduced | Failed |
| --- | --- |
| `Budget.retain` adds unconditionally (no dedup) | `TestBudgetCountsSharedAllocationsOnce` — "a shared batch counted twice: 64 then 128" |
| `RunSet.Compare` returns `r` instead of `direct(spec, r)` | `TestCrossComparatorMatchesWithinRun`, `TestMergerMatchesArgSort`, and end-to-end `TestExternalSortIsStable` — "key 7 after 0, but the sort is descending" |
| `chunk()` returns 1<<30, i.e. `Finish` emits one batch | `TestSortStreamsItsOutput` — "widest batch is 5000 rows, batch size is 256" |
| `Consume` spills instead of compacting under a limit | `TestTopKSurvivesSpilling` — "a bounded sort spilled 19 times". **The answer stayed correct**, which is what corrected §6. |
| `Merger.less` breaks ties towards the later run | `TestMergerBreaksTiesByRun`, `TestMergerMatchesArgSort`, `TestExternalSortMatchesInMemory`, `TestExternalSortIsStable` — "seq went 7993 then 6145" |
