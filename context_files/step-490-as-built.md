# Step 490 — as built

**`Round`, `Ceil`, `Floor` and `Sign` of a Decimal, as Polars 2.0 answers them.** The
third of the five features step 487 chose against Polars 2.1. All four were refused:
"round() is not defined for Decimal(3, 2)". Rounding money is the first thing done to
a Decimal.

## 1. The change

- **The type, `expr.DecimalRounded`, Polars 2.0's:**
  - `Round(d)`, `Floor` and `Ceil` of a Decimal(p, s) are Decimal(p+1, s), at most
    38, where digits are dropped: 9.99's ceiling is 10.00, a digit more;
  - the operand's type where none are, `Round(d)` with d ≥ s, or `Floor` and `Ceil`
    of scale 0;
  - `Sign` keeps the type where it has an integer digit, and is Decimal(p+1, s)
    where it has none: Decimal(1, 1)'s is a Decimal(2, 1).
  - `mathCallOut` takes the call now, for round's decimals.
- **The kernels,** `internal/kernel/decround.go`, on the unscaled Int128s, exact:
  - `decimalRound`: v = q·10^k + m by `DivMod`'s floor, k the digits dropped; the
    answer q or q+1, by m against 10^k − m, which cannot overflow at k of 38 where
    2m can; a tie half away from zero or to the even q by the mode;
  - `decimalSign`: 10^s, 0 or −10^s;
  - a result past the type's precision is refused, naming its row: "round of
    99…9.99 at row 1 needs more than the 38 digits Decimal(38, 2) holds". At 38
    digits the carry has no room, and Polars refuses it too.
- **Round's modes are a float's,** `RoundHalfAwayFromZero` by default, where Polars'
  default is half to even, and `RoundHalfToEven`.
- **Still refused:** `Truncate` and `RoundSigFigs` of a Decimal, which Polars
  answers; `Sqrt` and the rest of the math.
- **Tests that asserted the refusals,** changed: `TestDecimalMathIsRefusedAtPlanTime`
  keeps `Sqrt` only, `TestDecimalRefusalsRecommendOnlyPossibleCasts` drops the four,
  and `TestSignOnDecimalIsRefused` is gone: its "0.01" is the new test's sign row.

## 2. Polars 2.1.0's answers

Decimal(3, 2) 1.25, −1.25, 1.35, −1.35, 2.50, −2.50, 0.05, −0.05, 9.99, −9.99, 0.00,
null, 1.24, 1.26:

| | type | answers |
| --- | --- | --- |
| `round(0)`, half to even | Decimal(4, 2) | 1.00, −1.00, 1.00, −1.00, 2.00, −2.00, 0.00, 0.00, 10.00, −10.00, 0.00, null, 1.00, 1.00 |
| `round(0)`, half away | Decimal(4, 2) | … 3.00, −3.00 for the 2.50s |
| `round(1)`, half to even | Decimal(4, 2) | 1.20, −1.20, 1.40, −1.40, 2.50, −2.50, 0.00, 0.00, 10.00, −10.00, … 1.20, 1.30 |
| `round(1)`, half away | Decimal(4, 2) | 1.30, −1.30, … 0.10, −0.10 for the 0.05s |
| `round(2)`, `round(3)` | Decimal(3, 2) | the values |
| `ceil` | Decimal(4, 2) | 2.00, −1.00, 2.00, −1.00, 3.00, −2.00, 1.00, 0.00, 10.00, −9.00, 0.00, null, 2.00, 2.00 |
| `floor` | Decimal(4, 2) | 1.00, −2.00, 1.00, −2.00, 2.00, −3.00, 0.00, −1.00, 9.00, −10.00, 0.00, null, 1.00, 1.00 |
| `sign` | Decimal(3, 2) | ±1.00, 0.00 |

- Decimal(1, 1) `sign` is a Decimal(2, 1); Decimal(2, 2) `ceil` a Decimal(3, 2);
  Decimal(37, 1) `round(0)` a Decimal(38, 1); Decimal(38, 0)'s `ceil` and Decimal(5, 0)'s
  `sign` keep their types.
- Decimal(12, 10) 0.1234567891, −0.0000000005, 0.0000000015 `round(9)`, half to
  even, is 0.1234567890, 0, 0.0000000020, a Decimal(13, 10).
- **Refused:** Decimal(38, 2) 99…9.99's `round(0)`, `ceil` and `floor`, "overflow in
  decimal rounding: result doesn't fit Decimal(38, 2)"; Decimal(38, 38) 0.55…5's
  `round(0)`, and its `sign`, "sign of a nonzero Decimal(38, 38) value doesn't fit
  its type". Its sign of 0 answers.

ursus answers every one as Polars does, its default mode aside.

## 3. Tests

`decimal_round_test.go`, `TestDecimalRounding`: the table above and its edges, the
type and the text; the five refusals; and the oracle, big.Rat, over fourteen types
from Decimal(1, 0) to Decimal(38, 38), each ±max, ±1, 0 and 60 generated values,
half of them a tie at some place, at places 0, 1, s − 1, s and s + 1 in both modes,
`Floor`, `Ceil` and `Sign`: 11,440 rows, each answered as big.Rat rounds it or, past
the type, refused alone.

## 4. Teeth: 4 of 4 bite

| mutation | `TestDecimalRounding` |
| --- | --- |
| a tie rounded up where the mode says half to even | **fails:** `round(0)` of 2.50 is 3.00, Polars 2.00 |
| `DecimalRounded` keeping the operand's type | **fails:** "round of 9.99 at row 8 needs more than the 3 digits Decimal(3, 2) holds" |
| `Ceil` one up even where nothing is dropped | **fails:** ceil of 0.00 is 1.00, Polars 0.00 |
| `Sign` as unscaled 1, the old bug | **fails:** sign of 1.25 is 0.01, Polars 1.00 |

**Gate:** `./internal/...`, `./dtype/...`, `./i128/...` and the root package in full,
and vet.
