# Step 153 — as built

**A Tier 2 item of `v0.5-scope.md`: EWM and `Interpolate`.** `EwmMean`, `EwmStd`,
`EwmVar` and `Interpolate`, as ordered window functions.

## 1. Why these

**The scope put them right after item 6's windows.** Both are Polars calls a user
of time series reaches for early, and both are ordered functions over a partition.
Step 149's `Segments`, one argsort of the partition ids and the window's order
keys, already give each partition's rows in order. So, like the rolling functions,
they are new `WinFnOp`s with new kernels, and they work inside `.Over(g)` and in an
`OverWith` order with nothing else changed.

## 2. What changed

### The ops (`internal/expr/window.go`)

- **`ewm_mean`, `ewm_std` and `ewm_var`** (`IsEwm`), and **`interpolate`**.
- **`WinParams` gains** `Alpha`, `NoAdjust`, `IgnoreNulls` and `Bias`. `NoAdjust`
  rather than `Adjust`, so the zero value is Polars' default, adjust=True.
- **`args()` renders every one,** bias included where it does not apply, so two calls
  that differ in any parameter cannot share a temporary.
- **Types:** an integer or a Decimal answers a Float64 and a float keeps its width,
  as Polars' do. Anything else is refused, as are an alpha outside (0, 1] and a
  negative `MinSamples`.

### The kernels (`internal/kernel/ewm.go`)

**`winEwm` follows pandas' recurrences,** which Polars' follow: `ewma` for the mean,
and `ewmcov` of the column with itself for the variance.

- **Weights.** Each value is weighted (1-alpha) to the power of how far behind the
  row it lies. Adjusted, each answer divides by the weights its rows had. Unadjusted,
  the newest value weighs alpha and the running answer 1-alpha.
- **A null is no observation.** By default it still ages the weights before it, as
  a row would; with `IgnoreNulls` the weights count values only. A null row answers
  the running value.
- **`MinSamples`** values must have been seen before a row answers; one by default.
- **The variance's correction:** (Σw)²/((Σw)²−Σw²) unless `Biased()`. A first
  value's corrected variance has no denominator, and is null where pandas answers
  NaN. That matches `Var`'s rule for a count no greater than ddof.
- **A NaN is a value, not a gap.** The running value is NaN from there on, where
  pandas, which reads NaN as missing, would skip it. A flag for "a value has been
  seen" replaces pandas' `weighted == weighted` test, which would have reset the
  running value at the next value.

**`winInterpolate`** fills each run of nulls between two values with the points on
the straight line between them, by position in the window's order. A run before the
first value or after the last stays null.

### The public API (`window.go`)

- **`EwmMean`, `EwmStd`, `EwmVar(decay, opts...)`.** An `EwmDecay` is built from
  whichever quantity is to hand, Polars' four:
  - `EwmAlpha`;
  - `EwmSpan`, alpha = 2/(span+1);
  - `EwmCom`, alpha = 1/(1+com);
  - `EwmHalfLife`, alpha = 1 − exp(−ln 2/h).

  Each is validated, and the zero `EwmDecay` is refused as no decay.
- **The options:** `EwmAdjust(bool)`, `IgnoreNulls()`, `Biased()` and
  `MinSamples(k)`.
- **`RollingOption` became `WindowOption`,** before any release, so `MinSamples`
  serves both families. An option a function has no use for does nothing.
- **`Interpolate()`.** Polars' `method="nearest"` is not here.

## 3. Tests

**`ewm_test.go`:**

- **`TestEwmByHand`,** against pandas' answers for [1, 2, 3] at alpha 0.5:
  - the mean, 1, 1.667 and 2.429;
  - unadjusted, 1, 1.5 and 2.25;
  - the variance, null, 0.5 and 0.9286; the std; the biased variance;
  - `MinSamples(2)`;
  - a null between, with and without `IgnoreNulls` (2.6 and 2.333);
  - a leading null;
  - a NaN;
  - the four spellings of one decay;
  - per group in an order;
  - five calls in one `Select` differing in one parameter each;
  - the types;
  - six refusals.
- **`TestEwmAgainstItsClosedForm`:** the adjusted mean and variance, with and without
  `IgnoreNulls`, at three alphas, over 120 values one in six null. They are checked
  to 1e-9 against a closed form that weighs each value explicitly and shares no
  recurrence with the kernel: 1,440 answers.
- **`TestInterpolate`:** a run between two values, an integer column, the ends left
  null, and per group in an order.

## 4. Teeth

| tooth | result |
| --- | --- |
| a null never ages the weights | **bites:** by hand, the closed form |
| adjust ignored | **bites:** by hand |
| the variance never corrected | **bites:** by hand, the closed form |
| a first value's variance answered | **bites:** by hand, the closed form |
| `MinSamples` ignored | **bites:** by hand |
| a NaN read as a gap | **bites:** the NaN case |
| alpha not rendered | **bites:** the parameters case |
| bias not rendered | **bites:** the parameters case |
| interpolation off by one | **bites:** all three interpolations |
| a trailing run filled | **bites:** two interpolations |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean.
