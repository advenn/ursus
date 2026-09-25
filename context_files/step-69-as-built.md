# Step 69 — as built

**Decimal you can sum.** `v0.3-scope.md` item 2. Before this step a Decimal could be
read, added, compared and written, and could not be summed or turned into any number
type. Summing money is what the type is for.

Five commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference. Each was checked on its own
exit code. No benchmarks. The golden plans did not move. The suite is at **2393**
tests.

---

## 1. The rule was measured before anyone chose it

The scope doc called this an API commitment, "expect the decision to take longer than
the code". So the three reference engines in `bench/.venv` were run, not
remembered: Polars 1.44.1, DuckDB 1.5.5 and PyArrow 25.0.1, all on Decimal(10,2)
`[12.34, 5.00, null, -0.05, 100.00]`.

| | Polars | DuckDB | PyArrow | **ursus** |
| --- | --- | --- | --- | --- |
| sum | Decimal(38,2) 117.29 | same | same | **Decimal(38,2) 117.29** |
| sum past 38 digits | error | error | **wraps silently** | **refused** |
| sum of all-null | 0.00 | null | null | **null**, like every ursus sum |
| mean | Float64 29.3225 | DOUBLE | Decimal **29.32**, truncated | **Float64** |
| median / std / var | Float64 | DOUBLE; median stays DECIMAL | double (std/var are population) | **Float64** |
| product | Decimal **-308.00** | DOUBLE -308.5 | Decimal **-309.00** | **Float64 -308.5** |
| 12.34 → Int64 | 12 | 12 | error | **refused; `CastLossy` → null** |
| Float 2.675 → Decimal(10,2) | 2.68 | 2.68 | 2.67 | **refused; `.Round(2).Cast(…)` → 2.68** |

The first two rows are unanimous, apart from PyArrow wrapping, so they were not
put to you as questions. The other three were:
- **mean and the rest return Float64.** A mean needs a scale nobody wrote. The two
  engines that keep a Decimal for product both **get it wrong**, because a product's
  scale is n·s and not s.
- **Every Decimal cast is exact or refused.** This is the rule
  `Cast(Float64 → Int64)` has always followed, where 3.7 is refused. Rounding is
  something a caller writes, not something a cast does.
- **Fix the Float → Int128 inconsistency here** (§2).

## 2. The survey found silent wrong answers in code that already shipped

The plan named one of them. Measuring it found four, one type over from where they
were expected.

**`Col("x").Sum().Cast(Int64)` rounded above 2^53.** Every integer `Sum` returns an
Int128. The cast checked `Int64()` and then converted **through a float64**, and
narrow's round-trip check could not see the rounding, because it compares the value
after rounding with itself. `[2^53, 1]` summed and cast came back as …992.

Measuring that turned up a larger problem. **A sweep of all 81 integer cast pairs**,
derived from the TypeID enumeration and tried at every boundary any width has,
compared against `math/big`, found four wrong pairs, all at 64 bits:

| pair | what it did |
| --- | --- |
| Int64 → Uint64 | MaxInt64 became **2^63**, one *more* than the input, which fits a Uint64 and so passed |
| Uint64 → Int64 | MaxInt64 **refused** |
| Int128 → Int64 | ±(2^53+1) became ±2^53 |
| Int128 → Uint64 | rounded, and refused everything above MaxInt64 |

The comment above that path said it was *"exact for every remaining pair … any
value large enough to round is caught by the range check in narrow"*. It was not
exact. Every integer-to-integer cast now goes through `castInt`, which widens to an
Int128 (always exact) and range-checks against the target's own bounds, with no
float involved. The other 77 pairs were already right and still are.

**Strict `Cast(Int128)` from a float truncated 3.7 to 3**, while `Cast(Int64)`
refused it. That is two rules for one question. It refuses now.

**An Int128 sum wrapped.** The i128 package argues that its wrapping `Add` can't
wrap, because reaching 2^127 takes 2^63 rows of maximal Int64. That is true of
64-bit inputs. It is false of an Int128 column, where **two rows are enough**, and
the wrap can come all the way round. Four values of 2^126+5 total 2^128+20, which
wrapped to **20**, an ordinary number that no check on the result can tell from a
true 20.

**`i128.Min.Float64()` recursed forever**, because `Min.Neg() == Min`. A stack
overflow is fatal, not a recoverable panic. Decimal → Float64 would have gone
through it.

## 3. Refuse on the total, not on the first wrap

The obvious fix for §2's sum is a checked add that refuses as soon as it wraps. It is
wrong. `[Max, 1, -1]` fits, and a checked add refuses it in any order that adds the
1 before the -1. A parallel merge does not fix the order, so the refusal would
depend on the thread count.

So a 128-bit input **counts its carries**: the true total is `i + carry·2^128`, and
`Finish` refuses unless the carry is zero. That is a statement about the total, and
the test holds it to that: every permutation of seven value sets under three
partitionings, **2352 finishes**, against `math/big`. Inputs of 64 bits or fewer keep
the plain `Add`, which the i128 doc's argument does cover. That path is every
integer sum PDS-H runs, and it was left exactly as it was, because this machine
can't benchmark a change to it. PDS-H itself has no Decimal columns, checked in the
SF=0.1 files (its money columns are doubles), so its 22/22 says nothing about the
new path; the sweeps above are what cover that.

A Decimal needs one more check on top. **10^38 − 1 is below 2^127**, so a total can
fit the accumulator and still need a 39th digit: `[7.5e37, 7.5e37]` has no carry and
is refused by the precision check alone. The two checks are separate, and each has
a test only it can fail.

`cum_sum` has nothing to carry. It publishes every prefix, so a prefix that wraps
has no value to show for that row, and it is refused there.

## 4. Exact or refused, and what "exact" means for a float

| cast | succeeds when |
| --- | --- |
| Decimal → integer | the fraction is zero and the integer fits |
| integer → Decimal(p,s) | \|v\| < 10^(p−s); checked without multiplying |
| float → Decimal(p,s) | the float comes back unchanged: 0.1 → 0.10, 0.123 refused |
| Decimal → Decimal | rescaling up needs room; rescaling down drops only zeros |
| Decimal → Float64 | always; the nearest double |

**The float row gives a free oracle.** The candidate is `math.Round(v·10^s)`, which
is `Round`'s own arithmetic, so a strict cast succeeds **exactly when
`Round(v, s) == v`**. The public test uses the public `Round` as its oracle, not a
copy of it. And `.Round(2).Cast(Decimal(10,2))` gives 2.675 → 2.68, the digits
Polars and DuckDB give, with the rounding written in the query.

**A Float32 source is judged in its own precision.** Float32 0.1 is 0.100000001 as a
float64, so a round trip compared at 64 bits would never accept it as 0.10.

**Decimal → Float64 is correctly rounded**, and the oracle for that is also free:
the String cast is exact, and `strconv.ParseFloat` rounds correctly, so the two must
agree for every value. The fast path is exact arithmetic followed by one division,
below 2^53 and scale 22. Everything above goes through `ParseFloat` of the digits.

**`Decimal(200, 3)` constructs**, because a type constructor returns no error. So
`ValidDecimal` (1..38 digits, scale ≤ precision) is checked by `CanCast`, and the
planner says why it refused.

## 5. The mean divides once

`mean` keeps the exact carried sum and divides once, by `count · 10^s`, so the result
is the double nearest the true mean. The number of divisions matters. **The mean of
three 0.05s is 0.05.** Dividing by 100 and then by 3 gives 0.049999999999999996.
That case exists because the tooth that divides twice was silent without it (§7).

The mean never refuses. A Float64 mean exists even when the sum would need a 39th
digit, as in Polars. DuckDB refuses there.

## 6. The guard was right for the wrong reason

`decimalAggUnsupported` said that without it, a Float64 accumulator over a Decimal
*"would fail at runtime as an internal error"*. It wouldn't have. `widenFloat` and
`toFloat64` both read a Decimal's **unscaled** integer straight into a float, so
`mean` would have come out 100× too large and `var` 10^4× too large, with **no
error**. The guard was protecting against something worse than it described.

The fix is at the reader, not the consumers. `toFloat64` applies the scale, and
`widenFloat` becomes `toFloat64`. It used to carry its own copy of the Int128 arm,
which a Decimal matched first. So mean, var, std, median, quantile, product and
`cum_prod` all read a Decimal in one place, and nowhere else can forget the scale.

## 7. Teeth: thirty, all of which bite, and three went silent first

| commit | teeth | result |
| --- | --- | --- |
| Int128 exact | 8 | all bite; removing `Min`'s guard kills the test binary with a stack overflow |
| Decimal casts | 11 | all bite, after re-aiming |
| Decimal aggregates | 11 | all bite, after one new case |

Every patch was checked to have applied before its result was read. The two
evidence ratchets, `knownInexactInt128` and `knownInexactIntCasts`, are empty, and
a stale entry fails.

Silent first, each for a different reason:

- **`scaleDown` dividing only once.** It divides twice past 10^19, and a value is
  exact only if both divisions are. Every value in the sweep was divisible either
  by nothing or by everything, so the bug gave the right answer by accident. The
  sweep now has a one in every digit position: 3e-19 at scale 38 survives one
  division by 10^19 and not the second.
- **`toFloat64` reading unscaled, in the casts commit.** Nothing there read a
  Decimal through it: the cast calls `decimalFloats` directly, and the aggregates
  that do use it were still refused. The change moved to the aggregates commit,
  where the tooth bites.
- **The mean dividing twice.** None of the fixtures happened to round differently
  twice than once, until the three-0.05s case.
- **Four more read as bites and were compile errors.** The patched code left a
  variable unused. Re-aimed so that they compile, all four bite. This is the same
  lesson as steps 65 and 66: check that the patch landed, and check that it built.

## 8. Two instruments had excused themselves

`TestCanCastAgreesWithTheKernel` listed Decimal and Int128 as unsamplable, because
they *"have no Go literal"*. That was true and beside the point: a column of either
is one `data.NewFixed` call. So **neither had ever been checked as a cast source**.
Both are sampled now. That test checks agreement, not values, so it would not have
caught the rounding either; the value sweeps did.

`TestDecimalRefusalsRecommendOnlyPossibleCasts` had an anti-vacuity floor of 10
refusals out of 14. Six of those operations work now, so the floor is **every
remaining case**. An operation that starts working has to leave the list on
purpose, rather than quietly thinning the sweep.

## 9. Still open

- **Decimal `+` and `-` check neither the precision nor for 128-bit overflow.**
  Decimal(10,2) 99999999.99 + 0.01 comes back labelled Decimal(10,2) with eleven
  digits. This step makes it easier to hit, because sums are Decimal(38, s). It
  belongs with `*` and `/` as one binary-operator precision rule. It was measured
  with the rest: for `+` DuckDB gives Decimal(11,2), p+1, and Polars gives
  Decimal(38,2). For 12.34 × 12.34, DuckDB gives Decimal(18,4) 152.2756 and PyArrow
  decimal128(21,4), while Polars gives Decimal(38,2) **152.28**, which keeps the
  input scale and so gets it wrong. **Step 70's natural subject.**
- **Strict Int64 → Float32 accepts 2^53+1 as 2^53.** Measured: 2^24+1 is refused,
  2^53+1 is accepted and rounded, and 2^53+3 is refused. It is §2's shape one type
  over. Narrow's Float32 check compares against a float64 that has already rounded.
  Decimal → Float32 has the same hole above 15 digits. Left alone because this step
  treats float targets as approximate, but ursus's own Float32 rule says otherwise.
- `Col("price") > 100` is refused, because `weakTarget` won't coerce a literal to
  Decimal. `Lit(100).Cast(Decimal(10,2))` works now, and the coercion itself needs
  the precision rule above.
- `Int128 → Float64` goes through `i128.Float64()`, which can be one ulp off above
  2^53. The correctly rounded `decimalToFloat` could serve it at scale 0.
- Nothing checks decoded values against 10^p, in either Parquet or Arrow.
- Carried: `Optimizer.Verify` off in `Explain`; `!next.After(start)` misattributes;
  `rolling` unaccounted; nested Parquet write; object stores; `unique`/`over` spill;
  the byte-flip spill sweep; the seventeen-site planner leak;
  `spill.Writer.Write`'s bare error; `callCache` eviction; README debt (Pivot,
  1610 → **2393** tests, three stale forward claims, the spill sentence).
