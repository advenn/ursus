# Step 338 — as built

**`RollingRankBy` and `RollingMap`,** Polars' `rolling_rank_by` and `rolling_map`: the
rest of `polars-parity.md` §2's rolling row, with step 337's `Expr.Rolling`.

## 1. The API

| Polars 1.44.1 | ursus |
| --- | --- |
| `pl.col("x").rolling_rank_by("t", "3d", method="average")` | `Col("x").RollingRankBy(Col("t"), Every("3d"), RankAverage)` |
| `..., min_samples=2, closed="both"` | `MinSamples(2)`, `WindowClosed(ClosedBoth)` |
| `pl.col("x").rolling_map(f, 5)` | `Col("x").RollingMap[float64, float64]("f", Float64, 5, f)` |
| `..., min_samples=1, center=True` | `MinSamples(1)`, `RollingCenter()` |

**`RollingRankBy`** is `RollingRank` over `RollingSumBy`'s windows: the in-memory
by-time kernel already reached `rollingRank` with each row's window; only the method
and its type were missing. A rank by time is typed as a rank, a Float64 by average and
a UInt32 by min, max or dense (`rollingRankOut`, shared with `RollingRank`); ordinal is
refused, as Polars has none.

**`RollingMap`** is a generic method, as `MapBatches` is: `fn(window []In, ok []bool)
(Out, error)`, the window's values in its order and whether each is there, a null's the
zero value. The window is the row and the size−1 before it, fewer at a partition's
start, or centred with `RollingCenter`; one of fewer values than `MinSamples`, size
unless set, as Polars', is null and `fn` is not called. Polars' `weights` are not
taken.

## 2. How

- **`expr.WinRollingMap`,** appended so no op moves, `IsRolling`: its parameters hold
  an `*expr.RollingMapFn`, the name, the declared type and the kernel's function
  behind `UDFImpl`, its identity minted from the UDFs' counter (`NewRollingMap`).
  `WinParams.args` renders the name, so two windows of one function share a temporary.
- **`plan.CheckUDFNames`** takes a rolling map as a UDF: two of one name are refused,
  and a UDF and a rolling map of one name too; one used twice is one.
- **`kernel.WindowMap`** is the function with its types erased; `winRolling` gives it
  each partition's rows and each row's window, trailing or centred, as a rank's are,
  and `RollingMap`'s closure gathers each window, counts its values, calls `fn`, and
  recovers a panic as `MapElements`' does, naming the row's value.
- **A rank by time over one partition past a memory limit is refused,** by name in
  both one-partition refusals' hints: `rollingByOf` leaves it out of the sorted pass,
  whose walk (`RollingByNext`) keeps a window's values as floats, which would tie
  integers and instants a float cannot tell apart. Over partitions it spills a
  partition at a time.

## 3. Tests

**`TestRollingRankByAndRollingMap`,** Polars 1.44.1's answers: four rank methods, two
values at least, both ends closed, the types, ordinal refused; a rolling map's sum of
three, one value at least, centred, a length counting nulls, integers, within each
partition in an order; one function used twice, two of one name refused, a UDF's name
too, a panic and an error; past a memory limit a map streams over one partition, a
rank by time spills by partitions and is refused over one.

**Teeth (7 bite):**

- a map's window a row short;
- a centred map trailing;
- a window's nulls counted as values;
- a function's error dropped;
- a map's name unchecked;
- a rank by time typed as its value;
- a rank by time walked.

## 4. Performance

A million rows, the median of five, against Polars 1.44.1:

| | ursus | Polars |
| --- | --- | --- |
| `RollingMap` of a Go sum over 20 rows | 95.7 ms | 476 ms over a tenth of the rows, its function Python |
| `RollingRankBy(t, "100i")` | 293 ms | 275 ms |
| `RollingRank(100)` | 283 ms | 406 ms |

**Gate:** test-all 135 ok, race 27 ok (the root package 645 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
