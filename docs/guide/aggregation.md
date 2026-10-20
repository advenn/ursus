---
description: >-
  Aggregation and group-by in ursus, the Go dataframe library: sums, means, medians
  and counts per group, keys computed from expressions, conditional aggregates, lists
  per group, whole-frame aggregates, value counts and describe.
---

# Aggregation and group-by

`GroupBy(keys...)` splits the rows by their keys, and `Agg(exprs...)` computes each
expression once per group: one row per group comes out, its keys and its aggregates.

## The aggregates

--8<-- "Example_aggStatistics.md"

The usual statistics are all there:

- `Sum`, `Mean`, `Median`, `Min` and `Max`;
- `Std(ddof)` and `Var(ddof)`;
- `Quantile(q, interpolation)`;
- `Product`;
- `Skew`, `Kurtosis` and `Entropy`;
- `Corr`, `Cov` and `CorrSpearman` of two columns.

Every aggregate skips nulls. A mean is over the values that are there, and a group
with no values sums to null, not 0. An integer sum is an `Int128`, so it cannot
overflow: cast it to `Int64` if you want Polars' type.

Counting has several forms, each one value per group:

--8<-- "Example_aggCounting.md"

- `Len()` counts a group's rows;
- `Count()` counts a column's values that are not null;
- `NUnique()` counts its distinct values, a null not among them;
- `First()` and `Last()` are its first and last value in the rows' order.

## Keys

A key is any expression, not only a column. Alias a computed key to name it:

--8<-- "Example_aggByExpression.md"

Several keys group by their combinations:

--8<-- "Example_aggSeveralKeys.md"

**Groups come out in no promised order:** they are built on every core at once.
Sort the answer, as these examples do, or ask for the order the keys first appear in
with `MaintainOrder()`:

--8<-- "Example_aggMaintainOrder.md"

A null key is a group of its own, as in SQL's `GROUP BY` and Polars.

## Conditional aggregates

An aggregate's input is an expression, so it can be narrowed or computed first:

--8<-- "Example_aggConditional.md"

- `Filter(pred)` before an aggregate keeps a group's rows where pred is true:
  `apple_qty` sums only the apples;
- a `Bool` sums as 0 and 1, so the sum of a condition counts the rows that meet it;
- `MaxBy(other)` and `MinBy(other)` take a column's value where another column is
  largest or smallest: each region's biggest order's product.

## Lists per group

`Implode` gathers a group's values into a list. `Unique`, `Sort`, `Head` and the
other expressions that give several values give a list per group too:

--8<-- "Example_aggLists.md"

A list column can be exploded back into rows, or asked questions with `List()`.

## Every column at once

`GroupBy(...)` has shortcuts that aggregate every column but the keys:

- `Sum()`, `Mean()`, `Median()` and `Quantile(q, interpolation)` take the numeric
  columns;
- `Min()`, `Max()`, `First()`, `Last()` and `NUnique()` take every column;
- `Count()` counts each group's rows into a column named `count`, and `Len(name)`
  under a name of your own.

--8<-- "Example_aggShortcut.md"

## The whole frame

An aggregate in `Select` is one value for the whole frame, so a `Select` of
aggregates is one row. `GroupBy()` with no keys is the same, written as a group-by:

--8<-- "Example_aggWholeFrame.md"

`ValueCounts(exprs...)` counts the rows of each distinct value, most frequent first,
ties in the order their values first appear:

--8<-- "Example_aggValueCounts.md"

and `Describe` summarizes each column, as Polars' `describe` does:

--8<-- "Example_aggDescribe.md"

## Under a memory limit

A group-by spills to disk when its groups outgrow the query's memory budget, a
median's, a quantile's and an `NUnique`'s values included, and gives the answer it
gives in memory. `Implode` cannot divide its answer, and fails at the limit with an
error that says so. See [Memory and spilling](../concepts/memory.md).
