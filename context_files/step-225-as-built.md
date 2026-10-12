# Step 225 — as built

**F2 of `v0.6.3-scope.md`: `MaxHorizontal`, `MinHorizontal`, `SumHorizontal`,
`MeanHorizontal`, `AnyHorizontal` and `AllHorizontal`.**

## 1. Why

Polars reduces across columns, a row at a time, beside `concat_str`. ursus had
`ConcatStr` and `Struct` in its horizontal family, and no reductions.

## 2. What changed

**`internal/expr/call.go`:**

- six constants end the horizontal family;
- **max, min, sum and mean** are typed in two steps:
  - `HorizontalInput` meets the operands' types by `Promote`, as a conditional's
    branches meet;
  - the aggregate's own binding (`HorizontalAgg`, `ResolveAggBinding`) types the
    answer, as for a column;
- **any and all** take Boolean operands, or null literals.

**`internal/kernel/horizontalagg.go`:**

- **`horizontalReduce`** feeds each operand, cast to the meeting type, to the
  aggregate's accumulator, each row its own group. So a row's answer is the column
  aggregate's over the same values:
  - nulls are skipped, and the answer is null when every value is;
  - NaN is the largest value;
  - an integer sum widens to Int128;
  - a mean is the nearest double to the exact one.
- **`horizontalKleene`** is Or and And folded: a value that decides the row decides
  it, and an undecided row with a null is null.
- **A literal operand,** one row, is repeated.

**`horizontal.go`:** the six functions. Each needs at least one expression and is
named after the first.

**Three differences from Polars,** each the column aggregate's rule, given in the
docs:

- its max and min skip NaN;
- its integer sum keeps the operands' type and wraps (Int8 + Int8 is an Int8);
- its sum of a row of nulls is 0.

## 3. Tests and teeth

**`TestHorizontalReductionsAnswerAsPolars`:**

- Polars 1.44.1's answers over an Int32, a Float64 and an Int64 with nulls and a
  NaN, the three differing cells marked;
- integer max and sum, string max and min, and a literal operand;
- the Int128 and Float64 types of an integer sum and mean;
- Kleene's any and all over every pairing of true, false and null;
- refusals of a non-Boolean any and of a string against an integer, and of a call
  with no expressions.

| tooth | result |
| --- | --- |
| max read as min | **bites** |
| an operand left out | **bites** |
| a literal not repeated | **bites** |
| a null decides a Kleene row | **bites** |
| an undecided row answers the other identity | **bites** |
| operands meet exactly (PromoteExact) | **bites:** Float64 against Int64 |
| any admits a non-Boolean | **bites:** the test and the contract |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
