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

*New in 0.6.7.* `ConcatRelaxed` and `ConcatDiagonalRelaxed` meet a column's types where
the default refuses them, at Polars' supertype: an `Int64` beside a `Float64` at
`Float64`, which may round; a number beside text at text. The default meets types only
where every value comes out as it went in.

--8<-- "Example_reshapeConcatRelaxed.md"

`lf.Concat(others...)` and `VStack` are the same for one frame. Concatenation streams:
the frames are read one after another.

## Aligned by key

*New in 0.6.7.* `ConcatAlign` puts frames side by side by key, as Polars'
`concat(how="align")`: it joins them on the columns every frame has, a full join, and
sorts by those columns. `ConcatAlignInner`, `ConcatAlignLeft` and `ConcatAlignRight`
join so instead. A column in some frames but not every one would be two columns of
one name, and is refused; Polars keeps the first frame's.

--8<-- "Example_reshapeConcatAlign.md"

The joins spill past a memory limit, and the rows come out in the same order with one
or without, ties in each frame's order.

## Side by side

`HStack` places frames side by side, row i of each together, as
`Concat` with `ConcatHorizontal` does. The frames must be the same height and their
column names distinct. *New in 0.6.7:* `ConcatHorizontalExtend` pads a shorter frame's
columns with nulls instead, as Polars' `horizontal_extend`. Either reads its frames in
step, holding a batch of each.

--8<-- "Example_reshapeHStack.md"

## Transpose

`Transpose` turns rows into columns. `IncludeHeader` keeps the old column names as a
column, and `ColumnNames` names the new ones. *New in 0.6.7:* `ColumnNamesFrom` takes
their names from a text column instead, which is left out, as Polars' `column_names`
given a column's name:

--8<-- "Example_reshapeTranspose.md"

Every column of the answer has the type its values meet at, Polars' supertype, so
integers beside floats become floats and, *new in 0.6.7*, text beside numbers text,
where it was refused. It is a `DataFrame`'s only: its columns are the frame's rows, so
its schema waits for the data.

## Dummies

*New in 0.6.7.* `ToDummies` replaces a column with a `Uint8` column for each of its
distinct values, 1 where a row holds the value and 0 where it does not, as Polars'
`to_dummies`. A null is a value of its own. Each dummy is named the column, a
separator, `_` by default, and the value as Polars displays it, so `1.0` is `1.0` and
an instant `2024-01-02 03:04:05`. A column's dummies stand in its place, ordered by
name as Polars orders them, so `10` comes before `2`:

--8<-- "Example_reshapeDummies.md"

`DropFirst` leaves out the first value to appear that is not null, which the others
imply, and `DropNulls` the null. `Columns` names the columns to encode, every column
when empty, and a `Series` has its own `ToDummies`. The values are numbered in one
pass, as a group-by numbers its groups, so a column of many distinct values costs no
more than one of few, beside the dummies it makes.

## Unstack

*New in 0.6.7.* `Unstack` lays each column out as a grid without keys, as Polars'
`unstack`. Vertically, a column `x`'s values go `Step` at a time down new columns
`x_0`, `x_1` and on, `Step` rows high; with `UnstackHorizontal`, `Step` at a time
across `Step` new columns:

--8<-- "Example_reshapeUnstack.md"

The slots after the last value are null, or `Fill`, one value for every column or
one for each, cast to the column's type as `ExtendConstant` casts it. Each suffix is
zero-padded to the digits of the count of new columns, `x_00` to `x_12` for 13, as
Polars pads it. A vertical column shares the old one's values, a slice of them.

## Split by key: partition

*New in 0.6.7.* `PartitionBy` splits a frame into a part for each distinct key, in
the order the keys first appear, as Polars' `partition_by`. Each part has its `Key`, a
one-row frame of the key's values, typed as the columns are, beside its `Frame`, the
rows holding that key. Go has no map keyed by a tuple, so the parts are a slice where
Polars' `as_dict` gives a dict. `DropKey` leaves the key columns out of each frame:

--8<-- "Example_reshapePartitionBy.md"

Keys group as a group-by groups them: a null key is a part of its own, and -0 and 0
are one key. It is a `DataFrame`'s, and its parts together hold what the frame did.
