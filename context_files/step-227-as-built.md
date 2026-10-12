# Step 227 — as built

**F4 of `v0.6.3-scope.md`: `RollingMedian` and `RollingQuantile`, by rows, and by
time as `RollingMedianBy` and `RollingQuantileBy`.**

## 1. Why

The rolling functions were sum, mean, min, max, var and std. A rolling median or
quantile was on the README's "not done" list. Polars has both, by rows and by time.

## 2. What changed

**`internal/expr/window.go`:**

- `WinRollingMedian` and `WinRollingQuantile` end the rolling block, which
  `IsRolling` now reaches;
- `WinParams` gains `Q` and `Interp`, which a quantile renders, so two quantiles
  that differ in either cannot share a temporary;
- **the type** is the aggregate's: Median's and Quantile's binding, a Float64. A
  quantile outside [0, 1], or an unknown interpolation, is refused.

**`internal/kernel/rollingquantile.go`:**

- `rollingQuantile` keeps each window's values sorted under `compareTotalF64`:
  - a row entering is inserted where a binary search puts it;
  - a row leaving is removed from where one finds it;
- each row then reads its ranks and interpolates them with `ranks` and
  `interpolate`, step 220's, so it answers as Quantile does over the window:
  - NaN is the largest value, as in Polars here too;
  - a median is 0.5, linear;
- **`rollingOver`** sends both to it, so a centred window and a window by time take
  it as the other rolling functions do;
- **the stream** past the limit takes them as it takes every rolling function:
  `IsRolling`, a bounded window.

**`window.go`:** the four methods. `RollingQuantile`'s interpolation is a
parameter: Polars defaults its rolling quantile to nearest. The rolling-by doc no
longer lists them as missing.

**One difference from Polars:** a Float32 window answers a Float64, as the
aggregates do. Polars keeps a Float32.

## 3. Tests and teeth

- **`TestRollingMedianAndQuantileAnswerAsPolars`:**
  - Polars 1.44.1's answers over a null, a NaN, −0, MinSamples, a centred window,
    nearest and linear, in batches of three;
  - an Int32 window;
  - windows by time where a null row answers from its window;
  - a quantile of 1.5 refused.
- **`TestARollingQuantileIsEachWindowsQuantile`** (kernel):
  - 3,000 rows of repeats, NaN, ±0, infinities and nulls, in three interleaved
    partitions;
  - windows of 1 to 40 rows, trailing and centred, every interpolation, four
    quantiles, a random minimum;
  - against sorting each window afresh.
- **`TestUnpartitionedWindowsStreamPastTheirLimit`** adds a median, and a centred
  quantile over runs of nulls longer than two batches.

| tooth | result |
| --- | --- |
| a median read as the 0.25-quantile | **bites** |
| a leaving value never removed | **bites:** all three |
| values appended unsorted | **bites:** all three |
| MinSamples ignored | **bites** |
| a null taken as a value | **bites:** all three |
| quantile's interpolation ignored | **bites** |
| quantile not rendered by its q | **bites:** two quantiles shared a temporary |
| a quantile past 1 admitted | **bites:** the refusal, added for it when the tooth was first silent |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
