# Step 396 — as built

**A small query runs small.** B1 of `v0.8-scope.md`.

## 1. What was wrong

Every pipeline with a batch operator got a dispatcher and a worker a thread
(`parallel.go`), and a group-by a sink a thread and their merge, whatever its rows:
only the kernels' own splits waited for 65,536 rows. `svcprobe lat` on step 395's
build, the median of its runs:

| rows | query | all threads | one thread |
| --- | --- | --- | --- |
| 1,000 | filter, select | 75 µs | 32 |
| | group-by sum, mean | 133 | 48 |
| | join 100-row dimension | 164 | 108 |
| | sort, head 10 | 37 | 31 |
| 10,000 | filter, select | 272 | 209 |
| | group-by sum, mean | 409 | 268 |
| | join 100-row dimension | 734 | 1,026 |
| | sort, head 10 | 173 | 164 |

Measured at 10,000 to 70,000 rows on one, two, four and all threads: a filter and
projection is as quick on one thread as on all up to 20,000 rows, and the workers
are the quicker by two fifths at 40,000; a group-by is quickest on one sink up to
20,000 rows, on two at 40,000 and four at 70,000; a sort the same on any. A join is
slower on one thread at every size past a batch, 1.5 to 2.8 times: its streaming
build, the only one on one thread, keys its table by encoded bytes, which B4 is to
change. So a join keeps its threads past a batch.

## 2. How

Three rules, each only where the caller gave no `WithThreads`, by the sources' own
counts, an in-memory frame's length or a Parquet file's footers, through
`plan.LargestInput`, the most rows any one source of a plan gives, and false where
one has no count, a CSV file:

- **A query whose every input fits in a batch** is planned on one thread
  (`collectCfg.planThreads`): its kernels, its sources and its join's build too. A
  `Head` counts its scan's hint, so a head of ten of a large frame is one batch.
- **A pipeline whose sources give at most 16,384 rows** runs on the goroutine that
  pulls it (`Options.parallelisePlan`, in `PlanRoot` and `planPipelined`).
- **A group-by whose input is at most 32,768 rows** builds one sink (`aggWorkers`).

`physical.Options.Fewer` says the threads are the default's. `WithThreads(n)` runs a
query on n at any size, as before: the tests of the parallel paths run small inputs
on threads they name, and `WithThreads(1)` still reproduces the serial tree.

## 3. Measured

`svcprobe lat`, step 395's build and this one alternated four times, at a load of 5
to 8:

| rows | query | all threads, 395 | here | one thread, here |
| --- | --- | --- | --- | --- |
| 1,000 | filter, select | 74.9 µs | 32.8 | 32.0 |
| | group-by sum, mean | 132.5 | 47.0 | 47.1 |
| | join 100-row dimension | 164.4 | 101.9 | 101.9 |
| | sort, head 10 | 36.5 | 30.5 | 29.7 |
| 10,000 | filter, select | 272.1 | 200.2 | 194.6 |
| | group-by sum, mean | 409.1 | 250.9 | 236.4 |
| | join 100-row dimension | 733.5 | 665.5 | 911.2 |
| | sort, head 10 | 173.3 | 173.9 | 168.6 |
| 100,000 | the four | 0.94 to 1.07 of 395's | | |

**Many at once:** `svcprobe conc`, the 10,000-row group-by from 64 goroutines on all
threads, 7,187 and 7,191 queries a second where 4,278 and 5,088, and 66 goroutines at
the peak where 751 and 808; from 16, 5,454 and 5,407 where 3,791 and 2,200.

**The suites:** PDS-H at SF=0.1 against step 395's runner, four rounds: 1.00 on four
threads given, where the rules do not apply, and 0.98 on the default's, the small
tables' pipelines serial.

## 4. Tests

- **`TestLargestInputIsTheLargestSource`** (`physical`): a join of 3,000 and 100 rows
  is 3,000; a scan hinted at 10 rows, 10; a join reading a source of no count, none.
- **`TestASmallInputRunsSmall`** (`physical`): a pipeline of 16,384 rows planned
  serial, one of 16,896 parallel, and one of 512 on the caller's threads parallel; a
  group-by of 32,768 rows one sink, of 33,280 eight, and of 512 on the caller's
  threads eight.
- **`TestASmallQueryRunsOnOneThread`** (root): a frame of a batch, a join of a batch
  to 100 rows and a head of ten of 100,000 rows on one thread; a batch and a row, a
  join to 10,000 rows, a batch given `WithThreads(4)`, two batches of 1,024 rows and
  a CSV file on their threads.

**Teeth: 7 of 7 bite:** the query's threads not looking at its size; looking under
`WithThreads`; a small pipeline parallel; the pipeline rule under the caller's
threads; a small group-by on every sink; a source of no count counted as none; the
sources' rows summed.

**Gate:** the light gate, 28 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1. The internal packages and the root in
full before it, the root 238 s at a load of 5 to 8, 190 at step 395's of 3.
