# Step 47 — as built

**`Unpivot` ships, and `Pivot` gets a verdict instead of a twentieth appearance on
the standing list.** The two have been carried as one item since step 28. They are
not a pair: `Unpivot`'s schema is a function of its input schema, like every node in
the project, and `Pivot`'s is a function of the **data**, which no node here has ever
been.

Authoritative where it disagrees with [`step-46-as-built.md`](./step-46-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. What shipped

```go
lf.Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}})

  id  region  jan  feb          id  region  variable  value
  1   eu      10   20    ->     1   eu      jan       10
  2   us      30   null         1   eu      feb       20
                                2   us      jan       30
                                2   us      feb       null
```

One plan node, one `BatchOp`, one `Expr`-free API. `On` is required; `Index`,
`VariableName` and `ValueName` all have working defaults.

**`On` is required and `Index` is not**, which is the opposite of `Unnest`'s rule and
deliberately so. `Unnest` refuses to default its list because *"naming them keeps the
frame's width a property of the query rather than of whatever the file happens to
contain"* — there, a new source column would silently start being **consumed**. Here
a new column is **kept**, which is what anyone would want. Defaulting `On` would have
`Unnest`'s problem exactly, so it is not offered.

---

## 2. Row-major, which is not Polars' order

Polars stacks k frames vertically: every row's `jan`, then every row's `feb`. This
emits every variable for row 0, then row 1.

The reason is that unpivot is a `BatchOp` — one batch in, one batch out — so Polars'
order would give batch 1's `jan`s, then batch 1's `feb`s, then batch 2's `jan`s. **The
row order would depend on the batch size**, which is a property of how the query ran
rather than of what it asked for. Row-major is the order a streaming operator can
produce, and it is the same order at every batch size.

That trade is the right way round for this project, which tests batch-size invariance
on every operator that has it. The doc comment says so where a user will see it.

---

## 3. Why it is not a desugaring

The tempting design is `Union{Project₁ … Projectₖ}`, one arm per melted column. It is
genuinely attractive: `resolveUnion` already reconciles and promotes child schemas —
including the exact error this needed — and projection pushdown through an ordinary
`Project` would come free.

**It is wrong here, for a reason that has nothing to do with row order.** All k arms
share one input subtree, the physical planner has no memoisation and there is no CTE
node, so the plan would **execute the input k times**. Unpivoting twelve month
columns from a Parquet file would read the file twelve times.

So it is `explodeOp`'s shape instead: two gathers and one build.

```
idxSel[i*k+j] = i        which input row this output row carries
valSel[i*k+j] = j*n + i  which cell of the k columns laid end to end
```

`valSel` is what makes the value column one gather rather than an interleave: the k
columns are concatenated — already cast to the promoted type — which puts `On[j]`'s
row i at `j*n+i`, and the `Take` permutes that into row-major. The variable column is
built directly, and is the only column in the engine whose values come from the
**query** rather than from the data.

`kernel.ConcatColumns` is new and thin: `Concat`'s per-column half, exported because
unpivot has k columns to stack and no batches to build them from.

---

## 4. Two teeth that did not bite, and both are findings

**Dropping the `Expressions()` arm changes nothing.** `On` and `Index` are column
references in string form, invisible to a liveness rule that only walks expressions —
`Distinct.Subset` has the arm for exactly that reason. Removing it broke no test, and
there are two independent reasons:

- Projection pushdown's default for an unknown node is to require **all** of its
  input's columns — *"New node types are therefore correct-but-unoptimized the day
  they are added, rather than silently pruning columns they needed."* So nothing
  prunes, and the arm is never consulted.
- The liveness safety test walks a **fixed list of plans** with no `Unpivot` in it.
  And it could not catch this anyway: it asserts `needed ⊆ projected`, so it detects
  over-pruning, not under-declaring.

The arm is kept, because it is correct and becomes load-bearing the moment a
pushdown arm exists. **But `Unpivot` currently reads every input column**, which on a
wide frame is a real cost — and the pushdown arm that would fix it is not trivial:
with an implicit `Index`, the index columns are computed *against the input schema*,
so narrowing the input silently changes the output schema. That is the
naming-stability hazard `pushdownJoin` documents, in a new place.

**Marking the value column non-nullable changed nothing either**, and the reason is
larger than this step. `data.NewBatch` validates names and types and **not
nullability**; `WithVerify` is the optimizer's cross-rule schema check, not a
declared-versus-actual one. **A column declared non-null that holds a null is a lie
no machinery in the engine catches.** Only an explicit assertion does, so
`TestUnpivotValueNullability` makes one — in both directions — and with it the tooth
bites.

---

## 5. Teeth

| tooth | result |
| --- | --- |
| index columns taken with stride `j` instead of `i` | **bites** — every value lands against the wrong row |
| emit variable-major within a batch | **bites** |
| skip the type promotion, keep the first column's type | **bites** — `1<<40 is not representable as Int32` |
| mark the value column non-nullable | **bites**, once a test asserted the schema — §4 |
| drop the `Expressions()` arm | **does not bite** — §4 |

---

## 6. A defect the refusals found

The four validation errors — unknown column, both-melted-and-kept, no common type,
colliding output name — all live in `Schema()`, which is where `Explode` and `Unnest`
put theirs too. That meant the first caller of `Schema()` was a rule inside
`Optimizer.Run`, so melting a column that does not exist reported:

```
ursus: optimize: rule "projection_pushdown" failed
  caused by: ursus: unpivot: unknown On column "nope"
```

A user's typo, announced as an optimizer failure. `resolveJoin` solved this once and
says so — it computes a layout and throws it away *"rather than at the first Schema()
call inside Optimizer.Run, which wraps every rule failure as … the exact
maximally-confusing message defect P2 produced for a perfectly valid query."*

`resolveUnpivot` is that, and it is the whole function: compute the schema, discard
it. **`Explode` and `Unnest` have the same shape and no such arm, so they have the
same defect** — not this step's business to fix, but worth knowing it is a pattern
rather than a one-off.

---

## 7. The verdict on `Pivot`

Not built, and this is the argument rather than a line on a list.

`Pivot`'s output **columns are the distinct values of a column**, so its schema
depends on data. The invariant that forbids is stated in four independent places —
`node.go`'s package doc (*"SCHEMA WITHOUT DATA. Every node can compute its output
schema from its children's schemas alone"*), the `Node` interface (*"it never reads
data"*), `design/logical.md`, and `ursus-api.md`. **All 21 plan nodes obey it.**

`Scan` looks like the exception and is the proof: it does not compute a schema from
data, it caches one fetched from **metadata** at `Resolve` time, and `CollectSchema`
enumerates exactly what that permits — *"a Parquet footer, a CSV sample"*.

| option | what it costs |
| --- | --- |
| **Distinct scan at `Resolve`** | Structurally legal — `Resolve` already takes a `ctx`. But `CollectSchema` and `Explain` would read the whole table, **before the optimizer runs**, and the same sentence in four files becomes false. |
| **Eager-only `DataFrame.Pivot`** | The most expensive, not the cheapest. *"Each is Lazy().op().Collect(ctx). One implementation, not two: there is no separate eager engine to keep in step, so an eager result cannot disagree with the lazy one."* This would be the first operation to break that, and the first hand-written eager implementation. |
| **Caller supplies the columns** | Free. The schema becomes a function of the arguments, every invariant holds, and it desugars to `GroupBy(index).Agg(When(Col(on).Eq(c)).Then(agg))` — a path `extractAggs` has supported since step 7, which `cond_test.go` already calls *"how a pivot is written"*. The cost is that it is not Polars' `pivot`, and users will file bugs. |

**So `Pivot` is not a missing feature. It is an unresolved question about whether
"schema without data" admits an exception**, and it deserves its own step and its own
document. The manual form works today and the README now points at it.

The standing list and the README stop treating the two as one item, which is the
other thing this step ships.

---

## 8. Verification

- Row order asserted value-by-value, including the null that survives the melt.
- Batch sizes {1, 2, 3, 7, 8192} × threads {1, 2, 4} — §2's whole argument, and the
  check that it is a real `BatchOp` riding `parallelOp`.
- Type promotion, with `1<<40` in the Int64 column so a truncation to Int32 shows.
- Nullability asserted against the schema, in both directions — §4.
- `Index` implicit and explicit; custom `VariableName`/`ValueName`.
- The output group-bys and sorts like any other frame.
- Six refusals, each checked for the right substring **and** for not being reported
  as an internal error.
- A golden plan, which is the only place a reader sees which columns are melted.

---

## 9. What is still open

- **`Pivot`** — §7. A decision, not a gap.
- **Projection pushdown through `Unpivot`** — §4. It reads every input column today,
  and the arm that would fix it has to handle the implicit-`Index` hazard.
- **Nothing validates declared nullability** anywhere in the engine — §4. Not this
  step's defect and not this step's fix, but now written down.
- **`Explode` and `Unnest` have no resolve arm**, so their validation errors surface
  as optimizer-rule failures — §6.
- **`Transpose`** has `Pivot`'s problem and worse: its output *width* is the input's
  *height*.
- The standing list: the parallel join build (`joinBuildSink.Merge`, implemented and
  never called since step 10), spilling and parallelism being mutually exclusive in
  `aggWorkers`, CSV scanning, a suite re-run (now five steps unpublished), the `.list`
  set operations (which need the first column-valued `Call` argument), `Str().Join()`,
  `Expr`-level selection, inline keys for `KeyTable`, the heap sampler,
  `quantile`/`median` storage, nested writing, Map/Array, SQL, cloud stores, join
  reordering.
