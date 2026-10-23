# Step 324 — as built

**Polars' odds:** `item`, `lower_bound`, `upper_bound` and `is_sorted`; `to_physical`
and `reinterpret`; `arctan2`, `truediv`, `limit` and `pipe` (`polars-parity.md` §2).

## 1. The API

| Method | Polars | What it does |
| --- | --- | --- |
| `Item(allowEmpty)` | `item(allow_empty=)` | the one value, a group's or the column's beside the others; several refused, "expected a single value, got 3 values", and none unless `allowEmpty`, which gives null |
| `LowerBound()`, `UpperBound()` | `lower_bound`, `upper_bound` | the type's least and greatest: a number's own type, ±Inf for a float; a type stored as integers its storage's, a Date's Int32, a Datetime's Int64, a Decimal's Int128, an Enum's index, as Polars gives them; a Boolean, text and nested types refused |
| `IsSorted(descending, nullsLast)` | `is_sorted` | ascending or descending, ties allowed, NaN the greatest; the nulls all first, or all last; a group's in an aggregation |
| `ToPhysical()` | `to_physical` | as stored: a Date's days, the ticks of a Datetime, Duration and Time, a Decimal's unscaled units, an Enum's index as the smallest unsigned type that holds it, as `cat.physical` |
| `Reinterpret(signed)` | `reinterpret(signed=)` | an integer's bits at the other sign: a UInt64's maximum is -1 |
| `Arctan2(y, x)` | `pl.arctan2(y, x)` | the angle, a Float64 of integers and Decimals, a Float32 of two Float32s |
| `TrueDiv`, `Limit`, `Pipe` | `truediv`, `limit`, `pipe` | `Div`, `Head`, and a function of the expression |

**Where ursus differs:** a `Time` is its unit's ticks, where Polars' `Time` is always
nanoseconds; `Arctan2` takes any expressions, where Polars takes columns; and
`shrink_dtype`, a no-op on an expression in Polars 1.44, is not built.

## 2. How

- **`Item`, the bounds and `IsSorted` are aggregates,** `AggItem` to `AggIsSorted`,
  so they answer a group at a time in an aggregation and once beside the rows in a
  `Select`, as Polars' do:
  - `itemAcc` counts a group's rows and keeps its first, the one when there is one;
  - `boundAcc` reads nothing and makes the bound;
  - `isSortedAcc` keeps a run a group, whether in order, whether a null and a value
    are in it, and its first and last value, so two runs join exactly; it is
    order-dependent, so a group-by holding it runs on one worker.
- **`IsSorted`'s fast path:** a batch of one group with no null, as a `Select`'s is,
  is checked a pair at a time in one loop with no call a value; a group out of order
  reads no more. That took a million sorted Int64s from 6.9 to 2.8 ms, and random
  floats from 11 to 2.2 ms. Text keeps a group's first and last copied once a batch.
- **`ToPhysical` and `Reinterpret`** are general calls, a relabel of the values but an
  Enum's, which `EnumPhysical` narrows.
- **`Arctan2` is a binary operator,** `OpArctan2`, typed as `Pow` is, both sides to one
  float, and written as a function.

## 3. Speed

A million rows, medians of seven, two rounds interleaved:

| | ursus | Polars 1.44.1 |
| --- | --- | --- |
| `Arctan2` | 4.4–5.3 ms | 16.8 ms |
| `IsSorted`, sorted Int64s | 2.8–2.9 ms | 0.8 ms |
| `IsSorted`, random floats | 2.2 ms | 0.03 ms |
| `ToPhysical`, Datetimes | 1.6–2.2 ms | 0.02 ms |

`IsSorted` costs what ursus's other frame-wide aggregates cost, `Min` 1.6 ms and
`First` 2.6: the group-by's machinery a batch, and a scan that reads on after the
answer is known, where Polars stops at the first pair out of order or reads a sorted
flag. `ToPhysical`'s time is a `Select`'s copy of its batches, the relabel itself
free. `feature-gaps.md` §8 records the frame-wide aggregate's cost.

## 4. Tests

**`TestExpressionOdds`,** every answer Polars':

- `Limit`, `TrueDiv`, `Pipe`;
- `Arctan2`: π/4, a null, `atan2(0, 0)`, π, and two Float32s' Float32;
- `ToPhysical` of eight types, and `Reinterpret` of three, a float refused;
- `Item`: one value beside a column, a null one, none allowed, and three, none and a
  group of two refused;
- the bounds of six types, a scalar beside a column, a group's, a Boolean's and
  text's refused;
- `IsSorted` of nine columns in each of its four orders, a sorted column read seven
  rows at a time, one with a value out of place, and two groups.

**The contract tests took the new ones in,** and found one bug:

- `TestEvaluatorContract` typed `Arctan2` of a Decimal and a Binary, a List or a
  Struct as Float64, then failed to cast them: a Decimal's arm accepted any other
  side. It takes two numbers now;
- the merge sweep, `TestEveryAggregateMergeIsCovered`, asks `Item` of groups of one
  row each, the only kind it answers;
- the one-row sweep classes the bounds and `IsSorted` apart, each answering a group
  of one null with a value, as Polars does;
- the call functions number 213, and a Decimal's aggregates' types are stated.

## 5. Docs

- **Aggregation:** `Item`, the bounds and `IsSorted`.
- **Types and nulls:** `ToPhysical` and `Reinterpret`.
- **Coming from Polars:** three rows.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`:** the features, and the
  frame-wide aggregate's cost.

## 6. Teeth, all 14 biting

- `Arctan2`'s sides swapped;
- an Enum's physical type its storage's;
- `Reinterpret`'s sign swapped;
- `Item` of several taken, and its `allowEmpty` ignored;
- the bounds swapped, and a float's finite;
- a null after a value taken first;
- NaN compared as IEEE compares it;
- values never compared;
- the fast path never finding one out of order, and its NaN the least;
- `Limit` as `Tail`, `TrueDiv` as `FloorDiv`.

**Gate:** test-all 130 ok, race 26 ok (the root package 513 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
