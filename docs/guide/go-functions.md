---
description: >-
  Go functions inside ursus queries: MapElements runs a Go function on each value and
  MapBatches on a column at a time, typed by generic methods, with nulls, errors and
  concurrency handled by the Go dataframe library.
---

# Go functions

When no expression says what you need, a Go function can: it runs inside the query,
on every core, beside the expressions. A function call in Go is cheap, so a
user-defined function costs far less here than a Python function does in Polars.

## A value at a time: MapElements

`MapElements(name, type, fn)` calls `fn` on each value. The function's Go types are
the column's in and the answer's out, inferred from `fn` by a generic method:

--8<-- "Example_udfElements.md"

An answer can be of any type, and a mapped column is an expression like any other, a
group-by's key here:

--8<-- "Example_udfTypes.md"

**Nulls pass through:** `fn` is never called with one, so it cannot mistake a missing
value for a zero. Three of the six rows were null, and the function ran three times:

--8<-- "Example_udfNulls.md"

## A column at a time: MapBatches

`MapBatches(name, type, fn)` calls `fn` with a batch of values and whether each is
there, and takes back the same: the place to decide what a null means, or to call
something that works on many values at once.

--8<-- "Example_udfBatches.md"

## A row at a time: MapRows

*New in 0.6.7.* A frame's `MapRows(ctx, fn)` calls `fn` on each row, as Polars'
`map_rows`. A row is a struct, read as `Rows` reads one, or a `[]any` of its values in
the columns' order; a struct answer is a column a field, and any other answer one
column named `map`. The rows are decoded and `fn` called once each, in order, on one
goroutine, so an expression or `MapBatches` is the faster way where one can say it.

## A whole column: MapColumns

*New in 0.6.7.* A frame's `MapColumns(ctx, names, fn)` replaces each named column with
`fn` of it, as Polars' `map_columns`: the answer keeps the column's name and place,
and is as long as the frame, or one value, which every row takes.

## Errors

An error `fn` returns fails the query, naming the function, the value and the row of
its batch. A panic in `fn` is reported the same way, rather than ending the program.

--8<-- "Example_udfError.md"

## The name

The name is how `Explain` and every error show a function, since a closure prints as
nothing. Two different functions given one name are refused before the query runs:

--8<-- "Example_udfExplain.md"

A function used twice should be built once and held in a variable, so it is one
function under one name.

## What the function must do

- **Be safe to call from several goroutines.** A query runs on every core, and there
  is no opting one expression out: a closure over a shared map is a data race. Count
  with `sync/atomic`, as the null example does.
- **Not depend on the order it is called in.** The answer's order is the rows', the
  calls' is not.
- **Answer the type it declares.** The declared type is the column's from the plan's
  start; an answer of another type fails the query.
