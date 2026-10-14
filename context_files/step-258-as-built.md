# Step 258 — as built

**E4 of `v0.6.5-scope.md`, first half: length-changing expressions in `Select`.**
`Select(Col("a").Sort(), Col("b").Head(3))` evaluates each expression over the whole
column, as Polars does:

- columns of one length stand side by side;
- one value, an aggregate or a literal, is broadcast to their length;
- columns of two lengths are refused when the query runs.

## 1. What changed

**`zipselect.go`.** `Select` sends a list with a reshape or an `Expr.Filter` outside
an aggregate to `selectReshaped`, which lifts each expression into a plan of its own
over the input:

- **a value a row, or one value:** the input's own `Select` of it, a projection or a
  keyless aggregate, as before;
- **a constant:** its own node, broadcast by the final projection;
- **a reshape:** its values' plan, then the frame-level operation of the same
  effect:
  - `Sort`, `Head`, `Tail`, `Slice` (a negative offset as a `Tail`), `Reverse` and
    `Unique` (first-seen order);
  - `DropNulls` and `DropNans` as filters;
  - `GatherEvery` as a row index and a filter;
  - `SortBy` as a stable sort of the values and keys projected together;
  - `Get` as the list form an aggregation takes, one value, null past the end;
- **an `Expr.Filter`:** the values and the predicate projected together, then the
  frame's filter;
- **anything else over such columns** (`a.Sort().Add(b)`): its operands lifted and
  zipped, then a projection.

**`zip`** puts the plans side by side:

- the columns go into a `plan.HStack`, which refuses two lengths;
- a plan of one row goes beside them by a cross join, which keeps the other side's
  order whichever side it builds;
- a final `Select` names each column as the expression is named.

**The height error.** `plan.HStack` gains `Labels`, the expressions a `Select`'s
inputs compute. A height mismatch then names them, "col("b") is 5 rows and
col("a").head(3) is 3: a Select's columns are of one length", where a frame's HStack
still names frame numbers. `Label()` is unchanged, so the golden plans are too.

**Each lifted plan spills where its frame-level operation does.** The input is read
once a plan. A shared input that joins, aggregates or sorts is computed once
(`MarkShared`).

**Refused, with hints:**

- a reshape of a selection of several columns, `Col("a", "b").Sort()`, which only the
  input's schema expands: name each column;
- `Gather` in a `Select`;
- a `SortBy` or a filter whose keys or predicate are of another length.

**Unchanged:**

- `WithColumns` and `Filter` still refuse a column of another length: E4's second
  half;
- a `Select` of literals alone is still a row a frame row (step 257).

**`DropNans` of a non-float column** is refused, here and in an aggregation, where
Polars returns it as it was. `feature-gaps.md` §9 records it.

**`IsIn(Col("b").Implode())` moves to 0.7** (from step 254). It needs the implode
computed once and handed to the kernel as a List literal one row long: a scalar
subquery, run before the query that reads it. A plan inside an expression has to be
resolved, optimized and run at execution, a feature of its own. E1's scalars would
use it too, read twice where they are now spilled once.

## 2. Tests and teeth

**`TestLengthChangingExpressionsInSelect`:** 23 selects at batch sizes 1, 3 and 8192,
every answer Polars 1.44.1's (`Unique` with `maintain_order`):

- sorts, reverses, heads and tails beside columns;
- a sum and a literal broadcast;
- a head less its mean, a sort plus a column, chains;
- drop_nulls, drop_nans and unique;
- filters, slices (one from the end), get, sort_by and gather_every;
- `tail(0)` and a head past the end.

**`TestLengthChangingExpressionsRefuse`:** two lengths, nested and not; several
columns; a gather; a sort_by of a head.

**`TestLengthChangingExpressionsSpill`:** a sort and a reverse beside a column under
a tenth of the input spill, answer as in memory on one thread and on four, and leave
no file.

**Four tests flipped** from the refusal in `Select` to `WithColumns`, which still
refuses: `TestExprDropNullsIsRefused`, `TestAFilterIsAnAggregatesInput`,
`TestALengthChangingExpressionInAnAggregation` and `TestGatherInAnAggregation`.

| tooth | result |
| --- | --- |
| head is tail | **bites** |
| a negative slice offset is taken as it is | **bites** |
| one value is zipped as a column | **bites** |
| the columns' labels are lost | **bites** |
| sort_by sorts ascending always | **bites** |
| gather_every ignores its offset | **bites** |
| drop_nans drops nulls | **bites** |
| a filter keeps every row | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
