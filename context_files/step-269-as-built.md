# Step 269 — as built

**L of `v0.6.6-scope.md`: the list namespace's missing methods.** These are Polars'
`list.concat`, `shift`, `diff`, `count_matches`, `any`, `all`, `item`, `sample`, and
`sort` with `nulls_last`. Polars has no `list.shuffle`; `sample`'s `shuffle` is it.

## 1. What changed

**Two built on what exists:**

- **`Concat(others...)`** is `ConcatList(l, others...)`, step 266's. A null list among
  them makes the row's null, as Polars makes it;
- **`Diff(n, dropNulls)`** is `Eval(Element().Diff(n))`, step 268's, a window a list
  at a time. Polars' null_behavior `"drop"` slices off the first n.

**Six kernels** (`internal/kernel/listmore.go`):

- **`list.shift`,** in the horizontal block: the count is a constant or a value a
  row, as Polars' is, and a null count gives a null list;
- **`list.count_matches`,** in the horizontal block: a UInt32 count of the elements
  equal to the value, as == compares, by the group-key encoding. A null value counts
  the null elements, as Polars counts them;
- **`list.any` and `list.all`:** with `ignoreNulls`, Polars' default, a null is
  skipped, so an empty list is false and true. Without it they are Kleene's;
- **`list.item`:** a list of another length than one is refused naming its row, as
  Polars refuses it, but an empty one with `allowEmpty`, which is null;
- **`list.sample`:** n elements, or a fraction of the length rounded down, with or
  without replacement. A list shorter than the sample is refused without replacement,
  as Polars refuses it. The sample keeps the list's order unless shuffled. Each list
  draws from a PCG seeded by the seed and an FNV hash of its elements' encodings, so
  the draw does not depend on how the rows are batched, and two equal lists draw
  alike.

**`list.sort`** takes an optional nulls-last flag, absent where the existing callers
pass one argument.

**Public (`expr_list.go`):**

- `Concat`, `Shift`, `Diff`, `CountMatches` and `SortWith(descending, nullsLast)`;
- `Any(ignoreNulls)`, `All(ignoreNulls)` and `Item(allowEmpty)`: the flags are spelled
  out where Polars defaults them;
- `SampleN` and `SampleFraction`, always seeded. Polars draws a seed when none is
  given.

`Sort` and `SortDesc` are unchanged.

## 2. Tests and teeth

**`TestListMethods`:** every answer is Polars 1.44.1's on a frame of null lists, empty
ones and null elements:

- concat of a list and of a value;
- shift forward, back and by a column;
- diff by one and two, and with nulls dropped;
- sorts with nulls last, both ways;
- count_matches of a value and of null;
- any and all, skipping nulls and Kleene's;
- item with empty allowed;
- refused: item of an empty list and of three, a count of a string, any of integers.

**`TestListSample`.** A draw cannot be Polars', whose generator is its own (§6.3 of the
scope), so it holds the properties:

- Polars' lengths, with replacement and by a half;
- each drawn value is from its list;
- the same seed draws alike;
- a whole list unshuffled is the list, and shuffled keeps its sum;
- a sample larger than a list is refused without replacement.

| tooth | result |
| --- | --- |
| shift moves the other way | **bites** |
| count_matches never counts a null | **bites** |
| Kleene's all skips nulls | **bites** |
| item takes a list of three | **bites** |
| the seed is not the draw's | **bites** |
| an unshuffled sample is shuffled | **bites** |
| sort's nulls stay first | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
