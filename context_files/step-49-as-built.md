# Step 49 — as built

**The nullability flag stops lying, and step 48's argument for why that was harmless
turns out to be wrong.**

Step 48 wrote that `Field.Nullable` is a permission nothing exercises, so a false
`Nullable: false` is *"a schema lie, not a wrong-answer lie."* Two things falsify
that, and both were found this step: the optimizer already folds on the flag, and
**the Parquet struct reader already skips validity handling on it** — the exact fast
path the flag's own doc describes, in production and protected by four tests.

Authoritative where it disagrees with [`step-48-as-built.md`](./step-48-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. Correcting step 48: two consumers, not zero

Step 48 surveyed every reader of `Nullable` and concluded *"Nothing skips anything.
No kernel reads it; nor does the evaluator."* The kernel half is right. The
conclusion drawn from it is not.

**`internal/plan/local.go`'s `sameValue` reads the flag, and a fold turns on it.**
`boolIdentity` rewrites `x AND lit(false) → lit(false)`, which is sound under Kleene
logic for every `x` — but `Binary.Field` has no absorbing-element case, so the
rewritten node's *declared* nullability differs from the original's unless `x` is
already non-nullable. `sameValue` compares `fa.Nullable == fb.Nullable` and refuses
otherwise. **The flag decides whether the rewrite fires.**

**`internal/source/parquet/column.go` skips validity tracking outright.**

```go
if f.Nullable {
    t, ok := c.fields[0].(parentTracker)
    ...
    t.trackParent(1)
    c.parent = t
}
```

A struct declared non-nullable never learns to read its own definition levels. That
is *"a non-nullable field is one where the engine may skip validity handling
entirely"*, implemented, load-bearing, and protected — forcing the branch off fails
`TestStructSemantics`, `TestStructRenders`, `TestStructSurvivesTakeAndFilter` and
`TestUnnestErasesTheNullDistinction`.

**It is safe there for a reason worth naming: that `f` comes from `r.full`, the
file's own schema, where nullability is decoded from the Parquet repetition level
rather than inferred by an expression.** A REQUIRED group genuinely cannot be null.
It is the one place in the engine where the flag is a fact rather than a claim, and
it is the one place that acts on it — which is not a coincidence so much as the
shape any future fast path will have to justify.

So the step's premise stands but its stakes were understated: the lies were never
merely cosmetic.

---

## 2. Defect 1 — `Binary.Field` had no notion of Partial kernels

`internal/kernel`'s classes name the cause exactly: a **Partial** kernel *"may itself
produce nulls (integer division by zero, a narrowing cast, sqrt of a negative)"*.
`Binary.Field` declared `nullable := lf.Nullable || rf.Nullable`, which is
conservative about **propagation** and silent about **manufacture** — the axis the
kernel layer actually splits on.

```go
ursus.Values("ia", []int64{7, -7, 7, 5})   // FromColumns: Nullable = NullCount()>0
ursus.Values("ib", []int64{2, 2, 0, 2})
Col("ia").Mod(Col("ib"))                   // declared Int64!, row 2 is null
```

`TestFloatModAndFloorDiv` asserts that null — *"integer mod by zero must be null"* —
and has passed under `WithVerify()` the whole time.

The fix is `BinaryOp.MayProduceNull(out dtype.DataType)`, beside the four
classifiers the file already has. **It takes the output type, and that is the whole
content of it** — `kernel.arithmetic` dispatches on `Binding.Out.Physical()`, so the
operands' logical types mislead in both directions:

```
Int64 % Int64        Out Int64      -> arithNum   -> PARTIAL
Float64 % Float64    Out Float64    -> arithFloat -> total (math.Mod, NaN)
Int64 / Int64        Out Float64    -> arithFloat -> total (true division)
Duration // Int64    Out Duration   -> arithNum   -> PARTIAL, though the left
                                       operand is not an integer type at all
```

A predicate on the op alone gets row 2 wrong; one on the operands gets rows 3 and 4
wrong. `Binary.Field` already holds the `Binding` from `ResolveBinary`, so the right
type was in hand and unused.

Membership is spelled out rather than ranged: `OpDiv` sits immediately between
`OpFloorDiv` and `OpMod`, so `o >= OpFloorDiv && o <= OpMod` would be one constant
away from wrong — and true division is the case it would get wrong.

---

## 3. Defect 2, and a third one underneath it

`rescaleTemporal`'s doc has always said overflow *"produces a NULL rather than a
wrapped instant"* — and it took no `strict` parameter, while **four other lossy paths
in the same file branch on `strict`**. Since `Cast.Field` declares
`Nullable: cf.Nullable || !c.Strict`, a strict widening cast past the int64
nanosecond horizon (~1677–2262) declared non-nullable and handed back nulls.

Fixing it exposed a worse one two arms down. The Date narrowing was:

```go
out[i] = int32(v)          // before
```

— a bare truncation, with no `fits` check at all, under a comment promising a null.
Not a null and not an error: **a silently wrapped date**. It now computes `fits`,
errors under strict and nulls otherwise, like every other path in the file.

That one was not in the plan. It is the only defect this step found that nobody had
already written down, and it was found by reading the neighbours of a known bug
rather than by any instrument.

---

## 4. The instrument, and what it found

`data.CheckNonNullable` — a package-level toggle, off by default, switched on by an
`init()` in nine test packages. The check is one clause:

```go
if f.Nullable || c.Validity().IsAllSet() { continue }
if n := c.NullCount(); n > 0 { ... }
```

`bitmap.View.IsAllSet()` gains its first caller — it had **zero** and was dead code —
and is used the only way its doc permits: *"Use it to take a fast path, never to
decide correctness."* It answers "definitely no nulls" in O(1) for the no-storage
view; a materialised all-ones bitmap returns false and pays a popcount. Getting that
backwards is tooth 3, and it fires on honest columns immediately, because
`Builder.Finish` never returns the no-storage form.

**It goes in both constructors.** `NewBatchRows` is documented as existing *"to build
a zero-column batch that still has a row count"*, but **13 of 46 construction sites
use it, most passing real columns** — including `explodeOp` and `unpivotOp`. A check
in `NewBatch` alone would be blind to a third of the engine and blind in its newest
operators. `NewBatchRows` has no error return, so a violation **panics**: an
invariant violation is not a condition a caller can handle.

**The finding, committed to before the data existed: it found nothing beyond the two
known lies.** Every package, every configuration, with the check live on every
intermediate batch. That is the reportable result — the flag is now honest as far as
the suite can see, and the instrument stays so the next step that touches
nullability inherits it.

The check's own tests are in `internal/data/nullcheck_test.go`, because a suite that
passes with a **broken** check looks exactly like a suite that passes with an honest
engine. Five tests: it catches a lie through each constructor, it stays quiet on the
two legal shapes (non-nullable-with-no-nulls, and the nullable-with-no-nulls that a
filter produces and that `memsrc.FromBatch` exists to preserve), it needs the count,
and it is off by default.

---

## 5. A test whose reason for existing was the bug

`TestFoldingRefusesWhenNullabilityChanges` failed the moment defect 1 was fixed —
and its own doc explained why: the constant folder refused to fold integer division
by zero *because* `Binary.Field` claimed non-nullable while the fold produced a null,
so `sameValue` saw the declaration change and backed out.

**The lie was costing the optimizer a fold.** With the declaration honest, the fold
happens, and the test now asserts that under the name
`TestIntegerDivisionByZeroNowFolds`. Its old name described a limitation; the new one
describes a behaviour.

This is §1's point arriving from the other direction: the flag was already wired into
the optimizer, and the lie was already changing plans.

---

## 6. What is not a lie

`kernel.go`'s third Partial example is *"sqrt of a negative"*, and `Unary.Field` has
`Binary.Field`'s exact shape, so the unary family looked like a third defect.

It is not. `unaryMath` calls `math.Sqrt` and friends directly, and `math.Sqrt(-1)` is
**NaN, not null** — a value, and ursus keeps null and NaN carefully apart. The unary
maths kernels are Total, and the doc's own example does not describe this codebase.

Recorded because a negative result checked is worth more than one assumed.

---

## 7. Teeth

| tooth | result |
| --- | --- |
| declare integer `Mod` non-nullable again | **bites**, and the *check itself* is what catches it: `column "imod" (position 2) is declared non-nullable and holds 1 nulls of 4` |
| put the check only in `NewBatch` | **bites** — `NewBatchRows` builds the lie unchecked |
| use `IsAllSet()` alone, no count | **bites** — `column "v" (position 0) … holds 0 nulls of 2`, the misuse its doc warns about |
| make the predicate depend on the op alone | **bites** — float `Mod` declared nullable; only a schema test sees it |
| leave `rescaleTemporal` non-strict | **bites** — a strict cast returns nulls under a non-nullable declaration |
| force the Parquet struct reader's flag branch off | **bites** — four tests, §1 |

The middle two bit only after §8's tests were written.

---

## 8. Two teeth that did not bite until the tests existed

Teeth 4 and 5 were **silent on the first pass**, which is the same finding twice: the
predicate's type-sensitivity and `rescaleTemporal`'s new `strict` argument were both
*unexercised*. Fixing a defect and proving nothing is the failure mode the discipline
exists to catch, so both got tests before the step could close.

`TestPartialKernelsAreDeclaredNullable` walks eight cases — integer and float `Mod`,
`FloorDiv`, `Div`, `Add`, `Pow`, and the `Duration // Int64` case that settles it,
where neither *"both operands are integers"* nor *"the left operand is an integer"*
holds and the kernel is partial anyway.

`TestStrictTemporalWideningRefusesOverflow` casts ten billion seconds from the epoch
(~2286) up to nanoseconds and asserts the strict form **errors**, that the message
says `overflow`, and that it is a user error rather than an internal one — then that
the lossy form still yields the documented null. Datetime columns read as **ticks**,
not `time.Time`, which is what the first draft got wrong.

---

## 9. Verification

- The two defects, each asserted on the **schema**, since nothing else can see them.
- The whole suite under the live check — §4, the instrument's output *is* the finding.
- The check's own five tests, including both directions of one-sidedness.
- `TestFloatModAndFloorDiv` unchanged: the *values* do not move, only the declaration.
- Six teeth, two of which required new tests before they could fire.

---

## 10. What is still open

- **Nothing yet skips validity on a computed non-nullable column.** That is the
  payoff, and it now has an honest flag to stand on. §1's Parquet path is the model:
  it acts on the flag only where the flag is decoded from data.
- **`Binary.Field` has no absorbing-element case**, so `x AND false` still folds only
  when `x` is non-nullable — §5. Making it unconditional is a change to the null
  contract, not to the rule.
- **`Pivot`** — step 47's verdict stands.
- **`AsOfJoin`, `MergeSorted` and `HStack` have no projection-pushdown arm** — step
  48's leftovers, mechanical.
- The standing list: the parallel join build (`joinBuildSink.Merge`, implemented and
  never called since step 10), spilling and parallelism being mutually exclusive in
  `aggWorkers`, CSV scanning, **a suite re-run — eleven commits unpublished, and the
  machine has been unfit for eight steps**, the `.list` set operations, `Str().Join()`,
  `Expr`-level selection, inline keys for `KeyTable`, the heap sampler,
  `quantile`/`median` storage, nested writing, Map/Array, SQL, cloud stores, join
  reordering.
