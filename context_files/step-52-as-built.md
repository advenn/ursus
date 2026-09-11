# Step 52 — as built

**The type system stops disagreeing with itself.** Step 51's ratchet held ten
`Field`/`Eval` disagreements — queries the planner accepts and the evaluator cannot
run, so `CollectSchema` and `Explain` succeed and `Collect` fails. **It is now
empty**, and the two classes it held have checks that are total rather than lists.

Verifying those ten found seventeen more, one of which was a **silent wrong answer**.

Authoritative where it disagrees with [`step-51-as-built.md`](./step-51-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (80 package-ok lines = 16 packages × 5 SIMD configurations),
`make race` exit 0 (16), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. Date arithmetic never worked

All four forms errored at `Collect`. Two defects were stacked, and the first hid the
second.

**The width.** `kernel.arithmetic` dispatches on `Out.Physical()` and then reads
*both operands* at that width. Date is a signed 32-bit **day** count; every other
temporal type is an Int64 tick count. So `Date - Date` published `Duration(s)`
(Int64) with Date (Int32) operands, and `Date ± Duration` published Date (Int32)
against a Duration (Int64).

**The unit.** Had the widths merely been reconciled, `19001 - 19000` would have been
published as **1s rather than 86400s**. A type-only test passes against that; only a
value assertion does not.

Nothing in the repository cast to `Date` before this step, and PDS-H uses Date only
for comparisons — its TPC-H Q1 `date '1998-12-01' - interval '90' day` is
precomputed in Go as `1998-09-02`, because the arithmetic was not available.

### The fix needed no kernel

`kernel.Cast` routes any pair where both sides have a `NanosPerTick` into
`rescaleTemporal`, and `NanosPerTick(Date)` is 24h — so `Cast(Date → Duration(s))`
already multiplies by 86400, with step 49's overflow guard. Changing the binding was
enough:

- **`Date - Date` → `Duration(Second)`**, operands cast to that Duration instead of
  left as Date.
- **`Date ± Duration` → `Datetime(Second, "")`**, which is what
  `resolveTemporalArithmetic`'s **own doc comment already promised** while the code
  returned `Out: l`. The code was made to match its documentation. The zone is naive
  on the principle `Promote` states: *"A date is not an instant at midnight until
  someone chooses a zone."*

Staying a Date is not implementable — it would need an Int32-physical duration — and
flooring `date + 12h` to `date` is a silent precision loss. `instantResult` is the
new two-line function that promotes a Date and returns every other instant
unchanged, so `ts + 1h` keeps its own resolution.

**A Day `TimeUnit` was deliberately not added.** `Finer` is `u > v`, so Day would
have to sit at position 0 and `Second` would stop being the zero value — load-bearing
in four places including `parquet/types.go`'s guard against a panic in arrow-go.
Neither Arrow nor Parquet has a day-resolution duration to map onto.

---

## 2. A silent wrong answer, found while verifying

`dtype.Promote` refuses `Datetime(us,"UTC")` against `Datetime(us,"")` for
comparison, and explains why: *"an instant against a wall clock, and reconciling
them would have to invent an offset."*

`resolveTemporalArithmetic` checked only `l.ID() != r.ID()` — **no zone check** — so
the subtraction was accepted and `retimed` kept the left operand's zone. A naive
wall clock was silently reinterpreted as UTC, producing a plausible duration wrong
by the offset.

The same question, answered two ways, which is the defect class
`TestTemporalArithmeticAndComparisonAgree` exists to prevent. That test varies the
**unit** with both sides `"UTC"`; it never varies the **zone**. And the contract
matrix could not see it either: `contractBatch` had one Datetime column and no Time
column, so there was no pair to disagree about. Both are fixed — the guard adopts
`Promote`'s rule, and the matrix gained a naive Datetime and a Time.

---

## 3. `CanCast` and the kernel: a fifth point fix would have been the mistake

Six ratchet entries were Bool ↔ temporal. The class is not new — it has been found
and fixed **four times**, each individually:

| where | what |
| --- | --- |
| `unary.go` | Null casts — *"type-checked, planned, and failed at execution"* |
| `unary.go` | string parse/format |
| `unary.go` | Decimal → String, *"the same divergence this file records for null casts and string casts"* |
| `promote.go` | String ↔ Enum, *"the same divergence that unary.go records twice and **claims to have closed**"* |

It was not closed. So instead of a fifth point fix, the whole space is enumerated:
`TypeID` has a `TypeIDCount` sentinel and `String()` returns `"Invalid"` for
anything undeclared, so `TestCanCastAgreesWithTheKernel` walks every ordered pair —
the same trick `AggOp` took in step 51.

**It found 27 disagreements, not 6.**

| direction | count | what |
| --- | --: | --- |
| CanCast **promises**, kernel refuses | 18 | 16 × numeric → Decimal, plus String ↔ Binary |
| CanCast **refuses**, kernel performs | 8 | Bool ↔ all four temporal types, both ways |

The ratchet said six because its fixture has fourteen types and no Decimal column.
And the eighteen are the **dangerous** direction: the planner promises and `Collect`
fails.

### Which side moves is a judgement, and it went both ways

- **Bool ↔ temporal — the kernel narrows.** `Bool → Date` went through
  `boolToNumeric` to Int64 and came back relabelled; `Date → Bool` compared a day
  count to zero. A date is not a truth value, and 1970-01-01 is not "false".
- **numeric → Decimal — `CanCast` narrows.** `IsNumeric()` includes Decimal, which
  is what made the promise; the kernel's refusal is right and says why. The refusal
  now happens at plan time, where it belongs.
- **String ↔ Binary — the kernel implements it.** Identical storage, and `CanCast`
  has a deliberate arm promising both directions. It is a relabel.

Three of the four historical instances also moved the kernel, so "they must agree"
does not by itself say which one moves. That asymmetry is the point.

---

## 4. A tripwire from step 12 fired exactly as designed

`TestDecimalCastDivergenceIsKnown` pinned **both sides** of the numeric ↔ Decimal
disagreement, and step 12 deliberately did not resolve it:

> *"dropping Decimal from CanCast's numeric arm moves the refusal to plan time,
> which is where it belongs, but it also changes the fixture path decimal_test.go
> and TestDecimalMathIsRefusedAtPlanTime use to build a Decimal column at all. That
> is its own decision, not a side effect of a bool cast, so step 12 records it
> rather than making it."*
>
> *"Whichever one moves first, it fires, and the pair has to be reconciled rather
> than drifting further apart."*

It fired, and it had named the consequence in advance: `math_test.go` built its
Decimal column with `Col("v").Cast(Decimal(10,2))`, which is now a plan-time
refusal. It uses `prices(t)` — the directly-constructed fixture `decimal_test.go`
already had — instead.

The tripwire is rewritten rather than deleted, as `TestDecimalCastDivergenceIsClosed`,
pinning the pair from the other side: if `CanCast` starts promising again, or the
kernel starts implementing, it fires again.

One refusal **moved rather than appeared**: `Decimal → Int64` is now `ErrType` at
plan time instead of `ErrUnsupported` at execution. Earlier, same meaning.

---

## 5. The structural guard, which needs no evaluator

The width bug is mechanical: any binding where `Out.Physical()` differs from
`CastL.Physical()` or `CastR.Physical()` is a guaranteed runtime refusal.
`TestBindingsArePhysicallyCoherent` walks 18 ops × 17 types × 17 types and asserts
exactly that, at **resolution** time — no evaluator, no kernel, no fixture.

It would have caught all four Date bindings the day they were written.
`TestEvaluatorContract` found them by executing the matrix, which is later and more
expensive; this is the cheap check that belongs underneath it.

---

## 6. Teeth

| tooth | result |
| --- | --- |
| `Date - Date` leaves its operands as Date | **bites** — "dispatches on the OUTPUT and would read the operands at the wrong width" |
| `Date ± Duration` stays a Date | **bites** |
| drop the zone check | **bites** — and prints the disagreement: `sub=<nil> cmp=has no common type` |
| remove the kernel's Bool ↔ temporal guard | **bites** — exactly 8 resurface |
| restore `CanCast`'s numeric → Decimal promise | **bites** — exactly 16 resurface |
| leave a fixed gap in the ratchet | **bites** |

And two instruments caught **my own** mistakes during the step, which is the better
evidence that they work:

- My first expected values in the Date tests were each one day short. The tests
  failed; the engine was right.
- Renaming the tripwire left a comment citing the old name, and step 51's
  `TestEveryCitedTestExists` refused it. It cannot distinguish a historical mention
  from a citation and should not guess, so the sentence was reworded.

---

## 7. Verification

- Date differences asserted in **seconds** — `29 * 86400`, not `29`. The unit bug
  and the width bug fail differently, and only a value assertion sees the first.
- `Date ± Duration` at four epoch values, including a half-day that must survive.
- Mixed zones refused, and — the actual claim — arithmetic and comparison **agree**
  about refusing them.
- Every ordered `TypeID` pair, with the uncoverable types **named** rather than
  counted, so a newly-added type cannot take a covered one's place.
- The binding cross-product, with an anti-vacuity floor.

---

## 8. What is still open

- **`Time + Duration` can push the tick count past 24h with no wrap**, and
  `FormatTemporal` rolls it over silently through `time.Unix`. A different question
  — does a Time wrap or saturate? — and its own step.
- **`dtype.Interval` has no `OffsetBy`**, so `date + 1 month` is still unavailable.
  Calendar arithmetic exists and is wired only to `.Dt().Truncate`.
- **Eight `TypeID`s have no sample column** in the cast cross-product — Decimal,
  Enum, Categorical, List, Struct, Array, Int128, Uint128 — so they are covered as
  cast *targets* but not as *sources*. Named in the test, not silently skipped.
- **`numeric → Decimal` is now refused rather than implemented.** Implementing it
  means scaling by 10^scale and is a feature, not a divergence.
- The standing list: 32 weak `errors.Is` accept-assertions, `internal/uerr`'s zero
  tests (355 lines, 519 call sites, and a `Kind.String()` whose
  `default: return "internal"` is the same enum bug fixed for `JoinKind` in step
  51), `spill.Reader.wrap`'s uncovered IO-error paths, `Str().Join()`, the
  `AsOfJoin`/`MergeSorted`/`HStack` pushdown arms — whose step-51 goldens are
  currently vacuous for that purpose because every fixture selects everything — the
  UDF follow-ons and a serial opt-out, `.list` set operations, `Expr`-level
  selection, `Pivot`, and a benchmark suite eighteen commits stale on a machine that
  cannot run it.
