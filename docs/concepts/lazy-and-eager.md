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
- **projection pushdown:** a scan reads only the columns the query uses;
- **limit and top-k pushdown:** a `Head` after a `Sort` becomes a top-k;
- **constant folding and simplification;**
- **common subplans:** a subtree used twice runs once.

`Explain` shows the plan. `Optimized(false)` shows it as written:

--8<-- "Example_lazyExplainUnoptimized.md"

and without the option, the plan that runs. The `PROJECT` is gone, and the scan reads
two of six columns:

--8<-- "Example_quickstartExplain.md"

`Explain`'s output is stable on purpose: ursus's own tests compare plans as text, so a
query that still gives the right answer while reading forty columns instead of two
fails a test.

## Eager: a DataFrame

A `DataFrame` is an answer in memory, Arrow arrays underneath. Its methods mirror
`LazyFrame`'s, and each is a lazy query over the frame, collected at once. So each
takes a context, which can cancel it:

--8<-- "Example_eager.md"

`df.Lazy()` turns a frame back into a query, for a chain of several steps optimized
together.

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
  `JoinMaintainOrder(true)` keeps the left frame's order.
