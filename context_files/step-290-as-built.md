# Step 290 — as built

**R2 of `v0.6.7-scope.md`: duplicates over several columns, and a frame's distinct
rows.**

## 1. What changed

**`IsDuplicated(exprs...)` and `IsUnique(exprs...)`** (`window.go`):

- **The answer:** whether each row's values of the expressions together are another
  row's too, or no other row's: Polars' `pl.struct(...).is_duplicated()`, a Boolean a
  row that a `Filter` takes.
- **Written as** a partition's `Len` over the expressions, above 1 or equal to it, as
  `Expr.IsDuplicated` is over one column. So the key's equality is grouping's: nulls
  with nulls, NaN with NaN and −0 with 0, as `Unique` compares rows and as Polars
  does.
- **Named after the first expression,** as Polars names a struct after its first
  field.
- **The columns are named:** `All()` would expand the whole expression once a column,
  each partitioned by one column. Polars' `over(pl.all())` expands alike, and refuses
  the duplicate names. Polars reaches whole rows by `pl.struct(pl.all())`, and a
  struct is no key here (`feature-gaps.md` §3).

**`DataFrame.IsDuplicated(ctx)` and `IsUnique(ctx)`:** whole rows, every column, a
`Series[bool]`, as Polars' `DataFrame.is_duplicated` and `is_unique` give a Series.

**`DataFrame.NUnique(ctx, subset...)`:** the number of distinct rows over the subset,
every column when it is empty: `Unique`, counted. Polars' `n_unique` returns a count,
and so does this.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestDuplicatesOverColumns`:**
  - duplicated and unique over a and b, with nulls, NaN and the zeros;
  - duplicated over a and f;
  - a filter by unique;
  - the frame's duplicated and unique rows;
  - n_unique of whole rows, of a and b, of a, and of no rows;
  - no columns refused.

| tooth | result |
| --- | --- |
| duplicated of one | **bites** |
| unique of two | **bites** |
| keyed by the first column | **bites** |
| a frame's rows by their first column | **bites** |
| n_unique of whole rows | **bites** |
| named len | silent |

**The silent tooth is an equivalence.** Without the alias the name is still the first
expression's: a comparison is named after its first column, and the window's first
column is its first partition key. The alias says so outright, so the name does not
rest on that ordering.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
