# Step 9 — as built

The frame layer. Authoritative where it disagrees with
[`step-8-as-built.md`](./step-8-as-built.md) and the vision docs.

**741 tests green** (698 before this step) under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean.

`LazyFrame` went from **11 operations to 22**; `DataFrame` from a read-only view to
`Lazy()` plus twelve eager mirrors; `GroupBy` from 3 methods to 13.

```go
// stack frames, including ones with different columns
all := ursus.Concat(jan, feb, ursus.WithConcatMode(ursus.ConcatDiagonal))

// and the operations that were missing around them
top := all.
    Drop("internal_id").
    Rename(map[string]string{"amt": "amount"}).
    DropNulls("amount").
    WithRowIndex("row", 0).
    TopK(10, ursus.Asc(ursus.Col("amount")))

df, err := top.Collect(ctx)
again, err := df.Lazy().Filter(ursus.Col("row").Lt(uint32(5))).Collect(ctx)
```

---

## 1. Why this step

The library was lopsided. 122 expression methods — 78 on `Expr` plus the `.str` and
`.dt` namespaces — against **eleven** `LazyFrame` operations. You could compute
almost anything about a column and almost nothing about a frame: no way to stack two
of them, rename one, take a tail, or turn a `DataFrame` back into a query.

The pre-step-7 gap audit noted that §6 of the spec is the one chapter carrying **no
priority annotation at all**, and it is where the coverage was thinnest. Two items in
it were not merely missing but broken, and both are fixed here.

## 2. The dead top-k path

`plan.Sort.Limit`'s own doc said *"Set by the limit-pushdown rule"*. **No rule set
it** — no composite literal in the repo ever assigned the field — so `sortSink`'s
`ArgTopK` branch was unreachable and `Sort(...).Head(10)` over ten million rows did a
full `O(n log n)` sort.

`kernel.ArgTopK` was already written, already documented, and already carried two
named tie bugs that had been found and fixed:

> A bounded heap is not naturally stable, and getting this wrong is invisible on data
> without ties. Two independent bugs had to be fixed here.

The producer is **about fifteen lines**: a second branch in `limitPushdown` matching
`Limit` over `Sort`, with the same never-widen guard the `Scan` branch already had.
The structural-match discipline carries over unchanged — match the child exactly,
never descend — and it costs nothing here because predicate pushdown runs first and
already pushes filters *below* a sort.

`Sort.Label()` renders `top N` when the bound is set, so the golden plan is the proof
the rule fired.

### And a second pessimisation it exposed

With the top-k on, the golden plan still read **6 of 6 columns**. `rule_projection`
has arms for `Project`, `Filter`, `Limit`, `Join` and `Window` — and none for `Sort`,
which therefore fell to the conservative default and required every column. So *any*
query containing a sort read the whole table, and bounding the sort to ten rows is
worth much less if all forty columns were decoded to get there.

The arm is fifteen lines: what the parent needs, plus what the keys read. The golden
now reads `[id, price]` — 2 of 6.

**`Aggregate`, `WithColumns` and `Distinct` still have no arm.** Not fixed here: the
first two define columns and need more thought than a sort does, and expanding this
step to cover them was not the plan. Recorded so it is not rediscovered.

## 3. `Concat` — arity was cheap, reconciliation was not

`design/logical.md` reserved a `*Union{Children, Mode}` shape "so v0.2 slots in" and
asserted that projection pushdown's default arm would handle it correctly on day one.
**That assertion was true**, verified rather than assumed: both optimizer rules,
`Walk`, `TransformUp`, `Explain` and `Expressions` all needed nothing, and the
physical dispatch fails loudly on a missing arm rather than silently.

What it cost instead:

1. **`WithChildren` cannot assert an arity.** Every other node panics on the wrong
   count — `Join` on `!= 2`, the rest on `!= 1` — which catches a rewrite that loses a
   child. A Union takes N, so the only check left is that it kept at least one.
2. **Strict is not `Schema.Equal`.** `Field` compares by struct equality *including*
   `Nullable`, which is "a static property of the schema, not a count of nulls
   actually present". Two frames differing only in that flag must stack — it is the
   ordinary result of filtering one of them. So strict means *the same columns in the
   same order*, with types promoted and nullability unioned.
3. **`kernel.Concat` is the assembler, not the reconciler.** It takes the target
   schema as an argument and **never checks it**: the body pairs columns positionally
   and derives every name and type from batch 0. Hand it mismatched batches and it
   splices payloads rather than failing. So reconciliation happens in the plan, and
   the test for it says so.

**Adaptation is a `Project`, not something inside the operator.** A child missing a
column, or holding one at a narrower type, is wrapped in a projection that supplies
the typed null and performs the cast:

```
UNION diagonal (2 inputs)
  PROJECT [col("k").cast(Int64).alias("k"), col("v"), lit(null:Float64).alias("x")]
    MEMORY SCAN 1 rows
      projection: [k, v] (2/2 cols)
  PROJECT [col("k"), lit(null:String).alias("v"), col("x")]
    MEMORY SCAN 1 rows
      projection: [k, x] (2/2 cols)
```

Three things follow: "why is this column null" has a visible answer in the plan;
projection pushdown narrows each input through an ordinary `Project` with no
Union-specific rule; and a child whose schema already matches gets no node at all, so
the common case costs nothing.

**Diagonal column order is first appearance across the inputs**, never map iteration —
the test runs twenty times because a map-order bug is intermittent by construction.

**Predicate pushdown into a Union is unconditionally safe** and is the one n-ary case
where the join's four-cell legality table is unnecessary: a concat assigns each output
row to exactly one input, so filtering the inputs and stacking gives the same rows as
stacking and filtering. There is no null-extension to reason about.

**`HStack` buffers, and `Concat` does not.** Nothing aligns batch boundaries across
independent pipelines — one frame may deliver 8192 rows while another delivers 100 —
and pairing row *i* of each means having row *i* of each in hand. It also refuses
colliding names rather than suffixing: a join suffixes because it has a principled
left and right, and these inputs are peers.

## 4. Three pre-existing defects, found by testing the new code

All three are the same distinction, in three places, and none is exotic to reach.

**(a) `Head(0).Collect()` panicked on reading any column.** `exec.Collect`
concatenates zero batches when a query returns nothing, and `kernel.Concat`'s
zero-batch path built a batch whose schema promised N columns and whose storage had
none. `Batch.ByName` indexes storage by the schema's position:

```
panic: runtime error: index out of range [0] with length 0
```

Reached by `Head(0)`, a filter matching nothing, and a slice past the end — which is
how I found it.

**(b) A typed null literal could not be used.** `Select(ursus.Null(ursus.Float64))`
failed with *"has no fixed-width payload"*, because `litColumn` built it with
`data.NewNull`. A literal is an **operand**: it gets broadcast with `Take`, gathered
by a join, concatenated by a union, and every one of those reads the payload. This is
the fourth appearance of the `data.NewNull`-versus-`NullColumn` rule step 4 wrote
down, and diagonal concat is what surfaced it — the padding columns are typed null
literals.

**(c) `physical.emptyColumns` had the same problem**, so an empty result was readable
after (a) was fixed only if it came through `kernel.Concat`. Every operator that can
produce no rows uses that helper.

The fix in each case is the same one line, and the rule is now stated where it is
easy to hit: **a zero-row column still has to be readable.** `df.Column[int64]("v")`
on an empty result gives an empty series rather than an error, which costs nothing
because there are no values either way.

## 5. `DataFrame.Lazy()` — the obvious spelling is wrong three ways

`ursus.Frame(df.Batch().Columns()...)` compiles and runs, because `Column` is a type
alias. It is wrong:

1. **Nullability is silently rewritten.** `memsrc.FromColumns` derives
   `Nullable: c.NullCount() > 0` — *data-dependent* — while the frame carries the
   *declared* flag. A nullable column that happens to hold no nulls, the ordinary
   result of a filter, comes back non-nullable, and every downstream join, aggregate
   and cast then reasons from the wrong schema. `TestLazyRoundTripPreservesSchema`
   fails immediately under that spelling.
2. **A zero-column frame loses its row count.**
3. **The column slice is aliased**, against `Columns()`' own contract.

So `Lazy()` goes through a new `memsrc.FromBatch`, which preserves both the declared
schema and the row count. It is free — no copy, no re-derivation.

**The eager mirrors take a `ctx`.** The API notes sketch them with
`context.Background()`, which contradicts the same document's rule that a context
appears at every execution boundary — and these *are* execution boundaries, which is
exactly what distinguishes them from the lazy builders.

## 6. What turned out to be sugar

- **`Drop`** is `Select(Exclude(...))`, **`Rename`** is `Select(All().MapName(...))`,
  **`DropNulls`** is `Filter(Col(c).IsNotNull(), …)`, **`TopK`/`BottomK`** are
  `Sort(...).Head(k)`. Each inherits expansion, type checking, error messages and
  pushdown rather than reimplementing them — and `DropNulls` earns Parquet row-group
  pruning free, because the pruner claims `IsNotNull` over a bare column.
- **`DropNans` is deliberately absent.** `IsNotNull` is *total*, so the predicate is
  never itself null. `IsNotNan` is null on a null row, and `Filter` drops null
  predicates — so the obvious spelling of `DropNans` would silently drop nulls too.

### Two decisions inside the sugar

**`TopK` and `BottomK` place nulls last, unlike `Sort`.** Sort keeps direction and
null placement orthogonal on purpose — *"`.Desc()` silently relocating the nulls
surprises people every time"* — and that argument does **not** transfer. In a sort,
placement is cosmetic: every row comes back either way. In a top-k it is *selection*,
and a null has no rank. The first version inherited the default and `TopK(2)` over
`[3,1,4,1,5,null]` returned the null and the 5 — a row with no value in the very
column being ranked.

**The numeric `GroupBy` shorthands restrict to numeric columns rather than refusing.**
`GroupBy(k).Sum()` on a frame with one string column would otherwise be an error, and
almost every real frame has one — which would make the shorthand useless exactly
where it is most wanted. It is not a silent skip: the selector is part of the
expression, so Explain shows precisely which columns were summed. `Min`, `Max`,
`First`, `Last` and `NUnique` take everything, because they select rather than
compute.

## 7. Honest gaps

- **`Aggregate`, `WithColumns` and `Distinct` still have no projection-pushdown arm**
  (§2). A group-by over a forty-column table still reads forty columns.
- **`align` concat mode** is out. It joins on common columns, which is a rewrite to
  `*Join` rather than a concat.
- **`vstack` is not O(1).** Polars documents it as "cheap (adds a chunk)", which
  relies on a chunked column layout ursus does not have — a Column here is one
  contiguous run, so stacking copies. `VStack` is an alias for `Concat` and says so
  rather than implying otherwise.
- **`Gather` and `Sample`** are out. `Sample` has a hidden decision: a `BatchOp` must
  be "stateless and order-independent", an RNG is neither, and thread-count
  reproducibility is a documented guarantee.
- **`Explode`** needs a List layout; **`Transpose` and `ToDummies`** break the
  invariant every plan node rests on — schema from children alone — because their
  output columns are a function of the *data*. Both need a decision before an
  estimate means anything.
- **`MergeSorted` and `Update`** — the first is worthless without a sortedness
  property, the second needs a two-column merge kernel that does not exist.
- Still open: as-of join, `GroupByDynamic`/`Rolling`, streaming with spilling,
  external sort, List columns, the null-handling and math families.

## 8. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
```

| Test | What it would otherwise miss |
| --- | --- |
| `TestLimitPushdownReachesSort` | the dead path — asserts `top 3` appears optimized and does NOT appear unoptimized, so it cannot pass for a Sort born with a bound |
| `TestTopKMatchesSortHead` | the `ArgTopK ≡ ArgSort[:k]` contract from outside, over 400 rows of six distinct values, at seven values of k |
| `TestConcatRejectsMismatchedSchemas` | `kernel.Concat` splicing positionally without checking |
| `TestConcatWidensNullability` | frames differing only in a nullability flag being refused |
| `TestConcatDiagonalColumnOrder` | map-order column layout — run twenty times, because the bug is intermittent |
| `TestConcatPushdown` | a predicate reaching each input, and each scan reading only what it needs |
| `TestLazyRoundTripPreservesSchema` | nullability re-derived from the data |
| `TestEmptyResultKeepsItsColumns` | the panic in (4a), across four ways of producing nothing |
| `TestSliceTailReverseAcrossBatchSizes` | all three at seven batch sizes — reversing each batch in place is right only at one |
| `TestWithRowIndexIsBatchSizeIndependent` | a counter that restarts per batch |
| `TestFrameOpsAreThreadIndependent` | four order-dependent operations composed, across batch sizes and thread counts |
| Golden plans | `top N`, the UNION node, and the adaptation projections |

**Teeth**, each verified by reintroduction: removing the `*Sort` branch from
`limitPushdown` (the plan loses `top N`); removing the `*Sort` arm from projection
pushdown (the scan goes back to 6/6); making strict concat skip its schema check (the
refusals stop); ordering diagonal columns by map iteration; routing `Lazy()` through
`FromColumns` (the schema changes); and reverting each of the two payload-free null
fixes.
