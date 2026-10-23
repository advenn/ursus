---
description: >-
  Missing data in ursus, the Go dataframe library: counting nulls, filling them with a
  value, a statistic or a neighbour, coalescing columns, interpolating, and dropping
  rows, with null kept apart from NaN.
---

# Missing data

A missing value is a **null**, in a column of any type. It is not NaN, a float that is
not a number, and ursus keeps the two apart everywhere: see
[Types and nulls](../concepts/types-and-nulls.md#null-is-not-nan). The examples read
six hours of temperatures with gaps, and a backup reading for some of them:

--8<-- "func.readings.md"

## Counting

`NullCount` counts a column's nulls, and over `All()` every column's:

--8<-- "Example_missingCount.md"

`IsNull` and `IsNotNull` test each value, for a `Filter` or a `When`.

## Filling with a value

`FillNullWith(v)` fills with a value, `FillNull(strategy)` with one the column gives:
`FillMean`, `FillMin`, `FillMax`, `FillZero` or `FillOne`. `Coalesce(exprs...)` takes
each row's first value that is not null, here the temperature, else the backup, else
−1:

--8<-- "Example_missingFillValue.md"

A fill meets the column's type: `FillNullWith(0)` of an `Int32` column stays `Int32`,
and so does `FillNullWith(2.0)`, a whole number. A fill with a fraction, `0.5`, widens
an integer column to `Float64`. Polars widens it for any float.

## Filling from the neighbours

`FillForward` carries the last value forward and `FillBackward` the next one back;
`ForwardFill(limit)` and `BackwardFill(limit)` fill at most that many rows of a gap.
`Interpolate` draws a straight line across a gap, and leaves a gap at either end as it
is:

--8<-- "Example_missingFillNeighbours.md"

They run in the rows' order; under `Over(keys...)`, within each partition, so a
sensor's gap is never filled from another sensor's readings.

*New in 0.6.7.* `InterpolateBy(by)` draws the line at each row's `by`, a time or a
number, where `Interpolate` steps by rows, as Polars' `interpolate_by`: readings at
hours 0, 1 and 4, the middle one missing, fill it a quarter of the way along. `by`
need not be sorted, and a null in it is refused, as Polars refuses it.

*New in 0.6.7.* A strategy takes a limit too, `FillForward.Limit(n)` and
`FillBackward.Limit(n)`, as Polars' `fill_null(strategy=..., limit=n)`, for a column or
for every column of a frame at once:

--8<-- "Example_missingLimit.md"

A limit is at least 1; leave it off for none. A limit on another strategy is refused,
as Polars refuses it.

## Dropping

`DropNulls(names...)` drops the rows with a null in any of those columns, or in any
column at all:

--8<-- "Example_missingDrop.md"

*New in 0.6.7.* `DropNans(names...)` drops the rows with a NaN in any of those
columns, or in any float column. A null is not a NaN, and its row stays:

--8<-- "Example_missingDropNans.md"

## The whole frame

*New in 0.6.7.* A frame's `Interpolate()` interpolates every column it can: numbers,
an integer becoming a `Float64`, and dates and times, which keep their type, as
Polars' `interpolate` does. Text and Booleans are left as they are:

--8<-- "Example_missingInterpolateFrame.md"

A frame's `FillNull(v)` fills every column that can hold a null and that `v` can
fill, chosen from the schema before any data is read. `hour`, built by `Values`, cannot
hold a null, and is left as it is.

--8<-- "Example_missingFrameFill.md"

## How nulls behave

- **Arithmetic** with a null is null, and so is a comparison.
- **Logic** is three-valued, as SQL's: `null.Or(true)` is true, `null.And(false)` false.
- **A filter** keeps the rows where its predicate is true, so a null drops a row.
- **An aggregate** skips nulls; a group of nulls alone sums to null, not 0.
- **A group-by** makes the nulls of a key one group, and **a join** matches no null key,
  unless `JoinNullsEqual(true)`.
- **A sort** puts nulls first, unless a key asks for `NullsLast()`.
