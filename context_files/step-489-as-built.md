# Step 489 — as built

**`TruncDiv`, Polars 2.0's `truncdiv`: division whose quotient rounds toward zero.**
The second of the five features step 487 chose against Polars 2.1. `FloorDiv` floors,
as Python does; `TruncDiv` truncates, as Go, C and Rust do: −7 by 2 is −3, where
`FloorDiv` gives −4.

## 1. The change

- **`Expr.TruncDiv[T Operand](v T) Expr`:** a binary op, `OpTruncDiv`, appended after
  `OpArctan2`, as `OpPow`'s note asks, so no constant moves. It renders as Polars
  writes it, `(col("a") trunc_div lit(2))`.
- **Typed as `FloorDiv`:** every arm `resolveArithmetic` gives `//` it gives
  `trunc_div`: two integers their common integer, a float beside either a float,
  Int64 by UInt64 an Int128, a Duration by an integer a Duration. `MayProduceNull`
  holds it, for a zero integer divisor's null.
- **The kernels:**
  - integers, `divTruncIntScalar`: Go's `/`, a zero divisor a null as
    `divIntScalar`'s;
  - floats: trunc(a/b), as Polars computes it, so −0.5 by 2 is −0 and 0.3 by 0.1 is
    2, from IEEE's 2.9999999999999996;
  - Int128: `DivMod`'s floor, one more where the division is inexact and the signs
    differ;
  - a Duration: its ticks divided as an Int64's, MinInt64 by −1 refused, as
    `checkTemporalOverflow` refuses `FloorDiv`'s.
- **Refused as `FloorDiv` refuses them:** a Duration by a fraction, the hint now
  naming `TruncDiv`; a Decimal; text; a Boolean.

## 2. Polars 2.1.0's answers

| a | b | truncdiv |
| --- | --- | --- |
| 7, −7, 7, −7 | 2, 2, −2, −2 | 3, −3, −3, 3 |
| 5 | 0 | null |
| MinInt64 | −1 | MinInt64, wrapped |
| Int8 −128, −7, 127 | −1, 2, −2 | −128, −3, −63, an Int8 |
| UInt8 255, 7 | 2, 0 | 127, null |
| Int64 −7 | UInt64 2 | −3, an Int128 |
| Int128 −2^127 | 2 | −2^126 |
| 1.0, −1.0, 0.0 | 0.0 | +Inf, −Inf, NaN |
| −0.5, 1e300, 5.5 | 2, 1e-10, +Inf | −0, +Inf, 0 |
| Float32 1e30 | 3 | 3.333333383491554e+29, a Float32 |

**Where ursus differs,** each as its `FloorDiv` does:

| | Polars 2.1.0 | ursus |
| --- | --- | --- |
| Int128 −2^127 by −1 | −2^127, wrapped | refused: Int128 arithmetic is exact or refused |
| a Duration by an integer | refused: "trunc_div not allowed on duration[μs] and dyn int" | its ticks toward zero, −7µs by 2 −3µs |
| Decimal(5, 2) −7.50 by 2 | −3.00, a Decimal(38, 2) | refused, as `//` of a Decimal is |
| a Boolean by an Int64 | an Int32 | refused, no common type |

## 3. Tests

`truncdiv_test.go`, `TestTruncDiv`: Polars 2.1.0's answers above, by type and to the
bit for floats; the null a zero divisor makes of columns that have none, in the
schema; the five refusals; and the oracle, 3,000 generated Int64 pairs, a quarter of
their operands at MinInt64, −1, 0, 1 and MaxInt64, against Go's `/` and
a == b·q + a%b, Int8 pairs against Go's `/`, and Float64 pairs against `math.Trunc`
of IEEE's quotient. `binding_test.go`'s matrix holds `OpTruncDiv`.

## 4. Teeth: 4 of 4 bite

| mutation | `TestTruncDiv` |
| --- | --- |
| the integer kernel floors, as `divIntScalar` | **fails:** Int64 −7 by 2 is −4, Polars −3 |
| `MayProduceNull` without `OpTruncDiv` | **fails:** "column "a" (position 0) is declared non-nullable and holds 1 nulls of 2" |
| the float arm `math.Floor` | **fails:** −0.5 by 2 is −1, Polars −0 |
| Int128's correction dropped | **fails:** Int64 −7 by UInt64 2 is −4, Polars −3 |

**Gate:** `./internal/...`, `./dtype/...`, `./i128/...` and the root package in full,
and vet.
