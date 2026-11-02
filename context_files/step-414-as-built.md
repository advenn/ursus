# Step 414 — as built

**A decimal's exponent no longer wraps to 0.** H3 of `v0.7.2-scope.md` (found by H4's
reading), the plan's 424.

## 1. What was wrong

`i128.ParseDecimal`, which a String's cast to a Decimal reads through, added the
exponent to the scale in an `int`: `shift := exp - len(frac) + scale`. At
"1e9223372036854775807" and scale 2 the sum wrapped far below zero, the digits
rounded to nothing, and the text read as 0, with no error: a silent wrong answer.
DuckDB 1.5.5 and Polars 1.44.1 refuse it. Its mirror, "5.0e-9223372036854775808",
wrapped the other way and was refused as out of range, as both engines refuse it,
by accident.

## 2. The change

An exponent past ±2^40 is out of range, `strconv.ErrRange`, before the sum: at 2^40 a
non-zero value has more digits than any precision, and past −2^40 the engines refuse
it. A zero is still 0, and a tiny value inside the bound still rounds to 0 at its
scale, as both engines round 1e-50.

## 3. Tests

- **`TestParseDecimalExponentDoesNotWrap`** (`i128`): ±1e(2^63−1) and 9.99e(2^63−1)
  out of range where they read 0, 1e(2^63−2), the mirror, 1e(2^40+1), 1e−(2^40+1) and
  1e40 out of range; 1e−50 0, 5e−3 at scale 2 one unit, 0e(2^63−1) 0, 1.5e2 15000.
- **`TestCastToDecimalRefusesAWrappingExponent`** (root): the cast refuses the value,
  an `ErrValue`, where it gave 0.00; "1.5e2" reads 150.00.

**Teeth: 2 of 2 bite:** the upper bound gone reads the two 0s again; the lower bound
gone reads 1e−(2^40+1) as 0.

**Gate:** `i128`'s tests; the root, kernel and dtype tests of decimals, casts and
parsing; vet. CI's every width, the scalar build and the race detector on the pull
request.
