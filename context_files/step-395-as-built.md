# Step 395 — as built

**The queries of a process share one memory budget, an `Engine`'s.** A3 of
`v0.8-scope.md`.

## 1. What was wrong

Each `Collect` made its own budget (`lazy.go`'s `finish`): half the memory the
process may use, unless the query was given `WithMemoryLimit`. Sixty-four queries at
once each assumed half the machine. Measured with `svcprobe shared`: sixteen queries
over four million rows at once, half sorts and half group-bys of a million keys,
each on a budget of its own of 256 MiB, held 3.7 to 3.9 GB of RSS and spilled
nothing, where 256 MiB was the most any was meant to hold.

The maintainer chose the shape on 2026-10-27: shared by default, with an explicit
`Engine` for a service that wants pools of its own.

## 2. How

- **`execopt.Pool`** (`internal/execopt/pool.go`) is one budget its queries share.
  Each query keeps a ledger of its own, a `Budget`, as before: what it holds, each
  allocation once, its peak and its spills. It tells the pool its total when it has
  moved a sixty-fourth of the limit, at most a mebibyte, from what it last told, so
  the queries take no lock in common, an atomic add each mebibyte.
- **When a query is over** (`Budget.room`): when the pool's queries together hold
  more than its limit, and this one more than what the others leave it, never less
  than an even share among those running. Whenever the pool is over, some query
  holding more than its share is too, and spills; a query under its share never
  spills for the others. Alone on a pool, a query spills where it did on a budget of
  its own.
- **A refusal asks the query alone** (`OverAlone`): `Account.Check`, and the five
  refusals an operator makes where it cannot divide what it holds, at the last depth
  of its recursion or at once, a group-by's freeze at its last depth, a join's one
  key (`oneKeyOver` holds it instead), a join with no key and a residual that
  neither crosses nor masks, a window with no partition_by that needs all of it and one partition at its depth.
  Another query's holding makes a query spill sooner, not fail.
- **Thresholds an operator sizes by its budget take the room** (`Budget.Room`): a
  parallel group-by's fold to serial and its partitioned fold, a deferred join
  build's catch-up, a probe's read-ahead (`overHalf`), a top-k's least run, a
  window pass's, a rolling window's and a join order's runs, a pair spill's batch,
  and a scan's read-ahead. Alone, the room is the limit, so none moves.
- **A group-by's values spill under the engine's pressure** (`hashAggSink.pressed`,
  and a rolling window's in `chargeState`): a median's, a quantile's or n_unique's
  values go to disk once the query is over its room; the refusal after asks the
  limit.
- **Every entry point leaves its engine.** `compile` joins the query to its pool as
  it plans it, so its operators size themselves by its room; `collectCfg.done`, which
  was `report`, leaves it, deferred first by all ten entry points that compile:
  `Collect` and `Profile` called it on their way out, and a failed or panicked query
  skipped it. A budget joined and never left leaves at its collection
  (`runtime.AddCleanup`), the backstop for an entry point that forgets.
- **The public API** (`engine.go`): `Engine`, `NewEngine(WithEngineMemoryLimit,
  WithEngineSpillDir)`, `WithEngine`, `DefaultEngine`, `Engine.Stats` and
  `EngineStats`. A query with `WithMemoryLimit` keeps a budget of its own apart from
  its engine's, so its spills, and an unordered group-by's order under it, are the
  same on every run. An engine whose budget is the default's, `DefaultEngine` or one
  made without `WithEngineMemoryLimit`, sets the Go soft limit at its first query, as
  the default budget did; one given a limit does not. Its queries run a parallel
  group-by, as on the default budget, and check `Collect`'s result where its budget
  is the default's.

## 3. Measured

`svcprobe shared`, sixteen queries at once over four million rows, 256 MiB a budget,
at a load of 2 to 5:

| | each on an engine of its own | all on one engine |
| --- | --- | --- |
| eight sorts, eight group-bys | 4.5–4.6 s, 3.7–3.9 GB RSS, no spill | 3.9–4.2 s, 1.7–1.8 GB, 141 files |
| sixteen sorts | 5.4 s, 4.2 GB | 5.9–7.0 s, 1.7–2.1 GB, 40 files |
| sixteen group-bys | 5.0 s, 4.1 GB | 2.0–2.6 s, 1.0 GB, 256 files |

Sixteen sorts that spill take a tenth to a third longer than sixteen that do not:
they write their runs and merge them. The group-bys are faster spilled, folded to one
sink each where their parallel tables filled the machine.

**The budget is a target each query keeps to when it looks, not a cap.** The shared
engine's queries held at most 1.4 to 1.8 times its budget at once: 367 to 460 MiB.
At one sort and two the engine held exactly its budget, at four 1.17 times it, at
eight 1.2, at sixteen 1.5. A query that grew while the others were starting holds
what it took until it next looks, and sixteen sorts each spilling a run at once held
theirs while they wrote, 425 MiB for a second and a half; a sort alone on 16 MiB
peaked at 16.

**A query alone costs nothing more:** `svcprobe lat`, eight runs of step 394's build
and this one alternated, the geometric mean of the 24 medians 0.99, each within
0.92 and 1.10; `svcprobe conc`, 7,842 and 7,924 queries a second from 64 goroutines
on a thread each, where 7,423 and 8,179. PDS-H at SF=0.1 against step 394's runner,
two rounds and four: 1.01 and 1.01, at a load of 5.5 to 6.8.

**The Parquet page pool** is not counted, where the scope said it would be. Measured
in a copy of the reader counting the pool's buffers out: a scan of q1's columns of
lineitem at SF=1 holds at most 20 MiB of pages at once, and one of all sixteen 105
MiB. It is a scan's transient memory, as its read-ahead is, which step 214 bounded
rather than charged, so that a query's own sinks do not spill for bytes about to be
consumed. Counted as the engine's pressure alone, on the other queries, is the shape
it would take; it moved to B3, with what a query allocates.

## 4. Tests

- **`TestAPoolsQueriesShareItsLimit`** (`execopt`): two queries on 64 MiB, 48 and 24
  held: the first's room what the second leaves it, 40, the second's its even share,
  32; neither refused; one alone over the limit refused.
- **`TestALeftQueryLeavesNothing`**, **`TestAPoolIsToldAGrainAtATime`**,
  **`TestAQueryNoOneLeftIsTakenOutOfItsPool`** (a dropped query leaves at its
  collection) and **`TestAPoolsBudgetIsTheDefaultsKind`** (`execopt`).
- **`TestEveryEntryPointLeavesItsEngine`** (root): a sort through all twenty ways of
  running one, broken off, cancelled, panicked and failing to plan, leaves its
  engine with nothing held and none running; one paused is counted; a query given no
  engine, and a frame's own operation, run on the default one.
- **`TestAnEnginesPressureSpillsAndNeverFails`** (root): seven queries each alone on
  a budget of 3/2 its peak, with another holding 5/4 of its peak, on one thread and
  four: a sort, a median of the whole frame and a join by a predicate alone spill;
  an implode of the whole frame, a left join's build of one key and a cumulative_eval
  with no partition_by and over one key hold; each answers as alone. Half as much
  again, because a query made to spill holds its rows' positions and its files'
  buffers beside its rows: at 5/4 the cumulative_eval over one key passed the limit
  alone.
- **`TestTwoQueriesOnAnEngineShareIt`** (root): a sort paused halfway, and a second
  sort on its engine spills where it does not alone; a query given `WithMemoryLimit`
  does not, and is not counted.
- **The process limit** (`memlimit_test.go`): an engine of the default budget sets
  it, one given a limit leaves it.

**Teeth: 13 of 15 bite:** `Check` asking `Over`; a join's one key, a window with no
partition_by and a window at its depth refusing for the others' pressure; a group-by's
values spilling only alone; `done` not leaving; `Collect` leaving only on success; the
room not shared; no even share; `compile` not joining; a query's total not told; no
cleanup; a query of its own limit on the engine. Silent: a group-by's freeze at its
last depth, which pressure does not reach, its sub-sinks each holding a sixteenth of
the keys their parent routed; and a join with no key and a residual that neither
crosses nor masks, which no public query builds: `JoinWhere` is a cross join and a
filter, which spills.

**Gate:** the light gate, 28 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1. The internal packages and the root in
full before it, the root 190 s.
