# Step 310 — as built

**G3: `All()`, and `First` and `Last` ignoring nulls** (`v0.6.7-scope.md` §4), as
Polars' `group_by().all()` and `first(ignore_nulls=True)`.

## 1. What Polars 1.44.1 has, probed

- `GroupBy.all()`: each column a list a group, in order, nulls in it.
- `GroupBy.first(*, ignore_nulls=False)` and `last`, and `Expr.first(*,
  ignore_nulls=False)` and `last`.

With `ignore_nulls`:

- **the first or last value not null;** NaN is a value;
- **a group of none is null,** and so is a column of nulls or of no rows in a select;
- **over a window,** each row its group's;
- **a null list is skipped,** an empty one is a value.

## 2. The API

```go
func (e Expr) FirstNonNull() Expr
func (e Expr) LastNonNull() Expr
func (g *GroupBy) FirstNonNull() *LazyFrame
func (g *GroupBy) LastNonNull() *LazyFrame
func (g *GroupBy) All() *LazyFrame
```

**New names, where Polars has a keyword.** `First()` and `Last()` take no argument,
and are used as method values, `Agg: ursus.Expr.First`. A Boolean, or a variadic
one, would break those, so ignoring nulls is a name of its own. The Polars page maps
`first(ignore_nulls=True)` to it.

## 3. How

- **`FirstNonNull` is `e.Filter(e.IsNotNull()).First()`,** a conditional aggregate,
  and `LastNonNull` likewise. The aggregate's sink feeds the accumulator only the rows
  the filter keeps (step 251), so each streams and spills in a group-by, a window and a
  temporal group, as `First` does.
- **The shorthands and `All()` go through `each`,** every non-key column. `All()` is
  `Implode` of each.

**`All()` under a memory limit is `Implode`'s.** 20,000 rows of 4,000 keys, cycling,
were refused at 64 KiB and at 256 KiB alike: "group_by has already partitioned its
keys to disk and is still holding 554.1KiB of per-group state for 2112 resident
groups". Partitioning sends new keys to disk, but the values of the keys it holds stay,
and an implode's values do not spill. Its doc says so; the memory-limit test holds
`FirstNonNull` and `LastNonNull` only.

## 4. Found on the way

**A conditional of a List or a Struct was refused,** "a conditional is not implemented
for List(String)". The test built a list column with `When`.

- The kernel, `Select`, had paths for Booleans, text and fixed widths only.
- It now takes any other type from its branch: the two laid end to end, one `Take`,
  each branch's nulls kept, a literal branch's one row broadcast.
- Polars' answers held: a List `When`, a Struct `When`, a `Coalesce` of Lists.

**A null condition yields null,** where Polars takes Otherwise: `when(b).then(l)
.otherwise([9])` of `[true, false, null]` is `[[1], [9], [9]]` there and `[[1], [9],
null]` here.

- It is by design (`internal/expr/cond.go`: "a null condition takes neither branch
  and yields null") and holds for every type, numbers too. SQL's `CASE WHEN NULL`
  takes its ELSE, as Polars does.
- No document said so. It is §1's row now, open: changing it changes every
  conditional's answers, so it is for the maintainer to decide.

## 5. The site

`docs/guide/aggregation.md` on `dev` has `FirstNonNull`, `LastNonNull` and `All()`, new
in 0.6.7, with `Example_aggFirstNonNull`: a sensor's first reading, null, beside its
first and last seen, and its readings a list. The Polars page maps them.

## 6. Tests and teeth

**The tests:**

- `TestFirstLastNonNullAndAll`: the shorthands, `First` unchanged beside them, in an
  `Agg`, in a select, over a window, a column of nulls, no rows, `All()` and `All()`
  by a computed key, a list column, a dynamic group;
- `TestFirstNonNullUnderAMemoryLimit`: 20,000 rows of 4,000 keys under 64 KiB,
  spilling, equal to the run without a limit;
- `TestConditionalOfNestedTypes`: a List and a Struct `When`, a `Coalesce` of Lists.

**Teeth, all biting:**

- `FirstNonNull` as `First`;
- `LastNonNull` as `Last`;
- `All` as `First`;
- a nested conditional refused again;
- a literal branch read past its row.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
