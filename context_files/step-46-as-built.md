# Step 46 — as built

**`Implode` ships: the aggregate that collects a group into a list.** With step 45's
`Split` it completes the pair — one kernel that builds a list from a string, one
accumulator that builds a list from a group — and it makes the entire `.list`
namespace, fourteen functions, available inside a `GroupBy` without reimplementing
one of them.

It also settled a question the plan got wrong: `MapJoin`, refused since step 6 and
expected to ship here, turned out to be a **synonym**.

Authoritative where it disagrees with [`step-45-as-built.md`](./step-45-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. What shipped

```go
GroupBy(Col("k")).Agg(
    Col("v").Implode().Alias("all"),
    Col("v").Implode().List().Sort().List().Head(2).Alias("smallest2"),
    Col("v").Implode().List().DropNulls().List().Len().Alias("non_null"),
)
```

One `AggOp`, one accumulator, one `Expr` method. Everything above is free:
`extractAggs` already descends through a `Call` wrapping an `Agg`, so the
surrounding `.List().Sort().Head(2)` is evaluated by the phase-2 `Project` that
`planAggregate` already builds — *"Reusing Project means compound aggregate
expressions get the whole expression system for free."*

`Col("v").Implode().Over(k)` is free too: `finishAggregate` is `Finish` then
`kernel.Take`, and `takeList` has existed since step 29.

**No `Expr.Head(3)`-style sugar.** It would be legal inside `Agg(...)` and
meaningless outside it, which is the shape `fill.go` refuses for `DropNulls` and
`Explode`. One spelling that means one thing everywhere beats matching Polars.

---

## 2. The design: row indices, not values

`quantileAcc` is the existing accumulator that retains its input, and its storage is
`vals [][]float64` — one typed slice per group. Copying that shape means a typed
slice per element type, which is the four-way split (`extremumNum[T]`,
`extremumStr`, `extremumI128`, `extremumBool`) that `agg.go` spends thirty lines
justifying, and it would need six or seven arms rather than four.

`implodeAcc` retains the input column **once per batch** and remembers which row of
the concatenation each group took. `Finish` is one `concatColumn` and one `Take`.

```go
type implodeAcc struct {
    elem  dtype.DataType
    parts []*data.Column // one per BATCH
    rows  [][]int32      // per group: indices into the concatenation
    base  int32
}
```

`listRebuild`'s sentence applies verbatim: *"THE ELEMENT TYPE NEVER APPEARS. Take
does the gather, so `List(String)` works for exactly the reason `List(Int64)`
does."* Per-row state is four bytes, less than `quantileAcc`'s eight.

**It is not step 20's anti-pattern in new clothes.** That one allocated a map per
batch and a `Take` per group *per batch* — millions of one-row Columns, and 93
seconds on h2o gb7. Here the Columns are one per batch and the `Take`s are one in
total.

**And it is the only shape that is correct for Enum.** `data.NewList` derives its
element type from the child and offers no `WithDType` override, deliberately — *"A
List whose declared element type disagrees with the column actually holding the
elements would be a lie no caller could detect."* `extremumStr` needs exactly that
override to keep an Enum an Enum over string storage; a typed-slice implode would
need it too and could not have it. `Take` preserves the dtype on every arm.

`elem` is needed in exactly one place: an accumulator that saw no batch has nothing
to derive a child from. That case is the empty global aggregate, and its answer is
the empty list.

---

## 3. Implode does not skip nulls, and every other aggregate does

`agg.go` states the rule: *"Every aggregate skips nulls except Len, First and Last.
That is the SQL rule and it is applied uniformly."* Implode is the fourth exception,
for `positionAcc`'s reason — it is a **selection**, so a null is a value that takes a
slot.

An all-null group implodes to `[null, null]`: a non-null list **of** nulls, which is
neither the null list nor the empty one. Those are the three states `data.NewList`'s
doc is built around and `strToList` had to get right last step; this is the same
distinction one level in, and `listfn.go` already named it — *"Null ELEMENTS are a
third thing again: they occupy a slot, so len counts them."*

The tooth for it bites hard: skipping nulls turns that group into `[]`, and `len`
reports 0 instead of 2.

---

## 4. The cost: it makes the group-by single-threaded

A list's element order is its data. The parallel driver dispatches batches
round-robin, so an undeclared implode returns a different list every run.
`IsOrderDependent` gains it, and `aggWorkers` *"declines the whole aggregate rather
than the individual expression, because one query's Agg list is one hash table"* —
so `Agg(Col("v").Implode(), Col("w").Sum())` loses parallelism on both.

That is stated in the method's doc where a user will see it. The tooth is the one
that mattered most to run: removing the declaration failed on the first attempt with
*"element 16 of group a is 512, want 64"* — the shuffle is immediate, not rare.

Its state is O(rows), so a spilling group-by refuses once one key outgrows the
budget. `overBudget` needed no change: it compares `NBytes()` against the budget and
enumerates nothing.

---

## 5. `MapJoin` was a synonym

The plan said *"`MapJoin` becomes implementable, so it ships"*, and it was traced as
two refusals to delete plus one output-type arm. That was right about the mechanics
and wrong about the meaning.

`MapJoin` is *"each partition's values as a List, repeated across its rows"*. That is
exactly what `Col("v").Implode().Over(k)` does under the **default** mapping —
implode produces the list and the ordinary broadcast repeats it. It was implemented,
observed to be redundant, and reverted.

It has no coherent reading over a non-imploding aggregate either:
`Col("v").Sum().OverWith({Mapping: MapJoin})` would have to discard the sum.

So the refusal stays, and what changed is **why**, for the third time:

| step | the stated reason |
| --- | --- |
| 6 | "there is no List column layout" |
| 45 | "no aggregate builds one yet — the layout exists, the accumulator does not" |
| **46** | **"it is `Col(...).Implode().Over(...)`, which does this under the default mapping"** |

A second spelling is a second thing to keep correct for no expressive gain. The
enum comment in `internal/expr/window.go`, the public constant's doc in `window.go`
and the refusal hint all said different stale things; all three now say this one.

---

## 6. Two defects in the same seam

**A window inside `Agg()` was not refused, and the code said it was.**
`hashAggSink`'s comment — *"A window inside Agg() is refused at plan time, so there
is no non-row-local case to worry about"* — was false. `rejectWindow` covered filter,
sort, join keys and group-by **keys**, never the aggregates. So
`Agg(Col("v").CumSum().Sum())` resolved cleanly, `Explain` printed a plan, and
`Collect` died with *"a window reached the evaluator; it should have been extracted"*
— a message that announces itself as an ursus bug, for a user's mistake.

One `rejectWindow(e, "agg")` in the agg loop makes the comment true. The tooth shows
exactly what it prevents.

**`OverWith{OrderBy: …}` over an aggregate was silently ignored.** `planWindow` set
`spec.order` in the shared initializer, before the branch that distinguishes the two
forms, and only `finishOrdered` ever read it — the field even sits under the
struct's `// ordered form` comment. It is now carried on the `WinFn` branch only and
**refused** on the aggregate branch. Honouring it is `SortBy`'s much larger problem;
silence was the defect, not the absence.

**`OverWith` had zero call sites in any test in the repository.** `WindowSpec`'s
`OrderBy` and `Mapping` fields were entirely unverified. This step brings the first
three.

---

## 7. Three claims that stopped being true

- **`extagg.go`** said *"the only things that can still grow are the **two** holistic
  accumulators"*. Implode is a third. The stated property — *"a spilling group-by
  refuses exactly when an accumulator's state is O(rows)"* — is unchanged; implode is
  an instance of it, not a counterexample.
- **`aggstat.go`** said *"the two accumulators the freeze cannot bound are exactly
  the two whose Merge is ORDER-INDEPENDENT. Spilling within a hot key is therefore
  possible for precisely the two aggregates that need it."* Implode breaks the
  biconditional: it is O(rows) and its `Merge` is order-**dependent**. That sentence
  was pointing at a future design, and the honest correction is that within-key
  spilling would cover quantile and n_unique and would have to refuse implode — a
  narrower prize than it looked. Rewritten rather than extended.
- **`spill.go`** refused List with *"nested types have no data.Column representation
  yet"*. Stale since step 28. The refusal is real — `putColumn` has no List arm — but
  what is missing is the serialisation format, not the layout.

---

## 8. Verification

- Every payload shape, including **List-of-List** via `Split(...).Implode()`, which
  is the Take-based design's whole claim: no per-type arm.
- The null cases: a group with a null among values, a group entirely null, and the
  empty global aggregate (one **empty** list, not zero rows and not a null).
- Batch sizes {1, 2, 3, 7, 8192} × threads {1, 2, 4} — the accumulator concatenates
  per-batch columns and shifts each batch's indices.
- Element order stable over five attempts at 8 threads and batch size 64.
- **`Merge` tested directly**, both under the identity and under a remap, because
  nothing calls it: order-dependence forces one worker and spilling partitions by
  key. Steps 13, 17 and 39 each found a `Merge` that nothing exercised, and the
  `Accumulator` doc's rule is explicit — *"a Merge written later against forgotten
  invariants is a Merge that is wrong."*
- The O(rows) refusal, on a fixture of **few rows with big values**. With many small
  rows the index slices alone blow any budget and the `NBytes` tooth does not bite;
  two thousand indices are 8 KiB and two thousand 512-byte strings are a megabyte,
  and only the second crosses the limit.

**Teeth** — all six bite:

| tooth | result |
| --- | --- |
| skip nulls in `AddBatch` | **bites** — the all-null group becomes `[]` |
| do not declare `IsOrderDependent` | **bites** on the first attempt |
| forget the index shift in `Merge` | **bites** — `[1,3,1]` where `[1,3,4]` is right |
| `NBytes` omits the retained columns | **bites**, once the fixture was made discriminating |
| `Out: in` rather than `List(in)` | **bites** — `list.len requires a List operand, got Int64` |
| drop the `rejectWindow` call | **bites** — back to the internal-error message |

---

## 9. What is still open

- **`Str().Join()`** is now one `FnListJoin` away — `Implode()` is the `AggOp` step
  45 said it needed. A `.str` decision, better made with the rest of that tail.
- **Ordered aggregate windows.** `OverWith{OrderBy}` over an aggregate refuses rather
  than works, and honouring it needs a per-expression order key that exists nowhere
  in the aggregate path — `SortBy`'s problem, and every route to it costs
  `hashAggSink` either its O(distinct keys) memory or its spillability.
- **`MapExplode`**, the other refused mapping. It reorders its output, which is a
  different argument from `MapJoin`'s.
- **The published numbers are five steps stale** — REPORT.md is from step 40's tree.
- The standing list: the parallel join's memory trade, CSV scanning, `Expr`-level
  selection generally, inline keys for `KeyTable`, the heap sampler, `quantile`/
  `median` storage, nested writing and `as_struct`, `.list` set operations,
  Map/Array, Pivot/Unpivot, SQL, cloud stores, join reordering.
