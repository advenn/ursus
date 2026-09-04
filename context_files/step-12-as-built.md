# Step 12 — as built

Spilling hash aggregation. Authoritative where it disagrees with
[`step-11-as-built.md`](./step-11-as-built.md), [`step-10-as-built.md`](./step-10-as-built.md)
and the vision docs.

**907 test cases green** — 387 top-level tests and 520 subtests (849 before this
step) — under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the
experiment off, and under `-race`. `make levels` and `go vet` clean.

`Expr` still has 94 methods and `LazyFrame` 37 operations: **this step adds no
API.** 167 files, ~49.0k lines. What changed is what an existing flag and an
existing option now do.

```go
// This used to fail with "memory limit of 64.0KiB exceeded" and a hint naming
// group_by. It now radix-partitions its keys to disk and answers.
lf.GroupBy(ursus.Col("user_id")).Agg(ursus.Col("spend").Sum()).
    SinkParquet(ctx, "out.parquet",
        ursus.WithMemoryLimit(64<<20), ursus.WithSpillDir("/tmp/ursus"))

// And this flag, which had been assigned, rendered and never read for four
// steps, is finally load-bearing.
lf.GroupBy(ursus.Col("user_id")).MaintainOrder().Agg(...)
```

---

## 1. Why this step, and why aggregation second

§14's v0.2 line names *"streaming engine with spilling hash-agg and hash-join,
external sort"*. Step 10 delivered one of the three and said so: *"Accounting
covers all seven; spilling covers one."* Under a memory limit, six of the seven
buffering operators failed with an error naming the operator.

`hashAggSink` is second, and not because it is the largest — it is O(distinct
keys), which is why step 10 did not start here. It is second because **step 10's
machinery fits it unchanged**:

| Step 10 built | Step 12 uses it |
| --- | --- |
| `gatherRows(schema, srcs, pick)` | a partition pass has ONE source, so it takes the single-`Take` path with no permutation |
| `runSource` + `fileRun` + `runOperator` | "replay one spill file" *is* "replay partition p" |
| `internal/spill` | a partition pass writes ordinary batches — the format needed nothing |
| `execopt.Account` | `hashAggSink` already held one and already called `Retain`/`RetainBytes`/`Check` |

And the half of step 10 that was genuinely hard — `internal/kernel/merge.go`'s 418
lines of cross-run comparator and stability tie-break — is **irrelevant here**.
Radix partitions are disjoint by key: nothing to order, and no `SortSpec` exists
for a group-by. `internal/physical/extagg.go` is 511 lines and contains no
comparison at all.

---

## 2. The design: residency freeze, not full radix partitioning

Aggregate in memory exactly as before. When the account goes over budget, **freeze
the resident `ids` map**. From then on a row whose key is already resident feeds
the resident accumulators as before; a row with a **new** key is routed to spill
file `hash(key) % 16`. At `Finish`, emit the resident groups, then replay each
partition through a fresh sink and emit its groups.

The invariant, verbatim from `hashAggSink`'s doc:

> A key's residency is decided at its **first appearance**, and residency only
> grows *before* the freeze. So every key is either resident for its whole life or
> routed for its whole life; `partitionOf` is a pure function of the encoded key,
> so a routed key's rows all land in one file. No key's partial state ever exists
> in two places.

### 2.1 Why not "partition everything"

Four consequences, and the fourth decided it.

**No `Accumulator.Merge` is ever called.** That matters specifically. `positionAcc`
(First/Last) and `argExtremumAcc` both require `other` to have seen a strictly
*later* portion of the input, and `argExtremumAcc.Merge` carries an explicit
`a.n[i] + o.bestI[i]` shift to compensate. Under the freeze a partition file is in
input order and one sink sees all of a group's rows, so both are correct with **no
shift at all**. A partition-evict design would need exactly that precondition and
could not supply it.

**`Consume`'s fast path is untouched** — the freeze adds a branch inside the
`!seen` arm, which already allocates.

**No input buffering.** `hashAggSink` never retained `in` and still never does.

**A query that does not spill produces byte-identical output to before.**
`agg_test.go` asserts *"Groups come out in first-appearance order: eu, us, apac"*
and roughly sixty `GroupBy(` call sites do the same implicitly. Full radix
partitioning reorders every one of them on day one.

What the freeze is genuinely worse at, stated once: residency is decided by
*arrival* order, not frequency. If the hot keys arrive after the freeze they spill
and the resident table holds cold keys. That is a throughput property, never a
correctness one — no test can distinguish the two on correctness.

### 2.2 The hazard the freeze creates, and the construction that removes it

`s.groups` is a scratch buffer reused across batches, and `AddBatch` reads **every**
slot unchecked. If the post-freeze path wrote ids only for resident rows and left
the rest, each routed row would inherit **the previous batch's id at that index** —
always in range, because `ids` is monotone — and its value would fold into an
arbitrary resident group. No panic, no length mismatch, no error. A plausible wrong
number.

So `s.groups` is now **dense over resident rows**, built alongside a `keep []int32`
of resident row indices in the same pass, and the accumulators are fed over
`takeBatch(s.inSchema, in, keep)`. Nothing stale is reachable. It costs one `Take`
per aggregate per batch, on the post-freeze path only.

Sound because an aggregate's input is a **row-local** expression — the identical
argument `keyColumns` already makes for re-keying a gathered batch in `extsort.go`.
A window inside `Agg()` is refused at plan time, so there is no non-row-local case.

**This was verified by reintroduction** and it is the sharpest teeth in the step:
feeding the whole batch to `AddBatch` post-freeze made a `Len()` group report 2476
rows where 2000 belonged, and the `-0.0` group 4000 where 2000 belonged. No error
anywhere. See §7.

### 2.3 The hash

`kernel.HashKey` lives beside the encoder whose doc already said the encoding was
meant to be reusable *"for spilling to disk without re-deriving it"*. Two properties
are load-bearing.

**Hash the ENCODED key, never raw column values.** `NewGroupKeyEncoder` is the
single authority on grouping equality — *"Floats go through OrderKey first, so NaN
groups with NaN and -0.0 groups with +0.0"*. A raw-bits hash sends `-0.0` and `+0.0`
to different partitions, they aggregate separately, and the output has **two rows
where one belongs**, each with a plausible sub-total. Reintroducing that produced
3753 groups where 3752 belonged.

**Seedless and deterministic** — FNV-1a with a splitmix64 finaliser, not
`hash/maphash`. `maphash`'s seed can only come from `MakeSeed()`, so the group
*order* of a spilled aggregation would differ between processes: a flake that
reproduces only under a memory limit and only sometimes.

**The depth is mixed into the hash, not used to select a different slice of one
hash's bits.** That is termination, not quality: every key in a partition already
agrees in the bits the parent used, so re-hashing identically maps the whole
partition onto one sub-partition and the recursion never narrows. (The bit-slice
alternative also breaks at depth 16, where shifting a `uint64` by 64 is defined as
0 and every key collapses into bucket 0.)

### 2.4 The accounting rule — the sharp part

`Account.Check()` cannot simply be kept after the freeze: being over budget is the
*normal* post-freeze state, since that is what caused it. But something must still
fail, or a `Quantile` over one hot key grows unbounded while claiming to spill.

The criterion is exact rather than heuristic. After the freeze, compare `accBytes`
against **its own value at the freeze, plus the limit**. It fires for exactly
`quantileAcc` and `nuniqueAcc` and never for the other eight — because `keyParts`
cannot grow (new keys are routed) and `Reserve(nGroups)` is a no-op once `nGroups`
is fixed, so every O(groups) accumulator's `NBytes()` goes flat at the freeze.

That is a stated property rather than a promise: **a spilling group-by refuses
exactly when an accumulator's state is O(rows), and never otherwise.** It is also
the sentence `execopt`'s `holdsHint("group_by")` had been carrying since step 10.

---

## 3. The bug the design created and the tests did not find first

This is the part worth reading if you read nothing else.

A sub-sink draws on the **same query-wide budget** as its parent. The first
implementation had `Finish` rebase the parent's account onto its resident answer —
correct, and the same correction `joinBuildSink.freeze` makes — and then start
replaying partitions **while still holding it**. Every sub-sink therefore began
already over budget, froze after its very first batch, and routed almost everything
one level deeper.

The recursion then needed a level per *batch* rather than a level per factor of 16,
and hit `maxSpillDepth` on data that was not skewed at all. The error it produced
was:

```
memory limit exceeded: a single group is too large to partition
  partitioning divides work across KEYS, so 4 levels of it have not split
  this one — the data is skewed onto one key
```

— which is a **confident, specific, and completely wrong diagnosis**. The data was
uniform. Nothing in the message points at the budget.

The fix is `releaseState()`, called at three sites: `Finish` (before returning),
`aggSpillOp.Next` (the moment the resident answer has been handed downstream), and
`collectOrdered` (before each sub-sink recurses). Its doc says why, because the next
person to move a `Retain` will need to know.

**How it was found matters too.** It was not found by a test that failed for the
right reason. It was found because `TestGroupByMaintainOrderSurvivesSpilling` failed
at its smallest limit and the obvious response — "16 KiB is just too small, raise
it" — would have shipped the bug. The fixture was not wrong; the operator was.

---

## 4. `MaintainOrder`

`plan.Aggregate.MaintainOrder` existed, was assigned from the root, was rendered by
`Label()` — and **no operator had ever read it**. Root `agg.go`'s own doc said why it
was built:

> the flag documents the guarantee rather than changing behaviour, and **keeps the
> door open for a partitioned implementation that would not.**

Step 12 walks through that door. It had been inert for four steps.

### 4.1 The prefix property makes it a merge, not a sort

Every resident key first-appears strictly before every spilled key, and each
partition file is in input order — so each replay sink's first-appearance ids *are*
the global ranks restricted to its keys.

One `int64` per group carries the global input ordinal of its first appearance:
`firstSeen []int64` for resident groups, appended at exactly the site `newRows` is,
and a synthetic `__ord` column in the **spill schema only** for routed rows. A
sub-sink inherits `__ord`, so its `firstSeen` is already global — no extra aggregate
spec, and `__ord` never reaches the sink's output schema, which is what lets a
sub-sink be the same type as its parent.

### 4.2 The ordered path cannot stream, and the doc says so

First appearance interleaves resident and spilled groups arbitrarily: the group
created at input row 3 may be resident, the one at row 4 spilled to partition 9, the
one at row 5 resident again. Emitting row 3's group before row 4's therefore requires
partition 9's aggregation to be complete.

So `MaintainOrder()` on a spilling aggregation buys back the whole result in memory
at the end — O(distinct keys), which is what a non-spilling aggregation holds anyway,
so the peak is `budget + result` rather than `budget`. `nodes_agg.go` used to say
only *"It costs time and memory"*, with no number; it now says this, and points at
the benchmark.

---

## 5. What this does not bound

Radix partitioning divides the **key space**. Memory is bounded iff per-group state
is O(1). Eight of the ten accumulators are. Two are not, and no amount of
partitioning helps:

- **`quantileAcc.vals`** — *"keeps every non-null value of every group, and its
  memory is O(rows), not O(groups)"*, by its own doc.
- **`nuniqueAcc.sets`** — O(distinct values per group), and its `NBytes`
  *under*-reports, so the freeze can trigger late for long string values.

Two secondary terms, bounded but not by the limit:

- The freeze is tested at the **end** of `Consume`, so the resident set overshoots
  by up to one batch's distinct keys.
- 16 open writers × 8 KiB of buffer = **128 KiB**, retained through `RetainBytes` so
  `MemoryStats.Peak` is honest about it. (`spill.Create` hard-codes a 64 KiB buffer;
  sixteen of those would be 1 MiB — larger than four of the five limits the existing
  memory tests use. `internal/spill` gained `CreateSized`.)

**And a symmetry worth recording, because it names the next step precisely:** the two
accumulators the freeze cannot bound are exactly the two whose `Merge` is
**order-independent** — `quantileAcc.Merge` is an append, `nuniqueAcc.Merge` is a set
union. So spilling *within* a hot key is possible for precisely the two aggregates
that need it. That is a coherent next step, not a permanent wall. It is written into
`aggstat.go` beside the accumulator.

---

## 6. Measurements

Peak is `MemoryStats.Peak`, over 65 536 distinct keys, `Sum` + `Len`, batch 8192:

| Limit | Spills (files) | Peak |
| --- | --- | --- |
| 1 MiB | 16 | 1.16 MiB |
| 512 KiB | 16 | 656 KiB |
| 256 KiB | 16 | 392 KiB |

`benchtime=10x`, `count=3`, 65 536 groups, 1 MiB limit where bounded:

| Benchmark | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkGroupByHighCardinality` | 22.2 M | 20.2 M | 67 081 |
| `BenchmarkGroupBySpilling` | 20.2 M | 20.8 M | 72 091 |
| `BenchmarkGroupBySpillingOrdered` | 32.9 M | 33.1 M | 75 902 |

**Spilling is 0.91× — about 9% FASTER than not spilling.** That is not a typo and it
is worth being precise about why, because the naive reading ("spilling to disk is
free") is wrong. Sixteen small hash tables have better cache locality than one
65 536-entry table, the partition files are small enough to stay in the page cache,
and the input is uniform so no partition is oversized. On a real workload with cold
storage and skew the ratio will be worse. What the number does support is the weaker
and more useful claim: **the partitioning machinery itself is not the cost.** Step
10's sort measured 1.24× for comparison, and a sort has a k-way merge to pay for.

`MaintainOrder` costs **1.63×** on a spilling aggregation and nothing at all on one
that fits. That is the figure `nodes_agg.go` now cites in place of "time and memory".

---

## 7. Verification, and the two tests that were wrong

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean.

Seven teeth checks, each by reintroducing the defect and confirming the **named**
test fails:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| Feed the whole batch to `AddBatch` post-freeze | `TestSpilledGroupKeysUseGroupingEquality` | `Len` = 2476 where 2000 belonged; no error |
| Build the replay sink's accumulators with empty params | `TestSpilledAggregateParametersSurvive` | `p50 == p90` and `pop == samp` **on spilled groups only**; row 0 stayed right |
| Hash raw bits instead of the encoded key | `TestSpilledGroupKeysUseGroupingEquality` | 3753 groups where 3752 belonged |
| Reuse the parent's hash at depth+1 | `TestGroupBySpillRecursesOnSkew` | died at `maxSpillDepth` blaming skew on uniform data |
| Drop the post-freeze budget check | `TestSpilledQuantileStillRefuses`, `TestBudgetErrorNamesTheOperator/group_by_holistic` | the refusal case succeeded while over the limit |
| Restore ordinals in reverse | `TestGroupByMaintainOrderSurvivesSpilling` | sequence failed; every `IgnoreRowOrder` test passed |
| Clean up only when nothing spilled | all five `TestGroupBySpillFilesAreCleanedUp` subtests | files left on every exit path |

**Two of the seven found that the test was inadequate rather than that the code was
right**, which is the part worth recording.

`TestSpilledGroupKeysUseGroupingEquality` scattered NaN, `-0.0` and `+0.0` from row
0. Residency is decided at first appearance, so those keys were resident for the
query's whole life and **never reached the partitioner**. The test passed with the
partitioner hashing raw bits. The fixture now introduces them in the second half of
the input, and the test asserts the NaN and zero group sizes by name so a failure
says which key split.

`TestGroupBySpillRecursesOnSkew` did not exist. Every limit large enough for the
nineteen aggregates leaves each level-1 bucket small enough to fit, so **no test
reached depth 2** and the per-level seed was unexercised — the recursion was a base
case with a comment. A 2 KiB limit forces it, and `MemoryStats.Spills > 16` is the
only public evidence that a partition did not fit on the first try.

New tests: `TestSpillingHashAggMatchesInMemory` (all nineteen aggregates, three
limits, `AssertFrameEqual` + `IgnoreRowOrder`, floats bit-exact with no tolerance),
`TestSpilledAggregateParametersSurvive`, `TestSpilledGroupKeysUseGroupingEquality`,
`TestGroupBySpillIsDeterministic`, `TestGroupBySpillActuallyHappens`,
`TestSpillingHashAggIsBounded`, `TestGroupByOrderIsUnspecifiedWithoutMaintainOrder`,
`TestGroupByMaintainOrderSurvivesSpilling`, `TestGroupBySpillRecursesOnSkew`,
`TestSpilledQuantileStillRefuses`, `TestGroupBySpillFilesAreCleanedUp` (success, two
break points, cancellation, error), `TestDistinctIsAccounted`,
`TestZeroColumnBatchKeepsItsRows`.

Adequacy guards inside them, because a spilling test that does not spill proves
nothing: `Spills >= 2` (one partition cannot catch a cross-partition bug),
`unbounded.Spills == 0`, a **counting** aggregate in the list (`Min`/`Max`/`First`
all survive a dropped row; `Len` does not), an asserted group count far beyond any
plausible resident set, and — in `TestSpillingHashAggIsBounded` — the unbounded peak
asserted to exceed the limit *first*, so the comparison is not a tautology.

Changed tests: `TestBudgetErrorNamesTheOperator`'s `group_by` case **still passed for
a different reason** — before step 12 the group table itself overflowed, so the
message was right for the wrong cause, which is worse than failing. It is now
`group_by_holistic`, with a sibling proving `Agg(Sum)` at the same limit succeeds,
and a `unique` case for the seventh operator. `TestMemoryLimitIsNotASemanticKnob`
gained four group-by shapes and its `name == "sort_then_head"` exemption became a
per-shape `bounded` field. `TestGroupByAgg` gained `MaintainOrder()`: its comment
stated a dependency the query did not declare.

---

## 8. The API contract change, stated plainly

An unordered spilling group-by's row **order** depends on the limit, because the
freeze point does. **Content never changes.** That sits uncomfortably beside a test
named `TestMemoryLimitIsNotASemanticKnob`, and the resolution was already written
down four steps ago — `MaintainOrder`'s doc says the unordered order *"is
unspecified — which is standard (SQL uses set semantics and DuckDB exploits it for
parallelism) but means a test that assumes an order is flaky."*

Step 12 is the first time that sentence pays rent. `WithMemoryLimit` now has a
section saying it from the other side, and the group-by shapes added to
`TestMemoryLimitIsNotASemanticKnob` all declare `MaintainOrder()` — because it
compares positionally, and adding them without the flag would have made the test
flaky rather than made the library wrong.

The alternative was to always restore order, which costs every spilling aggregation
the ordering pass including the many that do not care. At 1.63× that is not a
rounding error.

---

## 9. Step 11's leftovers, cleaned up the way step 11 cleaned up step 10's

| Was | Now |
| --- | --- |
| **`Col(dec).Sign()` returned 0.01** | `ResolveUnary` guarded Decimal for Floor/Ceil and let Sign through, so `unaryI128` returned unscaled `1` labelled `Decimal(10,2)`. The argument three lines above it — *"a Duration of −1, 0 or 1 TICKS is not a sign, it is a nanosecond"* — applies verbatim and had not been applied. `math_test.go` **asserted it was correct** in a comment naming three ops while exercising two, through `CollectSchema` only, so `unaryI128` never ran on a Decimal column anywhere. `TestSignOnDecimalIsRefused` goes through `Collect`. |
| **`Clip` widened its receiver** | `Then`/`Otherwise` used `lift`, not `liftWeak`, so `Col("i32").Clip(0, 100)` was Int64 and `Col("u64").Clip(0, 100)` was **Int128** — exactly the inconsistency weak literals were built to remove. `TestClipSemantics` never asserted an output type; `TestClipPreservesType` does, over seven types, and pins the fallback (an Int8 clipped to 5000 is Int64 holding 5000, not Int8 holding −120). |
| **`Bool ↔ numeric` casts: `CanCast` promised, the kernel refused** | The third instance of a divergence step 11 closed twice, made reachable **without a user-written cast** by step 11's `IsClose`, which casts both operands to Float64. Both directions now work over every numeric type. |
| **`gatherRows` dropped a zero-column batch's row count** | And so did `kernel.Concat`, which is where `Select().Sort(...)` actually lost its rows — the one-line fix in `gatherRows` was not on that path. `NewBatchRows` exists precisely because *"a frame can legitimately have rows and no columns"*. |
| **`distinctOp` had no `Account` at all** | Its `seen` map grows with distinct rows and is held across batches — the same shape as `hashAggSink.ids`, which *is* accounted. `Scan(huge).Unique()` under a limit neither spilled nor failed; it grew until the OS killed it. Step 10's *"accounting covers all seven"* was one short. |
| `FitsExactly`'s dead `case int:` | Removed. No `Lit.Value` can hold a plain Go `int`; both literal paths normalise to `int64`, and the tell was that there was no `case uint:`. |
| `readAnyInt` had two first lines | The rewrite prepended a summary without deleting the old one; `go doc` rendered both. |
| `FillStrategy.String()` exported with zero callers | The one place a strategy was formatted used `%d`, which does not consult `Stringer`. Now `%s`. |
| Four docs that contradicted their code | `Diff`'s *"the type is the operand's own"*; `FillNan`'s "leftmost column" rule, which `OutputName` has an explicit `*Cond` case to override; the three-way disagreement on whether an identity op copies or relabels; and `Log(base)` silently widening Float32 where `Log10()` deliberately does not. |
| `ursustest.sortStrings` was an insertion sort | `slices.Sort`. O(n²) on the path `IgnoreRowOrder()` uses. |

### 9.1 A fourth divergence, found and deliberately not fixed

Running the bool differential over everything `CanCast` calls numeric surfaced
**numeric ↔ Decimal**. `CanCast`'s `from.IsNumeric() && to.IsNumeric()` arm admits
it; `kernel.Cast` refuses it on purpose, with a reason that is right:

> a decimal is stored as an unscaled integer, so this cast would be wrong by a
> factor of 10^scale rather than merely imprecise

So every `numeric → Decimal` cast plans cleanly and fails at execution. Reconciling
them has reach: dropping Decimal from `CanCast`'s numeric arm moves the refusal to
plan time, where it belongs, but it also changes the path `decimal_test.go` and
`TestDecimalMathIsRefusedAtPlanTime` use to build a Decimal column at all. That is
its own decision, not a side effect of a bool cast.

`TestDecimalCastDivergenceIsKnown` is a **tripwire, not an assertion of
correctness**: it pins the current state of both sides, so whichever moves first, the
pair has to be reconciled rather than drifting further apart.

---

## 10. Honest gaps

- **The grace hash join is not done.** It inverts `joinBreaker`'s
  build-blocks/probe-streams shape, changes a row order `TestJoinBatchSizeInvariance`
  pins across seven kinds × six batch sizes, and has a silent-row-loss mode: a
  null-keyed build row has no hash, so Right and Full would drop rows without a
  dedicated partition. Two of v0.2's three spilling operators now exist.
- **Window, reverse and hstack still refuse rather than spill**, and bounding them
  needs the chunked `Column` that has been open since step 1. `unique` now fails with
  an account instead of failing with the OS, which is an improvement and not a fix.
- **`quantile`, `median` and `n_unique` over one hot key still refuse.** §5 says why
  and names the shape of the fix.
- **Parallel aggregation still does not exist.** `Accumulator.Merge` remains
  implemented, tested and uncalled — and this step made its doc *more* honest rather
  than less, because spilling deliberately never reaches it.
- **`SinkParquet` cannot write Int128**, which is what every integer `Sum` outputs.
  `TestSpillingHashAggIsBounded` had to use `Mean` instead. That is a real hole in the
  writer, found by this step and not fixed by it.
- The `*Reverse` predicate-pushdown barrier and the missing `*Slice`/`*Tail`/`*Reverse`
  projection arms are still open, deferred since step 11.

---

## 11. Files

**New:** `internal/physical/extagg.go` (511 lines) — `partitionOf`, `partWriter`,
`route`, `takeBatch`, `withOrdinal`, `overBudget`, `newSub`, `aggSpillOp`,
`collectOrdered`, `finishOrdered`. `internal/kernel/castbool_test.go`,
`aggspill_test.go` (701 lines).

| File | Change |
| --- | --- |
| `internal/physical/agg.go` | `hashAggSink` gained `budget`, `batch`, `inSchema`, `spillSchema`, `level`, `frozen`, `frozenAcc`, the partition state and the `MaintainOrder` state. `aggSpec` gained `inType`, already computed at plan time and thrown away. `Consume` gained routing and a dense `groups`; `Finish` returns a streaming operator and calls `releaseState`; `Merge` gained the spilled guard `sortSink.Merge` already had |
| `internal/kernel/groupkey.go` | `HashKey`, `Mix64` |
| `internal/kernel/take.go` | `Concat`'s zero-column arm |
| `internal/kernel/unary.go` | `boolToNumeric` / `numericToBool`; `boolToNumeric` re-enters `Cast` through Int64 so a 128-bit target works |
| `internal/spill/spill.go` | `CreateSized`, so sixteen writers cost 128 KiB rather than 1 MiB |
| `internal/physical/extsort.go` | `batchRun`; the zero-column guard in `gatherRows` |
| `internal/physical/ops2.go` | `distinctOp` gained an `Account` |
| `internal/expr/resolve.go`, `fill.go`, `expr.go`, `window.go`, `ursustest/ursustest.go` | the step-11 fixes |
| `internal/execopt/budget.go`, `lazy.go`, `agg.go`, `internal/plan/nodes_agg.go`, `internal/kernel/agg.go`, `internal/kernel/aggstat.go`, `internal/physical/sort.go` | the doc sites step 12 falsified |
