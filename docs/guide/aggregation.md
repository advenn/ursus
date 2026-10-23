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

*New in 0.6.7:* `Item(allowEmpty)` is a group's one value, as Polars' `item`, and
refuses a group of several, or of none unless `allowEmpty`, which makes it null.
`LowerBound()` and `UpperBound()` are the least and greatest value of the column's
type. `IsSorted(descending, nullsLast)` says whether a group's values come in order,
NaN the greatest and the nulls all first, or all last; it reads the rows in their
order, so a group-by holding it runs on one worker.

*New in 0.6.7.* `First` and `Last` take a value that may be null, so that `First(a)` and
`First(b)` come from one row. `FirstNonNull()` and `LastNonNull()` skip the nulls, as
Polars' `first(ignore_nulls=True)`, so two of them may come from different rows. A
group-by has each as a shorthand over every column, and `All()` gathers each column's
values a group into a list, as Polars' `group_by().all()`:

--8<-- "Example_aggFirstNonNull.md"

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
- *New in 0.6.7:* `TopKBy(other, k)` and `BottomKBy(other, k)` take k of them, a
  list a group, as Polars' `top_k_by` and `bottom_k_by`: each region's three
  biggest orders' products. A null in `other` comes last, and rows that tie on it
  are ordered by their values, the greater first for `TopKBy`, where Polars' sort
  orders them as it finds them.

## Lists per group

`Implode` gathers a group's values into a list. `Unique`, `Sort`, `Head` and the
other expressions that give several values give a list per group too:

--8<-- "Example_aggLists.md"

*New in 0.6.7:* any expression that is not an aggregate is each group's values as a
list, as Polars answers it: `Agg(Col("qty"))` is each group's quantities,
`Agg(Col("qty").Mul(2))` doubled, and a window runs a group at a time,
`Agg(Col("qty").CumSum(false))` each group's running total, and
`Agg(Col("qty").CumSum(false).Max())` its greatest. An expression that mixes an
aggregate with a row's value, `Col("qty").Sub(Col("qty").Mean())`, is still refused.

A list column can be exploded back into rows, or asked questions with `List()`.

## Filtering groups: having

*New in 0.6.7.* `Having(predicates...)` keeps the groups whose predicates all hold, each
an aggregate of its group, as Polars' `having` and SQL's `HAVING`: the regions with more
than two orders, here. It filters what `Agg` gives, the shorthands, and `Head` and
`Tail`; a group whose predicate is null is left out.

--8<-- "Example_aggHaving.md"

A predicate must be an aggregate and Boolean. Polars casts a number to a Boolean, so
that a sum keeps every group not 0; here it is refused.

## Each group's first and last rows

*New in 0.6.7.* `GroupBy(keys).Head(n)` keeps each group's first n rows as rows rather
than lists, as Polars' `group_by().head(n)`, and `Tail(n)` its last n. The key columns
come first, the groups in the order each first appears, and each group's rows in the
frame's order:

--8<-- "Example_aggHeadTail.md"

They are a window over the keys and a sort, so they spill past a memory limit and keep
their order there. `Head(0)` is no rows, where Polars gives a row of nulls for each
group.

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

*New in 0.6.7.* A frame's own `Sum`, `Mean`, `Median`, `Min`, `Max`, `Std(ddof)`,
`Var(ddof)`, `Quantile(q, interpolation)`, `Product` and `NullCount` aggregate every
column at once, one row, as Polars' frame methods do. A column the aggregate does not
take is kept, as a null: the mean of text is null, and a Boolean's mean is the share of
its trues.

--8<-- "Example_aggFrame.md"

*New in 0.6.7.* `Corr(ctx)` is each pair of columns' Pearson correlation, a Float64
column a column, as Polars' frame `corr`, which NumPy computes: a column holding a
null is NaN throughout, where `Corr` of two expressions skips the rows holding one.
`SumHorizontal(ctx, ignoreNulls)`, `MeanHorizontal(ctx, ignoreNulls)`, `MinHorizontal`
and `MaxHorizontal` are each row's reduction across every column, named `sum`, `mean`,
`min` and `max`, and `Fold(ctx, fn)` folds a function across the columns from the
first, as Polars' frame methods do.

`ValueCounts(exprs...)` counts the rows of each distinct value, most frequent first,
ties in the order their values first appear:

--8<-- "Example_aggValueCounts.md"

and `Describe` summarizes each column, as Polars' `describe` does:

--8<-- "Example_aggDescribe.md"

## A group's rows as a frame

*New in 0.6.7.* `Groups(ctx)` gives each group's rows as a frame beside its key, a Go
iterator, as Polars' `for key, df in df.group_by(...)`: the groups in the order each
first appears, each frame the rows as the frame holds them, and the key a one-row
frame named as the key expressions are, so a key computed from a column is the key's
and never changes the rows. `MapGroups(ctx, fn)` runs a Go function over each group's
frame and stacks the answers, which must agree in their columns and types, as Polars'
`map_groups`, for what an aggregation cannot say. Both run the query and hold its
answer, a frame a group, as `PartitionBy` does.

## Under a memory limit

A group-by spills to disk when its groups outgrow the query's memory budget, a
median's, a quantile's and an `NUnique`'s values included, and gives the answer it
gives in memory. `Implode` cannot divide its answer, and fails at the limit with an
error that says so. See [Memory and spilling](../concepts/memory.md).
