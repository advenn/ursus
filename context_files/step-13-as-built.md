# Step 13 — as built

The spilling hash join. Authoritative where it disagrees with
[`step-12-as-built.md`](./step-12-as-built.md) and the vision docs.

**971 test cases green** — 400 top-level tests and 571 subtests (907 before this
step) — under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the
experiment off, and under `-race`. `make levels` and `go vet` clean.

`Expr` still has 94 methods and `LazyFrame` 37 operations: **this step adds no API.**
170 files, ~51.0k lines.

```go
// This used to fail with "memory limit of 4.0KiB exceeded" and a hint naming join.
// It now partitions both sides on the key and replays the buckets.
lf.Join(other, ursus.JoinOn(ursus.Col("user_id"))).
    SinkCSV(ctx, "out.csv",
        ursus.WithMemoryLimit(64<<20), ursus.WithSpillDir("/tmp/ursus"))
```

`dataframe-features.md` §14's v0.2 line names *"streaming engine with spilling
hash-agg and hash-join, external sort"*. Step 10 built the sort, step 12 the
aggregation, and both deferred the join by name. **This is the third of the three,
and the streaming line is complete.**

---

## 1. Step 12's design does not transfer, and the measurement says so

Step 12 freezes its resident KEY SET: a key already in the table stays, a new key is
routed. It bounds the aggregation because per-group state is O(1) —
`extagg.go`'s own words, *"Reserve(nGroups) is a no-op once nGroups is fixed, so
every O(groups) accumulator's NBytes goes flat at the freeze."*

A join retains build ROWS. A resident key keeps collecting them for as long as the
build side streams, so the keys admitted before the freeze go on growing after it.

**Measured, on data with no skew** — 500 distinct keys, 60 000 build rows, a 256 KiB
limit:

| Residency rule | Peak | Spills |
| --- | --- | --- |
| Step 12's freeze, dropped in as-is | **1 276 628 B** (4.9× the limit) | **0** |
| Shipped | 269 524 B (1.03× the limit) | 30 |

Zero spills, five times over budget, and the rule working exactly as designed — the
first batch admits every key and every later row belongs to one of them.

### What actually fixes it, stated precisely

The first version of this plan said "residency by hash bucket" and stopped there.
Building it showed that is half the answer, and the halves do different jobs.

**Residency by HASH BUCKET makes the hybrid stage useful.** The resident set becomes
a fixed *fraction* of the key space rather than "whatever arrived first", so it is
bounded by construction and the sink can stay there: bucket 0 is never written, never
read back, and its probe rows come out in probe order.

**`splitAll` makes it BOUNDED.** Giving up residency entirely is the escape hatch for
a bucket 0 that is itself too large, and it is what the guarantee rests on. Removing
it fails `TestSpillingJoinMatchesInMemory/RIGHT`; the bucket rule alone is bounded on
every fixture here but nothing makes it bounded in general, because one bucket can
hold one key.

So the build side has three states rather than two:

| State | Meaning |
| --- | --- |
| `splitNone` | nothing overflowed; **byte-identical to a join with no limit** |
| `splitHybrid` | bucket 0 is resident, everything else routed |
| `splitAll` | nothing resident; the floor is one batch plus the write buffers |

### Semi and Anti are the exception, and step 12's rule is exactly right for them

They never emit a right column, so they retain no rows at all: their state is the
`ids` map, O(distinct keys), and it goes flat at the freeze for precisely the reason
an accumulator's does. They also *cannot* use the bucket rule — repartitioning an
existing key set needs the rows it came from, and they do not have them.

Both rules satisfy the same invariant, which is what lets the probe, the replay and
the recursion be one code path:

> A key is either resident for its whole life or routed for its whole life.

`residentKey` is where they differ, and it is four lines.

---

## 2. The four silent wrong answers, and what removes each

Every one of these produces a plausible number rather than an error. All four were
reintroduced and confirmed to fail a named test (§6).

**Routing has to be a THIRD outcome.** `appendMatches` emits `(row, NullIndex)`
whenever `!matched && emitProbeUnmatched`. A routed row that merely leaves `nHit == 0`
falls into that arm, so the parent emits it null-padded *and* its sub-join emits it
again. Reintroduced: Left gave 23 784 rows where 8 000 belonged, Anti 14 976 where
2 791 did. → an explicit `routed` flag, checked first.

**Null-keyed build rows have no hash.** `Consume` skips the encoder for them
entirely, so there is nothing to route on, and what saves them today is a dense array
over every build row that partitioning destroys. Reintroduced: a Right join kept 0 of
286. → a dedicated 17th bucket, written only when `emitBuildUnmatched`, because for
the other four kinds such a row can never reach the output.

**Semi and Anti emitted through a mask over a contiguous slice.** `emit` sliced
`p.cur[p.row-n : p.row]` and filtered it; routing punches holes in that run, so the
window slides left and the filter lands on the wrong rows — with the row *count*
still right. Reintroduced: `TestSpilledSemiAntiPartitionTheLeftFrame` found a row in
**both** semi and anti. → a selection vector instead of a mask. Note the obvious
patch is also wrong: appending a bit for routed rows makes Anti emit them twice.

**The null bucket must not be replayed through a sub-join.** Its rows have no key, so
a sub-sink would classify them `noKey` again, route them to *its* null bucket, and
repeat to `maxSpillDepth` — reporting *"a single join key has more build rows than the
limit"* about data with no key at all. Step 12 shipped that class of
confident-and-wrong diagnosis once. → `nullPadOp` streams the file and pads, in O(1)
memory, with no recursion.

### And one hazard that removed itself

A replay that walked build files and joined each against the probe rows beside it
would drop every probe row routed to a bucket with no build file — invisible for
Inner and Semi, a missing output row for Left, Full and Anti. The fix turned out to
be cheaper than iterating a union: a probe key is routed **only when its bucket has a
build file**, because a key that is not resident and whose bucket holds no build row
provably matches nothing. Those rows are answered inline, in probe order, and no
probe file is written for an empty bucket at all.

---

## 3. What it does not bound

Partitioning divides the KEY SPACE. The join hits that wall in two places, and each
gets its own message:

- **A cross join has no key.** One bucket by construction, so there is nothing to
  divide. Refused.
- **One key with more build rows than the limit.** Caught by `maxSpillDepth` rather
  than by a byte comparison, because only the recursion can distinguish "this bucket
  did not narrow" from "this bucket is merely large".

`TestCrossJoinUnderALimitRefuses` and `TestBudgetErrorNamesTheOperator/one_key_too_large`
each pair a refusal with a sibling that succeeds at the same limit — without that,
either case reads as "a join cannot spill", which stopped being true in this step.

Secondary terms, bounded but not by the limit: the split is decided at a batch
boundary, so the resident set overshoots by up to one batch; and 17 build write
buffers plus 16 probe ones at 8 KiB each is 264 KiB, retained through `RetainBytes`
so `MemoryStats.Peak` is honest about it.

---

## 4. The order contract

`joinProbeOp`'s doc states the output as a formula and calls it *"a function of
(probe row order, build row order) only"*. That gains a term:

```
resident:  [ (i, r) for i in probe order restricted to the resident keys ]
        ++ [ (Null, j) for j in resident build order, unmatched ]
        ++ bucket 0's sequence ++ … ++ bucket 15's ++ the null bucket's
```

Still batch-size invariant. **No longer limit-invariant** — the same contract step 12
established for an unordered group-by.

**There is deliberately no `MaintainJoinOrder`**, though `ursus-api.md:938` reserves
the name. Restoring probe order means holding the whole output to permute it, and a
join's output can be larger than both inputs — so the flag would only work in the
cases that did not need it. `WithMemoryLimit` says this; `nodes_join.go` says it; and
`TestJoinOrderIsUnspecifiedUnderALimit` asserts the order really does move, which is
what stops every `IgnoreRowOrder` comparison in the file from proving nothing.

---

## 5. The accounting audit, and the number that justified it

Six retentions were invisible to the budget. The first is the one that mattered:

| Was unaccounted | Now |
| --- | --- |
| **`t.ids`** — a map entry plus a COPIED key string per distinct key | `RetainBytes` delta per new key, `idsBytesPerKey = 48` |
| `t.off`, `t.rows` — allocated *after* the rebase released everything | charged at `freeze` |
| `t.rowKey` after `freeze` | kept charged |
| `joinProbeOp.matched` | charged |
| `joinProbeOp.seen` — grows with the PROBE side, unbounded | charged, and now bounded by partitioning |
| Semi/Anti never rebased — the release sat inside `if needBuildRows` | rebase is unconditional |

**Measured: 517 KB reported against 1.66 MB actually held**, over 20 000 distinct
keys — a factor of 3.2, with the `ids` map making up ~1.1 MB of it.
`TestJoinAccountsItsHashTable` pins it, and reverting the fix makes
`TestSpillingJoinMatchesInMemory/SEMI` stop spilling at all, because an invisible map
never trips the budget.

Two smaller findings on the way. `needBuildRows`'s doc claimed *"false for Semi/Anti:
their build memory is O(keys)"* — **false by an O(rows) term**, because `rowKey` was
appended for every build row and `counts` for every key and Semi/Anti read neither.
Four charged bytes a row, retained and unread, deciding when the budget fired for
exactly the two kinds whose memory is smallest. And `joinTable`'s doc justified CSR
with a `Validate` check `off[id+1]-off[id] > 1` that **does not exist** and never did.

**`Validate` survives partitioning**, and the proof is short enough to sit in the
code: a key is wholly resident or wholly routed, and equal keys hash to the same
bucket, so a duplicate is always seen by exactly one sink. A duplicate spanning two
buckets cannot exist. `seen` is now checked only for resident keys and only after the
`ids` lookup — inserting a routed key there would have made the map O(all distinct
probe keys), which is the unbounded structure this step exists to bound.

---

## 6. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean.

Eight teeth checks, each by reintroducing the defect and confirming the **named** test
fails:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| Routing is not a third outcome | `TestSpillingJoinMatchesInMemory` | Left 23 784 rows where 8 000 belonged |
| Drop the null bucket | `TestSpilledNullKeysStillFlush` | Right kept 0 of 286 null-keyed build rows |
| Keep the mask over a contiguous slice | `TestSpilledSemiAntiPartitionTheLeftFrame` | one row in **both** semi and anti |
| Reuse the parent's level in the sub-join | `TestSpillingJoinMatchesInMemory` | all four row-retaining kinds wrong |
| Leave `t.ids` unaccounted | `TestJoinAccountsItsHashTable`, `…/SEMI` | peak 517 KB vs 1.66 MB; Semi never split |
| Remove the `splitAll` escape hatch | `TestSpillingJoinMatchesInMemory/RIGHT` | the hybrid stage alone is not enough |
| Step 12's freeze instead of the bucket rule | `TestSpillingJoinIsBounded` | 4.9× the limit, zero spills |
| Delete `Merge`'s id remap | `TestJoinBuildSinkMergeRemapsIds` | 7 rows where 6 belonged |

**Two of the eight found an inadequate test rather than correct code**, which is the
part worth recording. `TestSpillingJoinIsBounded` originally used one row per key —
a shape where step 12's freeze *is* bounded — so it could not distinguish the two
residency rules at all; it now uses a high-fan-out build side, which is the fixture
the whole design exists for. And `TestJoinBuildSinkMergeRemapsIds` first ran `Merge`
against an *empty* probe side, where a Full join emits every build row whatever its
id, so deleting the remap entirely still passed.

**One teeth check did not fire, and the reason is a real finding.** Holding the
parent's account across the replay — step 12's exact bug — changes nothing here,
because `splitAll` means a sink that would starve its children is holding nothing by
then. `releaseTable` is therefore defensive rather than load-bearing, and it is
documented as such rather than credited with a guarantee it does not provide.

New tests: `TestSpillingJoinMatchesInMemory` (six kinds × three limits),
`TestSpilledJoinEmitsEveryProbeRow`, `TestSpilledNullKeysStillFlush`,
`TestSpilledSemiAntiPartitionTheLeftFrame`, `TestJoinSpillIsDeterministic`,
`TestCrossJoinUnderALimitRefuses`, `TestSpillingJoinIsBounded`,
`TestJoinOrderIsUnspecifiedUnderALimit`, `TestSpilledJoinValidateStillFires`,
`TestJoinSpillFilesAreCleanedUp` (success, two break points, cancellation, error),
`TestJoinAccountsItsHashTable`, plus the two in §7.

Changed: `TestBudgetErrorNamesTheOperator`'s `join` case became `cross_join`, with a
`keyed_join_spills` sibling and a `one_key_too_large` case for the third face of the
same rule. `TestMemoryLimitIsNotASemanticKnob` gained four join shapes, compared as
whole-frame multisets via a per-shape `multiset` field — a join has no order flag, and
a Full join's left columns are null on an unmatched build row, so no single column is
even readable as a non-null sequence.

---

## 7. `df.String()` truncates at ten rows

`frame.go:74` — `const maxRows = 10`. **Eleven invariance tests across seven files**
compared `df.String()`, so every one of them checked the first ten rows and called it
equality:

- `TestJoinBatchSizeInvariance` — 7 kinds × 6 batch sizes, and the cross case is 20 rows
- `TestParallelIsOrderPreserving`'s case literally named *"join — left-input order is
  a guarantee"* — joins 5 000 × 200 rows and checked ten
- `TestParquetBatchSizeInvariance` — filters to ~86 rows, checked ten
- plus `TestScanCSVBatchSizeInvariance`, the namespace and fill thread-invariance
  tests, and two more in `parallel_test.go`

Nothing in the suite asserted a literal join row sequence, so a partitioned join that
reordered *consistently* would have passed all of them. **This had to be fixed before
the join was touched**, or the step had no way to tell a correct implementation from
a wrong one.

The fix was one line per site, because the right tool already existed:
`ursustest.AssertFrameEqual` renders every row (`renderRows` walks `b.Rows()`) and
compares in order by default. The ten-row ceiling was never a decision — just the
wrong function.

---

## 8. Two documented claims that were never asserted

- **`TestJoinFinishEqualsProbeOfEmpty` did not exist.** `join.go:34` names it as the
  reason `Finish` is kept total rather than `panic("use Probe")`; repo-wide the only
  occurrence of the name was that comment. Written — and deliberately *not* against
  `emptyOperator`, since `Finish` is implemented as `Probe(ctx, emptyOperator{})` and
  comparing those would be true by construction. It uses a stream that yields empty
  batches instead, which is both a different input and the realistic one.
- **`joinBuildSink.Merge` was unreachable.** Fully written, fully documented, no
  production caller and no test, so its remap, its `noKey` pass-through and its
  cross-sink `RequiresRightUnique` check had never executed. It now has a test, and a
  guard refusing to merge sinks that have spilled — the guard `sortSink.Merge` and
  `hashAggSink.Merge` both already had.

---

## 9. Measurements

`benchtime=10x`, `count=2`, 65 536 × 16 384 rows joined on `id`:

| Benchmark | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkInnerJoin` | 6.6 M | 5.4 M | 17 060 |
| `BenchmarkJoinSpilling` (256 KiB limit) | 16.8 M | 16.7 M | 39 052 |

**2.54×** — the cost of bounded memory when it is not needed. Higher than the sort's
1.24× and the group-by's 0.91×, and the reason is structural rather than incidental:
a join writes *both* sides to disk and reads both back, where the aggregation writes
only the keys it could not hold and the sort writes each row once.

---

## 10. Honest gaps

- **`SinkParquet` refuses Int128 and every temporal type, in both directions.** Ten of
  twenty-five `TypeID`s. So step 12's canonical output (`Sum` → Int128) cannot be
  written, and step 6's `.dt` namespace is unreachable from Parquet entirely —
  `TestSpillingJoinIsBounded` had to use `SinkCSV`. Found in step 12, still open, and
  by value-per-line the largest remaining hole in the repo: the writer's 16-byte
  physical path already exists; only `toNode`'s switch and the reader's logical-type
  arms are missing.
- **A cross join under a limit still refuses**, and a block-nested-loop fallback would
  be genuinely different machinery. A cross join large enough to need it produces an
  output nobody can consume anyway.
- **Window, reverse, hstack, unique and tail still refuse rather than spill**, and
  bounding them needs the chunked `Column` open since step 1.
- **Parallel aggregation still does not exist.** All five `Sink.Merge` implementations
  remain uncalled — and this step made the third of them *refuse* under spilling
  rather than waking it.
- `duplicateKeyErr`'s probe-side row index is batch-local where the build side's is
  global, a pre-existing inconsistency this step did not fix; at depth it now declines
  to guess a position at all rather than naming a row in a spill file.
- The `*Reverse` predicate-pushdown barrier and the missing projection-pushdown arms
  for `*Aggregate`/`*WithColumns`/`*Distinct` are still open, deferred since step 11.

---

## 11. Files

**New:** `internal/physical/extjoin.go` (727 lines) — the split state machine, the
null bucket, both writer sets, the replay, `newSub`, `nullPadOp`.
`internal/physical/join_test.go` (277), `joinspill_test.go` (558).

| File | Change |
| --- | --- |
| `internal/physical/join.go` | `joinBuildSink` gained `budget`, `split`, `level` and the partition state; `Consume` restructured around `admit`/`classify`; `freeze` rebases unconditionally and charges the CSR; `Probe` returns a spilling operator; `Merge` gained the spilled guard; `emit` split so `gatherOut` is shared with the null bucket; the mask replaced by a selection vector; the six accounting fixes |
| `join_test.go`, `parallel_test.go`, `parquet_test.go`, `csv_test.go`, `namespace_test.go`, `fill_test.go` | `df.String()` → `ursustest.AssertFrameEqual` at eleven sites |
| `memory_test.go` | `TestJoinAccountsItsHashTable`; the reworked budget-error table; join shapes in the semantic-knob test |
| `internal/execopt/budget.go`, `lazy.go`, `internal/plan/nodes_join.go`, `internal/physical/sink.go` | the doc sites this falsified |
| `internal/exec/exec.go` | *"the seam where morsel-driven parallelism arrives in v0.2 is here and only here"* — it arrived in step 5, in `internal/physical/parallel.go` |
