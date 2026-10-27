# Step 397 — as built

**A query's share of its engine's threads.** B2 of `v0.8-scope.md`.

## 1. What was wrong

A query given no `WithThreads` ran on `runtime.NumCPU()` workers, whatever ran beside
it: a pipeline's dispatcher and a worker a thread, a group-by's sinks, a join's
build. After step 396 a small query runs on one, so the cost was the large ones'.
`svcprobe conc` with `ROWS=200000`, step 396's build: a group-by of 200,000 rows alone
answered 583 to 633 queries a second on its threads, and 280 to 290 on one; from 16
goroutines 720 to 750 on their threads and 870 on one each, with 275 goroutines at the
peak where 19; from 64, 623 to 849 and 894, with 940 to 970 goroutines where 66.

`NumCPU` is also the machine's CPUs, not a container's limit: `GOMAXPROCS` follows a
cgroup's CPU quota since Go 1.25, `NumCPU` the CPUs the process may be scheduled on.

Six helpers sized themselves by `GOMAXPROCS`, whatever the query's threads: the eager
`inParallel` (`Corr` and its kin), `PartitionBy`'s parts and `ToDummies`' columns,
`FirstSeenKeys` under both, the JSON writer's batches in flight, and the Spearman
ranks' pair.

## 2. How

- **The engine's threads** (`engine.go`): `WithEngineThreads(n)`, `GOMAXPROCS` as the
  engine is made by default, `DefaultEngine`'s too.
- **A query's share** (`Engine.share`): a query wanting more than one thread is given
  the engine's threads divided by its queries on more than one, this one counted, at
  least one and at most what it wants. Given one, it does not count. A query takes its
  share as `compile` plans it, after step 396's rules, and keeps it to its end, when
  `done` gives it back (`workers`, `collectCfg.work`). Five large queries arriving
  together on eight threads take 8, 4, 2, 2 and 1; a small query, on one thread,
  takes none from a large one.
- **`WithThreads(n)`** runs a query on n, at any size and whatever ran beside it, and
  counts among the engine's on more than one (`Engine.hold`).
- **What a query does after planning** takes the threads it was planned on
  (`collectCfg.planned`): `Collect`'s concatenation, the CSV writer's batches and the
  JSON writer's in flight, which took `GOMAXPROCS`.
- **The eager frame operations** take a share of `DefaultEngine`'s as a query does,
  and give it back (`frameWorkers`): `inParallel`, `PartitionBy`'s parts and
  `ToDummies`' columns, and `FirstSeenKeys`, which takes its workers as an argument.
- **Left:** the Spearman ranks' pair (`aggrank.go`), two goroutines for a group of
  65,536 pairs or more: an accumulator knows no query, and two is its most.
- **`EngineStats`** gains `Threads` and `Parallel`, the queries on more than one now.

## 3. Measured

`svcprobe conc`, `ROWS=200000`, step 396's build and this one, four runs each, at a
load of 4 to 7:

| goroutines | queries a second, 396 | here | goroutines at the peak, 396 | here |
| --- | --- | --- | --- | --- |
| 1 | 583, 583, 633, 630 | 623, 434, 604, 566 | 20 | 20–39 |
| 4 | 676, 710, 760, 717 | 826, 737, 858, 691 | 74–98 | 42 |
| 16 | 720, 719, 750, 749 | 768, 926, 948, 870 | 275–278 | 52–63 |
| 64 | 755, 849, 623, 805 | 1,001, 944, 949, 858 | 936–968 | 96–102 |

One goroutine runs the same plan in both; its 434 is the load's. The peak counts
the sampler's own and the last phase's goroutines.

**A query alone costs nothing more:** `svcprobe lat`, four runs each, the order
alternated: the geometric mean of the 24 medians 0.996, of the one-thread ones 0.994.
Three runs with step 396's always first gave 1.11, and its one-thread ones, which take
no share, as much: the machine's drift. PDS-H at SF=0.1 against step 396's runner:
1.00 on the default threads, four rounds, and 0.98 on four given.

## 4. Tests

- **`TestAQueryTakesItsShareOfItsEngine`** (root): an engine's threads `GOMAXPROCS`;
  five shares of eight wanted of eight, 8, 4, 2, 2 and 1; a query of one thread, or
  of none to spare, not counted; all given back. A sort of 100,000 rows paused holds
  eight, and a second beside it is planned on four, a row on one, a query given
  `WithThreads(6)` on six; a sort alone on an engine of two threads, and of NumCPU and
  four more, on its engine's.
- **`TestAFrameOperationTakesItsShare`** (root): `inParallel` of 64 pieces on an engine
  of two threads runs two at once at most, where it ran `GOMAXPROCS`; it and a
  `PartitionBy` of 200,000 rows give their share back.
- **`TestEveryEntryPointLeavesItsEngine`** (root, step 395's): each entry point gives
  its threads back too, and a paused one holds them.

**Teeth: 8 of 8 bite:** no division; a query of one thread counted; release giving
nothing back; `WithThreads` taken as a share; `done` keeping the threads; a query's
threads `NumCPU`, not its engine's; `inParallel` on `GOMAXPROCS`; `PartitionBy`
keeping its share.

**Gate:** the light gate, 28 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1. The internal packages and the root in
full before it: step 396's test of a small query's threads took `NumCPU` for the
default, now the engine's `GOMAXPROCS`, and a lint of `Expr`'s field took the
ticket's `n` for it, now `threads`.
