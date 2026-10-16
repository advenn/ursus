# Step 267 — as built

**F2 of `v0.6.6-scope.md`: selection by position, and the index functions.**
Polars' `nth`, `first` and `last` as selectors, `arg_where`, `arg_sort_by`, and
`Expr.arg_sort`, `arg_true` and `arg_unique`.

## 1. What changed

**`expr.IndexMatcher`,** a selector by position, in the order written:

- a negative index counts from the end;
- one past either end is refused when the query is planned, as Polars refuses it;
- like `Col("a", "b")` it expands, so `Nth(0, 1).Mul(2)` is two columns.

**Public selectors:**

- `Nth(i...)`;
- `First()` and `Last()`: with no name, the first and last columns; with names,
  `Col(names...).First()` and `.Last()`, as Polars' `first` and `last` are.

**Four reshapes,** each a UInt32 column of its own length, as Polars' indices are:

- **`ArgSort(descending, nullsLast)`:** in `Select`, a row index beside the values,
  sorted by the frame-level `Sort`, which is stable, so ties keep their order in
  either direction, as Polars' do;
- **`ArgTrue()`**, and `ArgWhere(pred)`, which is `pred.ArgTrue()`: a row index and
  the frame's `Filter`, under the column's own name, so a predicate that is not
  Boolean is refused naming it;
- **`ArgUnique()`:** a row index and the frame-level `Unique` of the values, each
  value's first row in the order they first appear, a null among them;
- **`ArgSortBy(keys...)`:** the indices that sort the frame by `SortKey`s, as
  `Sort(keys...)` sorts it. Its keys ride in a `struct_of` child, each key's
  direction and nulls-last in its Args.

**In an aggregation** each is a list a group, of positions within it, by three list
kernels in the 700 block, `listArgs`:

- `list.arg_sort`, a stable sort of the positions by the comparator the sorts use;
- `list.arg_true`;
- `list.arg_unique`, by the group-key encoding, a null a value.

**`ArgSortBy` in an aggregation** takes one key, which is its `ArgSort`. It refuses
several: Polars 1.44.1 answers `arg_sort_by("a", "b")` in a group-by with positions
past the group's end, `[[1, 1, 0], [2, 0]]` for groups of three rows and two.
`feature-gaps.md` §9 records it.

## 2. Tests and teeth

**`TestSelectionByPosition`:** `Nth` of one, from the end, of two, in an
expression; `First()`, `Last()`, `First("a")`; and `Nth(9)` refused. Each is Polars
1.44.1's answer.

**`TestIndexFunctions`:** every answer is Polars':

- `ArgWhere` of a Boolean and of a comparison;
- `ArgSort` four ways, ties in order;
- `ArgSortBy` of two keys, one descending, and of two keys with nulls last;
- `ArgUnique` of values and of a key;
- one beside the frame;
- in an aggregation, all five as List(Uint32), one descending;
- refused: two keys in an aggregation, and `ArgTrue` of integers in a `Select` and
  in an aggregation.

| tooth | result |
| --- | --- |
| a negative index counts from the start | **bites** |
| arg_sort keeps nulls first | **bites** |
| a group's arg_sort ignores its direction | **bites,** once a descending case was in the aggregation |
| arg_unique keeps every row | **bites** |
| arg_sort_by's keys all ascend | **bites** |
| a group's arg_true takes every element | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
