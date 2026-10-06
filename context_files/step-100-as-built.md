# Step 100 — as built

**Int128 arithmetic is exact or refused, with `*`, `//` and `%` alongside `+` and
`-`.** This is `v0.4-scope.md` item 10, "the usability bug". It also closes the open
row "Int128 arithmetic wraps".

## 1. What was wrong

These all produce an Int128:

- every integer `Sum` and every Bool `Sum`, so that the sum is exact;
- UInt64 `Diff`;
- Int64 with UInt64.

But `arithI128` had only `+` and `-`. **Measured** (`TestInt128Arithmetic`, before the
fix):

| query | answer |
| --- | --- |
| `GroupBy().Agg(Col("x").Sum().Mul(2))` | refused: *operator \* is not implemented for Int128* |
| `Col("a").Mul(Col("b"))`, Int64 × UInt64 | refused |
| `//` and `%` on an Int128 column | refused |
| Max + Max, Min − 1 | **ran, and wrapped**: the type that exists to keep a sum exact gave a wrapped one |

## 2. The fix

**`i128` gains:**

- **`AddChecked` and `SubChecked`,** beside the existing `MulChecked`.
- **`DivMod`:** floor division and its remainder. The quotient rounds toward
  negative infinity, and the remainder takes the divisor's sign, which are the
  rules Int64's `//` and `%` follow.
  - It is built on an unsigned 128/128 `quoRem`. A divisor under 2^64 is two
    `bits.Div64` steps. Otherwise the quotient fits in 64 bits: a trial quotient
    from the normalised top word is exact or one too small (Hacker's Delight 9-5),
    and one comparison corrects it.
  - `ok` is false only for Min // −1. The remainder, 0, is right regardless.

The package doc said it had "no division and no modulo — `sum(x) % 7` is not a query
anyone writes". It now says what the package has, and why the engine uses the
checked forms.

**`arithI128` runs `+ - * // %`:**

- **An overflow is a `KindValue` error naming the row and the operands,** with a
  hint to cast to Float64 for an approximate answer.
- **A zero divisor is null,** as for Int64.
- **A null row is never judged.** Its payload is arbitrary, and refusing an
  overflow there would refuse a query over data nobody can see. This is the rule
  `checkTemporalOverflow` states.

**Int64 still wraps,** as Polars' does, and as the changelog documents. Int128 does
not, because it is the type ursus chose for exactness.

**The resolver's Int128 gate now lists what the kernel has:** every arithmetic
operator. `/` and `**` were already cast to floats.

## 3. One control moved

`TestCastScannerFindsARecommendation` needs a refusal that names a cast target. It
drove Int128 multiplication, and said so: *"Int128 multiplication is implemented now;
this control needs a new op"*. It now drives an Int128 product past the type, whose
refusal recommends Float64.

## 4. Tests and teeth

**`TestCheckedArithmeticAgainstBig` and `TestFloorDivModAgainstBig`** (`i128`)
compare every pair from 316 operands against `math/big`. The operands are:

- the edge values: 0, ±1, Min, Max, their neighbours, ±2^64 and 2^63;
- 300 random values of random width, from 1 to 127 bits, and random sign.

Overflow is checked both ways: false exactly when the exact result leaves the type.

**`TestInt128Arithmetic`** (public API, 12 cases):

- a sum times two;
- a sum times itself;
- Int64 × UInt64;
- `//` and `%` in every sign combination, with a zero divisor giving null;
- a sum `//` a literal;
- Min % −1;
- four overflows refused;
- an overflow hidden under a null, not judged.

| tooth | result |
| --- | --- |
| `AddChecked` does not check | **bites:** the sweep, and Max + Max |
| `quoRem` never corrects its trial quotient | **bites:** the `DivMod` sweep |
| truncation instead of floor | **bites:** the sweep and three public cases |
| null rows judged | **bites:** the null-row case |
| a zero divisor not null | **bites:** both zero-divisor cases |
| Min // −1 let through | **bites:** the sweep and the public case |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
