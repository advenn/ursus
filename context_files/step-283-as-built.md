# Step 283 — as built

**M5 of `v0.6.6-scope.md`: numeric odds.** These are Polars' `round` with a mode,
`truncate`, `round_sig_figs`, `cot`, and `clip` with one bound. This finishes M1 to
M5; tier 2 is next, if time allows.

## 1. What changed

**Rounding, ported from Polars' source** (`crates/polars-ops/src/series/ops/round.rs`
at `py-1.44.1`), so each answer is Polars' to the bit:

- **`Round(decimals, mode...)`:**
  - **Modes:** `RoundHalfAwayFromZero`, ursus's default as before, and
    `RoundHalfToEven`, Polars' default. One mode at most; an unknown one is refused.
  - **In the plan:** the mode is `round`'s second argument, `"half_to_even"`. The
    default's plan is the one it always had, with no golden plan touched.
- **`Truncate(decimals)`:** toward zero, `FnMathTruncate`, round's kernel with
  `math.Trunc`, as Polars' truncate is its round toward zero.
- **The kernel,** `round` in `internal/kernel/mathfn.go`: `op(v · 10^d) / 10^d`, and
  where Polars departs from that, so does it:
  - `10^d` by `powi`, Rust's `__powidf2`, repeated squaring;
  - d of 0, the op alone;
  - d of 300 to 325, scaled by 2^d and 5^d apart. So `Round(0.1, 300)` is
    `0.09999999999999999`, as Polars', and a subnormal rounds at 310 places;
  - d of 326 or more, or 47 or more for a Float32, the value as it is;
  - an answer that is not finite, the value as it is.

  Before, `Round` used `math.Pow`, which agrees with `powi` for an integer power, and
  kept a value whose scaling overflowed. It took 10^310 as infinite, so a subnormal at
  300 to 325 places was kept where Polars rounds it.
- **`RoundSigFigs(digits)`,** `FnMathRoundSigFigs`: Polars' algorithm.
  - `e = digits − 1 − floor(log10|v|)`;
  - scaled by `Ldexp(v, e) · 5^e`, rounded half away from zero, and scaled back;
  - an answer that is not finite, the value as it is;
  - an integer through a float64 and back as Rust's `as` brings it: toward zero,
    saturating at the type's ends. So an Int8 127 to two figures is 127 and a UInt8
    251 to one figure is 255, as Polars'. An Int128, every integer `Sum` here, the
    same way.
- **The floor of log10** was harder than it looks:
  - Go's `math.Log10` is `Log`'s, which on amd64 misreads a subnormal:
    `Log10(1.5e-315)` is −307.95. So the floor is `Log2`'s, scaled, as `Log2` takes
    the exponent apart by `Frexp` first.
  - Scaled, it falls short of 45 powers of ten, 10^23 and 10^46 among them. A
    correctly rounded log10, glibc's and so Polars', gives the exponent itself
    there. So the floor is checked against the float64 nearest the next power of
    ten, from a table the parser builds once.
  - Without the check, 10^23 and 10^46 land an ulp from Polars' answer at a figure
    count in two.
- **Refused,** as Polars refuses them: digits below 1; text, a Boolean, a Duration.
  A Decimal is refused too, as `Round` refuses one, where Polars rounds its digits.
- **CallFns:** a math block at 1300, `FnMathRoundSigFigs` and `FnMathTruncate`, under
  `IsMath`. The sweeps run to 1400, and the names test counts 207.

**`Cot()`:**

- **The answer:** the cotangent, a unary op, `OpCot`, appended to the trigonometric
  block, so it widens as `Tan` does: an integer to a Float64, a Float32 kept.
- **The kernel:** `1/math.Tan`, as Polars' is 1/tan. A Float32's is 1/tan in
  float32, as Polars divides it: cot(1) is 0.6420926 there, where the float64
  cotangent rounded once is 0.64209265.
- **Its last bits are Go's,** as `Tan`'s are. Over 20,000 values Go's tangent agrees
  with glibc's 70% of the time and 1/tan 75%, within 3 ulps. Near a multiple of π/2
  the gap grows, as Go reduces the argument by three parts of π/4. There the reduced
  argument is tiny and that last part's error is a large share of it: cot(π/2) is
  `6.123233995736757e-17`, Polars' `6.123233995736766e-17`, an absolute error below
  1e-30.
- **Not done:** a four-part reduction of ursus's own would close that gap. It would be
  trigonometry ursus keeps, with its own risk of v1 and v3 fusing differently, for the
  last bits only.

**`Clip`, `ClipLower` and `ClipUpper`:**

- **A null bound is no bound,** row by row, as Polars' `None` is. Each comparison's
  null is taken as false. Before, a null bound gave a null row, and a null literal
  bound nulled the whole column. That is a break, listed.
- **`ClipLower(lo)` and `ClipUpper(hi)`** bound one side, Clip's sugar, their Go
  bounds weak as `Clip`'s are, so an Int32 stays one.
- **Unchanged:**
  - NaN passes unchanged, and a NaN bound bounds nothing;
  - lo above hi gives lo, as Polars'.
- **A difference:** a float bound of an integer column promotes the column here, as a
  fill value does. Polars casts the bound to the column's type: `clip(2.5)` of an
  Int64 is 2 there and 2.5 here.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, compared by its bits, but `Cot`'s, held to 4 ulps
and π/2 to 1e-30.

- **`TestRoundModesAndTruncate`:**
  - to even and away, at 0 and 2 places, over Polars' ties;
  - the floats there are; past 2^52;
  - 15 and 23 places, 300, 310, 325 and 326, 20 places of 1.5e300;
  - truncate at 0, 1, 2, 20, 30 and 310;
  - Float32s to even, at 25, 46 and 47 places, truncated;
  - integers;
  - the plan with and without a mode;
  - a mode that is none, two modes, truncate to −1, text refused.
- **`TestRoundSigFigs`:**
  - 1, 2 and 3 figures of zeros, NaN, infinities, subnormals and the largest float;
  - Polars' edges at 1, 2 and 20;
  - powers of ten at 1, 3, 17, 20, 23, 25 and 30;
  - 10^23 and 10^46 at 1 to 18;
  - halves away from zero;
  - a Float32;
  - Int64, UInt8, Int8, Int16 and UInt64 at their ends, and an Int128 sum;
  - 0 figures, text, a Boolean, a Duration and a Decimal refused.
- **`TestCot`:**
  - zeros, NaN, a null, ordinary values and π/2;
  - integers, a Float32, text refused.
- **`TestClipOneSidedAndNullBounds`:**
  - lower, upper;
  - a bound null in a row, on one side and both;
  - a null lower and upper bound;
  - lo above hi;
  - floats with NaN, a NaN bound;
  - an Int32 kept.
- **`TestTrigonometry`** has `cot`.
- **Probes:** a Go test written in the repository, run, and moved to the scratchpad
  measured `Clip`'s old null bounds. Every Polars answer was printed as Python's
  repr, which Go parses back to the same bits.
- **Two mistakes the tests caught:**
  - a test's `0.1 + 0.2`, which Go folds exactly, as a constant, to 0.3;
  - the subnormals' floor.

| tooth | result |
| --- | --- |
| half to even as away | **bites** |
| truncate as round | **bites** |
| the mode left off the call | **bites** |
| no 2^d and 5^d apart | **bites** |
| an overflowed answer kept | **bites** |
| sig figs by Log10 | **bites** |
| no power-of-ten check | **bites**, once 10^23 and 10^46 were added |
| sig figs to even | **bites** |
| an overflowed figure kept | **bites** |
| no saturation above | **bites** |
| no saturation below | **bites** |
| an Int128 lost | **bites** |
| a Float32 cot in float64 | **bites** |
| cot as tan | **bites** |
| a null bound a null | **bites** |
| lower as upper | **bites** |
| cot not widening | **bites** |
| a Float32 past 47 rounded | silent |
| powi as Pow | silent |

The two silent teeth are equivalences, not gaps:

- **At 47 places or more,** every float32 is its own rounding: its value times 10^47
  rounds back to itself. So Polars' cutoff is a shortcut no answer can show.
- **Go's `Pow`** takes an integer power by the same squarings as `powi`, in its
  mantissa. `powi` is kept because it names Polars' algorithm.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
