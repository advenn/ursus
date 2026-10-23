---
description: >-
  Lists and structs in ursus, the Go dataframe library: building list columns,
  asking questions of each list, sorting, joining and evaluating expressions over
  their elements, set operations, explode, and struct columns with fields, updates and
  JSON.
---

# Lists and structs

A `List` column holds a list of values a row, all of one type; a `Struct` column holds
named fields a row. Both come from Parquet and Arrow as they are, and are built from
other columns as below.

## Building lists

`Str().Split` makes a list a row, as does any aggregation that gives several values,
`Implode` among them:

--8<-- "func.baskets.md"

--8<-- "Example_listBuild.md"

`ConcatList(exprs...)` joins columns and lists into one list a row, and a Go slice is a
`List` literal: `ursus.Lit([]string{"apple", "pear"})`.

## Questions of each list

Methods for lists sit behind `List()`, and each answers once per row:

--8<-- "Example_listQuestions.md"

- `Len`, `Get(i)`, `First` and `Last`; `Get` is null past the end;
- `Contains(v)` and `CountMatches(v)`;
- `Sum`, `Mean`, `Min`, `Max`, `Median`, `Std`, `Var`, `Quantile` and `NUnique`;
- `Any` and `All` for lists of `Bool`.

## Transforming lists

`Sort`, `Reverse`, `Unique`, `Head`, `Tail`, `Slice`, `Shift` and `Diff` give a list
back; `Join(sep)` joins a list of text into one string. `Eval(expr)` runs any
expression over each list's elements, `Element()` naming the element:

--8<-- "Example_listTransform.md"

`Filter(pred)` keeps the elements a predicate holds for, and in `Eval` a window or a
rank is over the list's elements:

--8<-- "Example_listFilterEval.md"

## Set operations

`SetIntersection`, `SetUnion`, `SetDifference` and `SetSymmetricDifference` combine two
lists a row, in the order elements first appear:

--8<-- "Example_listSets.md"

## Back into rows

`Explode` gives every element its own row, several list columns zipped together; see
[Reshaping](reshaping.md#lists-into-rows-explode):

--8<-- "Example_listExplode.md"

## Arrays

*New in 0.6.7.* An `Array(T, n)` is a fixed-size list: each row holds `n` values of
`T`, or is null. A Go array is one, `[3]float64` an `Array(Float64, 3)`, in `FromRows`
and back in `Rows`; `List().ToArray(n)` makes one of a list whose every row holds `n`,
as a cast to the type does, and refuses one that does not, as Polars does;
`Arr().ToList()` is the list again:

--8<-- "Example_arrays.md"

`Arr()` is Polars' `arr` namespace, a list's functions of the same elements: `Sum`,
`Mean`, `Max`, `ArgMin`, `Get`, `Contains`, `Join`, `Unique`, `Head` and the rest
answer as `List()`'s do, a value or a list a row; `Reverse`, `Sort`, `Shift` and
`Eval` of an element's value keep each array's length and so its Array; `ToStruct`
makes a field of each element, `field_0` on unless named, and `Dot` sums two arrays'
products.

An Array is read and written as Polars writes it: an Arrow `FixedSizeList` in IPC, and
in Parquet a list whose size the file's Arrow schema keeps; as JSON, a list.

## Structs

`Struct(exprs...)` packs columns into one struct column, a field for each, named as
its expression:

--8<-- "Example_structBuild.md"

Methods for structs sit behind `Struct()`:

--8<-- "Example_structFields.md"

- `Field(name)` reads one field, and `Field` of several names reads each as a column;
- `WithFields(exprs...)` replaces fields of the same names and adds new ones; read a
  field inside it through `Field`, as anywhere;
- `RenameFields(names...)` renames them in order, and *new in 0.6.7*,
  `Name().PrefixFields`, `SuffixFields` and `MapFields` rename each from its name;
- *new in 0.6.7*, `Drop(names...)` leaves fields out, as Polars' `struct.drop`, and
  refuses a name that is no field;
- `JsonEncode` writes each struct as JSON text, and `Str().JsonDecode(type)` reads it
  back;
- `Unnest` turns the fields into columns, as the frame's `Unnest` does.

A struct is also how ursus gives several values a row: `SplitExact`, `ExtractGroups`,
`ValueCounts` of an expression, and `Rle`.

## What is not here yet

Polars' `reshape` and `concat_arr`, which make Arrays of columns, are not, and neither
are lists of lists or of structs, which are refused by name. A List or a Struct cannot be a group-by, join or
`Unique` key. The scope records each in `feature-gaps.md`.
