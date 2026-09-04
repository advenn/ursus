# Step 7 — as built

Conditionals, aggregate breadth, and `IsIn`. Authoritative where it disagrees with
[`step-6-as-built.md`](./step-6-as-built.md) and the vision docs.

**647 tests green** (567 before this step) under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean.

```go
df, err := sales.
    Select(
        ursus.Col("id"),
        ursus.When(ursus.Col("score").Ge(90)).Then("A").
            When(ursus.Col("score").Ge(80)).Then("B").
            Otherwise("F").Alias("grade"),
        ursus.Coalesce(ursus.Col("discount"), ursus.Lit(0.0)).Alias("disc"),
    ).
    Filter(ursus.Col("region").IsIn("eu", "us")).
    Collect(ctx)

df, err := sales.GroupBy(ursus.Col("region")).Agg(
    ursus.Col("latency").Quantile(0.99, ursus.InterpLinear).Alias("p99"),
    ursus.Col("latency").Std(1).Alias("sd"),
).Collect(ctx)
```

---

## 1. Why this step and not one of the four queued ones

The four remaining v0.2 items — window functions, as-of join, streaming with
spilling, external sort — are all named in `dataframe-features.md` §14. Checking the
expression IR against the same document's **priority key** turned up two gaps that
no milestone line owns, which is exactly how they stayed unbuilt:

- **There was no conditional expression at all.** The IR held `Col`, `Lit`, `Binary`,
  `Unary`, `Alias`, `Cast`, `Err`, `Match`, `Agg`, `Call`, `Rename` — and nothing
  else. `CASE WHEN` was inexpressible. §4.6 of `ursus-api.md` specifies it; §4.7
  specifies `Coalesce`.
- **9 of the 31 P0/P1 aggregates existed.** No `std`, `var`, `median`, `quantile`,
  `product`, `arg_min`, `arg_max`, `any`, `all`, `null_count` — a dataframe library
  that could not compute a median or a p99.

`IsIn` came along because §5.2 is P0 and it is the most-used predicate after
comparison.

## 2. The conditional could not be sugar, and that was worth finding out

`IsBetween` is the precedent, and it is **two lines**: `e.Ge(lo).And(e.Le(hi))`. It
earns `Field`, evaluation, expansion, pushdown *and* Parquet row-group pruning free,
because the pruner recurses through `OpAnd`. Trying the same for a conditional
failed for four reasons, all properties of this codebase rather than of conditionals:

1. **There was no masked-select kernel.** Nothing in `kernel` took `(mask, a, b)`.
   Sugar has to desugar *into* something.
2. **The arithmetic dodge does not type-check.** `cond*a + !cond*b` needs Bool to be
   numeric, and `dtype.Promote` refuses on purpose — *"true + true is rejected"*.
3. **There is no CSE pass**, so a desugaring naming an operand twice evaluates it
   twice.
4. `String()` would be an unreadable nest — and it is load-bearing (§6).

So `expr.Cond{Pred, Then, Else}` plus `kernel.Select`. **Three children rather than
parallel `[]Pred`/`[]Then` slices**, because `dtype.Promote` is strictly pairwise and
a left fold is not safe to invent: its mixed-sign rule makes the result **depend on
association order** for operand sets like `(Uint8, Int8, Uint32)`. The builder nests
right, matching the order the user wrote.

### `Coalesce` needed no new node

It desugars to `Cond{a.IsNotNull(), a, Coalesce(rest…)}`. The honest cost is that
each operand is *mentioned* twice and there is no CSE, so each is *evaluated* twice.
For `Coalesce(Col("a"), Col("b"))` that is two column reads. A dedicated n-ary kernel
would remove it and change no semantics; it is not built, and the doc comment says so
rather than implying the desugaring is free.

## 3. What a new IR node actually costs, measured

`Node` is sealed, so every type switch over it has a `default` — and they do not all
fail the same way. For a conditional:

| Site | Behaviour | Outcome |
| --- | --- | --- |
| `expr/expand.go` `substitute` | **panics** | arm added — and it is not theoretical, `Coalesce(All(), Lit(0))` reaches it |
| `physical/agg.go` `extractAggs` | internal error | arm added — conditional aggregation is how a pivot is written |
| `physical/eval.go` `Eval` | internal error | arm added — the kernel call |
| `expr/node.go` `OutputName` | leftmost-column-wins | **explicit case**: would have named the result after the *condition's* column |
| `plan/rule_predicate.go` ×2 | fail closed | **opened** |
| `expr/agg.go` `IsAggregation` | generic over children | correct already |
| `expr.HasAgg` → `rejectAggregate` | plain `Walk` | correct already — `Select(When(c).Then(x.Sum()))` is rejected with the right message, free |
| `RootNames`, `FirstErr`, `HasMatch`, `BareColumns` | generic | free |
| `parquet/prune.go` | fail closed | correct — reads more row groups, never fewer |

**Three mandatory arms, two explicit cases, one deliberate opening.** The rest is free
because `Children()` returns every operand — the property `expr.Call`'s doc predicted.

### The optimizer opening was wider than planned, on purpose

`substitutable` and `rewriteForSide` also refused **`*expr.Call`**, which step 6 left
behind: every `.str`/`.dt` predicate was silently unpushable. Both are elementwise, so
both are now opened. The golden plan shows the result — `Filter(Col("id").IsIn(…))`
sits **below** the `PROJECT` it could not previously cross.

## 4. `Agg` grew parameters, and `String()` was a correctness bug

`Std(ddof)`, `Var(ddof)` and `Quantile(q, interp)` carry arguments and `Agg{Op, Child}`
had nowhere to put them. Step 6 answered this for scalar functions with
`Call{Fn, Args []Node}`; **here the answer is deliberately different.** `Call`'s doc
says a column-valued argument *"is representable in this IR and simply not implemented
yet, which is the right way round for a constraint that may lift later"* — true of a
regex pattern, false of a ddof. Those are configuration, never operands. So a typed
`AggParams` struct, and `Children()` stays `[Child]`.

**The load-bearing part is `String()`.** `physical/agg.go` deduplicates inner
aggregates by keying a map on it:

```go
case *expr.Agg:
    key := t.String()
```

`Agg.String()` rendered `child.op()` with the arguments invisible. So
`Agg(x.Quantile(0.5), x.Quantile(0.99))` produced `"x.quantile()"` **twice**, collapsed
into one temporary, and returned **the median under both names** — no error, no
warning. Verified by reintroduction: restoring the old `String()` makes p50 and p90
both report 2.5, and `Var(1)` report the population variance.

## 5. The ten aggregates

| Aggregate | How | Decision worth knowing |
| --- | --- | --- |
| `NullCount` | third mode on `countAcc` | `count + null_count == len`, tested as an identity |
| `Any` / `AllTrue` | **`extremumAcc` unchanged** | on Bool, `false < true`, so max *is* any and min *is* all. Separate ops rather than sugar so Explain says `any()`. Named `AllTrue` because `All()` is already the column selector |
| `Var` / `Std` | Welford + Chan merge | not sum-of-squares — see below |
| `Product` | new | **Float64 even for integers**; the one place it is weaker than `Sum` |
| `ArgMin` / `ArgMax` | new | position *within the group*, `Uint64`, nulls occupy a position but cannot win, ties to the earliest |
| `Median` / `Quantile` | new | the only genuinely new *shape* |

**Welford, not E[x²]−E[x]².** Over `[1e9, 1e9+1, 1e9+2]` the textbook formula subtracts
two nearly equal numbers around 1e18, where float64 spacing is ~256, and returns 0 or
garbage. The true sample variance is 1. `TestStdVarPrecision` pins it.

**Merge is Chan's parallel formula, not addition.** Two partial `(n, mean, M2)` triples
measured their deviations from *different* means; the correction term is
`δ²·nA·nB/(nA+nB)`. Dropping it moves the answer by 0.13% on the test fixture —
invisible single-threaded, wrong under every partition.

**`Product` returns Float64 for integer input, and this is a real compromise.** `Sum`
widened to Int128 so overflow became unreachable; `i128` has no multiplication, so
product cannot follow. Float64 is exact to 2^53 and approximate above it. The
alternative, Int64, *wraps* after about twenty ordinary factors. An approximate large
answer beats a precise wrong one — but it is a weaker guarantee than sum's and the doc
comment says so.

**Median and Quantile change the aggregate sink's memory story.** They are holistic:
the accumulator retains every non-null value, so its memory is O(rows), not O(groups).
`hashAggSink` was the *only* pipeline breaker whose memory was O(distinct keys);
`sortSink` and `joinBuildSink` both retain their input and say so. It now joins them
**for queries that use it**. Documented on the accumulator rather than discovered
later; bounding it is the streaming-and-spilling step's job.

## 6. `IsIn` needed no new IR

`expr.Call` already carried it. `CallArgs` requires each `Args[1:]` to be a `*Lit` but
places **no constraint on `Lit.Value`'s Go type**, and `evalCall` never routes those
through `litColumn`. So N values as N literal args worked unchanged.

It is the **third `CallFn` family**. The existing two classify by what the receiver
*is* — a String, an instant — and `IsIn` applies to anything hashable, so it needed its
own `IsGeneral()` classifier rather than widening either.

**The signature diverges from `ursus-api.md:319` (`IsIn(other Expr)`) because that
cannot be written**: `Literal`'s only slice term is `~[]byte`, so a list-valued literal
is unspellable. The variadic generic form is what `design/logical.md:1842` actually
sketches. The `Expr`-valued form is a semi join wearing a predicate's clothes and
belongs with the join machinery.

**Membership uses grouping equality, not IEEE.** The probe set is encoded through the
same `kernel.NewGroupKeyEncoder` that `n_unique`, `distinct` and the join use, so
**NaN matches NaN and −0.0 matches +0.0**. An `IsIn` built on `==` would disagree with
`Distinct` about the same values — the trap `nuniqueAcc` records for `n_unique`. The
test cross-checks against `Unique()` on the same column. Teeth: replacing the shared
encoder with a plausible second one makes every match vanish.

`null.IsIn(…)` is **null**, so `IsIn` and its negation do not partition — the same
property comparison has. The variadic form also sidesteps a question with no spec
answer anywhere in the repo: what a *null inside the set* means. `T Literal` cannot be
instantiated with nil, so the case cannot arise.

## 7. Three defects found in passing

1. **`kernel.Cast` refused every cast from `Null`.** `CanCast`'s first line is
   `if from == to || from.IsNull()`, so it promised Null→anything; the kernel answered
   *"cast from Null to Int64 is not implemented yet"* at execution. The same
   plan-accepts/kernel-rejects divergence step 6 fixed for strings, one type further
   along. Found because `Otherwise(Null(NullT))` promotes to the other branch's type
   and then has to get there. Fixed via `NullColumn` — **not** a relabel, because
   `data.NewNull` has no payload buffer and a relabelled column fails in the first
   kernel that reads one.
2. **`TestMergeEquivalence` could not fail for most types.** Its `renderRow` returned
   `"?"` for anything outside `{float64, uint64, Int128}`, so a Bool, Int64, Uint32 or
   String result compared `"?"` against `"?"`. Proven by reintroduction: with a
   genuinely wrong `First`, the two-partition string case **passed**. `renderRow` now
   errors on a type it does not know, and `TestMergeEquivalenceOverStrings` is the
   coverage that was hiding behind it.
3. **The merge test only ever folded left-to-right.** Its own doc comment promised a
   reverse pass that was not in the code. Both associations now run — every `Merge`
   still has `other` holding the later slice, but the receiver is a growing *suffix*
   rather than a growing prefix. That is what catches `arg_min`'s position shift.

## 8. One test-harness decision worth recording

Float comparisons in `TestMergeEquivalence` now use a **1e-12 relative tolerance**;
everything else stays exact. Floating-point addition is not associative, so a merged
partial tree legitimately rounds differently from a single pass — `var()` differs in
the last ULP. The existing aggregates passed an exact comparison only because the
fixture is `float64(rng.IntN(20))`: sums and means of small whole numbers are exact, so
the question never arose.

The tolerance is ~1e4 ULP: far above rounding noise, and nine orders of magnitude below
the 0.13% error a broken Chan merge produces. Both were checked by reintroduction.

## 9. Honest gaps

- **`IsUnique`, `IsDuplicated`, `IsFirstDistinct`, `IsLastDistinct` are deliberately
  absent.** They are not elementwise. `projectOp` is a `BatchOp`, contractually
  *"stateless, order-independent"*, and `IsUnique` must see every row before answering
  for row 0 — so at any batch size below the input size it would return `true` for a
  value that reappears later, and `WithBatchSize(1)` would make everything unique.
  That violates `data/batch.go`'s *"batch size is a tuning knob rather than a semantic
  one"*, which six tests enforce. They are the same shape as window functions and wait
  for that step. `IsFirstDistinct` is the near-miss: `distinctOp.Next` already computes
  exactly its bit, but `distinctOp` is an `Operator`, not a `BatchOp`.
- **`IsClose`** needs an `Expr` operand plus two float parameters, which `Call`'s
  literals-only rule does not admit.
- **`dtype.Promote` still refuses two different temporal types.** `When(c).Then(dateCol)
  .Otherwise(datetimeCol)` is refused at plan time with a hint naming the cast, rather
  than widening a rule every binary operator shares.
- **No approximate quantile.** Nothing in `context_files/` discusses one, and choosing
  an error bound is a decision to make deliberately.
- **`IsBetween` still has no `Closed` parameter**, diverging from `ursus-api.md:318`.
- **The `GroupBy` shorthands** (`g.Mean()`, `g.Median()`, `g.Quantile(…)`) — eleven are
  specified, only `Count()` exists. Sugar over `Agg`.
- Still open: window functions, as-of join, streaming/spilling, external sort, List
  columns.

## 10. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
```

| Test | What it would otherwise miss |
| --- | --- |
| `TestSelectNullMaskTakesNeitherBranch` | a null condition falling through to `Otherwise` — the likeliest silent wrong answer in the step |
| `TestSelectIgnoresMaskPayloadUnderANull` | Arrow does not zero a null lane's payload bit |
| `TestSelectBroadcastsLengthOneBranches` | literals arrive length-1; the boundary broadcast runs too late |
| `TestSelectAcceptsAPayloadFreeNullBranch` | `Lit(nil)` has no buffer to gather from |
| `TestCoalesceThroughMatch` | **panics the process** without the `substitute` arm |
| `TestCondNamesAfterThen` | a result named after a column that is not in it |
| `TestQuantileParametersAreNotCollapsed` | two quantiles silently becoming one |
| `TestVarDdofIsNotCollapsed` | the same on the other parameterised family |
| `TestStdVarPrecision` | sum-of-squares variance returning 0 on real data |
| `TestAggregatesMatchNaiveImplementation` | every new accumulator, against the obvious implementation |
| `TestAggregatesOnAllNullGroup` | the shape step 4 found `Min` broken on |
| `TestArgMinMaxPositions` | nulls occupying a position; ties to the earliest |
| `TestIsInUsesGroupingEquality` | an `IsIn` that disagrees with `Distinct` about NaN |
| `TestMergeEquivalence` (both folds) | an accumulator whose merge assumes the receiver is the prefix |
| `TestNullCastsAgreeWithCanCast` | defect 1, across nine target types |
| Golden plans | `String()` renderings the optimizer depends on |

Teeth verified by reintroduction, each making its named test fail: dropping the
`substitute` arm (process panic), reverting `OutputName`, removing the mask
canonicalisation, hiding `Agg`'s parameters, dropping Chan's correction term, dropping
`arg_min`'s position shift, restoring `renderRow`'s `"?"`, and encoding `IsIn`'s probe
set with a second implementation of equality.
