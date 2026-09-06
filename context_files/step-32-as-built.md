# Step 32 — as built

**The `.list` namespace, list → scalar.** Seven functions that let a caller ask
questions about a list without destroying it: `Len`, `Get`, `First`, `Last`,
`Contains`, `Min`, `Max`, `Sum`, `Mean`.

Authoritative where it disagrees with [`step-31-as-built.md`](./step-31-as-built.md),
the vision docs and [`design/`](./design/).

**1448 test cases green** — 1443 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 still
validates 22/22.

```go
ScanParquet(f).Filter(Col("tags").List().Len().Gt(uint32(1)))
// the list is still a list afterwards — that is the point
```

---

## 1. Where the plan was wrong: sum of an empty list

The plan said `Sum` of an empty list is 0, following polars. The implementation
came back null, and **the implementation was right**.

ursus's own `Sum` answers null for a group with no values — verified directly
rather than assumed, with a group whose rows are all null. So `.list.sum([])`
answering 0 would mean `.list.sum()` and `Sum()` disagreed about the same
question, and a user who knew one would be wrong about the other. Agreeing with
the aggregate standing beside it beats agreeing with polars.

The table as it actually is, and as both doc comments now state:

```
                Len()  Sum()  Min()  Get(0)  Contains(x)
[1, 2]            2      3      1      1      as asked
[]  (empty)       0     null   null   null      false
null (list)      null   null   null   null      null
```

An empty list HAS a length and it is zero; a null list has no length at all. An
empty list CONTAINS nothing, which is a false answer to a real question, where a
null list cannot answer it. The aggregates are the row where the two agree, and
that is now a decision rather than an accident.

---

## 2. A post-mask that was written, checked, and deleted

`listReduce` originally applied the row validity after the accumulator, on the
reasoning that an empty list and a null list both contribute zero elements so the
accumulator cannot tell them apart. That reasoning is correct and the conclusion
did not follow: it does not *have* to tell them apart, because every ursus
aggregate answers null for a group with no values either way.

Removing the mask changed no test. Rather than keep dead code with a careful
justification attached — which is worse than no code, because the justification
makes it look load-bearing — it is gone, and the doc says why it is not there and
when it would be needed. `TestListNamespaceSemantics` pins both rows so the
equivalence stays deliberate.

---

## 3. Min/Max/Sum/Mean are the accumulators, not new reductions

`Accumulator.AddBatch` takes "groups[i] is the group ordinal of row i", and a
list's offsets say exactly that: element *j* belongs to row *i*. So a per-row
aggregate is the existing accumulator fed a groups array built from the offsets,
then `Finish(name, nRows)`.

**No per-type reduction code exists in this step.** Every type the column-wide
aggregates support works here through the flat typed storage step 20 built, and
`Sum`'s Int128 widening comes from `ResolveAggBinding` rather than being restated
— so the per-row and column-wide sums cannot drift apart in type either.

---

## 4. Contains reuses is_in's machinery, including its strictness

Comparison goes through `NewGroupKeyEncoder`, the same path `is_in` takes. That
matters for one reason worth naming: the encoder puts floats through `OrderKey`
first, so `NaN` matches `NaN` and `-0.0` matches `+0.0`, consistently with GroupBy
and Sort. Comparing raw bits would give IEEE's answer, and a
`.list.contains(NaN)` that is always false is a defensible-looking bug.

The needle is prepared evaluator-side by `buildListNeedle`, which is `buildInSet`
for one value and strict for the same reason: `Contains(int64(5000))` against
Int8 elements reports that 5000 does not fit rather than silently matching
nothing. The cast target is the receiver's **Inner** — encoding against
`List(Int8)` instead would have matched nothing at all.

`Get` is likewise a `Take` over computed indices, so the element type needs no
switch: `NullIndex` covers the null list, the empty list and the out-of-range row,
and `Take` turns all three into a null.

---

## 5. The plumbing was a well-worn path

`.str` and `.dt` had already established every layer: a `ListExpr` wrapper and
`Expr.List()`; a `FnList*` block with a `fnListEnd` sentinel and `IsList()`
beside the four existing family predicates; `listCallOut` inside `ResolveCall`,
which its own doc calls "the single authority ... two copies would drift"; a
`ListCall` kernel mirroring `StrCall`; and one arm in the evaluator's dispatch.

A non-List receiver is refused there with a type error, the way `.dt` refuses a
non-temporal one.

---

## 6. Teeth

**A null list reports a length.** `len = (0, valid true), want (0, valid false)` —
the empty/null conflation this arc keeps returning to, now caught at the
namespace level too.

**`Get(-1)` indexes from the front.** Every negative index goes out of range. The
fixture's first row has THREE elements for this reason: with single-element rows,
indexing from the front and from the back agree, and the test would pass.

---

## 7. Verification

`listns_test.go`: the whole semantics table at four batch sizes, checked cell by
cell — one test rather than several, because the cells are only meaningful
against each other; the index arithmetic including `-1`, `-3`, and past the end;
the namespace over `List(String)`, where `Min` orders and `Contains` compares;
filtering on `Len` with the list intact afterwards; and the two refusals.

Plus the full gate and **PDS-H SF=0.1 revalidated 22/22**, since the evaluator's
Call dispatch is on every expression's path.

`git status bench/results/REPORT.md`: untouched.

---

## 8. What is still open

- **The list → list half** — `sort`, `unique`, `reverse`, `slice`, `head`, `tail`,
  `gather`, `drop_nulls` and the four set operations. They construct a new List
  column, which is why they were not in this step, and they are the natural next
  slice.
- **`join(sep)`**, `to_struct`, and `list.eval` with `element()` — the catalogue
  singles the last one out as "a powerful and non-obvious feature worth
  preserving", and it needs a sub-expression evaluated per row.
- **`List(Bool)`, `List(Decimal)`, nested lists** — one reader accumulator each.
- **Struct**, **Array**, **Map**; **writing** a List column.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger, which this arc has not touched — **join reordering** (no cost
model exists; PDS-H is 10.7x polars at SF=1), **join parallelism** (8 threads
slower than 1), **memory** (4.7x polars at SF=1), and the ~30% measurement noise
floor that makes all three hard to work on.
