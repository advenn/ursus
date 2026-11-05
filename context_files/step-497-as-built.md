# Step 497 — as built

**The expressions-and-math wave of tests converted to recorded oracles.** Wave 497 of
[`oracle-v2-scope.md`](./oracle-v2-scope.md) §3, on step 496's framework
([`step-496-as-built.md`](./step-496-as-built.md)): thirteen files' hand-typed
expected values are Polars 2.1.0's answers now, recorded in thirteen groups of
`testdata/oracle` by `bench/scripts/oracle/<group>.py` and spelled in
`oracle_<group>_test.go`. Each file keeps what asserts a refusal, a type, a plan or a
count, and what Polars 2.1 has no answer for is a Go oracle or a differential.

## 1. What

- **Thirteen groups**, one a source file, named after it: `scalar_byhand`,
  `castround_byhand`, `castedges_byhand`, `castmore`, `exprodds`, `exprargs`, `fold`,
  `variadic`, `math`, `trig`, `bits`, `decimal_arith_byhand`, `int128exact`;
  registered in `__init__.py`'s `GROUPS`. 264 cases, 72 of them generated from
  `Gen(seed)` with nulls and the types' edges, every literal frame the Go test's own.
  Every float by bits unless the case says why: Go's `exp`, `log`, `cbrt`, `pow`,
  `atan2` and the trigonometric functions against glibc's at rel:1e-15 (rel:1e-6 at
  Float32, where each rounds its own double once); Polars' float remainder, `a -
  b*floor(a/b)`, inexact in its last bits where ursus's fmod is exact, at rel:1e-9 and
  with a small dividend, the formula being 0 or garbage past 2^53.
- **The Go side**: thirteen `oracle_<group>_test.go` registries; nineteen
  `oracleDifferences` entries (§3); two cases skipped as findings (§4). The thirteen
  source files trimmed (§2). Nothing exported.
- **Two fixes the oracle found** (§4): `Ln` and `Log10` of a subnormal, and a
  Float32's text at 1e13 and 1e-6.

## 2. The files

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `scalar_byhand_test.go` | 328 | 204 | 37 | 0 | 46 | 12 |
| `castround_byhand_test.go` | 250 | 118 | 38 | 0 | 34 | 10 |
| `castedges_byhand_test.go` | 115 | 105 | 7 | 0 | 13 | 5 |
| `castmore_test.go` | 111 | 52 | 9 | 0 | 13 | 5 |
| `exprodds_test.go` | 195 | 77 | 34 | 0 | 32 | 8 |
| `exprargs_test.go` | 135 | 102 | 9 | 0 | 8 | 2 |
| `fold_test.go` | 122 | 61 | 15 | 0 | 22 | 5 |
| `variadic_test.go` | 146 | 91 | 26 | 0 | 25 | 3 |
| `math_test.go` | 623 | 544 | 10 | 0 | 20 | 8 |
| `trig_test.go` | 85 | 85 | 0 | 0 | 7 | 4 |
| `bits_test.go` | 89 | 59 | 14 | 0 | 16 | 3 |
| `decimal_arith_byhand_test.go` (and `decimal_arith_test.go`, 198 → 217) | 228 | 112 | 24 | 0 | 19 | 4 |
| `int128exact_test.go` | 182 | 215 | 6 | 0 | 9 | 3 |

A hand-typed line is one of the original that held an expected value: a `want`
frame, an expected CSV string, a `check` of a cell. What stays in each file, and
what Polars 2.1 has no spelling for:

- **scalar_byhand**: the IsIn refusals (a value of another type is refused, as Eq
  refuses it), the S11 refusals (a truncated instant past the column's range).
  `Truncate(time.Hour)`, a Go duration, floors the UTC instant where `Every("1h")` and
  Polars' `"1h"` floor the wall clock: held to Go's own tick arithmetic (`hourFloor`)
  over Kolkata instants and the ns minimum's neighbour. A Duration's `FloorDiv` by an
  integer, which Polars 2.1 refuses ("// not allowed on duration and dyn int"): a
  differential through Int64. Dropped: `Datetime(s) IsIn(00:00:01.5)`, seconds being a
  unit Polars has not; `is_in_dt_ms_finer` asks the same of a Datetime(ms, UTC) and a
  literal finer than the unit.
- **castround_byhand**: the refusals and their wording (`"256" → Uint8` names the
  value and the row and does not say "cannot parse"; a float or a string past a
  Float32's range; `2^63 → Int64`). `"-0"` read as a Uint8's 0, which Polars' unsigned
  parse refuses: a differential through Int64. The Int128 inputs are Int128 columns
  now, which Polars 2.1 has, where the Go test summed Int64s to make one.
- **castedges_byhand**: the S18 and S20 refusals. Seconds, a unit Polars has for
  neither Duration nor Datetime: `Duration(ms) → Duration(s)` truncating and
  `Datetime(ms) → Datetime(s)` flooring held to Go's integer division and floor over
  210 seeded ticks; the recorded cases ask the same of us → ms and ns → us.
- **castmore**: a strict `ToTime`'s refusal, `ToDecimal`'s type, a zoned
  `CastTimeUnit`'s type and a Date's refusal.
- **exprodds**: the refusals (a float reinterpreted, `Item` of three or none, a
  Boolean's bound) and `IsSorted` across batches of 7, a property of a column built
  there. Dropped as covered by the recording's dtype: `Arctan2` of two Float32s.
  `is_sorted_gen`'s columns are rebuilt from lists: a Series Polars sorted carries a
  sorted flag that `is_sorted` reads instead of looking, and the flag does not know
  where the nulls are.
- **exprargs**: the refusals; the same question at batch sizes 1, 3 and 8192, a
  differential now; a Float64 needle against a `List(Int64)`, which Polars 2.1 refuses
  ("cannot check for Float64 values in List(Int64)") and ursus meets as == meets them:
  the Int64 needle's answer. The recorded `lists_cast` casts the needle to Int8 instead.
- **fold**: `ConcatList`'s refusal of a number beside text (feature-gaps §9),
  `CumReduce`'s struct schema, `Format`'s refusal of too few arguments. A fold's seed is
  spelled `pl.lit(0, dtype=pl.Int64)` in Python: Polars' bare 0 is an Int32 where
  ursus's Go 0 is an Int64 (§9's literal rule), and the fold is the question.
- **variadic**: `HashRows` over a selection is one column named `a` with no null (its
  numbers are ursus's own, §9); a selection of nothing refused; a fold in
  `CumulativeEval`, which Polars refuses ("not allowed with non-scalar output") and
  ursus answers (§9), a differential against the sum written out; the two body
  refusals; `Format` of a selection refused.
- **math**: the Go oracle against the math package stays and changes (§4); the type
  checks (`Round` of an integer, `Pow` a float, `Sqrt` of an integer a Float64, of a
  Float32 a Float32) and the refusals. `Pow`'s integer −1 is refused by Polars 2.1
  ("conversion from i32 to u32"); the recorded case asks the float −1.0, and ursus's
  integer −1 answers the same, Pow always a float.
- **trig**: unchanged, a Go oracle; the recorded group is beside it.
- **bits**: the result types and the refusals.
- **decimal_arith_byhand**: the refusals (past 38 digits, a scale past 38, an Int128
  beside a Decimal, a Decimal key beside a Float64 key, `//` and `%`), and a join on
  Decimal keys of two scales, which Polars 2.1 refuses ("datatypes of join keys don't
  match") and ursus matches at the value: a count. Polars types every Decimal sum
  `Decimal(38, s)` and ursus a digit wider than the operands'; its digits are cast to
  ursus's width in the Python query, as numodds casts the Int128 sum, and the sweep
  asserts the width. A product of two Decimals and every quotient, which Polars 2.1
  rounds to the wider scale (1.25 × 2.125 is 2.656 there, 2.65625 here) or refuses (a
  division by zero), are `decimal_arith_test.go`'s against big.Rat, which gains three
  seeded values a type and the (38, 0) value whose rescale by ten digits passes 2^127:
  the hand cases are among its 28,160 rows now.
- **int128exact**: the refusals (a fraction cast to Int128, a sum or running sum past
  2^127). An Int64 column's Sum, which Polars totals as an Int64 and wraps past 2^63
  where ursus widens it to an Int128 (§9): `TestInt64SumWidensExactly` holds it to
  math/big over 309 seeded Int64s of the whole range, whole, by group and running, and
  the total cast to an Int64 or a Uint64 where it fits. The recorded cases ask the
  casts and the sums of an Int128 column, which Polars 2.1 has; `neg` of one it has
  not.

## 3. Where ursus answers otherwise, named

Each in `oracleDifferences` with its reason, all documented in feature-gaps.md §9:

- **A count is a Uint64**, Polars' a UInt32: `scalar_byhand/gbd_kolkata_1h` and
  `gbd_ny_spring_gen` (`Len`).
- **An integer Sum is an Int128** (`internal/expr.ResolveAggBinding`), Polars' an
  Int64 and a Boolean's a UInt32: `math/sum_abs_neg_sign_sqrt` (abs, neg and sign keep
  it), `fold/fold_agg` (a fold over sums), `exprargs/lists_agg` and `lists_agg_gen` (a
  Boolean's). `SumHorizontal` widens the same way, and answers null for a row of nulls
  where Polars sums it to 0 (its doc; §9's all-null sum): `variadic/sum_horizontal_cols`,
  `all_minus_sum_horizontal`, `horizontal_gen`, `eval_sum_horizontal`, `eval_gen`
  (`List(Int128)`), `cumulative_eval_max_horizontal` and `sum_horizontal_all_null`
  (the dtype and the null row).
- **Casts are exact or refused, null when lossy**: `castround_byhand/
  f64_1e39_to_f32_lossy` (Polars' cast overflows to an infinity) and
  `int128exact/float_to_i128_lossy` (Polars truncates 3.7 to 3).
- **`Pow` always returns a float**: `math/pow`'s square, an Int64 in Polars.

## 4. What the oracle found

**Fixed, in the CHANGELOG's Fixed:**

- **`Ln` and `Log10` of a subnormal.** Go's amd64 `math.Log` takes the exponent field
  apart, which a subnormal has not, so `math.Log(5e-324)` is ln(2^-1022), −709.09,
  where ln(5e-324) is −744.44, and `Log10`, which is `Log`'s, −307.95 for −323.31;
  `kernel.floorLog10` worked around the same misreading since step 283, and
  `TestMathMatchesStdlib`, whose oracle is `math.Log` itself, could not see it. The
  kernel's `safeLog` and `safeLog10` scale a subnormal by 2^54 into the normal range
  and take the scale's log off, within two ulps of the correctly rounded answer; the Go
  test's oracle is `Log2`'s scaled for a subnormal (Frexp reads one right), two ulps
  allowed there. Polars' −744.4400719213812 is matched to rel:1e-15.
- **A Float32's text.** Polars writes a Float32 plain from 1e-6 to below 1e13 and in
  scientific notation outside, where a Float64's bounds are 1e-5 and 1e16;
  `kernel.FloatText` used the Float64 bounds for both, so `float32(9.007199e15)` was
  `9007199000000000.0` here and `9.007199e+15` there, and `1e-6` `1e-6` here and
  `0.000001` there. Each bound is the float of the width nearest its power of ten,
  as Polars reads the shortest digits' exponent (1e13 as a float32 is 9999999827968).
  Step 325's hand table had no Float32 between 1e7 and 1e20.

**Left for the maintainer, recorded and skipped with `t.Skip`:**

- **`scalar_byhand/float_mod_zero_sign`**: a zero float remainder takes the divisor's
  sign here, −0 for 4 % −2, as Python's does; Polars' `a - b*floor(a/b)` is +0. The old
  test documented the choice; feature-gaps §9 does not.
- **`castmore/to_decimal_ties`**: `ToDecimal` rounds a tie half away from zero, 0.005
  to 0.01, and its doc says Polars does; Polars 2.1's `str.to_decimal` rounds it half
  to even, 0.00 (and 0.015 to 0.02, 2.5 to 2 at scale 0). Step 325's "1.255 → 1.26"
  agrees under both. §9 names `Round`'s tie-break, not `ToDecimal`'s.

**Seen and not a finding of ursus:** glibc's `logf(2)` is 0.69314724, an ulp above
the correctly rounded float32 0.6931472 that Go's double rounded once gives
(`castround_byhand/f32_libm`, rel:1e-6); Go's `pow` is a few ulps from glibc's
(`math/pow_gen`, rel:1e-14); Polars' `is_sorted` reads a sorted Series' flag, which
does not record where its nulls are (§2, exprodds). **The first CI run found glibc's
`asinh` and `acosh` an ulp otherwise on GitHub's runner than on Debian's glibc 2.41:**
27 values of the double ones moved between the two machines (`arccosh(2)` is
1.3169578969248166 here and …168 there), so a recording of them holds on one machine
only, as step 496 found of `describe`'s std. They are left out of the trig group's
cases, as the std was; `TestTrigonometry` holds them, with every function there, to
Go's math package.

## 5. Checks

`go test .` in full, `go test ./internal/kernel/`, `GOEXPERIMENT=simd go vet ./...`,
`go run ./internal/gen/api -check` (0 gone), `gofmt -l .` empty, all under
`GOEXPERIMENT=simd GOAMD64=v3`. The thirteen groups recorded three times to the same
bytes (`md5sum -c`, 18 of 18 files); 2.0 MB of recordings in all, the largest group
195 KB.
