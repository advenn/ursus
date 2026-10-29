# Step 394 — as built

**A cancelled query stops, and says so.** A2 of `v0.8-scope.md`.

## 1. What was wrong

Measured first with `svcprobe cancel`, each query over ten million rows cancelled 10
and 50 ms in, and a count of h2o's `g1` read from CSV and Parquet:

- **A cancelled query could answer part of its input as the whole.** A count and sum
  of `g1.csv`, cancelled 50 ms in, returned an empty frame and no error, and so did
  `g1.parquet`'s cancelled at 10. `parallelOp`'s dispatcher stops when its context is
  cancelled and its lanes close; `Next` read a closed lane as the end of its input
  (`parallel.go`), and a group-by above it answered on the rows it had. The join's
  probe did the same (`parjoin.go`). The drivers, `drain`, `Batches` and `Count`,
  took the end of a stream for its end. A service whose request timed out answered
  "no rows".
- **A sort** came back 410 to 530 ms after its cancel: the in-memory sort at its
  `Finish` ran every radix pass, and no kernel took a context.
- **A join** to five million keys came back 100 to 170 ms after: its partitioned
  build inserted every key first.
- A group-by, a collect and the scans came back within 9 ms.

## 2. How

- **The drivers** (`exec.go`): a stream that ends under a cancelled context answers
  the cancellation, from `Collect`, `CollectBatches` and `Count` and every writer
  through them. Whatever operator truncated its output, the query cannot answer it.
- **A closed lane** (`parallel.go`, `parjoin.go`) under a cancelled context is the
  cancellation, not the end, so a group-by above does not finish on a part.
- **The sort** (`kernel.ArgSortColumnsN` takes the context): the radix sorter looks
  at it before each column and each byte's pass and at its end; the comparator sort,
  before each round of merges and at its end, a run being sorted whole.
- **The join's partitioned build** (`buildPartitioned`, `buildDense`) looks at it a
  batch at a time in each of its passes.

## 3. Measured

`svcprobe cancel`, step 393's build and this one, at a load of about 2:

| cancelled at 10 and 50 ms | step 393 | here |
| --- | --- | --- |
| a group-by of five million keys | 0.5 and 8.0 ms, the second not cancelled | 0.6 and 0.9 |
| a sort of ten million rows by two keys | 531 and 410 ms | 25 and 49 |
| a join to five million keys | 169 and 100 ms | 16 and 1.5 |
| ten million rows collected | 0.2 and 4.3 ms | 0.1 and 1.0 |
| `g1.parquet` scanned and grouped | 1.9 and 5.2 ms, the second not cancelled | 0.5 and 0.9 |
| `g1.csv` scanned and grouped | 9.2 and 5.0 ms, neither cancelled | 5.9 and 4.4 |

"Not cancelled" is an answer and no error, from a query whose input could not have
been read in the time: the bug above. Here every one is cancelled but the collect at
50 ms, which had read all ten million rows before the cancel, and answered them: its
whole answer, as it should. No goroutine is left after any.

The 22 PDS-H queries at SF=0.1, step 393's runner and this step's alternating, two
rounds of three at a load of about 5: 594.9 ms summed, 590.9 after, 0.99, each query
0.90 to 1.08.

## 4. Tests

- **`TestACancelledQueryNeverAnswersInPart`** (root): 300,000 rows from CSV bytes,
  Parquet bytes and a frame, counted and summed, grouped, sorted and joined, each
  cancelled 24 times at moments spread over its own running time: each answer the
  cancellation or the whole answer.
- **`TestAStreamEndedByACancelIsTheCancel`** (`exec`): an operator that cancels its
  query and ends its stream, through `Collect`, `Batches` and `Count`.
- **`TestASortStopsWhenCancelled`** (`kernel`): a radix sort of a Float64 and an Int64
  and a comparator sort of an Int128 and an Int64, cancelled at their second look,
  answer the cancellation; uncancelled they sort.
- **`TestARadixSortStopsAtItsNextPass`** (`kernel`): four million floats cancelled at
  the first byte's pass come back in under half the time a whole sort takes, the
  least of three of each in one process.

**Teeth: 3 of 6 bite:** `drain` answering a stream's end whatever its context;
neither the drivers nor the lanes asking it, which the first test catches; a radix
pass not looking, which the time test catches where the cancellation test did not.
Silent, each covered by a check behind it, so their cost is time, not a wrong
answer: a closed lane read as the end with the drivers' check kept; a radix sort's
last look gone, its columns' still there; a merge round's gone, its end's still
there.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
