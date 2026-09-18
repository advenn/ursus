# Step 61 — as built

**Temporal arithmetic wrapped int64 silently.** Step 57 fixed Time of day and
deferred the rest by name — *"`Datetime ± Duration` wraps int64 silently … Its own
step"* — where it stayed on the open list for steps 57, 58, 59 and 60. This is that
step, and it turned out to be four defects in one family rather than one.

Five commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** — each asserted on its own exit code. PDS-H cannot be
affected by the new refusal and that was checked rather than assumed: its queries
contain no temporal arithmetic at all, because q1's `date − interval '90' day` is
precomputed in Go, which `datearith_test.go` records as deliberate.

---

## 1. Measured first, through the public API

Before a line was changed. Every row is a query someone could write today:

| query | got | want |
| --- | --- | --- |
| `Datetime(ns) − Datetime(ns)`, two valid instants 570 years apart | **−124095h34m33.709551616s** — minus fourteen years | +570 years |
| `Date("2300-06-01").Dt().Epoch()` | **−8019905673** | 10429516800 |
| `Duration(s)` of 300 years `.Dt().TotalDays()` | **−103928** | 109575 |
| `Duration(1s) × 1e10` | **−2346317h47m53.709551616s** | 1e19 ns, which does not fit |
| `Duration(200y) + Duration(200y)` | **−1620095h34m33.709551616s** | 400 years |
| `Values`, `Lit` or `Cast` of 2300-06-01 | **1715-11-11T00:25:26.290448384Z** | 2300-06-01 |
| `Int64 (1<<62) × 4` | 0 | 0 — must keep wrapping |

Every wrong answer above is **in range and plausible**. `ToDuration` cannot catch a
wrapped nanosecond span because at nanoseconds every int64 is a valid
`time.Duration`; `FormatTemporal` prints it as an ordinary negative span. That is why
the instrument compares against `math/big` rather than against arithmetic of the same
width.

## 2. What the measurement found that the plan did not

The step was planned for the arithmetic. Measuring the headline turned up a **fourth
defect, in front of it**: `time.Time.UnixNano` is documented as *undefined* before
1678 or after 2262, and `dtype.FromTime` called it and returned `true` regardless —
so every caller that dutifully checked the flag was told a wrapped value was fine.
Two more callers skipped `FromTime` entirely. A year-2300 timestamp was therefore
wrong **before any arithmetic ran**, on the most ordinary path there is: putting data
into a frame.

The `.dt` defect is the same shape and more reachable than the step's own subject:
a Date is 86_400e9 nanoseconds per tick, so `t * npt` wrapped for **every date past
2262-04-11**. An ordinary column, an ordinary call, a wrong number.

## 3. The arithmetic guard

A **pre-pass that only refuses**, after the Time arm in `kernel.arithmetic`:

```go
if out.IsTemporal() && out.Physical().ID() == dtype.TypeInt64 {
    if err := checkTemporalOverflow(op, out, l, r, n, valid); err != nil {
        return nil, err
    }
}
```

- **It writes nothing**, so it cannot change an answer, and `arithNum` stays the
  single implementation of add, sub and mul. Checking *afterwards* would be step 57's
  mistake one type over: the wrapped value is comfortably in range, as the sweep's own
  examples show (`-1 * MinInt64` comes back as `MinInt64`).
- **The gate is on the output type.** Only `resolveTemporalArithmetic` produces a
  temporal result, so ordinary integer columns are untouched.
- **An overflow refuses and names the row** — `KindValue`, op `"arith"` — rather than
  nulling or saturating. `evalBinary` already casts operands with `strict=true`, so
  the cast half of the same expression refuses; and refusing keeps the output field's
  declared non-nullability honest, with no `MayProduceNull` change. The op string is
  load-bearing, not decorative: it is how the sweep tells a cast refusal from an
  arithmetic one without matching on message text.
- **Validity comes from the combined view, never from the operands.** See §6.

Nine shapes wrapped; all nine now refuse: `Datetime ± Duration`, `Duration + Datetime`,
`Datetime − Datetime`, `Duration ± Duration`, `Duration × Int64`, `Int64 × Duration`,
`Duration // Int64`.

## 4. The `.dt` fix is exactness, not refusal

`TotalDays/Hours/Minutes/Seconds` and `Epoch` scaled a tick count up to nanoseconds
before dividing back down. Neither ever needed a multiply:

- Every total is a whole number of ticks — `per >= 1e9 >= npt` at every unit — so
  `per/npt` is an integer and `t/(per/npt)` is the **same rational floored the same
  way**, with nothing to overflow.
- A tick is either a whole number of seconds (Date, `Datetime(s)`) or a whole fraction
  of one, so exactly one branch of `Epoch` multiplies, and it is bounded: int32 days
  times 86_400 cannot leave int64. The predicate guards it anyway.

The old comment stated the goal correctly — *"multiply before dividing so a coarse
unit does not floor to zero"* — and implemented it unsafely.
`TestTotalsStillFloorTowardsZeroAtEveryUnit` keeps the goal.

## 5. The front door

`FromTime` now derives ticks from Unix **seconds** and range-checks the one multiply,
so its flag means what its callers always assumed. A side effect is that **more fits**:
a millisecond instant in the year 3000 is an ordinary tick count, and only the
nanosecond intermediate was ever the problem.

| path | out-of-range behaviour | why |
| --- | --- | --- |
| `Cast(string → Datetime)` | refuses | it was checking the flag all along |
| `Lit(t)` | refuses, naming the instant | one value the user wrote |
| `Values([]time.Time)` | **null** | a constructor has no error to return, and the row is the user's data rather than a bug in the caller — `rescaleTemporal`'s principle: a wrapped timestamp is a plausible wrong answer and a null is not |

**A limitation, named rather than hidden:** `Values` always builds nanoseconds, so
there is no coarser unit to ask for at construction. Build tick counts and `Cast`, or
parse from strings, for instants outside 1678–2262.

## 6. Two fixtures that made a tooth go quiet

Both were found by teeth that failed to bite, and both are the same lesson — the
**ninth and tenth** recorded instances of *the fixture, not the test, is the unit of
coverage*.

- **Pair ordering.** Removing the two `-1` cases from the multiplication predicate did
  not fail the sweep. The fixture put a pair and its reverse in one column, so
  `(MinInt64, -1)` was caught and masked the missed `(-1, MinInt64)` — the case the
  special arm exists for, where the wrapped product is `MinInt64` and `p/a == b`, so
  the division test alone reports no overflow. Row 2 is now a neutral value; the outer
  loops already walk both orders.
- **Three rows.** Replacing the validity gate with `l.IsValid(i) && r.IsValid(i)` —
  the pattern used elsewhere in this codebase — did not fail the null-row test. A
  broadcast literal's validity is one bit in one byte: rows 1..7 read zeros and look
  null, and only row 8 runs off the end. At sixteen rows the naive guard **panics**
  with "index out of range". `combinedValidity` has already resolved the broadcast,
  which is why the guard reads that.

## 7. The instrument

`internal/physical/temporaloverflow_test.go`.

- **Arms are derived:** every `(op, left, right)` `ResolveBinary` accepts with a
  temporal result, over every operand type the enum can build. `allOperandTypes` must
  produce or explicitly excuse **every** `TypeID`, so the sweep grows the day a type is
  named — which is how the Decimal divergence in §8 surfaced without anyone looking
  for it.
- **Operands are built at the post-cast types**, so no cast runs and the oracle is
  pure integer arithmetic on stored ticks. It never re-implements `rescaleTemporal`:
  computing the answer a second way, the way the thing under test computes it, is how
  an oracle stops being one.
- **A Time result is modular by contract**, so the oracle models step 57's wrap
  directly rather than skipping those arms — which asserts that contract with an
  independent implementation as a side effect. It agrees everywhere.
- **Every case is `[value, null, ordinary]`**, so "a null row is never judged" is
  asserted 31,479 times rather than once.

| | before | after |
| --- | --- | --- |
| arms derived | 357 | 345 (twelve Decimal arms no longer resolve) |
| comparisons | 43,197 | 31,479 |
| exact agreements with `math/big` | 64,670 | 62,166 |
| **wrapping shapes** | **9** | **0** |
| refusals | none | `+`2552 `−`2890 `×`4752 `//`72 |

Both ratchets — `knownTemporalWraps` and `knownArmDivergences` — were populated with
measured evidence in the first commit and emptied by the fixes, and fail in both
directions: an unlisted wrap appears, or a listed one goes stale.

## 8. The contained fix the sweep found

`Duration * Decimal` and `Duration // Decimal` were **accepted by the planner and
refused by the kernel**: `integralScale` rejected only floats, `IsFloat()` does not
cover Decimal, and `kernel.Cast` refuses Decimal → Int64 outright. Twelve arms, and
`resolveArithmetic` refuses Decimal for `*` thirty lines above — two arms of one file
answering one question two ways. `integralScale` now refuses a Decimal multiplier, so
those arms no longer resolve at all.

`TestEvaluatorContract` exists to catch exactly this and could not: its fixture has no
Decimal column.

## 9. Teeth

| reintroduce | result |
| --- | --- |
| delete the guard | **bites** — all nine shapes return, including the 570-year subtraction |
| drop only the Mul arm / only FloorDiv | **bite**, each in its own op family alone |
| drop the two `-1` cases in Mul | **did not bite** — §6; re-aimed, then bites on `-1 * MinInt64` |
| `l.IsValid(i) && r.IsValid(i)` | **did not bite** at three rows — §6; at sixteen it **panics** |
| drop the validity gate | **bites** — the sweep and the null-row test |
| widen the gate to every Int64 output | **bites** — `TestOrdinaryInt64ArithmeticStillWraps`, which did not exist before this step |
| restore the `Total*` multiply | **bites** — `-103928` days |
| restore the `Epoch` multiply | **bites** — every date past 2262 |
| `FromTime` stops range-checking | **bites** — the cast and the literal |
| `Values` / `Lit` back to `UnixNano` | **bite**, each its own test |
| oracle from `big.Int` to `int64` | the sweep goes vacuous — the counters are what say so |
| hard-code `allOperandTypes` | the arm floor and the `TypeIDCount` exhaustiveness check fire |

## 10. Still open

- **`agg.go`'s `sum`/`mean` over Duration accumulate in float64** and convert back with
  `int64(f)` — lossy past 2^53, and implementation-defined out of range, which is
  verbatim the construct `dispatch.go` documents as removed from FloorDiv. Worse than
  what this step fixed; it needs an Int128 accumulator, and it is the obvious next step
  in this family.
- **`asof.go`'s tolerance** does `left - right`, `-d` and `d*npt` unchecked on one
  line, so a candidate three centuries away can test as within a one-hour tolerance.
- `Duration / Duration` casts through Float64, lossy above 2^53.
- The shared contract fixture has **no Decimal, List or Struct column** — §8 is the
  second defect that hid there.
- The `errors.Is` ratchet is stale in the docs: measured now at **55 weak assertions
  across 29 files**, with 17 `ErrInternal` rejections and 10 `errors.As`, against the
  "~37 … two … one" the older as-builts still say.
- Carried: `listSort`/`listUnique` whole-child work; the byte-flip spill sweep;
  `Optimizer.Verify` off in `Explain` and the golden inventory; six planners leaking an
  opened operator; `spill.Writer.Write`'s bare error; UDF name uniqueness;
  `list.mean`'s element type; `callCache` eviction; `Pivot`; a stale benchmark suite.
