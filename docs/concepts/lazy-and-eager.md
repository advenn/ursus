---
description: >-
  How ursus runs a query: a LazyFrame is a plan that the optimizer rewrites and
  Collect runs; a DataFrame is an answer; CollectBatches streams one.
---

# Lazy and eager

## A query is a plan

A `LazyFrame` is a query, not data. `ScanCSV`, `Filter`, `GroupBy` and the rest each
add a step to a plan, and nothing is read or computed until the plan runs:

- `Collect(ctx)` runs it and gives a `DataFrame`, the answer, held in memory;
- `CollectBatches(ctx)` runs it and gives the answer a batch at a time;
- `Count(ctx)` runs it and gives the number of rows;
- `WriteParquet(ctx, w)` and `WriteCSV(ctx, w)` run it into a writer.

Because the whole query is known before it runs, ursus can check it first and then
rewrite it.

## Checked before it runs

When a query is collected, ursus resolves every column's type, expands each selection
of several columns, and type-checks every expression, before any data is read. A
mistake is reported then, with what was meant:

--8<-- "Example_lazyDeferredError.md"

The `Select` with the typo did not fail when it was written. A query being built never
fails: the first error is kept, the chain goes on, and `Collect` returns it. So a long
chain has one `if err != nil`, at the end.

## Rewritten before it runs

The optimizer rewrites the plan into one that gives the same answer for less work:

- **predicate pushdown:** a filter moves toward the scan, through joins, so fewer rows
  travel;
- **projection pushdown:** a scan reads only the columns the query uses, and a count
  none (*new in 0.6.8*);
- **limit and top-k pushdown:** a `Head` after a `Sort` becomes a top-k, and, *new in
  0.6.8*, a `Head` or a `Slice` reaches its scan or its sort through a projection, a
  row index, a union's inputs and an ordered left join's left input;
- **constant folding and simplification;**
- **common subplans:** a subtree used twice runs once.

A subtree used twice runs once when it joins, aggregates or sorts, which is worth the
fence it puts around it: a filter above one use stops there. *New in 0.6.7:*
`Cache()` asks it of any query, as Polars' `cache` does, a scan read twice or a Go
function's answer; a query used once is not cached.

`Explain` shows the plan. `Optimized(false)` shows it as written:

--8<-- "Example_lazyExplainUnoptimized.md"

and without the option, the plan that runs. The `PROJECT` is gone, and the scan reads
two of six columns:

--8<-- "Example_quickstartExplain.md"

`Explain`'s output is stable on purpose: ursus's own tests compare plans as text, so a
query that still gives the right answer while reading forty columns instead of two
fails a test.

*New in 0.6.8:* `Profile(ctx)` runs the query, as Polars' `profile()`, and answers its
frame and a second one, a row a node of the plan as `Explain` prints it, after a row
for the optimization: when the node began and ended its work, in microseconds since
the query began, the rows and batches it handed on, its own time, summed over the
threads that did it, and the most memory it held. A profiled query reads the clock
around each operator, so it takes a little longer; `Collect` does not.
`Explain(ctx, ursus.ExplainOptFlags(f))` prints the plan `WithOptFlags(f)` runs.

*New in 0.6.7.* `Columns(ctx)`, `DTypes(ctx)` and `Width(ctx)` are a query's column
names, their types and how many there are, as Polars' lazy `columns`, `dtypes` and
`width`: read from `CollectSchema`, which types the plan without running it.

## Eager: a DataFrame

A `DataFrame` is an answer in memory, Arrow arrays underneath. Its methods mirror
`LazyFrame`'s, and each is a lazy query over the frame, collected at once. So each
takes a context, which can cancel it:

--8<-- "Example_eager.md"

`df.Lazy()` turns a frame back into a query, for a chain of several steps optimized
together.

Printed, a frame draws its first ten rows. *New in 0.6.7:* `Show(n)` draws its first
n, every one, as Polars' `show`, and a query's `Show(ctx, n)` computes only those.

## Streaming the answer

`CollectBatches` is a Go iterator over the answer's batches, so an answer larger than
memory never has to be held at once:

--8<-- "Example_lazyBatches.md"

Breaking out of the loop stops the query and removes any spill files it wrote.

## Order and threads

A query runs on every core by default; `WithThreads(n)` sets how many. A scan's rows
come in the file's order at any thread count, a filter keeps its input's order, and a
sort's is sorted. Two orders are not promised, as in Polars, and each has an option
that promises one:

- **a group-by's groups:** `MaintainOrder()` gives them in the order their keys first
  appear;
- **an inner join's rows,** which follow whichever input ursus did not hash:
  `JoinMaintainOrder(ursus.JoinOrderLeft)` keeps the left frame's order, and
  `JoinOrderRight` the right's, for a join of any kind.
