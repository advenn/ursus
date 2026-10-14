# Step 259 — as built

**E4 of `v0.6.5-scope.md`, second half, part one: `WithColumns` and `Explode`.**
`WithColumns` takes a length-changing expression of the frame's height, and
`Expr.Explode`, a refusal stub until now, is a reshape.

## 1. What changed

**`WithColumns`** sends a list with a length-changing expression to
`withColumnsReshaped` (`zipselect.go`):

- the frame is zipped with the lifted expressions, first, labelled "the frame". A
  column of another height is refused naming both, "col("a").head(2) is 2 rows and
  the frame is 3", as Polars refuses it;
- then come the frame's own `WithColumns` of the zipped columns, replacing by name
  in place, and a `Drop` of the temporaries;
- one value, an aggregate, is broadcast beside them, as in `Select`.

The height error's wording is now "columns side by side are of one length", for
both contexts.

**`Expr.Explode`** is `ReshapeExplode`:

- **in `Select`** it is the frame-level `Explode` of its one column. The column is
  exploded under its own name, so exploding a column that is not a List is refused
  naming it. Polars returns such a column as it was;
- **in an aggregation, and aggregates over it** (`Col("tags").Explode().Sum()`), it
  lowers to `list.explode` of the group's implode;
- **`list.explode`** (`listExplode`, in the list block at 700) flattens a List of
  Lists a level. An empty or null inner list is one null, as explode makes it a
  null row;
- `Reshape.Field` gives Explode its elements' type.

**`Filter`** still refuses a length-changing expression. A predicate of another
length than the frame's has no rows to keep.

**Four refusal tests moved again,** from `WithColumns` to `Filter`:
`TestExprDropNullsIsRefused`, `TestAFilterIsAnAggregatesInput` and
`TestALengthChangingExpressionInAnAggregation`. `TestExplodeRefusesNonList`'s
expression case now checks the refusal of a column that is not a List.

## 2. Tests and teeth

**`TestLengthChangingExpressionsInWithColumns`.** Every answer is Polars 1.44.1's:

- a sort beside the columns, and one in place;
- a reverse plus a column;
- a sort and a broadcast sum;
- a filter that keeps every row.

**`TestExplodeIsAnExpression`.** Polars' answers for:

- in `Select`, alone and plus one;
- summed;
- in an aggregation, with an empty and a null list;
- an integer column refused, naming it.

**`TestLengthChangingExpressionsRefuse`** gains a column of another height than the
frame's in `WithColumns`.

| tooth | result |
| --- | --- |
| the zipped columns are left in | **bites** |
| an empty inner list is dropped | **bites** |
| explode in an aggregation does not flatten | **bites** |
| explode in a Select keeps the lists | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
