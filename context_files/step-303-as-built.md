# Step 303 — as built

**P1: a lazy `Pivot`, and several `On` columns** (`v0.6.7-scope.md` §3), as Polars'
`LazyFrame.pivot` and `pivot(on=[...])`.

On the way, a fix: a cell that no value falls in is now `Agg` of none, as Polars'
is. A count of none was null; it is 0.

## 1. The API

```go
type PivotOptions struct {
	On            []string  // was a string: a break
	OnColumns     []*Column // new
	Index, Values []string
	Agg           func(Expr) Expr
	Separator     string
	SortColumns   bool
}
func (lf *LazyFrame) Pivot(o PivotOptions) *LazyFrame                         // new
func (df *DataFrame) Pivot(ctx context.Context, o PivotOptions) (*DataFrame, error)
```

**`On` is a `[]string`,** as `Index` and `Values` are, for several columns whose
values together name a new column. `On: "quarter"` becomes `On: []string{"quarter"}`;
the CHANGELOG lists it under breaks.

**`OnColumns` are the new columns' On values:**

- a column for each of On, named as it, matched by name in any order;
- a row for each new column, in their order;
- each is cast to its On column's type, strictly. Text beside a number is refused,
  "which cannot be compared", as Polars refuses comparing them, rather than parsed.

**`LazyFrame.Pivot` needs them,** as Polars' `LazyFrame.pivot` needs `on_columns`: a
plan's schema is known before it runs. **`DataFrame.Pivot` finds them when nil:**
`GroupBy(On...).MaintainOrder().Agg()`, the combinations in the order each first
appears, sorted with `SortColumns`, a null first, as Polars sorts it. Then it is the
lazy pivot, collected. One implementation answers both.

## 2. What Polars 1.44.1 answers, probed

**Names:**

- one On column: the value's String cast, "null" for a null;
- several: `{"y","p"}`, text quoted and not escaped, numbers bare;
- several values: `v_{"y","p"}`.

**`on_columns`:**

- a subset leaves the other values out, their index still a row;
- a value never held is a column of empty cells;
- the given order wins, even beside `sort_columns`;
- a null given is a column "null";
- NaN matches NaN and 0.0 matches -0.0, as its `==v` does: two given zeros are two
  columns of the same cells;
- a value twice, a string for a number, and a frame of other names are refused.

**A cell is the aggregate of the values that fall in it, possibly none:**

- a never-met cell's `sum`, `len`, `count` and `n_unique` are 0;
- its `first`, `mean`, `min` and `max` are null;
- without an aggregate it is null, and a cell of two values fails, "aggregation
  'item' expected no or a single value".

**Polars' lazy plan shows why:** `AGGREGATE [col("v").filter(col("on") ==v "y")
.item(allow_empty=true).alias("y")] BY [col("k")]`, one filtered aggregate a new
column.

## 3. The design: a group-by, then a spread

**Built as Polars builds it, the pivot was too slow.** That plan, with existing ursus
operators, a group-by of one filtered aggregate a column, timed at 200,000 rows
(one targeted run):

| new columns | filtered group-by | ursus's eager spread | Polars |
| --- | --- | --- | --- |
| 10 | 25 ms | 15 ms | 6–11 ms |
| 1,000 | 1.99 s | 73 ms | 134–144 ms |

**So the lazy pivot is two nodes:**

1. **`GroupBy(Index..., On...).MaintainOrder().Agg(cells)`** reduces each pair of an
   index and On values over its rows. It streams its input and spills past a limit,
   as any group-by does.
2. **`plan.Pivot`** (`internal/plan/nodes_pivot.go`) lays the pairs out, a row an
   index in the order each first appears, a column a value and row of OnColumns.

**`plan.Pivot`'s children** are the aggregate and OnColumns, an in-memory scan, as
HStack's frames are. A field could not hold the columns: the plan package holds no
data, which `TestPlanPackageNeedsNoArrow` enforces (`design/logical.md` §13). Its
`Schema()` is the index columns, then a column a name of the cell's type, so a lazy
pivot's schema is known before it runs.

**A cell no value falls in** is Polars' aggregate of none. Each value's cells are
followed by its cell over none:

- the group-by computes it as `Agg(Col(v).Filter(Lit(false)))`, the same on every
  pair's row;
- the spread appends it to the cells and points a never-met cell at it;
- no query runs while the plan is built.

Without Agg, a count follows instead. A cell of two rows is refused, naming the
meeting, "2 rows fall where k=1 and on=x meet, and Agg is not set". A cell never met
is null.

**Matching** goes through the group-key encoder, as the group-by keyed the pairs, so
NaN, -0 and null match as Polars' `==v` does. A key can be several rows of
OnColumns, both zeros given, each a column.

**`Agg` reads only the value it is given.** One reading another column is refused,
since the cell over none could not then be one value for every row. Polars'
aggregate is an expression of `pl.element()`, which reads nothing else.

## 4. The physical sink, and memory

`pivotSink` (`internal/physical/pivot.go`):

- **holds the pairs,** retained, and checks the budget at each batch;
- **at Finish** reads OnColumns, numbers each index by first appearance (`groupsOf`,
  moved from the root package), buckets the pairs by their row of OnColumns, and
  takes each new column in one `Take`;
- **past a limit** refuses with the account's message and a pivot hint: "pivot holds
  a row for each index and On values that meet, from the group_by beneath it, which
  spills".

**Its `Merge` appends,** as `reverseSink`'s must. `TestEverySinkMergeIsExercised`
asked for a test; `TestPivotSinkMergeEquivalence` merges two sinks and holds them to
one.

**Under a limit:** 20,000 rows of 50 index values in batches of 64 answer under
64 KiB as without one, a peak of 11 KB. With 20,000 index values the group-by spills
272 files, and the pivot then refuses its pairs.

**What the optimizer does with it:**

- predicate pushdown treats it as a barrier, as it treats the aggregate beneath
  (§8's row);
- projection pushdown keeps every column of the aggregate, which computes nothing
  else;
- `Expressions` names its input's columns, for the liveness check;
- the golden-plan inventory has a pivot query.

## 5. Recorded

**§1, open:** a float cast to String is Go's shortest text, `2`, `-0`, `1e-07`,
`+Inf`, where Polars writes `2.0`, `-0.0`, `1e-7`, `inf`. A pivot names its columns
by that cast, so a column on 2.0 is `2`. The CSV writer already writes `2.0`. Fixing
the cast is its own step.

**§9:**

- names follow ursus's cast: a float's and an instant's (§1's rows) differ, and a
  duration, which Polars cannot cast, is named;
- a combination holding a null is `{1,null}`, where Polars names the column "null"
  and refuses a second;
- several On columns' instants are written as their casts, where Polars' display
  differs again;
- `Agg` reads only its value;
- the rows keep first appearance, Polars' lazy default leaving the order open;
- the pivot holds its pairs past a limit.

Polars' `column_naming="combine"`, marked unstable there, is left out.

## 6. The site

`docs/guide/reshaping.md` on `dev`:

- the eager example now writes `On: []string{"product"}`;
- the lazy pivot, new in 0.6.7, `Example_reshapePivotLazy`: plum given and never
  sold, fig sold and left out;
- several On columns, new in 0.6.7, `Example_reshapePivotSeveral`, Polars' answer
  exactly;
- a count of none, fixed in 0.6.7.

The Polars page maps `lf.pivot(on, on_columns=...)`.

## 7. Tests and teeth

**The tests,** each answer Polars 1.44.1's but where §9 says otherwise:

- `TestPivotAnswersAsPolars`, step 229's, on the new API;
- `TestPivotSeveralOn`: one and two values, sorted, lazy with given combinations,
  nulls among them;
- `TestPivotOnColumns`: a subset, a reorder with a value never held, lazy, an index
  none of whose values is given, a null given, none given, NaN and the zeros, both
  zeros given, an Int64 given for an Int32;
- `TestPivotCellsOfNone`: ten aggregates' cells of none, and none without Agg;
- `TestPivotRefusals`: thirteen refusals by their message; the lazy schema, a count
  a `Uint64`; the plan;
- `TestPivotUnderAMemoryLimit`;
- `TestPivotSinkMergeEquivalence`.

**Teeth, all biting:**

- a cell of none null;
- a key one combination;
- OnColumns not cast;
- two values a cell let through;
- an Agg reading another column let through;
- the pairs in hash order, which the plan's "(ordered)" catches;
- names unquoted;
- held pairs unchecked;
- SortColumns ignored;
- a lazy pivot finding its columns.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
