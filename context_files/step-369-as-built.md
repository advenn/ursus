# Step 369 — as built

**`Profile`:** Polars' `LazyFrame.profile()`, a query's answer and a frame of how each
node of its plan ran; and `Explain` honouring the optimizer's flags. P1 of
`v0.6.8-scope.md`, first of its performance tasks, so the rest are measured by it.

## 1. How

**`LazyFrame.Profile(ctx, opts...) (df, profile *DataFrame, err error)`** runs the
query as `Collect` does. The profile has a row a node, in the order `Explain` prints
the plan, after a first row, `optimization`, the time to resolve and optimize it, as
Polars' first row is:

| column | |
| --- | --- |
| `node` | the node as `Explain` names it, indented two spaces a level |
| `start`, `end` | µs since the query began, of its first work and its last, a node's span covering its inputs'; null for a node that did none, as a union's second input under a `Head` |
| `rows`, `batches` | what it handed on |
| `own` | µs of its own work, summed over the threads that did it |
| `peak_memory` | the most its operators held at once, as the budget counts them |

Polars' profile has `node`, `start` and `end`; the rest are ursus's.

**The wrappers** (`internal/physical/profile.go`), built only where
`Options.Profiler` is set, so `Collect` builds none; `Plan` is `planNode` behind one
nil check:

- `Plan` opens a `ProfNode` for each plan node, a child of the node being planned,
  and wraps what it lowers the node to;
- a node's **batch transforms**, its stages' `BatchOp`s, each in a `profBatchOp`,
  timed on whichever worker runs it. The parallel pool finds a pipeline by its
  stages' type (`pipelineOf`), so it is the `BatchOp` inside that is wrapped, and the
  topmost counts the node's rows;
- a node's **operators** in a `profOp`: its edge, the one its consumer pulls, timing
  how long the consumer waited; and an operator of its own beneath its stages, its
  base, as a scan's source under the stage that trims its columns, or an
  aggregation's breaker under its projection;
- a stage chain's edge is a `profOp` that `pipelineOf` sees through; where the chain
  is parallelised, `planPipelined` and `PlanRoot` time the parallel operator in its
  place (`Options.parallelise`).

**Own time:** a node's batch transforms' time, summed over workers, and its
operators' time less what they waited on its inputs' edges, never below zero. On one
thread the nodes' own times add to the query's; where an operator works on threads
beside its input, as a parallel aggregation's `Consume` does, only what it did beyond
the wait is its own.

**Memory:** an `execopt.Account` keeps its peak (`Peak`), and a budget tells a
profile of each account opened (`OnAccount`), which charges it to the node being
planned. An account a sink opens as the query runs, after planning, is charged to
none.

**`ExplainOptFlags(f)`** renders the plan as `WithOptFlags(f)` runs it; `Explain`
ran every rule whatever a query would run.

**Measured:** a group-by of 2M rows on four threads, profiled and not, five runs each:
15.6–17.7 ms and 15.9–29.4 ms, the same within this laptop's noise.

## 2. Tests

- **`TestProfileMeasuresEachNode`:** four queries, a filtered, computed, grouped,
  sorted `Head`; a join; a Parquet scan under a window; a union's second input under
  a `Head`, each on 1 and 4 threads: the answer as `Collect`'s; the optimization's row
  first; a row a node, labelled as `Explain` prints it; each span after the
  optimization and covering its inputs'; the nodes' rows; memory held by the sort, the
  aggregation and the join and none by the filter; the unread input null, of no
  batches or time; on one thread, own times adding to no more than the query's.
- **`TestAProfiledPipelineStaysParallel`:** a profiled stage chain is still a pipeline
  of its two transforms over its node's base, and parallelised is a parallel operator
  under its edge, its node's rows counted once on four workers.
- **`TestAnAccountKeepsItsPeak`**, and **`TestExplainHonoursOptFlags`:** every rule off,
  the scan reads both columns.

**Teeth: 13 of 13 bite:** a stage chain wrapped whole; `pipelineOf` blind to a stage
edge; no edge for a parallel pipeline; every transform counting rows; own time not
less its inputs' wait; own time in ns; a span not covering its inputs'; no account's
peak, or `RetainBytes` not growing it; no account charged to its node; a node never
run reported as run; the optimization never marked; `Explain` ignoring its flags.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
