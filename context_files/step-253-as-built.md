# Step 253 — as built

**E3 of `v0.6.5-scope.md`, second half: the list kernels E3 needed.** `SortBy`,
`SortByDesc`, `GatherEvery` and `DropNans` as reshapes; `median`, `std`, `var` and
`quantile` over a reshape; and each as a public `List()` method.

## 1. What changed

**New call functions:**

- **`list.median`, `list.std`, `list.var`, `list.quantile`, `list.drop_nans`,
  `list.gather_every`** are in a block of their own at 700. The first families
  share one const block whose `iota` runs on, so the list family's first block ends
  at the struct family's base, 500. Six more there collided with `FnStructField`,
  which the build caught as a duplicate key. `IsList` covers both blocks, and the
  families test counts to 800: 120 classified.
- **`list.sort_by` and `list.sort_by_desc`** are in the two-operand block, since the
  keys are a column.

**Their typing (`listCallOut`):**

- the four reductions take the aggregate's own rule, as sum and mean do;
- `drop_nans` needs a List of floats;
- `gather_every` keeps the list's type;
- `sort_by` needs two Lists, the keys of an ordered type.

**The kernels:**

- **the reductions** run through `listReduce`, which now takes the aggregate's
  parameters: ddof, q and the interpolation;
- **`drop_nans`** keeps a null and drops a NaN, as Polars' does;
- **`gather_every`** takes every n-th element from an offset;
- **`listSortBy`** (`listset.go`) sorts each row's indices by the keys beside them,
  stably, nulls first, and refuses a row whose two lists differ in length.

**The reshapes (`internal/expr/reshape.go`):**

- **`DropNans` and `GatherEvery`** lower to their list kernels.
- **`SortBy`** gains a `By` child, the keys, which lower to their own implode beside
  the values. A `SortBy` of a reshaped column is refused: its keys would be the
  group's length and its values not. So are keys that are not a value a row.
- **`median`, `std`, `var` and `quantile` over a reshape** lower to the new list
  reductions, with their parameters.

**Public:**

- `Expr.SortBy`, `SortByDesc` and `GatherEvery`;
- `Expr.DropNans`, which was a refusal, as `DropNulls` was until step 252. Alone in
  `Select` it is still refused, its hint the frame-level spelling with `.Or`;
- `List().Median`, `Std`, `Var`, `Quantile`, `DropNans`, `GatherEvery`, `SortBy` and
  `SortByDesc`.

## 2. Tests and teeth

**`TestSortByGatherEveryAndDropNansInAnAggregation`.** Every value is Polars 1.44.1's
on the same frame:

- **the lists:** `sort_by`, both ways, `gather_every` from 0 and 1, and `drop_nans`
  keeping a null;
- **six reductions** over reshapes;
- **one std** held to 1e-15 of Polars'. Both are the std aggregate's answer, one
  unit apart in the last place, rounded along different paths;
- **ties** in a sort_by keep their order either way;
- **a refusal:** a sort_by of a reshaped column.

**`TestExprDropNullsIsRefused`** still holds for `DropNans` alone in `Select`.

| tooth | result |
| --- | --- |
| sort_by breaks ties backward | **bites,** once ties were in the test; silent before, and so was an unstable sort, which Go's sort is not on slices this small |
| sort_by_desc ascending | **bites** |
| gather_every ignores its offset | **bites** |
| drop_nans drops nulls too | **bites** |
| std ignores ddof | **bites** |
| quantile ignores its interpolation | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
