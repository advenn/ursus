# Step 11 — as built

Expression completeness: the maths and null-repair families. Authoritative where
it disagrees with [`step-10-as-built.md`](./step-10-as-built.md) and the vision
docs.

**849 tests green** (782 before this step) under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean.

`Expr` went from 71 to **94** methods. 164 files, ~46.8k lines.

```go
// none of this existed
ursus.Col("price").Sqrt()
ursus.Col("qty").Pow(2)
ursus.Col("x").Round(2)
ursus.Col("x").Clip(0, 100)
ursus.Col("temp").FillNull(ursus.FillForward)
ursus.Col("amount").FillNullWith(0)         // and it stays Int32, not Int64
lf.FillNull(0)                              // numeric columns only

// and this compiled, planned, printed in Explain, and failed at EXECUTION
lf.GroupBy(g).Agg(ursus.Col("x").Sum().Abs())
```

---

## 1. Why this step

The gap was not where a milestone put it. §14 declares v0.1 done with *"`Expr` with
arithmetic, comparison, boolean, cast, alias"* — and `pow` fell out of
"arithmetic" and was never noticed. v0.2 names window functions and the
namespaces, both shipped. **No milestone line mentions §5.4 at all**, which was 7
of 62 present.

| Feature-doc section | Before | After |
| --- | --- | --- |
| §5.3 Operators (P0) | 19 / 20 | **20 / 20** |
| §5.2 Predicates (P0) | 13 / 14 | **14 / 14** |
| §5.4 Computation (P1) | 7 / 62 | ~22 / 62 |
| §6.5 Null handling (P1) | 1 / 7 | **7 / 7** |

---

## 2. The type hole, and the query that hit it

`ResolveUnary` accepted every `IsNumeric()` type for `abs` — which includes the
unsigned integers, `Int128` and `Decimal`. `unaryArith` dispatched on
`out.Physical().ID()` with arms for Int8/16/32/64 and Float32/64 only. Everything
else was `KindUnsupported` **after** the plan had type-checked and `Explain` had
printed.

The reachable case is not exotic. **Every integer `Sum` outputs `Int128`**, so

```go
GroupBy(g).Agg(Col("x").Sum().Abs())
```

was an ordinary query that reached Int128 without anyone asking for it. And
`grep -rn "\.Abs()" --include=*_test.go` returned **zero** — `Abs` had no test
anywhere in the module, which is how it survived four steps.

**Widened the kernel, did not narrow the resolver.** Narrowing would have made
`abs` on an unsigned column an *error* where the answer is the identity, and would
have broken `sum(x).abs()`. The Uint arms relabel rather than copy; the Int128 arm
is `Sign()` and `Neg()`, three lines, and it covers Decimal too — where `abs` on
an unscaled integer is exact, unlike the multiplication `resolveArithmetic`
already refuses.

**A twin one function away.** `arithFloat` had no `OpMod` case while
`resolveArithmetic` accepted `%` on floats, so `Col("f64").Mod(Col("f64"))`
type-checked, planned, and failed at execution — for nine steps, because
`grep '.Mod('` over every test file returned nothing. `math.Mod`, one line. The
same arm's `if common.IsFloat()` had two byte-identical branches, which *read* as
if floats were special-cased and is part of why nobody looked.

---

## 3. Two families, split by what happens to the type

| Type-preserving | Widening |
| --- | --- |
| `Neg` `Abs` `Sign` `Floor` `Ceil` `Round(n)` | `Sqrt` `Cbrt` `Exp` `Ln` `Log10` `Log1p` |

`floor` on an integer is the identity, so widening it to a float would be a schema
change in exchange for nothing. `sqrt(4)` is `2.0`, so an integer result would be
wrong for every input that is not a perfect square.

**That split is why `unaryArith` could not serve the second family.** It dispatches
on the OUTPUT type and then reads the INPUT with the same `T`, so `Sqrt(Int64)`
would pick `unaryFloat[float64]` and then ask `data.Values[float64]` for an Int64
column — which is exactly what it refuses. `unaryMath` widens through the existing
`toFloat64`, computes in `[]float64`, and narrows back through `fromFloat64`;
~25 lines, no third conversion table.

`toFloat64` gained an **Int128** arm on the way, which is what makes
`Col("x").Sum().Sqrt()` — an RMS, an ordinary query — work at all.

Every one of these is **total**: `sqrt(-1)` is NaN, `ln(0)` is `-Inf`, and neither
becomes a null. That is the rule float division already set, so this is
consistency rather than a new decision.

### `Sign`'s edge cases are the design

`if v > 0 {1} else if v < 0 {-1} else {v}` gives `sign(-0.0) == -0.0` and
`sign(NaN) == NaN` with no special case, matching NumPy. Both need
`math.Float64bits` to observe — `-0.0 == 0.0` is true and `NaN == NaN` is false —
which is why the differential compares bit patterns throughout.

### `Round` is a decision, and it rounds twice

Half **away from zero** (`math.Round`, Polars, a spreadsheet), not half-to-even.
`Round(0.5)` is 1 and `Round(2.5)` is 3.

And it rounds twice: `Round(x, d)` is `math.Round(x * 10^d) / 10^d`, so the scaling
multiply rounds before `math.Round` ever sees the value. `Round(2.675, 2)` is
**2.68** — not because 2.675 is exact (it is 2.67499999999999982…) but because
`2.675 * 100` lands on exactly 267.5. The first draft of this document said 2.67;
the test said otherwise.

It lives in a fourth `CallFn` family rather than as a field on `expr.Unary`,
because **three places rebuild a `Unary` by hand** as `&expr.Unary{Op, Child}` —
`extractAggs` and both predicate-pushdown walkers — and a new field would have been
silently dropped the moment a predicate moved. That is step 7's `Agg.Params` bug,
three sites over. All three now go through `expr.Rebuild`, which step 8 built for
exactly this and which was only ever applied to one of them.

### `Log(base)` is sugar, and `Pow` is fifteen lines

`Log(base)` is `Ln(x)/Ln(base)` — Polars' own definition, no parameterised kernel.
`Log(10)` and `Log10()` can differ in the last ULP, and `Log10` is the exact form.

`OpPow` is **appended after `OpXor`**, not placed beside the other arithmetic. Both
dispatchers route anything the classifiers do not claim to arithmetic through a
`default:` arm, so position carries no meaning — and `IsArithmetic()` had **zero
callers**, so it was **deleted** rather than fixed. A dead classifier that lies
about a live operator is the trap `call.go`'s "Appending only" note warns about,
and the next appended op would have inherited it.

The binding is `OpDiv`'s arm verbatim: `2 ** -1` is 0.5, so the default
`Out: common` would truncate every negative integer power to zero.

---

## 4. Weak literals — the only non-additive change

`ursus.Lit(0)` fixes `DT = Int64` at **build** time, and `Promote(Int32, Int64)` is
Int64. So `Col("i32").FillNull(FillZero)` would have widened a column with **no
literal in the source at all** to explain it.

`expr.Lit` gained `Weak bool`. The zero value is strong, which is what kept the
change small: all thirty-odd construction sites keep today's meaning untouched, and
arithmetic stays weak-free by construction because nothing on that path sets it.

The concept is not new to the IR. `dtype.Promote`'s first branch is already *"a
null literal has no type of its own; it takes the other operand's"*. A weak
literal is that, extended from "no type" to "a default type", with a fit check
bolted on because a default type carries a value.

| | |
| --- | --- |
| `Col("i32").FillNullWith(0)` | Int32 |
| `Col("f32").FillNullWith(0.0)` | Float32 |
| `Col("u64").FillNullWith(0)` | Uint64 — ordinary promotion routes this through **Int128** |
| `Col("i8").FillNullWith(int64(5000))` | Int64, holding 5000 — falls back rather than truncating to -120 |
| `Col("i32").Add(1)` | Int64, unchanged |
| `Coalesce(Col("i32"), Lit(0))` | Int64, unchanged |
| `Otherwise(0)` | Int64, unchanged |

`TestStrongLiteralsStillWiden` is the fence, and it is the most important test in
the set: weakness must not leak out of the fill family.

### `ResolveCond` had to take nodes

Its doc calls it *"the single authority, consulted by Field for the plan's schema
and by the evaluator for the type it casts both branches to. Two copies would
drift."* But a `dtype.DataType` cannot carry weakness, and the evaluator holds only
the evaluated **columns** by the time it asks. So the signature changed to take the
nodes as well, and both callers derive weakness through one `IsWeakLit` helper.
A second resolver that only the planner consulted would have been exactly the
drift the function exists to prevent.

### `FitsExactly` is a second range check, and it is pinned to the first

`kernel.narrow` already answers "is this value representable in that type", and
`internal/expr` (L20) **cannot import `internal/kernel`** (L30). So the check is
written twice, and `TestWeakFitAgreesWithCast` — in `internal/physical`, the lowest
level that sees both — asserts the one-directional claim that everything
`FitsExactly` admits survives a *strict* `kernel.Cast` and round-trips.

**It found a live bug on its first run.** A strict `Cast(Float64 → Float32)`
refused every NaN row with *"value NaN is not representable as Float32"* — which is
false; NaN is exactly representable in a float32. `narrow`'s round-trip test
`float64(t) != v` cannot see it, because `NaN != NaN` is true however faithfully it
converted. So `Col("f").Cast(ursus.Float32)` on any column containing a NaN failed,
and nothing had noticed.

### `String()` is a correctness requirement here too

A weak literal renders `weak_lit(0:Int64)`. It must not render like a strong one:
`Coalesce(col_i32, Lit(0))` is Int64 and `col_i32.FillNullWith(0)` is Int32, and
three maps key on the rendered string. Put both under `.Max()` in one `Agg` and
the aggregate dedup collapses them, after which the batch's column type and the
plan's promised schema disagree. The **type** is in the rendering for a second
collision: two weak literals that both fall back — an int64 5000 and a float64
5000 on an Int8 column — resolve to Int64 and Float64 while printing the same
digits.

---

## 5. Null repair

| Spelling | Built from |
| --- | --- |
| `FillNullWith(v)` | `Cond{IsNotNull(e), e, weak(v)}` |
| `FillNan(v)` | `Cond{IsNotNan(e), e, weak(v)}` |
| `FillNull(FillZero\|FillOne)` | a synthesised weak literal |
| `FillNull(FillForward\|FillBackward)` | the new `WinFnOp` |
| `FillNull(FillMin\|FillMax\|FillMean)` | `Coalesce(e, e.Min().Over())` |
| `LazyFrame.FillNull/FillNan(v, subset...)` | `WithColumns` over a restricted matcher |

**`FillNan`'s predicate is `IsNotNan`, not `IsNan`, and the reason is naming.**
Both spell the same thing — `IsNotNan` on a null row is null, and a null mask takes
neither branch, so a null keeps its null either way. But `OutputName` takes the
**leftmost column** of an expression, so with the branches the other way round the
leftmost column reference was the literal and every filled column came back called
`"literal"` — which at frame level meant a new column instead of a replaced one.

**Forward fill is `winCumExtremum` with the reset removed.** That kernel carries a
row INDEX rather than a value, which is the trick that makes one implementation
serve every type; a fill is the same loop with `best` updated on every valid row
and not reset on an invalid one. It inherits `Take`'s coverage — strings and
Int128 included — for free, and `NullIndex` gives leading nulls for free.
Backward fill costs one bool: `forEachOrdered` flips the direction rather than the
permutation.

**Two ops, not one plus a `Reverse` flag.** `WinFn.String()` renders the op name,
so the direction can never collide even if the parameter rendering is imperfect.
With one op the direction would have lived only in `WinParams.args`, which is
precisely the field that has been the source of every collision bug so far — and
sure enough, without an `args` arm `ForwardFill(1)` and `ForwardFill(3)` render
identically and share one window temporary. Third occurrence of that shape after
step 7's quantiles and step 8's partition keys.

**Frame-level restriction did not use `expr.FieldMatcher`, and the plan said it
would.** The plan's argument was that `DTypeMatcher` compares types by exact
equality so `Decimal(p,s)` is not expressible in a `[]DataType`, making the dead
`FieldMatcher` finally necessary. That is true — but `FieldMatcher.String()` is
`"cols(" + Label + ")"` and `distinctMatchers` dedups on `String()`, so two field
matchers sharing a label would silently expand as one. Deriving the target types
from the *literal's* dtype instead (`fillTargets`) needs no new matcher and no new
hazard. `FieldMatcher` stays dead; that is a finding, not a fix.

Consequence, recorded rather than hidden: `numericTypes` omits Decimal, so
`lf.FillNull(0)` skips a Decimal column. Naming it applies it and produces the
plan-time refusal instead.

**`FillMin`, `FillMax` and `FillMean` are pipeline breakers**, and their doc says
so. They also rest on `.Over()` with no partition keys, which had **zero call sites
anywhere in the suite** before this step. `TestKeylessWindowMatchesGlobalAggregate`
came before the feature.

---

## 6. `Clip` and `IsClose` are sugar, and that is what makes them right

`Clip` is `When(e.Lt(lo)).Then(lo).When(e.Gt(hi)).Then(hi).Otherwise(e)`. A
parameterised `Call` could not do it: `CallArgs` requires every argument to be a
literal and rejects a column-valued one by name, while the spec's signature takes
`Expr` bounds. Four edge cases fall out rather than being coded — a null value
gives null, a NaN passes through, **a null bound makes the whole column null**, and
`lo > hi` lets `lo` win because the chain is right-nested in written order.

`IsClose` needed **two** guards, and the second was found by a failing test:

- The **equality** disjunct makes `IsClose(Inf, Inf)` true — the tolerance term
  computes `|Inf - Inf|`, which is NaN, and `NaN <= x` is false.
- The **finiteness** conjunct makes `IsClose(Inf, -Inf)` false. Without it the
  relative tolerance scales by `max(|a|,|b|) = Inf`, the limit becomes Inf, and
  `Inf <= Inf` says "close". Python's `math.isclose` has the same special case.

It computes in Float64, casting both operands first, so an integer column works
and the float-only finiteness test has something to test.

---

## 7. The rest of the same divergence

Auditing for the `Abs` hole turned up **the same class in the same file**, and
worse, because the path had no test at all. `kernel.formatToString` is what every
`Cast(ursus.String)` runs, and `dtype.CanCast` promises every numeric→String pair:

| | |
| --- | --- |
| `Int128 → String` | **failed outright** — and Int128 is what every integer `Sum` outputs, so `Col("x").Sum().Cast(String)` was the same ordinary query that broke `Abs` |
| `Uint64 → String` | routed through `float64` and **rounded above 2^53** — and `Count`, `Len`, `NUnique` and `NullCount` all output Uint64 |
| `Decimal → String` | refused, although the scale is known and the conversion is exact |

`readAnyInt` now dispatches on the exact physical type with no float detour, and
`TestCastToStringAgreesWithCanCast` is the differential the file already ran in the
other direction — `namespace_test.go` has had String→other since step 6, and
nothing checked other→String.

Two more of the same shape, each one line:

- **`dtype.CanCast` promised `String → Enum`** and the kernel refuses it, because
  `IsString()` is true for Enum. Narrowed to `HasStringStorage()`.
- **The `.str` call family gated on `IsString()`**, so an Enum column — stored as a
  Uint32 index — passed and reached `kernel.StrCall`, which calls `Strings()`, gets
  a zero accessor, and **panics** on the first row. `dtype.IsString`'s own doc warns
  about exactly this misuse and names `HasStringStorage` two functions down. Latent
  only because Enum columns are not constructible from the public API.

---

## 8. Step 10's leftovers, cleaned up the way step 10 cleaned up step 9's

| | |
| --- | --- |
| `Sink.Consume`'s doc said *"must not retain it beyond the call without copying"* | Four sinks retain it uncopied, three say so in a comment, and step 10's whole accounting rests on it. Step 10 rewrote the `Merge` half of that comment and left the `Consume` half asserting the opposite. |
| **`MemoryStats.Peak` under-reported a join by a full copy of its build side** | `freeze` concatenates `s.parts` into `t.build` and never retained it, at the moment the operator is largest. The account is now re-based onto the table. |
| **…and missed the merge phase entirely** | `mergeOperator` had no `Account`, so `Peak` measured only accumulation. Now recomputed per refill, the way `tailOp`'s evicting ring does. |
| **`spill.putView` ignored its own `n`** | `View.Buffer()` returns the whole backing array at bit offset zero, and `Slice(0, k)` produces exactly that — so a 100-row slice of an 8,192-row nullable column wrote 1,024 validity bytes instead of 13. A size bug, never a wrong answer, which is why only `TestSpillValidityIsNotOversized` can see it. |
| **`hashAggSink.Merge` appended duplicate key rows** | `sameNumbering` accepts only when every key of `other` is already present at the same id, so every appended row was a duplicate and `Finish` would have built ~2× `nGroups` key rows against `nGroups` aggregate rows. It never fired because **`Merge` had zero coverage** — the existing test calls `sameNumbering` directly, and the only non-test `.Merge(` in the module is inside `Merge` itself. |
| `ErrResource` was unreachable outside the module | Step 10 added it so *"a caller should be able to detect that without matching on a message"*, but it lives under `internal/`. The root now re-exports all seven sentinels. |
| `data.BufferSet.Len`/`.All`/`.Reset`, and `Add`'s `bool` return | Exported, zero callers including tests. Deleted. |
| `execopt.Account.RetainColumn` | Exported with no cross-package caller — the pattern step 10 unexported `plan.ReconcileSchemas` for. Unexported. |
| `Account.Check`'s hint | Interpolated to *"tail buffers its whole input"*, contradicting `plan.Tail`'s own doc. Now per-operator. |
| `Column.Buffers` said "four slots" and named five | One word. |
| `WithMemoryStats`' doc omitted `SinkParquet`/`SinkCSV` | The code populates them; the doc listed only the four `Collect` forms — so a reader of step 10's own headline example would conclude the stats were not written and reach for `Collect`, the one path that is not a spill target. |
| `NewGroupKeyEncoder`'s order-preserving overclaim | Step 10 corrected it **in prose only** — the exact failure mode it fixed for `sink.go`. Now corrected in the code, and falsified by working code: `kernel.RunSet` exists *because* the encoder carries no `SortSpec`. |

And four docs that lied:

- **`plan.Expressions` was missing its `*Window` arm**, which made a safety test
  vacuous: `plan_test` walks the tree asserting no scan prunes a column read above
  it, and under a `Window` it saw nothing at all. Its doc also claimed projection
  pushdown as a consumer; that rule reads its slots directly, and the `*Join` arm's
  own WARNING explains why it must.
- **`MapExplode` was refused at *physical* plan time** while its sibling `MapJoin`
  is refused at *logical* time — so `CollectSchema` and `Explain` both succeeded and
  printed a plan containing an operator that cannot be built. Now both are logical.
- **`plan.Distinct.MaintainOrder`** was never assigned, never read, and not even
  rendered by `Label()`. Unlike `Sort.Limit` — which step 9 found dead and *woke
  up*, because `ArgTopK` was waiting — this one had no implementation to switch to.
  Deleted, with the reason recorded on the type.
- `checkDistinctNames` took a schema it discarded with `_ = in`.

**Eight name tables had an unreachable fallback.** `unaryOpNames` and its seven
siblings are `[somethingCount]string`, sized by the enum's own sentinel, so
`int(o) < len(names)` is true for every declared constant and the `"?"` branch could
never run — a forgotten entry rendered as the **empty string**, and two unnamed
constants render identically and collide in the three dedup maps.
`dtype.TypeID.String()` has always had the `!= ""` half of the guard; these did
not. `TestEveryEnumConstantHasADistinctName` is now the structural defence, and
this step alone appended to three of those tables.

---

## 9. Honest gaps

- **`expr.FieldMatcher` is still dead**, and §5 says why waking it would have added
  a collision hazard rather than removed one. The selector dtype families it was
  written for still do not exist.
- **`Interpolate`/`InterpolateBy`** are out, as planned — the only item in §6.5
  whose semantics are not already decided somewhere in the tree.
- **Trigonometry (15 ops) and the bit-count family (6)** are out. Mechanical
  repetition once `unaryMath` exists, and 21 more oracle assertions.
- **The 12 missing aggregates** (`MinBy`/`MaxBy`, the bitwise folds, `Mode`, `Skew`,
  `Kurtosis`, `Entropy`, `ApproxNUnique`) are out — one `AggOp` plus an accumulator
  each, which is a coherent step rather than a tail on this one.
- **`*Reverse` still blocks predicate pushdown**, and `*Slice`/`*Tail`/`*Reverse`
  still have no projection-pushdown arm, so `Scan(...).Tail(10).Select(Col("a"))`
  decodes every column. Both real, both ~25 lines plus golden plans, both
  *optimizer* work rather than expression work. Deliberately deferred.
- **A single-pass k-way merge holds one batch per run**, so the sort's real peak is
  `budget + runs × batch size`, not `budget` — and the run count is input size
  divided by budget. Step 10 could not see this because the merge phase was outside
  the ledger; making the accounting honest is what surfaced it. At ten runs the
  second term is a fifth of the first; at a thousand it would dominate, and the
  answer is a multi-pass merge that bounds the fan-in.
- **`toFloat64` is lossy for Uint64 above 2^53 and for Int128**, so `sqrt` and
  friends round there. Inherent — a square root has no exact integer answer anyway
  — and said out loud rather than discovered.

---

## 10. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean. 849 test cases, up from 782.

**No SIMD work, and deliberately no differential test.** `internal/kernel/scalar.go`
carries no build tag and compiles unconditionally; the only build-tagged surface is
the eight dispatch vars in `dispatch_simd_amd64.go`; and the portable `simd` package
exposes no transcendental operations. It does expose `Float64s.Sqrt` and `Abs` —
both deliberately unused, and a differential against them would be vacuous anyway,
because `vsqrtpd` is IEEE-correctly-rounded and therefore bit-identical to
`math.Sqrt`. The oracle is Go's `math` package.

| Test | What it catches |
| --- | --- |
| `TestMathMatchesStdlib` | eleven ops against Go's `math`, compared as **bit patterns** — `-0.0 == 0.0` is true and `NaN == NaN` is false, so `==` can see neither signed zero nor NaN |
| — its four corpus-adequacy assertions | that the corpus actually produced a NaN, a negative zero, an infinity and a subnormal. Without them a corpus that quietly lost one still passes every comparison |
| `TestRoundTieBreakIsHalfAwayFromZero` | the tie-break as a decision: 0.5→1 and 2.5→3 are the two cases that distinguish it from round-half-to-even |
| `TestAbsAndNegCoverEveryNumericType` | ten numeric types × three ops, plus `GroupBy(g).Agg(Col("v").Sum().Abs())` by name |
| `TestFloatModAndFloorDiv` | the twin defect, and the deliberate disagreement — integer `%0` is null, float `%0` is NaN |
| `TestIntegerMathReturnsFloat` | `CollectSchema` and `Collect` agree, which is the invariant `WithVerify` exists for |
| `TestWeakLiteralsDoNotWiden` / `TestStrongLiteralsStillWiden` | the feature, and the fence that keeps it inside the fill family |
| `TestWeakLiteralThatDoesNotFit` | falls back to Int64 holding 5000, not Int8 holding -120 |
| `TestWeakAndStrongDoNotShareATemporary` | the `String()` collision, end to end through the aggregate dedup |
| `TestWeakFitAgreesWithCast` | the two range checks that cannot import each other. **Found the NaN cast bug** |
| `TestForwardFillLimitsAreNotCollapsed` | the `WinParams.args` pin |
| `TestKeylessWindowMatchesGlobalAggregate` | a path with zero call sites before this step |
| `TestFillNanLeavesNullsAlone`, `TestClipSemantics`, `TestIsCloseHandlesTheInfinities` | the null and infinity cases that fall out of the desugaring rather than being coded |
| `TestFillIsBatchSizeInvariant` | four batch sizes × two thread counts over the four windowed fills |
| `TestEveryEnumConstantHasADistinctName` | seven enums, no empty name, no duplicate |
| `TestCastToStringAgreesWithCanCast` | fourteen types into String, including Int128, Uint64 max and Decimal |
| `TestHashAggSinkMergeDoesNotDuplicateKeyRows` | the first test to call `Merge` at all |
| `TestSpillValidityIsNotOversized` | the bitmap half of the rebasing story, by **file size** |
| `TestExprDropNullsIsRefused` | refused at build time, so `Explain` fails too |

**Teeth.** Eight, each by reintroduction, each confirmed to fail the named test:

| Reintroduced | Failed |
| --- | --- |
| `unaryArith` without the Int128 arm | `TestAbsAndNegCoverEveryNumericType` |
| `Pow` with the default `Out: common` binding | `TestPowIsNotIntegerTruncated` |
| `weaken` not setting the flag | `TestWeakLiteralsDoNotWiden` |
| `Lit.String()` ignoring `Weak` | `TestWeakAndStrongDoNotShareATemporary` |
| `WinParams.args` without the fill arm | `TestForwardFillLimitsAreNotCollapsed` |
| `putView` writing the whole backing array | `TestSpillValidityIsNotOversized` |
| `readAnyInt` routed back through `float64` | `TestCastToStringAgreesWithCanCast/Uint64` |
| `hashAggSink.Merge` appending `keyParts` | `TestHashAggSinkMergeDoesNotDuplicateKeyRows` — "4 key rows for 2 groups" |
| a blanked `unaryOpNames` entry | `TestEveryEnumConstantHasADistinctName` |
