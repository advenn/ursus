# Step 15 — as built

The as-of join, and four deferrals paid off. **v0.2 is complete.**

Authoritative where it disagrees with [`step-14-as-built.md`](./step-14-as-built.md)
and the vision docs.

**1162 test cases green** — 450 top-level tests and 712 subtests (1107 before this
step) — under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the
experiment off, and under `-race`. `make levels` and `go vet` clean.

186 files, ~56.0k lines. `LazyFrame` goes from 39 operations to 41.

```go
// The last unbuildable line of ursus-api.md §16.
trades.JoinAsOf(quotes,
    ursus.AsOfOn(ursus.Col("ts")),
    ursus.AsOfBy(ursus.Col("symbol")),
    ursus.AsOfTolerance(ursus.Every("1m")),
)

lf.MergeSorted(other, "ts")

// And this used to fail at PLAN time: temporal types promoted only to themselves.
ursus.Col("ts").Gt(someTime)          // on any column that is not Datetime(ns, UTC)
```

`dataframe-features.md` §14's v0.2 line — *"streaming engine with spilling hash-agg
and hash-join, external sort, `.Over()` window functions, `GroupByDynamic` +
`Rolling`, string and temporal namespaces, `AsOf` join, `Collect` into Go structs via
generics"* — is **eight of eight**.

---

## 1. The as-of join is a LEFT join with a different match rule

That framing is the whole design, and it is what kept the new code small. Every left
row survives and the right columns are null when nothing is near enough, so:

| Piece | Where it came from |
| --- | --- |
| output schema, collisions, suffixing, key merging | `plan.JoinLayout`, **verbatim** — `AsOfJoin.Layout()` delegates to a synthesised `Join{Kind: JoinLeft, Coalesce: CoalesceOn}` |
| emitting a batch from two selection vectors | `gatherOut`, extracted in step 13 for the spilling join's null bucket. An unmatched left row is `rsel[i] = NullIndex`, which it already handled |
| two-input breaker, ownership, Close ordering | `joinBreaker` |
| key evaluation and casting | `evalKeys` |
| sortedness verification | step 14's discipline |

`join_layout.go` calls itself *"the single authority on a join's output"*, and
delegating rather than re-deriving is exactly what that sentence exists to protect.

**What is genuinely new is one function.** `nearest` holds the entire as-of rule —
three strategies, the exact-match flag and the tolerance — in one place, so they
cannot disagree about what "nearest" means. Two `sort.Search` calls give the last key
at or before and the first at or after; the strategy picks between them.

### Why a separate node rather than another JoinKind

`plan.Join` has no slot for a strategy, a tolerance or an exact-match flag, and
`Keyed()`, `filtersLeft()` and `leftCanBeNull()` all read as statements about equi
semantics. A kind that quietly meant "nearest" would make every one of them subtly
wrong rather than obviously absent.

### The predicate rule is stricter here than for a left join

Left-only predicates push; right-only and both-sides are barriers. The reason is
sharper than the equi-join's: **removing a right row does not merely delete matches,
it changes which row is nearest** — so a left row that matched quote A silently
re-points at quote B. The answer is different, not smaller, and no schema check can
see it.

### Sortedness is verified, not asserted

`ursus-api.md`'s example calls `SetSorted("ts", false)`. Step 14 already decided this
and the answer did not change: `plan.Pushdown`'s doc argues that a capability claim
which is trusted but wrong returns wrong rows silently, and an unchecked `SetSorted`
is precisely that. Both sides are checked while they stream, and the refusal names
which side and which row.

### Tolerance is recomputed per row when it is calendar-aware

`Every("1mo")` is not a number of ticks, so the bound is `tolerance.Neg().AddTo(left)`
rather than a fixed delta — which is the whole reason `Interval` has three fields.
`TestAsOfCalendarToleranceIsNotFixed` pins it on a 29-day February: `1mo` reaches from
1 March to 1 February and `28d` does not.

On a **numeric** key there is no unit for an Interval to measure in, so a tolerance
there is refused rather than silently reinterpreting nanoseconds as counts.

---

## 2. Temporal promotion — a prerequisite, and a doc comment made true

`dtype.Promote` refused **every** non-identical temporal pair, and `plan.keyTypes`
calls it. So the canonical case — trades at `Datetime(us)`, quotes at `Datetime(ns)`,
because two systems wrote them — failed at plan time with *"no common type"*.

The fix is small because the hard half already existed: `kernel.rescaleTemporal` has
converted between resolutions correctly all along, flooring rather than truncating
(*"the classic pre-1970 date bug"*) and turning overflow into a null because *"a
wrapped timestamp is a plausible-looking wrong answer and a null is not"*.

**And the library already disagreed with itself.** `TimeUnit.Finer`'s doc reads:

> *"Finer reports whether u has strictly higher resolution than v. **Used by type
> promotion**: combining two temporal columns keeps the finer unit, so no precision is
> silently discarded."*

Its only caller was `resolveTemporalArithmetic`. So `ts_a - ts_b` worked across
resolutions and `ts_a > ts_b` refused — one library answering one question two ways,
with a doc comment describing behaviour the code did not have. This step makes the
sentence true rather than deleting it.

The new rule is narrow: same temporal **kind**, same **zone**, differing only in
**unit** → the finer one. A zone mismatch stays refused (an instant is not a wall
clock, and two named zones leave the result's own zone undecidable); `Date` against
`Datetime` stays refused. Blast radius was one row of the `Promote` table test.

---

## 3. MergeSorted

Two frames already sorted on a key, interleaved. Not a concat — a concat appends, so
the result is ordered only if the second frame begins after the first ends. Not a
join — nothing is matched and no row is dropped, so the height is always the sum.

Ties take the **left** row first, which is what makes the merge stable rather than
dependent on the inputs' sizes. Schemas must match exactly, and both sides are
verified sorted.

**Honestly stated**: it materialises both sides rather than streaming. A true
streaming merge would hold one batch per side and pull whichever is behind — which is
what `kernel/merge.go`'s `Merger` does for the sort's runs — but the breaker protocol
has no two-input streaming shape, and `joinBreaker` drains one side by design. Memory
is O(both inputs), which is the same as a `Union` under `Collect`.

---

## 4. The projection arms, after four deferrals

Nine node types fell to `rule_projection.go`'s conservative default, and the list had
grown at every step that deferred it. All nine now have an arm, and `*Reverse` is no
longer a predicate barrier.

The visible result, in a checked-in golden that had been wrong for four steps:

```
 AGGREGATE [col("region")] -> [ ... ]
   MEMORY SCAN 7 rows
-    projection: [region, rep, amount, qty] (4/4 cols)
+    projection: [region, amount] (2/4 cols)
```

Two arms carried the risk and both are recorded in the code:

**`*Aggregate` REPLACES the required set rather than extending it.** An aggregate's
output is keys ++ aggs and nothing of the input passes through, so what the parent
wants is satisfied by this node's own output. The reflex from `*Filter` — a node that
consumes columns adds to what the parent wants — is exactly backwards here. It is only
*observable* when an aggregate's output name shadows an unrelated input column, which
is what `aggregate_output_shadows_an_input_column` exists to be: without that case the
teeth check passed with the bug in place.

**`*Distinct` with no subset must prune NOTHING.** Whole-row distinct dedups on every
column, so dropping one collapses rows that were distinct — fewer rows out, no schema
change, nothing to fail resolution. `TestPushdownSoundness` cannot catch it because
`Expressions(*Distinct)` returns an empty slice in exactly this case. Reintroduced, it
gave **1 row where 2 belong**.

`*WithColumns` is walked **backwards**, through `WalkWithColumns` rather than by
re-deriving names: the running schema means a forward pass would ask the child for a
column the node itself defines, and replace-in-place (`Col("x").Mul(2).Alias("x")`)
makes subtract-then-union mandatory rather than stylistic.

And the four nodes that must **not** join `*Reverse` in the predicate rule get a
paragraph, because that is the interesting half: `*Slice` and `*Tail` depend on how
many rows precede them, `*RowIndex` renumbers every row, `*HStack` pairs its children
by position.

---

## 5. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean.

Seven teeth checks, each by reintroducing the defect:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| `<` where backward needs `<=` | `TestAsOfBackwardMatchesAManualScan` | every exactly-on key matched the row before it |
| Ignore the `by` columns | `TestAsOfByIsolatesGroups` | matches leaked across symbols |
| Drop the tolerance check | `TestAsOfToleranceBounds` | a quote 40 minutes away matched a 30-minute tolerance |
| Skip the sortedness verification | `TestAsOfRefusesUnsortedInput` | unsorted input accepted on both sides |
| Revert temporal promotion | `TestAsOfPromotesTemporalUnits` | *"no common type for Datetime(us, UTC) and Datetime(ns, UTC)"* — the canonical case, refused |
| `*Aggregate` extends instead of replacing | `TestProjectionArmsNarrowTheScan` | `3/6 cols` where `2/6` belong |
| Prune under a subset-less `*Distinct` | `TestDistinctPrunesNothingWithoutASubset` | **1 row where 2 belong** |

The oracle worth naming: `TestAsOfBackwardMatchesAManualScan` finds each left row's
match by a linear scan over every right row, sharing no code with the operator — and
it checks *which* row matched, not just that one did, so a tie among equal keys is
pinned too.

**One teeth check needed a better test before it fired.** Extending rather than
replacing the `*Aggregate` required set passed every case in the suite, because in an
ordinary query the parent only names the aggregate's own outputs and those are
filtered out by the scan anyway. It is observable only when an output name shadows an
input column, and that case had to be added before the check had any teeth.

---

## 6. Honest gaps

- **`JoinWhere` is not done.** A filtered cross product with its own pushdown story:
  *"`on` equality is optimizable into a hash join, the rest into a loop join"*. It is
  the one item of §7.4 with no implementation and no prerequisite left.
- **`SetSorted` / `IsSorted` still unshipped**, for the reason step 14 gave. Three
  operators now verify sortedness independently; a shared property with a
  sort-elimination rule is the coherent version of that, and it needs a rule to make
  it worth anything.
- **`MergeSorted` materialises both sides.** §3.
- **`Len()` keeps a column nothing reads.** The `*Aggregate` arm names the column via
  `RootNames` even for an aggregate that never touches it. A missed optimisation, not
  a wrong answer, and closing it needs `aggSpec` to carry a "no input" marker.
- **`SimplifyExprs`** is declared in `plan.Flags`, defaulted false with a "not
  implemented" comment, and read by nothing — and implementing it would wake an
  `UntilStable` run mode with a `maxIterations` bound and a fixed-point error path
  that no registered rule currently exercises. That is a `Sort.Limit`-shaped find and
  deserves its own step.
- The 25 `Expr.Rolling*` methods, `Upsample`/`DateRange`, `Interpolate`, the trig and
  bit-count blocks, the twelve missing aggregates, parallel aggregation and
  `Duration` in Parquet are all unchanged from step 14.

---

## 7. Files

**New:** `internal/plan/nodes_asof.go` (231), `internal/physical/asof.go` (433),
`internal/plan/nodes_merge.go`, `internal/physical/mergesorted.go` (185), `asof.go`
(225). Tests: `asof_test.go`, `pushdown_arms_test.go`, six new golden plans.

| File | Change |
| --- | --- |
| `dtype/promote.go` | the within-kind temporal branch (§2) |
| `internal/plan/rule_projection.go` | nine arms plus `requiredOf` and `withColumnsDefs` |
| `internal/plan/rule_predicate.go` | `case *Sort, *Reverse:`; the `*AsOfJoin` and `*MergeSorted` arms |
| `internal/plan/resolve.go`, `node.go`, `internal/physical/operator.go` | one arm each for two new nodes |
| `testdata/plans/agg_params.txt` | `4/4 cols` → `2/4 cols` |
