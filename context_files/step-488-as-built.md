# Step 488 — as built

**`Erf` and `Erfc`, Polars' `erf` and `erfc`.** The first of the five features step
487 chose against Polars 2.1: the error function and its complement, the normal
distribution's CDF.

## 1. The change

- **`Expr.Erf()` and `Expr.Erfc()`:** two unary ops, `OpErf` and `OpErfc`, appended
  after `OpCot`, so no constant moves. `IsMath`'s second range ends at `OpErfc`
  now, so they widen as `Sqrt` does: an integer to a Float64, a Float32 kept.
- **The kernel:** `math.Erf` and `math.Erfc`, in `unaryMath`. `Erfc` is not 1 − `Erf`:
  `erfc(6)` is 2.15e-17, where 1 − `erf(6)` is 0.
- **A Float32's answer** is the Float64's rounded once, as Polars rounds it: over
  30,000 Float32s Polars' answer is `float32(erf(float64(x)))` in every one.
- **Refused,** as `Sqrt` refuses them: text, as Polars refuses it, and a Boolean and a
  Decimal, which Polars answers as Float64s. ursus's math refuses a Decimal by its
  rule that a Decimal's stored value is unscaled, and a Boolean is not numeric here.
- **The names test** knows the two as widening ops.

## 2. Polars 2.1.0's answers

| x | erf | erfc |
| --- | --- | --- |
| null | null | null |
| NaN | NaN | NaN |
| +Inf, −Inf | 1, −1 | 0, 2 |
| −0, 0 | −0, 0 | 1 |
| 1e-300 | 1.1283791670955126e-300 | 1 |
| 1 | 0.8427007929497149 | 0.15729920705028513 |
| 6 | 1 | 2.1519736712498916e-17 |
| 27 | 1 | 5.23705e-319, a subnormal |
| −30 | −1 | 2 |

- **Types:** an Int8, a UInt64, an Int128, a Boolean and a Decimal are Float64s; a
  Float32 stays one. Text and a Duration are refused: "`erf` operation not supported
  for dtype `str`".
- **The last bits are Go's,** as `Tan`'s and `Cot`'s are. Over 40,000 values in
  [−30, 30], Go's `math.Erf` is glibc's but for 674, each an ulp apart, and
  `math.Erfc` but for 3,379, at most 3 ulps; under GOAMD64=v3, where Go fuses its
  polynomials into FMAs, 860 and 3,359, at most 1 and 4. Polars' are glibc's, Python's
  `math.erf` to the bit. A port of glibc's would be ursus's own erf to keep, for the
  last bits.

## 3. Tests

`erf_test.go`, `TestErfAndErfc`: Polars 2.1.0's fifteen edges and its typed answers,
an Int8's and a Float32's, `Erf` within an ulp and `Erfc` four, the infinities, zeros
and NaN by their bits; the three refusals; and the oracle, 4,000 generated values
through every branch of Go's rational approximations, Float64 and Float32, to the bit
against `math.Erf` and `math.Erfc`.

## 4. Teeth: 3 of 3 bite

| mutation | `TestErfAndErfc` |
| --- | --- |
| `Erfc` computed as 1 − `Erf` | **fails:** erfc(6) = 0, Polars 2.1519736712498916e-17 |
| `IsMath`'s range left ending at `OpCot` | **fails:** "ResolveUnary: unhandled op 33" |
| `Erf` by Abramowitz and Stegun's 7.1.26, within 1.5e-7 | **fails:** erf(1) = 0.8427006897475899, Polars 0.8427007929497149 |

**Gate:** `./internal/...`, `./dtype/...`, `./i128/...` and the root package in full,
and vet.
