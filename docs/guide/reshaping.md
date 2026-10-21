---
description: >-
  Reshaping frames in ursus, the Go dataframe library: pivot wide, unpivot long,
  explode lists into rows, unnest structs into columns, concatenate frames
  vertically, diagonally or side by side, and transpose.
---

# Reshaping

## Wide: pivot

`Pivot` spreads a column's values into columns of their own: a row for each distinct
`Index`, and a column for each distinct `On` value, holding `Values`. `Agg` reduces the
values that fall in one cell; without it, a cell of two values is refused.

--8<-- "Example_reshapePivot.md"

A cell is `Agg` of the values that fall in it. East sold no figs, so its cell is the
sum of nothing: Polars' sum gives 0 there; here, as for every sum of nothing, it is
null (see [Coming from Polars](../getting-started/coming-from-polars.md#where-ursus-answers-differently)).
A count of nothing is 0, as Polars' is (*fixed in 0.6.7*; before, every such cell was
null). New columns come in the order their values first appear, or sorted with
`SortColumns`.

*New in 0.6.7.* `Pivot` is a `LazyFrame`'s too, as Polars' `LazyFrame.pivot` is, its
new columns given in `OnColumns`, since a lazy plan knows its columns before it reads
the data. A value given that the data never holds is a column of empty cells, plum
here; one the data holds and `OnColumns` leaves out is left out, fig here. A
`DataFrame`'s pivot finds them itself. Either way a group-by of the index and the
`On` values reduces the rows first, and spills past a memory limit as any group-by
does.

--8<-- "Example_reshapePivotLazy.md"

*New in 0.6.7.* Several `On` columns name a new column by their values together, as
Polars writes them, text quoted:

--8<-- "Example_reshapePivotSeveral.md"

## Long: unpivot

`Unpivot` is the opposite: several columns become two, which column and what it held.
`Index` names the columns carried along; empty, it is every column not in `On`.

--8<-- "Example_reshapeUnpivot.md"

*New in 0.6.7.* `On` may be left out instead: it is then every column not in `Index`,
as Polars' `on=None`, and every column when `Index` is left out too. The melted
columns share one value column, so their types must meet in one that holds each of
them exactly: an Int32 beside an Int64 melts into an Int64, but text beside a number
is refused, where Polars writes the numbers as text. Cast first.

--8<-- "Example_reshapeUnpivotRest.md"

!!! note "The row order is not Polars'"

    Polars gives every row's first column, then every row's second. ursus gives each
    row's columns together, then the next row's, because it unpivots a batch at a time
    and its order must not depend on the batches. Sort afterwards when the order
    matters.

## Lists into rows: explode

`Explode` gives each element of a list its own row, the other columns repeated:

--8<-- "Example_reshapeExplode.md"

Several columns explode together, zipped, so their lists must be the same length in
each row. An empty list and a null list each become one row holding a null.

## Structs into columns: unnest

`Unnest` turns a struct column into a column for each of its fields:

--8<-- "Example_reshapeUnnest.md"

## Concatenating

`Concat` stacks frames' rows. By default each frame must have the same columns in
the same order; an `Int32` and an `Int64` of one name meet at `Int64`.

--8<-- "Example_reshapeConcat.md"

`WithConcatMode(ConcatDiagonal)` takes the union of the columns instead, each frame's
missing ones null:

--8<-- "Example_reshapeConcatDiagonal.md"

`lf.Concat(others...)` and `VStack` are the same for one frame. Concatenation streams:
the frames are read one after another.

## Side by side

`HStack` places frames side by side, row i of each together. The frames must be the
same height and their column names distinct. It holds its inputs in memory, since
pairing rows means having each in hand; a join by a key is the streaming way.

--8<-- "Example_reshapeHStack.md"

## Transpose

`Transpose` turns rows into columns. `IncludeHeader` keeps the old column names as a
column, and `ColumnNames` names the new ones:

--8<-- "Example_reshapeTranspose.md"

Every column of the answer has the type its values meet at, so integers beside floats
become floats; text beside numbers is refused, where Polars casts everything to text.
Like `Pivot`, it is a `DataFrame`'s.
