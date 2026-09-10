# Step 50 — as built

**The escape hatch, forty-nine steps late.** `MapElements` and `MapBatches`: a Go
function applied to a column, participating in a plan.

They had never been on a standing list. Every other deferred item gets re-named each
step; this one simply never came up, though both design documents specify it and
`dataframe-features.md` §12 calls per-element UDFs in Go *"a genuine selling
point"* — what costs a Python interpreter round trip in Polars is a function call
here.

Authoritative where it disagrees with [`step-49-as-built.md`](./step-49-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. What shipped

```go
Col("celsius").MapElements("to_fahrenheit", ursus.Float64,
    func(c float64) (float64, error) { return c*9/5 + 32, nil })

Col("v").MapBatches("normalise", ursus.Float64,
    func(vals []float64, ok []bool) ([]float64, []bool, error) { ... })
```

Both are generic **methods** — the reason this project targets Go 1.27 — and the
type parameters are inferred from the function, so a caller writes neither.

`MapElements` never sees a null: null in, null out, and `fn` is not called. The
alternative hands `fn` a zero value it cannot distinguish from a real one.
`MapBatches` receives and returns the validity mask, which is the whole reason to
reach for it — the shipped test turns a negative value into a null, something
`MapElements` cannot express at all.

**Half of this already existed, eagerly.** `data.Series[T].MapErr[U]` had exactly
these semantics — nulls skipped, the first error naming the row and column — and its
own doc says *"This is a generic METHOD, which is what lets it chain."* What was
missing was the lazy counterpart. The new error message deliberately matches it.

---

## 2. The levels wall, and why the erasure is two steps

The obvious field does not compile:

```go
Impl func(*data.Column) (*data.Column, error)   // illegal
```

`internal/gen/levels` puts **`internal/data` and `internal/expr` at the same level
(20)** and the rule is strictly-lower, so `expr` cannot import `data`. Nor can that
be fixed by renumbering: `internal/plan` imports `expr`, and
`TestPlanPackageNeedsNoArrow` asserts `plan` has **zero** transitive dependency on
`data`, `bitmap`, `arrowx` or anything named arrow.

So there are two indirections, not one:

```go
// internal/expr (20) — declares the marker, sees no columns
type UDFImpl interface{ UDFKernel() }

// internal/kernel (30) — may import data
type ColumnUDF func(context.Context, *data.Column, string) (*data.Column, error)
func (ColumnUDF) UDFKernel() {}
```

**The marker cannot be sealed, and the plan said it could.** `Node` is sealed with
an unexported `node()` because every implementation lives in `expr`. That technique
is unavailable here for precisely the reason the interface exists: an unexported
method can only be implemented inside its own package, and this one *must* be
implemented outside. So `UDFKernel` is exported, and `evalUDF` asserts for the one
concrete type it knows and reports anything else as an internal error — the
containment the seal would have given.

`make levels` passing is what makes this design provably necessary rather than
merely defensible.

---

## 3. Two sites that fail LOUD, and everything else fails closed

The optimizer needed **no new guards for opacity**. Every rule that inspects node
types already refuses what it does not recognise: predicate substitution's default
arm (*"Refuse rather than guess"*), constant folding's **allow-list**
(`Lit`/`Binary`/`Unary`/`Cast` only), `ClassifyPredicates` where `Unsupported` is the
zero value, `equiKeys` requiring a `*Binary`. Projection pushdown needs nothing
because `RootNames` rides `Walk`, which recurses through the `Children()` *interface
method* — the type-switch defect step 48 found in `Expressions()` has no analogue
here.

**Two sites are not fail-closed, and both are reachable by ordinary queries:**

- **`expr.Rebuild` PANICS** on an unknown node. It is called by `substitute` during
  multi-column expansion and by `simplify.walk` on **every optimizer pass**. Without
  an arm, `Col("a","b").MapElements(...)` crashes — the tooth reproduces it exactly.
- **`extractAggs` errors.** Without an arm, a UDF anywhere inside `Agg()` fails with
  `unhandled node *expr.UDF`.

**`design/logical.md:1485` predicted the wrong hazard**, and the prediction is now
retired. It says dead-column elimination is *"safe in v0.1 because no v0.1
expression has side effects; v0.2 UDFs will carry a purity flag consulted here."*
That rule was never built — `pushdown`'s `*Project` arm narrows what the **child**
supplies and never drops an expression. No purity flag is needed for the reason
given, and none was added.

---

## 4. The golden plan found the real hazard, which no result test could

The plan's §5 claimed four rules needed nothing. That was wrong, and the **golden
plan** is what showed it:

```
 WITH_COLUMNS [... col("s").map_batches("widths" -> Int64).alias("w")]
-  FILTER [(col("s").map_batches("widths" -> Int64) > lit(1))]      <- ran TWICE
+FILTER [(col("w") > lit(1))]
+  WITH_COLUMNS [...]
```

Predicate pushdown rewrites a filter into its child's namespace by **substituting**
the column's definition — which *copies* it. `substitutable`'s type switch cannot
stop that, because the UDF arrives through `defs[name]` as a **replacement** and is
never walked; the default arm is not reached.

And for a UDF the rewrite **can only lose, arithmetically**. Pushing `Filter(w > 1)`
below the `WithColumns` that defines `w` runs the function on every input row inside
the filter, and the `WithColumns` still runs it on every survivor to produce the
column: `n + survivors` calls where not pushing costs `n`. There is no input for
which it wins, and the function may be arbitrarily expensive.

So `expr.HasUDF` is new, and `substitutable` consults it before substituting. The
answer is identical either way — which is exactly why only a plan snapshot, or a
test that **counts calls**, could see it. Both are checked in.

---

## 5. The name is required, and it is a correctness argument

`Node.String()` is *"load-bearing: it appears in Explain output, in golden test
files, and in error messages"* — and worse than that, **three separate maps
deduplicate expressions on their rendering**: `resolveWindow`'s temporaries,
`extractAggs`' `byKey`, and window partition sharing. `names_test.go` states the
consequence: *"two expressions that RENDER THE SAME become one computation, and both
names get the first one's answer."*

A Go closure has no printable identity. The tooth is unambiguous — with the name
removed from `String()`, two different functions over one column in one `Agg` give:

```
doubled = 20, tripled = 20     (want 20 and 30)
```

That is the shape of two bugs this project has already shipped and fixed: an
unparameterised `Quantile` collapsing p50 and p99, and weak-versus-strong literals.
A monotonic counter would deduplicate correctly too and would make every golden plan
depend on allocation order. So the caller supplies a name, which
`ursus-api.md`'s own `RegisterScalarUDF[In, Out](name string, …)` had already
anticipated.

---

## 6. The evaluator checks the claims

A UDF is the first node where **the user supplies both sides** of `Eval`'s contract,
`Eval(ctx, n, b).DType() == n.Field(b.Schema()).Type`, whose own doc says a
violation means *"the promise is a lie and the failure surfaces far downstream as
corrupted output."*

| claim | where it is checked |
| --- | --- |
| the declared output type | `buildTyped`, on the physical layout |
| the row count | `evalUDF`, ahead of the operator boundary |

**The row-count check has to be in `evalUDF`.** `evalColumn` reports a length
mismatch as `Internalf` — an ursus bug rather than the user's — and **silently
broadcasts** a length-1 result, which is right for a literal and for a UDF is a bug
being papered over.

**Nullability is not among them, and that is a decision.** Every other node derives
nullability from something the engine can see; a UDF cannot, and step 49 established
that a false non-null declaration is no longer cosmetic — `plan.sameValue` gates a
fold on it and the Parquet struct reader skips validity tracking on it. So **the one
claim the engine cannot verify is the one it does not accept**: `UDF.Field` declares
every UDF nullable and there is no way to say otherwise. A caller who genuinely
knows better says so with a strict `Cast`, which *is* checked.

That is a change from the plan, made because the tooth demanded it — §8.

---

## 7. Physical layout, not Go type, is what makes an output type legal

`valuesWith` derives a dtype from the Go type, which is right for `Values()` and
wrong here: a UDF may legitimately return `[]int64` and declare
`Datetime(Second, "UTC")`, because that is the type's storage. So `buildTyped`
builds the natural column and reinterprets it, permitting that only when the
**physical** layouts agree.

The check earns its place in both directions. It admits `int64 → Datetime` (shipped
test) and refuses `float64 → Decimal(10,2)`, whose storage is `Int128`, where the
values would be reinterpreted bits.

**Enum needed more than the layout.** Its categories live in the `DataType`, so
`WithDType` is structurally complete — but its *values are indices*, and a UDF
returning 999 against three categories would produce an out-of-range column nothing
downstream would catch. `buildTyped` range-checks it.

**A new primitive: `Column.FixedSlice()`.** `data.Values[T Fixed]` is the zero-copy
reader, but a caller constrained to `Literal` — which also admits `string` and
`bool` — cannot instantiate it. Without a way through, `udfInput` would fall back to
`Series.Get`, which is `any(vals[i]).(T)`: **an interface boxing per row**, giving
up most of the "a function call, not a Python round trip" claim the feature rests
on. `FixedSlice` returns the payload as an `any` holding a typed slice, so
`raw.([]In)` is one assertion per batch and no per-row work.

---

## 8. Teeth

| tooth | result |
| --- | --- |
| drop the `Rebuild` arm | **bites** — `panic: expr: Rebuild: unhandled node type` |
| drop the `extractAggs` arm | **bites** — `unhandled node *expr.UDF` |
| render without the name | **bites** — `doubled = 20, tripled = 20` |
| drop `buildTyped`'s physical-layout check | **bites**, on both the Decimal and the Int64-as-Float64 case |
| drop both row-count checks | **bites** |
| allow a UDF to be substituted into a predicate | **bites** — the golden, and the call count |
| **check a declared non-null output in `evalUDF`** | **does not bite** — §6 |

The last one is the finding. `Nullable` was always `true` at every public entry
point, so `!u.Nullable && …` was **unreachable**: a guard that reads as protection
and provides none. Step 48 made exactly this criticism of `IsAllSet` sitting as dead
code, so the check and the field went, and the decision they encoded is now stated
in `UDF.Field` where a reader will find it.

---

## 9. Concurrency, stated rather than solved

`parallelise` decides **structurally** — a run of `*stage` over a pullable base gets
`runtime.NumCPU()` workers, and `Filter` and `Project` both become `*stage`. So
**any UDF in a Select or Filter is handed to N goroutines, and there is no
operator-level flag to decline**: `BatchOp`'s contract is *"stateless,
order-independent… Every operator that CAN be a BatchOp MUST be one."*

The doc says so where a user will see it. `TestUDFIsCalledConcurrently` runs a
thousand rows at four threads and a batch size of 16 under `-race`, and asserts the
one thing that is guaranteed: output order is preserved even though invocation order
is not.

A serial opt-out — the analogue of `AggOp.IsOrderDependent`, consumed to decline
parallel aggregation — is the follow-up, not smuggled in here.

---

## 10. Verification

- Both methods end to end, with the type changing across the call in each.
- The two null contracts, which differ deliberately: `MapElements` called exactly 3
  times on 4 rows with one null; `MapBatches` turning a value into a null and a nil
  mask declaring every row valid.
- Four false declarations: wrong type, wrong row count (too few and too many),
  incompatible storage, wrong `In` type.
- The user's own error unwrapped with `errors.Is`, naming the row and the column.
- Batch sizes {1, 2, 3, 7, 8192} × threads {1, 2, 4}: identical frames, and a
  failing UDF that fails the same way regardless of how the query ran.
- A UDF under an aggregate and over one — and `Sum` widening `Int64` to `Int128`,
  which no `Literal` type names, is why the test needs a cast to reach a UDF at all.
- A UDF surviving a join and a filter, with projection pushdown on and off.
- `Explain` must not run the function.
- Two golden plans, raw and optimized.

---

## 11. What is still open

- **`MapGroups`, `RollingMap`, `CumulativeEval`, `MapBatchesFrame`** — they change
  the *plan* shape rather than an expression, needing a new node and a declared
  output **schema** that `Schema()` can return without running anything.
- **A serial opt-out** for a closure that is not goroutine-safe — §9.
- **`In` is restricted to what has a typed view.** `Literal` admits `time.Time`,
  which `accessor[T]` has no arm for, so a UDF over a Datetime column must read it
  as `int64` ticks. The restriction is a clean runtime error, not a compile-time one.
- **`TestEvaluatorContract` does not exist**, though `eval.go`'s doc claims it
  *"asserts it across the whole op × dtype matrix."* Verified: the only occurrence
  in the repository is the comment. The contract a UDF is most likely to violate had
  no automated backstop before this step, and now has one only for UDFs.
- **The nullability fast path**, surveyed this step and worth recording: the
  data-keyed `IsAllSet()` check is structurally better than a schema-keyed one, and
  the reason it does not pay today is that **producers destroy the O(1) form** — the
  Parquet `maxDef == 0` arm builds an all-ones bitmap after having already *proven*
  no nulls exist, and `Builder.Finish` never returns the no-storage form.
- **`sortSink.Merge` does no memory accounting**, the same hole `reverseSink`'s just
  had fixed; latent for the same reason, that nothing calls it.
- The standing list: the parallel join build, spilling ⊥ parallelism in `aggWorkers`,
  CSV range-splitting, `.list` set operations, `Str().Join()`, `Expr`-level
  selection, **a suite re-run (thirteen commits unpublished)**, inline keys for
  `KeyTable`, the heap sampler, `quantile`/`median` storage, nested writing,
  Map/Array, SQL, cloud stores, join reordering.
