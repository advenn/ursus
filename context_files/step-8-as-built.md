# Step 8 — as built

Window functions. Authoritative where it disagrees with
[`step-7-as-built.md`](./step-7-as-built.md) and the vision docs.

**698 tests green** (647 before this step) under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean.

```go
df, err := trades.Select(
    ursus.Col("sym"), ursus.Col("px"),
    ursus.Col("px").Sub(ursus.Col("px").Mean().Over(ursus.Col("sym"))).Alias("centered"),
    ursus.Col("px").Rank(ursus.RankDense, true).Over(ursus.Col("sym")).Alias("rk"),
    ursus.Col("qty").CumSum(false).Over(ursus.Col("sym")).Alias("running"),
    ursus.Col("px").Shift(1).Over(ursus.Col("sym")).Alias("prev"),
).Collect(ctx)
```

---

## 1. The design decision, and it was not the one the plan expected

`Node.Children`'s doc offers a ready-made mechanism for a node whose body is
"different": *"A nested SCOPE body is deliberately not a child … so a generic walker
must not rewrite across it."* Using it for a window would have been wrong, twice over:

1. **The stated justification does not apply.** That mechanism exists because a list
   body resolves against a *different schema* — the element type. A window's child
   resolves against the same schema as everything around it:
   `Col("x").Mean().Over(Col("g"))` resolves `x` and `g` against one input schema.
2. **Hiding the child breaks four things that otherwise work for free** — and one of
   them is a silent wrong answer. `RootNames` would report `{g}` and not `{x}`, so
   **projection pushdown would prune away the very column being aggregated**.

That last one is not speculation. Removing the child from `Children()` and re-running
the pushdown test gives:

```
projection: [g] (1/3 cols)
```

The scan reads the partition key and nothing else, and the window aggregates a column
that was never loaded. So the operands stay **ordinary children**, exactly as
`expr.Call`'s do, and the scoping moved into the predicate instead — which
`design/logical.md` had predicted: *"`Spec.PartitionBy` and `OrderBy` are ordinary
children, so `RootNames` and expansion pick them up automatically."*

## 2. The scoping change, done additively

`rejectAggregate` used `expr.HasAgg` — a blunt "is there an `*Agg` anywhere" walk —
at **seven call sites**. `Col("x").Mean().Over(g)` contains an `*Agg`, so `Select`,
`WithColumns`, `Filter` and `Sort` all refused it, with a message telling the user to
use `GroupBy`. The spec's own showcase example was rejected at plan time.

`HasAgg` was **not** redefined. It has a second caller with a different intent —
`IsAggregation` uses it to reject `sum(a).mean()`, and an aggregate of a *windowed*
aggregate is just as meaningless. Instead there is a new `HasUnboundAgg`: an
aggregate not enclosed by a window. It descends into a window's partition and order
keys, so `Sum(x).Over(Sum(g))` is still refused — a partition key must be a per-row
value.

`IsAggregation` and `BareColumns` gained explicit `*Window` arms. Both would have
reached the right answer through their generic paths, and both would have reached it
for the wrong reason and blamed the wrong thing in the error message.

## 3. `String()` is a correctness requirement, one node along

Step 7 fixed exactly this for `Agg` after two quantiles with different `q` rendered
identically, collapsed into one temporary, and returned the median under both names.
The same machinery deduplicates windows, so `Window.String()` renders the partition
keys, the order keys and the mapping.

Verified by reintroduction — with the partition key hidden from `String()`:

```
row 0: byG=3 byH=3, want 3 and 5
```

Two windows over different columns became one computation, and every row got the
wrong partition's total. No error, no length mismatch, nothing downstream to notice.

## 4. Ordered windows: one sort, not one per partition

The partition ids go in as the **first** key column of a lexicographic comparator.
`NewComparator` stops at the first non-zero, so rows in different partitions never
reach the remaining keys and rows in the same partition are compared by them alone. A
single `ArgSort` therefore produces a permutation that is simultaneously
partition-major and correctly ordered inside each partition, and the boundaries fall
out of one linear scan.

No per-partition sort loop, no new comparator, no `ArgSortSubset`.
`TestOrderedWindowMatchesPerPartitionSort` checks it differentially against the
obvious implementation — pull each partition out and sort it alone — over an input
with many ties, because ties are where a stability bug would hide.

`ArgSort` being stable is load-bearing: with no order keys at all, the rows of a
partition keep their input order, which is exactly what `CumCount` and `Shift` want.

### What each function actually cost

| Function | How |
| --- | --- |
| `CumCount` | ten lines after the boundary scan |
| `Shift` / `ShiftFill` | a gather. `sel[j] = perm[j-n]` or `kernel.NullIndex`, then one `Take` — so it works for **every** type `Take` handles, strings and Int128 included, with no per-type code. `ShiftFill` reuses step 7's `kernel.Select` and was free. |
| `CumMin` / `CumMax` | carries a **row index**, not a value — `extremumAcc`'s trick. One implementation serves every ordered type and there is no identity value to pick wrong. |
| `CumSum` / `CumProd` | the bulk: real arithmetic, so Int128 for integer sums and Float64 otherwise |
| `Rank` | the fiddly one: five methods over a peer-run scan |

`CumSum` shares `Sum`'s accumulator width deliberately, so the last row of a running
total equals the total rather than differing by an overflow. `TestCumSumMatchesSum`
pins it. `CumProd` is Float64 for the reason `Product` is: `i128` has addition and no
multiplication.

**Null semantics, which is the decision worth reading:** a null is *skipped* by the
running value and *preserved* in place. `cum_sum([1, null, 3])` is `[1, null, 4]` —
not `[1, 1, 4]`, which would claim a value where there was none, and not
`[1, null, null]`, which would let one missing reading destroy the rest of the series.

## 5. The three predicates step 7 deferred here

`IsUnique` turned out to be *exactly* a window: a value is unique iff the partition
keyed by that value has one row.

```go
func (e Expr) IsUnique() Expr        { return e.Len().Over(e).Eq(int64(1)) }
func (e Expr) IsDuplicated() Expr    { return e.Len().Over(e).Gt(int64(1)) }
func (e Expr) IsFirstDistinct() Expr { return e.CumCount(false).Over(e).Eq(uint32(1)) }
func (e Expr) IsLastDistinct() Expr  { return e.CumCount(true).Over(e).Eq(uint32(1)) }
```

Four one-line definitions, no new machinery. Because the partitioning uses the shared
`GroupKeyEncoder`, equality is **grouping equality** — NaN equals NaN, nulls group
together — so `IsUnique` agrees with `Unique()` about the same column.
`TestIsUniqueAgreesWithDistinct` cross-checks that the rows `IsFirstDistinct` marks
are exactly the rows `Unique()` keeps.

`IsFirstDistinct` is knowingly the slow implementation: `distinctOp` already computes
its bit in one streaming pass with O(distinct keys) memory, whereas this routes it
through a pipeline breaker with O(rows). A ~60-line streaming operator would be
better and is not built.

## 6. One refactor that was not planned and paid for itself immediately

Rewriting an expression tree means copying each node with new children, and that walk
existed **twice** — `substitute` and the physical planner's `extractAggs` — with a
third about to be written for windows. Each copy enumerates the node types and
carries their non-child fields across by hand, which is precisely how step 7 lost an
`Agg`'s `Params` twice.

`expr.Rebuild(n, kids)` copies the struct and replaces only the child fields, so a
node that grows a parameter carries it by default. `substitute` now delegates to it
and went from a 30-line type switch to eight lines. The window extraction is four
lines because of it.

Its teeth: deleting `Rebuild`'s `*Window` case panics the process on
`All().Sum().Over(g)`, which is the same fail-loud behaviour `substitute`'s old
`default` had, now in one place instead of three.

`plan.SortKey` also became an alias for a new `expr.OrderKey`. A window carries its
own ordering and `internal/expr` may not import `internal/plan`, so the type had to
move down a level; two structurally identical types would have been two places for
the null-placement rule to drift.

## 7. Structure

- **`plan.Window` is synthesised at Resolve time**, after expansion — `All().Mean().Over(g)`
  becomes N windows, so a builder could not know the count. Each window subtree is
  replaced by a `__winN` reference and the node is spliced beneath the Project or
  WithColumns. Explain then shows it, and the optimizer can see it.
- **`windowSink` is `hashAggSink` with two forced changes.** `groups []int32` is
  documented there as *"scratch, reused across batches"*; a window must keep the
  group id of every row. And it retains input batches, because its output is the
  input plus the window columns. Together those make it O(rows) where the aggregate
  sink is O(distinct keys) — inherent, since a window's output is as large as its
  input.
- **The broadcast is one `kernel.Take`**, and it covers every type an aggregate can
  produce because every `Accumulator.Finish` returns exactly `nGroups` rows indexed by
  ordinal with no gaps. `TestAllAggregatesAsWindows` runs all 19 through it.
- **Several partitionings in one node** share by rendered key list, which is the
  common case — every window in a query usually partitions the same way.

## 8. Two things that turned out to be documentary rather than protective

Worth recording precisely, because both look like fixes and are not:

- **`plan.Window` in the predicate-pushdown barrier list is equivalent to the
  fail-closed default.** For a single-child node the two arms are the same code. The
  explicit listing states the reason — pushing a filter below a window shrinks the
  partitions and changes every value — and marks the partition-key case as an
  unclaimed optimisation rather than an oversight. Removing it changes nothing.
- **The `OutputName` case is redundant while `Children()` puts the child first.**
  Removing the case alone changes nothing; removing it *and* reordering `Children()`
  makes the window take its partition key's name. So it is genuine defence against a
  future reordering, and not a fix for a present bug.

## 9. Honest gaps

- **`MapExplode` was cut.** It is listed in the API and was in the plan; the sink
  refuses it. With one spec it is cheap — skip the inverse permutation — but a node
  may hold several specs with different partitionings, and "explode" has no single
  meaning across them. Refused at plan time naming the strategy.
- **`MapJoin` needs a List column** and there is still no List layout. Refused in
  `Window.Field` with the reason.
- **Windows are refused in `Filter`, `Sort` and join keys.** `Filter(x.Sum().Over(g).Gt(100))`
  is SQL's `QUALIFY` and is meaningful; what is missing is plumbing, because those
  nodes do not choose their own output columns and the temporary would leak. The error
  names the rewrite that works: `.WithColumns(w.Alias("t")).Filter(Col("t") > …)`.
- **`windowSink.Merge` refuses**, like `hashAggSink.Merge`, and for a strictly harder
  reason: even given a group-id remap, the retained per-row ids would need remapping
  and the retained batches concatenating in order.
- **Nested windows are refused.**
- Still open: as-of join, `GroupByDynamic`/`Rolling`, streaming with spilling,
  external sort, List columns.

## 10. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
```

| Test | What it would otherwise miss |
| --- | --- |
| `TestWindowPushdownKeepsAggregatedColumn` | the aggregated column pruned out of the scan — hiding the child gives `projection: [g] (1/3 cols)` |
| `TestWindowStringRendersSpec` | two different windows collapsing into one temporary |
| `TestWindowAcceptedWhereAggregateIsNot` | the scoping change, with a reject case beside every accept case |
| `TestWindowThroughMatch` | `All()` inside a window — **panics the process** without `Rebuild`'s arm |
| `TestOrderedWindowMatchesPerPartitionSort` | the partition-major comparator, differentially against per-partition sorting, over an input full of ties |
| `TestRankMethods` | all five methods over a tie and a null |
| `TestShiftPastPartitionBoundaries` | a shift reading the neighbouring partition's value |
| `TestCumulativeNullSemantics` | a null stopping a running total, or being filled in |
| `TestCumSumMatchesSum` | a running total that diverges from the total at width |
| `TestWindowAggregateMatchesGroupBy` | the broadcast, differentially against `GroupBy` over 300 random rows |
| `TestIsUniqueAgreesWithDistinct` | an `IsUnique` that disagrees with `Unique()` about NaN |
| batch-size × thread invariance | the invariant that makes a window a Sink and not a `BatchOp` |
| Golden plans | the `WINDOW` node, its rendering, and the pushed projection |

Teeth verified by reintroduction: deleting `Rebuild`'s `*Window` case (process panic),
hiding the partition key from `String()` (both columns get the same value), and
hiding the aggregated child from `Children()` (the scan stops reading it). The two
documentary cases in §8 were probed and honestly reported as not-a-fix.
