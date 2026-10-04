# Step 80 — as built

**Decimal arithmetic, exact or refused.** This is the item `v0.3-scope.md` §3 named as
"the next thing" when step 69 decided Decimal aggregation: the precision rule for
`+ - * /`. It was the seventh step of the road to 0.3.

Seven commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference. Its money
  columns are Float64 here, and nothing moved.

No benchmarks.

**The suite is 2814 passing tests and subtests** (2785 at step 79).

---

## 1. What was wrong, measured

Measured through a `go test -overlay` file:

- **Only an identical pair of Decimals added or subtracted.** These were all refused
  as having no common type:
  - `Decimal(10,2) + Decimal(12,3)`;
  - a Decimal with an integer column or a Go int, or with a float;
  - `*` and `/`;
  - `>` and `==` between two different Decimals;
  - a join, a Concat or a When of two.
- **The one pair that worked was wrong at its edge.** It was typed `Decimal(p, s)` and
  added through `arithI128`'s unchecked Add:
  - 99999999.99 + 99999999.99 was stored in a type that holds 10 digits;
  - past 38 digits the sum wrapped and answered.

The engines, measured on `a = Decimal(10,2) [1.25, −3.50]` and
`b = Decimal(12,3) [2.125, 0.375]`:

| | DuckDB | PyArrow | Polars |
| --- | --- | --- | --- |
| `a+b` | (13,3) | (13,3) | (38,3) |
| `a*b` | 2.65625 | (23,5) 2.65625 | (38,3) **2.656, truncated** |
| `a/b` | DOUBLE | (23,12), rounded | (38,3) **0.588, truncated** |
| x/0 | ±inf, nan | | |

## 2. Evidence first

**E1 is `decimal_arith_byhand_test.go`**: 25 cases, each stating the result's type
and its exact unscaled values.
- 21 were wrong and listed in a two-way ratchet.
- Two cases were added after the teeth (§4), bringing it to 27.

**E2 is `TestDecimalArithmeticAgainstExactRationals`**, generated.
- **Shape:** every ordered pair of eight Decimal types, (1,0) through (38,38), under
  `+ − * /`, over each type's edges. That is 256 pairs and about 11,600 rows.
- **Oracle:** `big.Rat`.
  - For `+ − *`: the exact value at the rule's scale, or a refusal when it needs more
    digits than the rule's precision. Each such row runs on its own, so a refusal is
    attributed to the row that earns it.
  - For `/`: the nearest Float64.
- **Today:** 245 pairs were wrong.

## 3. The rules, and the fixes

1. **An integer is a `Decimal(d, 0)`.** d is the digits its width holds
   (`dtype.DecimalDigits`): Int64 is 19, Uint64 20. Int128's 39 is more than a Decimal
   holds, and is refused with a cast hint.
2. **Meeting** (`dtype.DecimalMeet`, used by `Promote` and so by comparisons, joins,
   Concat and conditionals) is `Decimal(max integer digits + max scale, max scale)`,
   which holds both exactly.
   - With a float, a Decimal meets at Float64.
   - `PromoteExact` refuses that pair for a join or a Concat, where 0.1 would round.
3. **`+ −`** is `Decimal(min(38, max(p1−s1, p2−s2) + max(s1,s2) + 1), max(s1,s2))`,
   DuckDB's and PyArrow's rule.
   - The new `decimalArith` kernel rescales each row and adds in 128 bits, checking
     every step.
   - **It redoes in `big.Int` a row whose rescale leaves 128 bits while the sum still
     fits.** A 29-digit integer at scale 10 is 1.70e38, and the sum can still have 38
     digits.
   - A result past the precision is refused, naming the row.
   - `i128.MulChecked` is the checked multiply the rescale needs.
4. **`*`** is `Decimal(min(38, p1+p2), s1+s2)`. That is exact: the unscaled product is
   the answer.
   - A product past 128 bits or past the precision is refused.
   - A scale past 38 is refused at plan time.
   - Polars truncates here; ursus does not.
5. **`/`** is **the Float64 nearest the exact quotient**, rounded once.
   - It takes one IEEE division when both unscaled values are exact doubles at one
     scale, and `big.Rat` otherwise. Dividing the two operands' doubles would round
     three times.
   - A zero divisor gives ±Inf, and 0/0 gives NaN, as DuckDB has it.
   - With a float operand, all arithmetic is the float's, at Float64.
6. **`//`, `%` and `**`** stay refused, with the remedy hint.

**Older tests moved with the rule:**
- `TestDecimalAdditionIsExact` now wants Decimal(11,2).
- `TestDecimalGuards`' multiplication guard became
  `TestDecimalMultiplicationKeepsTheScale`: 12.34 × 12.34 is 152.2756.
- The remedy sweep dropped `mul` and `div`, as its own comment asks of an operation
  that starts working, and gained `//` and `%`.
- The followed remedy is floor division's.
- `TestBindingsArePhysicallyCoherent` says why the Decimal quotient — Float64 from
  Int128s, routed to `decimalDiv` ahead of the width dispatch — is exempt from its
  width rule.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**All 13 bite.**

| reintroduce | fails |
| --- | --- |
| the meet at the coarser scale | `>`, the join, Concat and When |
| no +1 on `+ −` / no digit check on them / no `big.Int` fallback | six cases and the sweep / the sweep / the fallback case |
| no digit check on `*` / `MulChecked` without its cross-term check | `*` past 38 / the sweep |
| the product's scale not summed / no scale check at plan | `a*b`, the sweep, the multiplication test / the plan refusal and the sweep |
| division through two doubles | the sweep |
| Int64 as 18 digits | four integer cases |
| no float rule / `PromoteExact` admitting Decimal with a float | `a + 0.5` / the Decimal–float join |

**Silent at first, and fixed:**
- **The `big.Int` fallback.** The sweep's edges never made a row whose rescale overflows
  while the sum fits.
- **`PromoteExact`'s Decimal–float refusal.** No case joined a Decimal key to a float.

A case was added for each, in one commit. The `MulChecked` tooth did not build at
first, and was re-aimed.

## 5. Behaviour changes

- **Every Decimal pair, and a Decimal with an integer, adds, subtracts, multiplies,
  compares, joins and stacks.** The results are exact or refused past 38 digits.
- **An identical pair's sum is one digit wider**: `Decimal(10,2) + Decimal(10,2)` is
  `Decimal(11,2)`.
- **A quotient of Decimals, and arithmetic with a float, is Float64.**

## 6. Still open

- `//`, `%` and `**` on a Decimal.
- Int128 arithmetic still wraps on overflow, as Int64's does.
- The rest of the road to 0.3: nested write to Parquet next.
