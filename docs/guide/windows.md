---
description: >-
  Window functions in ursus, the Go dataframe library: aggregates over partitions with
  Over, cumulative sums and maxima, shift, diff and percent change, rank, rolling
  windows by rows and by time, and exponentially weighted means.
---

# Window functions

A window function computes over a group of rows and gives an answer back on **every**
row, so the frame keeps its height. `GroupBy` reduces each group to one row; a window
keeps the rows and puts each group's answer beside them.

The examples read the quick start's orders, and a series of two tickers' closing
prices over six trading days, a weekend before the sixth:

--8<-- "func.prices.md"

## An aggregate beside every row

`Over(keys...)` computes an expression within each partition of rows with the same
keys. An aggregate under `Over` is its partition's value on each of its rows, which an
expression can then use, here for each order's share of its region:

--8<-- "Example_windowOver.md"

`Over()` with no keys is the whole frame as one partition: `all_qty` is the grand
total on every row. Rows keep their order, whatever the partitions.

## Ordered windows

Cumulative functions, shifts and ranks depend on the rows' order. Without one of its
own a window takes the rows as they come. `OverWith` gives a partition its order
with a `WindowSpec`, whatever order the frame is in:

--8<-- "Example_windowCumulative.md"

`CumSum`, `CumProd`, `CumMin`, `CumMax` and `CumCount` each take a `reverse` flag, to
accumulate from the end.

## Shift, diff and percent change

`Shift(n)` moves values n rows later within each partition, nulls where they were;
`Diff(n)` is the difference from n rows before, and `PctChange(n)` the change as a
fraction:

--8<-- "Example_windowShift.md"

`ShiftFill(n, value)` shifts with a value instead of nulls.

## Rank

`Rank(method, descending)` numbers the rows by value. The methods differ only on ties,
here two 9s and two 7s:

--8<-- "Example_windowRank.md"

| Method | Ties get |
|---|---|
| `RankOrdinal` | distinct ranks, in the rows' order |
| `RankMin` | the lowest rank of the tie |
| `RankMax` | the highest rank of the tie |
| `RankDense` | the same rank, with no gap after them |
| `RankAverage` | the mean of the tie's ranks, as a `Float64` |

Under `Over`, a rank is within its partition: the [expressions](../concepts/expressions.md)
page ranks each order in its region. A null's rank is null, and the values are ranked
among themselves, as Polars ranks them. *Changed in 0.6.7:* a null ranked as the least
value before, and took a position.

## Rolling windows by rows

`RollingMean(n)` is the mean of each row and the n − 1 before it, and so on for
`RollingSum`, `RollingMin`, `RollingMax`, `RollingStd`, `RollingVar`, `RollingMedian`,
`RollingQuantile`, `RollingSkew`, `RollingKurtosis` and `RollingRank`:

--8<-- "Example_windowRolling.md"

- a window needs all n values by default, so the first n − 1 rows are null;
  `MinSamples(k)` answers from k;
- `RollingCenter()` centres the window on its row.

## Rolling windows by time

A window by rows counts rows; a window by time counts time. `RollingSumBy(by,
period)` sums the rows whose `by` lies in the period up to each row's, `(t − period,
t]` by default. Across the weekend the two differ: Monday's three rows reach back to
Wednesday, and its three days only to Saturday.

--8<-- "Example_windowRollingBy.md"

A period is an `Every` interval: `"3d"`, `"2h"`, `"1mo"`, or a combination such as
`"1d12h"`; `m` is minutes and `mo` months. `WindowClosed` says which ends of the window
belong to it.

The `By` forms take a `Date`, `Datetime` or `Time` column, which need not be sorted:
a window is found by value, and rows tied with a row's instant are in its window.
Unlike the windows by rows they answer from one value by default. *New in 0.6.7:* `by`
can be an integer of 32 or 64 bits, a step rather than an instant, whose period counts
it in Polars' integer unit: `Every("3i")` is three steps, as in
[Dates and times](dates-and-times.md#an-integer-index).

## Exponentially weighted

`EwmMean`, `EwmStd`, `EwmVar` and `EwmSum` weight recent rows more. The decay is
`EwmAlpha`, `EwmSpan`, `EwmCom` or `EwmHalfLife`, as Polars' parameters are, and
`EwmAdjust(false)` uses the recursive form:

--8<-- "Example_windowEwm.md"

`EwmMeanBy(by, halfLife)` decays by time between rows rather than by rows, or by the
steps of an integer `by`, its half-life such as `Every("2i")`.

## Without Over

A window function with no `Over` runs over the whole frame in the rows' order:
`Col("close").CumSum(false)` is a running total from the first row to the last.

## A window inside a window

*New in 0.6.7:* a window function's input may hold another, and an aggregate, each
computed first in the outer window's partitions and order, as Polars computes them:
`Col("x").Shift(1).CumSum(false).Over(Col("g"))` shifts within each g and then sums
within each g, and `Col("x").Sub(Col("x").Mean()).CumSum(false).Over(Col("g"))` sums
each row's distance from its group's mean. A window with an `Over` of its own inside
another is computed within the outer one's partitions, so
`Col("x").Sum().Over(Col("g")).CumSum(false).Over(Col("h"))` sums g's values within
each h. An aggregate whose answer depends on the order, `First`, under an `OrderBy` is
refused, as an ordering over an aggregate is.

## Under a memory limit

Windows spill to disk under a memory budget, over many partitions or one huge one,
and give the answer they give in memory. A rolling window by time whose period holds
more than the budget fails rather than exceed it. See
[Memory and spilling](../concepts/memory.md).
