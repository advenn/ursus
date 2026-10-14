# Step 256 — as built

**E5 of `v0.6.5-scope.md`, last part: `Dt().OffsetBy` of an expression,** an offset
a row, as Polars' `offset_by(expr)`. E5 is then built, but for
`IsIn(Col("b").Implode())`, which follows E6's list literals (step 254).

## 1. What changed

**`dt.offset_by_expr`, in the horizontal block.** Its typing:

- the receiver must be a Date or Datetime. A Time or a Duration is refused, with the
  constant form's hint to add a Duration with `Add`;
- the offsets must be Strings. A Duration column is refused, with the same hint.

**The kernel (`internal/kernel/dtexpr.go`), `dtOffsetEach`:**

- each row's offset is read by `dtype.Every`, as `Every("1mo2d")` reads one, with
  each distinct offset cached;
- the offset is checked by `expr.OffsetRefusal`, the constant form's check. An offset
  that does not parse, or that is finer than the column holds (an hour on a Date), is
  an error naming the offset and its row, where the constant form is refused while
  the query is planned. Polars ignores an hour on a Date;
- a calendar offset moves the wall clock in the column's zone, and the rest moves
  elapsed time, as the constant form does;
- an answer past the type's range is refused, by `offsetRange`, which the constant
  form now shares;
- a null instant or offset gives null.

**Public:** `OffsetBy[T Span | Expr]`. An `Interval` or a `time.Duration` compiles
to the constant call as before.

## 2. Tests and teeth

**`TestOffsetByAnExpression`:**

- eight offsets, naive and in America/New_York, at batch sizes 1, 3 and 8192: a
  month to a shorter month's end, `1d2h` across the change to daylight time, a
  negative month, minutes, a week and a year, with a null instant and a null offset;
- a Date by a month and by a year;
- a literal offset.

Every answer is Polars 1.44.1's.

**`TestOffsetByAnExpressionRefuses`:**

- an offset that does not parse, named with its row;
- an hour on a Date;
- a Time;
- an integer offset.

| tooth | result |
| --- | --- |
| a month moves elapsed time | **bites** |
| the wall clock is read in UTC | **bites** |
| every row takes the first offset | **bites** |
| a null offset is read | **bites** |
| a bad offset is not named | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
