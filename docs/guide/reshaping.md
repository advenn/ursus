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

East sold no figs, so its cell is null. Polars' sum gives 0 there; here, as for every
sum of nothing, it is null (see [Coming from Polars](../getting-started/coming-from-polars.md#where-ursus-answers-differently)).

`Pivot` is a `DataFrame`'s, not a `LazyFrame`'s: its columns are the data's values, so
the frame's schema is not known until the data is read, and a lazy plan needs it
first. New columns come in the order their values first appear, or sorted with
`SortColumns`.

## Long: unpivot

`Unpivot` is the opposite: several columns become two, which column and what it held.
`Index` names the columns carried along; empty, it is every column not in `On`.

--8<-- "Example_reshapeUnpivot.md"

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
