# Step 337 — as built

**The rolling of an expression,** Polars' `Expr.rolling`, the first item of
`polars-parity.md` §2's last open row; `rolling_map` and `rolling_rank_by` remain.

## 1. The API

`e.Rolling(index Expr, o RollingOptions) Expr`: e, an aggregate expression, over each
row's window, placed by o as the frame's `Rolling` places it, `(t + Offset, t + Offset
+ Period]` and `Closed`. `RollingOptions.GroupBy` is refused: `Over` gives the
partitions, as Polars' rolling takes none.

| Polars 1.44.1 | ursus |
| --- | --- |
| `pl.col("x").sum().rolling("t", period="2i")` | `Col("x").Sum().Rolling(Col("t"), RollingOptions{Period: Every("2i")})` |
| `(pl.col("x").max() - pl.col("x").min()).rolling(...)` | `Col("x").Max().Sub(Col("x").Min()).Rolling(...)` |
| `pl.col("x").rolling(...)`, a row's value | the list of its window's |
| `....rolling(...).over("g")` | `....Rolling(...).Over(Col("g"))`, the index sorted within each g |
| `group_by(...).agg(... .rolling(...))` | a list a group where rolled by windows; refused where by the rolling group |

**Two paths.** Where every aggregate in e is a `Sum`, `Mean`, `Min`, `Max`, `Var`, `Std`,
`Median` or `Quantile` over a value a row, and o gives no `Offset`, `rollingByWindows`
makes each its window by time, `RollingSumBy` and kin (`WinFn` of `PairOf(child,
index)`, `By`, `MinSamples` 1, o's `Closed`, the aggregate's ddof, q and interpolation):
an ordinary window expression, found by value, so the index need not be sorted, and in
`Agg` a list a group. Anything else is a `Reshape` of the new `ReshapeRolling` op,
carrying a `RollSpec`, which the zipper lifts into the frame's rolling group,
`Agg(e)`, a non-aggregate imploded, its column zipped back by position.

**Under `Over`, by the rolling group:** `plan.TemporalGroup.RowOrder` answers the
windows in their rows' order, their numbers, not a bucket at a time: the bucket sort is
skipped under the limit and keyed by the window alone past it. The sink checks the index
sorted within each bucket (`perBucket`), as Polars' rolling over a partition needs it,
and hands no window out before the input ends: a later row of one partition may come
after another's, which the frame's watermark would have closed.

**Where ursus differs** (§9): an empty window's sum is null (§9's rule); an index out
of order is answered by the windows by value, where Polars refuses it; under `Over` by
the rolling group the windows are held to the end; an index unsorted within a
partition is refused, where Polars answers from rows out of order. And found here, of
the frame's own: a keyed `Rolling` or `GroupByDynamic` wants the index sorted across
the frame, where Polars' want it within each group.

`kernel.rollingBy` skips its stable sort when a partition's `by` is already in order.

## 2. Tests

**`TestRollingOfAnExpression`,** Polars 1.44.1's answers but empty sums: eleven
expressions over an integer index (a sum, a range, a count, the rows, a row's list, no
offset, both ends closed, a first, a sort's first, over g); over a partition sorted
within itself, by windows and by the rolling group, `row_order` in the plan, a forward
window, a tie after another partition's later row, a row a batch; a datetime index
with an offset, a filter, two keys, an Int32 index; in an aggregation; refusals; the
two paths against each other over 400 random rows with ties, gaps, nulls and NaN, ten
aggregates, four closed ends, with and without `Over`; past a memory limit, streamed
and held.

**`TestARollingGroupInRowOrderSortsByItsWindows`:** the row-order sink's runs on disk
sorted by window alone.

**Teeth (15 of 16 bite):**

- a partition's rows in bucket order;
- row order as the buckets came;
- sorted across the frame under Over;
- the hold dropped;
- a row's values not a list;
- no keys from Over;
- in an aggregation lowered;
- a group-by in the options taken;
- the offset dropped;
- no aggregate rolled by its own window;
- a rolled window's ends ignored;
- a variance's ddof dropped;
- a quantile's q dropped;
- an offset rolled by the windows;
- a by out of order left so.

**Silent:** the rolling reshape's type in `Field`. Nothing reads it: the zipper lifts
the reshape into a plan before any type is asked, and the plan's own `Agg` types the
column. It is kept as the reshape's true type.

## 3. Performance

A million rows, a window of 100, the median of seven, against Polars 1.44.1:

| | by the rolling group | now | Polars |
| --- | --- | --- | --- |
| `x.Sum().Rolling(t)` | 1685 ms | 49.0 ms | 10.0 ms |
| `x.Max().Sub(x.Min()).Rolling(t)` | 2503 | 64.6 | 14.6 |
| `x.Sum().Rolling(t).Over(g)`, 1000 groups | 264 | 60.4 | 47.7 |
| `x.RollingSumBy(t)` | 48.9 | 41.8 | 18.2 |

The rolling group gives each row to each of its windows, a hundred times a row here;
the windows by time slide, a compensated sum, which is what is left between 49 and
Polars' 10 (§8).

**Gate:** test-all 135 ok, race 27 ok (the root package 789 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
