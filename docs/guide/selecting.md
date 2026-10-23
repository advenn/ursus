---
description: >-
  Selecting columns and filtering rows in ursus, the Go dataframe library: columns by
  name, pattern and type, predicates, nulls in filters, sorting, top-k, unique rows,
  slices and row indexes.
---

# Selecting and filtering

## Columns

`Select` keeps the columns it names, in its order. `Col` names columns,
`ColRegex` matches them by a pattern, and `ColDType` by their type, so the same query
adapts when the schema grows:

--8<-- "Example_selectByName.md"

`Exclude` is every column but the named ones:

--8<-- "Example_selectExclude.md"

*New in 0.6.7:* package `selector` picks columns by what they are, as Polars'
selectors do: `Numeric`, `Integer`, `Float`, `Temporal`, `String` and the other
types; `StartsWith`, `EndsWith`, `Contains` and `Matches` by name; `ByName`,
`ByIndex`, `First` and `Last`; and their `Union`, `Intersect`, `Difference`,
`SymmetricDifference` and `Complement`, Polars' `|`, `&`, `-`, `^` and `~`, named
so that a selector's complement is never taken for an expression's negation, and
`Exclude`. A selector's `Expr` is the expression of its columns, and `Expand` the
names it selects in a schema:

--8<-- "Example_selectSelectors.md"

`Drop` removes columns and `Rename` renames them, from a map of old names to new:

--8<-- "Example_selectDropRename.md"

An expression is named after the first column it reads, and `Alias` gives it one
name. *New in 0.6.7:* `Name()` has Polars' name functions, which name each column a
multi-column expression makes: `Prefix`, `Suffix` and `Map`; `ToLower` and
`ToUpper`; `Replace`, of a regular expression or of the text; and `Keep`, the first
column's name through any alias:

--8<-- "Example_selectNames.md"

For a struct, `PrefixFields`, `SuffixFields` and `MapFields` rename its fields and
leave the column's name, as Polars' `name.prefix_fields` and the rest do.

*New in 0.6.7.* `InsertColumn(i, column)` puts a column at a place, an expression or
Go values, as Polars' `insert_column` does; a negative place counts from the end, −1
before the last column:

--8<-- "Example_selectInsertColumn.md"

`Select` can also compute columns, as `WithColumns` does: see
[Expressions](../concepts/expressions.md). A pattern is explicit: ursus does not read
`Col("^qty$")` as a regular expression, as Polars does, so a column named with a `^`
can still be selected by name.

## Rows

`Filter` keeps the rows where every predicate is true; several predicates are and-ed.
`IsIn` tests membership and `IsBetween` a range, both ends included unless a `Closed`
says otherwise:

--8<-- "Example_filterPredicates.md"

`Or`, `And` and `Not` combine predicates into one:

--8<-- "Example_filterOr.md"

An aggregate in a predicate is over the whole frame, so a filter can compare each row
with the frame: here, the orders larger than the mean, 6.625.

--8<-- "Example_filterAggregate.md"

### Nulls in a filter

A comparison with a null is null, neither true nor false, and `Filter` keeps only the
rows where its predicate is **true**, as SQL's `WHERE` does. So `Filter(p)` and
`Filter(p.Not())` together can miss rows: the ones where `p` is null.

`Remove` is the opposite of `Filter`: it drops the rows where any predicate is true.
It drops the rows where one is null too, so `cai`, with no score, is in neither:

--8<-- "Example_filterNulls.md"

!!! note "Polars' `remove` keeps those rows"

    Polars' `remove(p)` keeps a row where `p` is null; ursus's drops it, as its
    `Filter` does. Write the null case out to keep it:
    `Remove(p.And(p.IsNotNull()))`, or `Filter(p.Not().Or(p.IsNull()))`.

## Sorting

`Sort` takes keys, `Asc` or `Desc` of an expression, the first key first:

--8<-- "Example_sortKeys.md"

A sort is stable, so rows equal on every key keep their order. Nulls come first in
either direction, unless a key says `NullsLast()`:

--8<-- "Example_sortNulls.md"

## The largest rows

`TopK(k, keys...)` keeps the k largest rows by its keys, and `BottomK` the k smallest,
without sorting the whole frame. Write each key `Asc`: `TopK` takes the largest by it,
and a `Desc` key ranks that column the other way, as Polars' `reverse` does. Nulls
never make the cut.

--8<-- "Example_topK.md"

## Unique rows

`Unique(names...)` keeps the first row of each distinct key, in the rows' order, or of
each distinct row when no names are given:

--8<-- "Example_unique.md"

`UniqueKeep` chooses which row a key keeps: `KeepFirst`, `KeepLast`, `KeepAny`, or
`KeepNone`, which keeps only the keys seen once. *New in 0.6.7.*

--8<-- "Example_uniqueKeepLast.md"

## Slices

`Head`, `Tail` and `Slice(offset, length)` take rows by position. A negative offset
counts from the end, as Polars counts it (*new in 0.6.7*), and `Reverse` turns the
frame around:

--8<-- "Example_slices.md"

*New in 0.6.7.* `HashRows(seed, columns...)` is each row's hash over its columns, every
column when none is named, as Polars' `hash_rows`: rows equal as a group-by sees them
hash alike. The numbers are ursus's own, not Polars'.

--8<-- "Example_selectHashRows.md"

`WithRowIndex(name, offset)` numbers the rows, from offset:

--8<-- "Example_withRowIndex.md"

ursus has no index of its own, as pandas has: row order is a property of a frame, not
a label space, and a row index is a column like any other.
