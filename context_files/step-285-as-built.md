# Step 285 — as built

**Tier 2 of `v0.6.6-scope.md`, second part: `ewm_sum`, `ewm_mean_by` and
`ewm_sum_by`.** `CumulativeEval` and `Hist` are step 286.

## 1. What changed

**`EwmSum(decay)`,** Polars' `ewm_sum`, `crates/polars-compute/src/ewm/sum.rs` at
`py-1.44.1`:

- **The recurrence:** s = x + (1−alpha)·s, the first value its own.
- **Nulls:** a null decays the sum, but with `IgnoreNulls`. A null row is null, and a
  row answers once `MinSamples` values have been seen. `EwmAdjust` and `Biased` do
  nothing.
- **Width:** a Float32 is summed in float32, as Polars sums it.
- **The op:** `WinEwmSum`, appended to the window ops; `IsEwm` takes it, so it
  streams with its running sum carried in `EwmState`, as the other ewm functions do
  (step 221).

**`EwmMeanBy(by, halfLife)` and `EwmSumBy(by, halfLife)`,** Polars' `ewm_mean_by` and
`ewm_sum_by`, `crates/polars-ops/src/series/ops/ewm_by.rs` (`internal/kernel/ewmby.go`):

- **The ops:** `WinEwmMean` and `WinEwmSum` with `Params.By`, their child
  `PairOf(value, by)` and the half-life in `Params.Period`, as a rolling window by
  time carries its by. So the plan reads `col("x").ewm_mean_by(col("t"), half_life
  2d)`, and the answer is named after the value, with no new rendering.
- **The recurrence:** in by's order, each row holding a value and a time decays the
  last answer by d = 0.5^(Δt/half-life), Δt from the last such row:
  - mean: d·last + (1−d)·x, or x where x equals the last;
  - sum: d·last + x.

  The first such row is its value. A row missing either is null and skipped.
- **Units, Polars':** a Datetime's own, a Date's microseconds, and the half-life
  truncated to them.
- **Ties:** in memory a partition's rows are sorted stably by the time, so rows of
  one instant are taken in the window's order. Polars' sort is unstable there.
- **`0.5^r`:**
  - glibc's `pow(0.5, r)`, Polars', is within 0.52 ulp; Go's `Pow` and `Exp2` agreed
    with it for 86% and 89% of 50,000 values.
  - So `halfPow` is 2^−floor(r) times exp(−f·ln 2) for r's fraction f, the
    exponential in double-double: the argument scaled by 2^−10, a series of nine
    terms, squared back ten times. It agreed for 99.93%, on v1 and v3 alike.
  - Its products are rounded where written, against v3's FMA, as step 284's are.
- **Typing (`ewmByOut`):**
  - by is a Date or a Datetime; a Time and an integer are refused. Polars takes an
    integer by with a "2i" half-life, which ursus's intervals do not have.
  - The half-life is a constant duration, as Polars requires: no months, and no days
    over a zoned by. Negative is refused. Zero is taken, as Polars takes it, as the
    zero `Interval`, since `Every` refuses "0d".
  - The value is a number. A Float32 is computed in float32, any other number answers
    a Float64. Polars refuses an Int8 or an Int16 value; here every integer is taken.
- **Streaming:** a by-time ewm has no bounded need in the file's order, so over the
  whole frame it takes step 235's sorted pass, ordered by its by (`timeByOf`). It
  walks that with `EwmNext`, carrying its last time and answer, as a rolling window
  by time does (step 246). Over a key its partitions spill. Both carry their state
  exactly, so streamed answers equal those in memory to the bit.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, by its bits.

- **`TestEwmSum`:**
  - alpha, nulls ignored, from 2, a span and a com, over g;
  - NaN and Inf, leading nulls both ways;
  - a Float32.
- **`TestEwmMeanAndSumBy`:**
  - a mean at 2d and 1d, over g;
  - by a date at 1d and 36h; by milliseconds at 1500µs, truncated; by nanoseconds at
    31h17m3s;
  - zoned at 24h, a half-life of 0;
  - unsorted, a null time, a NaN, one instant, a constant;
  - a sum; Float32s;
  - the name and the plan;
  - a month, a zoned day, negative, a Time, an integer and text refused.
- **`TestEwmFingerprints`:** 300 rows at irregular times, six forms, against Polars'
  fingerprints of their bits.
- **`TestEwmMoreStreamPastTheirLimit`:** four over the whole frame, by out of order,
  and two over a key, under 16 KiB, frame-equal.

| tooth | result |
| --- | --- |
| a null does not decay | **bites** |
| min samples ignored | **bites** |
| the sum not decayed first | **bites** |
| a Float32 summed in float64 | **bites**, once the fingerprints were added |
| decay by Go's Pow | **bites**, once the fingerprints were added |
| decay by Go's Exp2 | **bites** |
| a Date in seconds | **bites** |
| no constant shortcut | **bites**, once 123.456 was added |
| one instant's rows reversed | **bites** |
| a null time taken | **bites** |
| a mean by weighed as a sum | **bites** |
| a Float32 by in float64 | **bites**, once the fingerprints were added |
| halfPow's series short | **bites** |
| halfPow squared once less | **bites** |
| no sorted pass | **bites** |
| a zoned day taken | **bites** |
| streamed in the file's order | silent |

**Three teeth were silent at first:** the probe frame's handful of values round
alike either way, and its whole-day gaps make exponents Go's Pow gets right. 123.456
is the constant the shortcut shows on: weighed, d·x + (1−d)·x is
123.45599999999999.

**The silent tooth is an equivalence.** A by-time ewm's pass order is its by, so the
sorted pass takes it before its need in the file's order is asked.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
