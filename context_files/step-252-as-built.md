# Step 252 — as built

**E3 of `v0.6.5-scope.md`, first half: length-changing expressions in an
aggregation.** These are `Sort`, `SortDesc`, `Head`, `Tail`, `Slice`, `Reverse`,
`Unique`, `DropNulls` and `Get`, as Polars has them. In a group-by, a rolling or
dynamic group each is a list a group, and an aggregate over one is one value: in a
group-by, a window, and a scalar in `Select`.

## 1. What changed

**`internal/expr/reshape.go`:**

- **`Reshape{Op, Child, Args}`** is the node. Its type is its child's. `Get` is the
  one that yields a value rather than a column.
- **`LowerReshapes(n, agg)`** rewrites into what ursus already computes:
  - **a reshape in an aggregation** is the group's values imploded, then the list
    kernel of the same name: `list.sort`, `list.head` and the rest. A chain is each
    kernel over the one before;
  - **an aggregate over a reshape,** in any context, is that list's reduction:
    `first`, `last`, `sum`, `mean`, `min`, `max`, `len`, `count` (the list without
    its nulls, counted), `n_unique`, `arg_min` and `arg_max`;
  - **what it cannot lower** is an error node: a reshape of an aggregate, a reshape
    of an `Expr.Filter` (not there yet), or another aggregate over a reshape (median,
    std and var come with their list kernels).
- **`LooseReshape(n)`** finds a reshape left outside an aggregation, and
  `expr.Resolve` refuses it. The hints give the aggregate form and the operation's
  own frame-level form: `lf.Sort`, `lf.Head`, `lf.DropNulls`.

**The public methods** (`reshape.go`, new) build a `Reshape`. `Expr.DropNulls`,
which existed only to refuse, is one of them now. `Expr.DropNans` still refuses
until a list kernel drops NaN.

**Where the lowering runs.** `GroupBy.Agg`, plain and temporal, lowers as an
aggregation. `Select`, `WithColumns` and `Filter` lower before step 250's rewrite,
so `Select(Col("a").Sort().First())` is a scalar too. A reshape under `.Over` is
lowered inside the window's body.

**What it costs.** The implode holds a group's values, as Polars holds them, and
past a memory limit refuses rather than spill, as implode does.

## 2. Tests and teeth

**`TestALengthChangingExpressionInAnAggregation`.** Every value is Polars 1.44.1's on
the same frame:

- **eight list columns:** compared with ursus's rendering of the frame. Lists do not
  fit CSV; every value was checked against Polars' printed lists:
  - nulls first in a sort, either way;
  - `Unique` keeping first-seen order and the null;
- **in a group-by:** `Get` and eight aggregates over reshapes;
- **two chains;**
- **a scalar `Select`** of three;
- **a window;**
- **a rolling group's** reshaped windows;
- **three refusals:** a reshape alone in `Select`, of an aggregate, and of a filter.

**`TestExprDropNullsIsRefused`** keeps its claims for `Select` on its own: refused,
naming `lf.DropNulls`, in `CollectSchema` and `Explain` too.

| tooth | result |
| --- | --- |
| an aggregate over a reshape reads the column | **bites** |
| descending sorts ascending | **bites** |
| count counts nulls | **bites** |
| last is first | **bites** |
| a chain loses its inner reshape | **bites,** once the chains were added; silent before |
| a reshape planned outside an aggregation | **bites** |
| the row contexts not lowered | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
