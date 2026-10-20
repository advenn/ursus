---
description: >-
  A ten-minute tour of ursus, the Go dataframe library: build a DataFrame from Go
  values, read a CSV, filter, add columns, group and aggregate, join, and read the
  answer back into Go structs.
---

# Quick start

This tour builds a frame, reads a file, and runs the queries most programs need. Every
block of code on this site is a Go example that the test suite runs, and the output
under it is what it printed.

The examples read [`sales.csv`](https://github.com/advenn/ursus/blob/master/docs/examples/testdata/sales.csv),
eight orders:

```text
order_id,date,region,product,qty,price
1,2026-01-03,north,apple,10,1.50
2,2026-01-03,south,apple,4,1.50
3,2026-01-04,north,pear,7,2.25
4,2026-01-05,east,pear,2,2.25
5,2026-01-05,south,fig,9,3.00
6,2026-01-06,east,apple,12,1.50
7,2026-01-07,north,fig,3,3.00
8,2026-01-07,south,pear,6,2.25
```

## A frame from Go values

`Frame` builds a frame from columns, and `Values` makes a column from a Go slice. Its
type follows the slice's: `[]int64` is an `Int64` column, `[]string` a `String` one.

--8<-- "Example_quickstartFrame.md"

`Frame` gives a `LazyFrame`, a query. `Collect` runs it and gives a `DataFrame`, the
answer.

## Reading a file

`ScanCSV` reads a CSV and infers its columns' types; an option overrides one. Here the
dates become a `Date`:

--8<-- "Example_quickstartScan.md"

`ScanParquet` reads Parquet the same way, and `ScanCSVReader`, `ScanParquetBytes`,
`ScanCSVFrom` and `ScanParquetFrom` read from memory or from any reader you open, an
object store's included.

## Selecting and filtering

`Filter` keeps the rows where an expression is true, and `Select` keeps or computes
columns:

--8<-- "Example_quickstartSelect.md"

Columns are named with `Col`, and an expression's methods build on it: `Eq`, `Gt`,
`Ge`, `And`, `Or`, `IsNull`. Methods have names because Go has no operator
overloading: `Col("qty").Gt(3)`, not `qty > 3`.

## Adding columns

`WithColumns` adds columns, or replaces those of the same name. `When`, `Then` and
`Otherwise` choose a value by a condition:

--8<-- "Example_quickstartWithColumns.md"

## Grouping

`GroupBy` takes the keys, and `Agg` the aggregates to compute for each group:

--8<-- "Example_quickstartGroupBy.md"

A sum of integers is an `Int128`, so it cannot overflow; cast it if you need an
`Int64`. Groups come out in no promised order: sort them, or call `MaintainOrder()`
for the order their keys first appear in.

## Joining

`Join` matches rows by keys. `JoinHow` picks the kind: inner by default, and left,
right, full, semi, anti and cross. A row with no match on the other side takes nulls:

--8<-- "Example_quickstartJoin.md"

## Back into Go

`Rows` decodes a frame into a slice of structs, matching fields to columns by an
`ursus` tag, by name, or by name ignoring case:

--8<-- "Example_quickstartRows.md"

`df.Column[T](name)` gives one column as a typed `Series[T]`, and `df.At[T](row, name)`
one value and whether it was null.

## What will run

`Explain` shows the plan the optimizer made. Here the scan reads two of the file's six
columns, and the `Select` is gone, because the scan already gives just those two:

--8<-- "Example_quickstartExplain.md"

## Next

- [Coming from Polars](coming-from-polars.md): the names you know, and where ursus
  differs.
- [Lazy and eager](../concepts/lazy-and-eager.md): when a query runs, and how to stream
  its answer.
- [Expressions](../concepts/expressions.md): the same expression per row, per group and
  over a window.
- [Memory and spilling](../concepts/memory.md): running a query bigger than memory.
