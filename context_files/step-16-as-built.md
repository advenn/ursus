# Step 16 — as built

The optimizer's second half. **`SimplifyExprs` is implemented, and two of the
things the design doc said about it are wrong.**

Authoritative where it disagrees with [`step-15-as-built.md`](./step-15-as-built.md),
the vision docs and [`design/`](./design/).

**1299 test cases green** — 466 top-level and 833 subtests (1162 before this step) —
under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment off,
and under `-race`. `make levels` and `go vet` clean.

203 files, ~58.5k lines. No new `LazyFrame` operations: this step is entirely
behind the existing API.

```
- PROJECT [col("id"), col("price")]     <- gone: the scan below already reads exactly this
    FILTER [(col("price") > lit(5))]
      MEMORY SCAN 6 rows
        projection: [id, price] (2/6 cols)

  PROJECT [lit(0.30000000000000004).alias("k")]   <- 0.1 + 0.2, through the REAL kernel
  LIMIT 0                                        <- Filter(lit(null)), which keeps NOTHING
```

`design/logical.md` §12 listed four deferred optimizer passes — *"predicate / slice
pushdown, CSE, constant folding, type-coercion casts"*. Three of the four have now
shipped; CSE is the only one left, and it needs a cost model.

---

## 1. One guard, and it is the whole design

Every rewrite here is accepted only if the replacement resolves to the same
`dtype.Field` — Name, Type **and** Nullable — as the thing it replaces. That single
check is what makes the rest of the step safe, because three rewrites that look
obviously sound are not:

| Rewrite | What the guard sees |
| --- | --- |
| `x AND lit(false)` → `lit(false)` | value-sound under Kleene logic (`null AND false = false`) but **schema-unsound**: `Binary.Field` computes `nullable := lf.Nullable \|\| rf.Nullable` with no absorbing-element case, so the original is nullable and the replacement is not |
| `Not(Not(Col("a").Alias("b")))` → the child | `OutputName` consults naming nodes at the **root only**, so the doubled negation is named `"a"` by leftmost-column-wins and the bare child is named `"b"` |
| a fold whose literal cannot round-trip | `Lit.Field` reports the literal's own dtype; a Decimal's scale or a Datetime's unit that does not survive `litColumn` shows up as a type mismatch |

**The guard is better than a blanket refusal, and that is the interesting part.**
The first draft of this step's plan listed `x AND false` as sound; the corrected
version listed it as refused. Both were wrong. It is sound exactly when `x` is
non-nullable, and the guard decides per expression — so `sure AND false` folds and
`maybe AND false` does not, from one rule with no special case.

`Optimizer.Verify` cannot do this job: it compares the **root** schema, and an
expression buried in a `Filter` predicate never reaches it.

### Two guards at two levels, because the levels ask different questions

- `sameValue` (Type + Nullable) runs at **every expression node**. An interior
  node's name is not an output name, so checking it there would decline
  `x AND true` → `x` inside a Filter predicate, where nothing reads the name.
- `fieldEqual` (Name too) runs **once per installed expression**, and only where
  names become column names — `Project`, `WithColumns`, `Aggregate`.

`Local` adds a third at the plan-node level: a `Match` is accepted only if the
node's `Schema()` is unchanged. That one is a backstop rather than the guard, and
the difference is **blast radius**: it can only decline the whole `Match`, so
without `fieldEqual` one unsound rewrite inside a `Project` throws away every sound
rewrite beside it. `TestRefusalIsPerExpressionNotPerNode` is that distinction —
remove `fieldEqual` and it does not fail loudly, it quietly stops optimizing.

---

## 2. Folding is injected, so `internal/plan` still has no Arrow

Constant folding must not compute the answer a second way. `0.1 + 0.2` has to fold
to exactly what the executor produces or a query mixing folded and unfolded values
disagrees with itself — so folding runs `physical.Eval` over a one-row, zero-column
batch, the same evaluator the query would have used.

But `Eval` is L50 with Arrow, the allocator and `GOEXPERIMENT=simd` behind it, and
`design/logical.md` §13 claims a plan can be built, resolved, optimized and
explained *"without an executor, without Arrow, and without a Parquet file"*.
Importing `kernel` from L40 to fold two literals retires that for the whole layer.

So `plan.ConstEvaluator` is an interface naming no Arrow type,
`physical.KernelFolder` implements it, and `lazy.go` — which already imported
`internal/physical` — wires it in three lines. With no folder attached, which is
what `internal/plan`'s own tests see, every structural rewrite still runs.

`TestPlanPackageNeedsNoArrow` asserts it rather than trusting it: add
`_ "ursus/internal/kernel"` to the package and it names nine dependencies that
appear, from `ursus/internal/data` to `arrow-go/v18/arrow/memory`.

**Not `simd/archsimd`.** The obvious version of that test fails immediately,
because under `GOEXPERIMENT=simd` the standard library pulls it in at every level
— `internal/uerr` at L0 has it too — so its presence says nothing about a
package's own imports.

---

## 3. Two things `design/logical.md` gets wrong

### It needs `Once`, not `UntilStable`

§8.1 offers constant folding as *the* example of a rule needing the fixed-point
driver, and this step began by registering it that way. It does not need it.

Both walks are bottom-up — `TransformUp` over plan nodes, `simplify.walk` over
expressions — so a child is always rewritten before the parent that could exploit
it, and the enabling chain runs downward: `lit(2) > lit(1)` → `lit(true)` →
`x AND lit(true)` → `x` → a Filter with a literal predicate → the node goes. Every
step of that is a parent consuming an already-rewritten child, so one sweep
completes it. Registered `UntilStable`, the second pass changed nothing on every
query in the suite; registered `Once`, the whole suite passes.

Six stacked negations collapse completely in one pass, and
`TestSimplifyReachesItsFixedPointInOnePass` is a proof rather than an illustration
*because* the rule is registered `Once` — a leftover would still be in the plan
text.

**So the `UntilStable` machinery is exercised directly instead.** `Optimizer.rules`
is unexported with no `Register`, so this lives in an in-package test file rather
than behind a public seam whose only consumer would be a test.
`TestOscillatingRuleIsCaught` reaches the `maxIterations` error path that had never
executed since step 2 — and asserts the rule ran *exactly* 16 times, because a
bound nobody has reached is a bound nobody has checked the sign of.

### Division by zero is not the folding hazard

The intuitive example of "do not fold, it might error" is `1/0`, and it is wrong
twice over. Float division by zero is `+Inf` and integer division by zero is a
**null** — both the kernel's documented answers, neither an error. The first folds,
correctly, to `lit(+Inf)`; declining it out of caution would be wrong in a quieter
way. The second is declined by the **nullability** guard instead, because
`Binary.Field` says two non-null operands give a non-null result.

The operation that actually errors is a strict cast that cannot represent its value
(`Lit(300).Cast(Int8)`). And the cost of folding it is not what the plan claimed
either: a `Cond` does **not** protect its untaken arm here — `evalCond` computes
both branches and selects between them, so `When(c).Then(a).Otherwise(bad)` fails
whether or not `c` is ever false, with this rule on, with it off, and before this
step existed. What folding would genuinely break is `Explain`, which resolves and
optimizes without evaluating anything and must still work on a query that only
fails when run.

---

## 4. The dead filter, and the one silent wrong answer available here

A predicate has **three** outcomes and only `true` keeps a row:

| | |
| --- | --- |
| `Filter(lit(true))` | drop the node — a true predicate constrains nothing |
| `Filter(lit(false))` | `Limit{N: 0}` |
| `Filter(lit(null))` | `Limit{N: 0}` — a null predicate is not true |

"Remove the Filter when its predicate is constant" is right for the first and
catastrophic for the other two: it returns **every** row where none belong, with an
identical schema and no error anywhere. Reintroduced, `TestDeadFilterReturnsNoRows`
gives **6 rows where 0 belong**.

`Limit{N: 0}` is not an invention. `rule_limit.go` already calls it *"a legal,
degenerate query"* and notes *"the Limit operator above already returns nothing at
no cost"* — and the same comment is why `limitPushdown` refuses to push an `N` of 0:
`MaxRows` and `Sort.Limit` use 0 for **unlimited**, so pushing it would say the
exact opposite of what it means.

---

## 5. Identity `Project` removal, and the test it defanged

A `Project` whose expressions are exactly its child's columns in order is removed.
It is `design/logical.md:1557`'s worked example, and it is 100% of this step's
golden churn — five plans, because after projection pushdown narrows the scan the
`Project` above it becomes a no-op:

```
- PROJECT [col("id"), col("price")]
  LIMIT 3
    SORT [col("price") DESC NULLS FIRST] top 3
      MEMORY SCAN 6 rows
        projection: [id, price] (2/6 cols)
```

`ursus_test.go`'s `TestProjectionReachesTheScan` — *"the architectural claim.
Without it the lazy plan is decoration"* — loses its `PROJECT` line. The claim
itself is the last line and is untouched; the node that used to carry the
projection is now redundant precisely **because** it reached the scan.

**And `TestPushdownSoundness` went vacuous, which is the finding worth keeping.**
Its case 0 is `Project([id], Scan(wide))`. That is not an identity projection as
written — but projection pushdown narrows the scan to `[id]`, and *then* it is, so
the Project is removed, no expression remains anywhere in the tree, the required
set is empty and the assertion loop never executes. It passed by having nothing
left to check.

Both halves of the repair matter. The flag is pinned off, because this is a
property of **pushdown** and it needs each case's shape intact to have a property
to test. And a vacuity guard counts the assertions **per plan** — a total across
all four stays comfortably above zero while case 0 quietly contributes nothing,
which is exactly the failure being guarded against.

---

## 6. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean.

Seven teeth checks fired, each by reintroducing the defect:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| `lit(null)` predicate treated like `lit(true)` | `TestDeadFilterReturnsNoRows` | **6 rows where 0 belong** |
| Drop the `sameValue` (nullability) guard | `TestBooleanIdentitiesUnderNulls` | `maybe AND false` collapsed a nullable column to `LIMIT 0` |
| Drop the `fieldEqual` (name) guard | `TestRefusalIsPerExpressionNotPerNode` | the sound rewrite beside the refused one was discarded too |
| `simplify.walk` applies the node before its children | `TestSimplifyReachesItsFixedPointInOnePass` | two of six negations survived |
| Fold on kernel error | `TestFoldingRefusesOnError` | nil-pointer **panic** — see below |
| `internal/plan` imports `internal/kernel` | `TestPlanPackageNeedsNoArrow` | nine new dependencies, `arrow-go/.../memory` among them |
| Remove the `maxIterations` bound | `TestOscillatingRuleIsCaught` | the oscillating rule ran forever |

**Two did not fire, and both are recorded rather than dressed up.**

`Local` applying **top-down** instead of bottom-up breaks nothing any test can see.
The bottom-up property that matters is `simplify.walk`'s, over expressions — the
interesting nesting lives inside one node's expression list, not across plan nodes.

`isIdentityProject` weakened to an **order-blind set comparison** also breaks
nothing, because `Local`'s `sameSchema` declines any rewrite that reorders the
output. The positional test is written precisely because saying what is meant is
cheaper than relying on a backstop — but it is a clarification, not the guard.

And the error refusal in `KernelFolder.Fold` is load-bearing against a **crash**
rather than a wrong answer: `Eval` returns a nil column with its error, so removing
the check dereferences nil. There is no value to fold to, which is why the refusal
is unconditional rather than a judgement call.

---

## 7. Honest gaps

- **Simplification runs once, at the end.** A `Filter` that folds away to nothing
  no longer prunes the work the pushdowns did above it. Missed optimization, not a
  wrong answer; closing it means registering the rule a second time at the front.
- **Only four expression slots are simplified** — `Project`, `Filter`,
  `WithColumns`, `Aggregate`. Sort keys and join keys hold columns in practice.
- **Folding accepts a narrow type set**: Bool, String, the integers and the floats,
  plus typed nulls. Temporal, Decimal, Int128 and Binary are refused because
  `litColumn`'s reconstruction is lossy or unit-blind for them — `time.Time` is
  rebuilt via `UnixNano` regardless of the literal's declared unit.
- **`Cond` and `Call` are not folded.** `Cond` evaluates both arms anyway (§3), so
  folding it would be sound but pointless; `Call` ranges from pure string functions
  to regexes with their own error paths.
- **CSE** is the last of §12's four passes and needs a cost model.
- **`reverseSink.Merge` has no test**, found while correcting the README's
  `Sink.Merge` count. There are seven `Merge` methods, not five; three are refusals
  (`windowSink`, `temporalSink`, `asOfBuildSink`); of the four real ones
  `reverseSink` is untested, and it is the one whose semantics are PREPEND rather
  than append.
- `JoinWhere`, the `Expr.Rolling*` family, `Upsample`/`DateRange`, `Interpolate`,
  the trig and bit-count blocks, the twelve missing aggregates, parallel
  aggregation, `SetSorted`/`IsSorted` and `Duration` in Parquet are unchanged from
  step 15.

---

## 8. Files

**New:** `internal/plan/rule_simplify.go` (443), `internal/plan/local.go` (148),
`internal/physical/fold.go` (133). Tests: `internal/plan/simplify_test.go` (352),
`internal/plan/optimize_internal_test.go` (103), `simplify_test.go` (305).

| File | Change |
| --- | --- |
| `internal/plan/optimize.go` | `ConstEvaluator`, `SetConstEvaluator`, the `constFolder` holder; `simplify` registered last and `Once`; `SimplifyExprs` defaults **true** |
| `lazy.go` | `optimizer()` injects `physical.KernelFolder{}` |
| `internal/plan/plan_test.go` | `optimizeWith`; `TestPushdownSoundness` pins the flag off and gains a per-plan vacuity guard |
| `ursus_test.go` | `TestProjectionReachesTheScan` loses its `PROJECT` line |
| `testdata/plans/` | five goldens lose an identity `PROJECT` |
| `context_files/README.md` | indexes `design/`; corrects the `Sink.Merge` count |
