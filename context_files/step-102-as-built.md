# Step 102 — as built

**Parquet row groups are decoded in parallel, in file order.** This is
`v0.4-scope.md` item 2, and the first lever step 101's profile ranked.

## 1. What was wrong

The reader decoded every column of every row group on one goroutine, under
`reader.mu`, and ignored `ScanSpec.Threads`. Step 101 measured it:

- **37–47% of the CPU** of q6, q7 and j3, and so roughly their whole wall time;
- **fewer than 2.3 of 8 cores** ever busy.

A row group is independent of every other. Its column chunks are separate byte
ranges, read by separate page readers.

## 2. The design

**`parallelReader`, in `internal/source/parquet/parallel.go`.**

- **Issuing.** `Next` issues row groups in file order, pruning each exactly as the
  serial reader does. It starts one goroutine per row group, with at most
  `Threads` in flight.
- **Order.** Each worker decodes its row group batch by batch into its own channel,
  holding at most two batches ahead. `Next` reads the channels in the order they
  were issued, so a row group's rows all come before the next one's. The output is
  the serial reader's, batch boundaries apart.
- **Memory.** At most `Threads` row groups decode at once, each about three batches
  ahead. A row group is issued only when an earlier one has been read to the end.
- **Files.** Each file is shared by the workers decoding its row groups:
  - arrow-go's `RowGroup` is built for concurrent use, and says so;
  - each column's page reader reads its own range through the file's `io.ReaderAt`,
    whose contract allows parallel `ReadAt`.

  A file is reference-counted and closed when its last row group finishes and the
  issuer has moved past it.
- **Failures.**
  - A panic in a worker is the file's (`corrupt`), as in the serial reader.
  - A worker that ends any other way — a `runtime.Goexit` runs the defers and
    recovers nothing — sends an internal error. Otherwise its closed channel would
    read as a finished row group, and its rows would silently go missing.
  - The first failure is returned from every `Next` after it.
- **`Close`** closes `stop` under the lock, so no worker starts once `Close` has
  begun: a `WaitGroup`'s `Add` may not race its `Wait`. It then waits for every
  worker, outside the lock, and releases the files. `Next` waits on a channel
  without the lock, so `Close` can stop it while it waits.
- **When it is not used:** with one thread (`WithThreads(1)` still reproduces the
  serial tree), and under `MaxRows`. Under a limit, reading ahead would decode row
  groups the query never wants.
- **The serial reader and the parallel one share their code.** `openColumnIn`,
  `shouldSkipIn` and `readBatch` were moved out of `reader`, unchanged.

The goroutine inventory (`TestEveryGoroutineIsCovered`) refused the new `go`
statement until it was listed with the test that shows its panics are errors.

## 3. Measured

One targeted query at a time, against step 101's runs:

| query | serial reader | parallel | |
| --- | --- | --- | --- |
| q6, SF=0.1 | 44–48 ms | 33–36 ms | −26% |
| q7, SF=0.1 | 158–169 ms | 123–134 ms | −21% |
| q15, SF=0.1 | 92–112 ms | 67–79 ms | −28% |
| q19, SF=0.1 | 109–115 ms | 84–93 ms | −22% |
| q1, SF=0.1 | 98–110 ms | 75–86 ms | −23% |
| q9, SF=0.1 | 164–180 ms | 111–125 ms | −31% |
| **q6, SF=1** | 440–456 ms | 265–277 ms | **−40%** |
| **q7, SF=1** | 1.76–1.94 s | 1.19–1.34 s | **−31%** |
| j3, 2M rows | 586–633 ms | 573–578 ms | −6% |

- **Peak RSS rose with the batches in flight:** q6 at SF=1 from 50 to 108 MB, q7
  from 555 to 633 MB.
- **j3 moved least, and its profile says why.** The decode is parallel now (2.6
  cores busy), and what remains is `Collect`'s final concatenation, serial: 0.37 s
  of the 0.58 s. It rebuilds every String column through `data.NewString`, one Go
  string per row. That is the next step.

## 4. Tests and teeth

**`TestParallelParquetScanMatchesSerial`:**

- 30,000 rows, as three files of 1000-row row groups (30 row groups in all), every
  decoder shape, with nulls:
  - integers, a float, a string;
  - a Date, a Bool;
  - a List of strings, a Decimal.
- Four queries, compared at 8 threads against `WithThreads(1)` at batch sizes 7,
  1000 and 8192, order included:
  - every column;
  - a pruning filter;
  - two columns;
  - a limit, which takes the serial path.

**`TestParallelParquetScanReleasesEverything`:** a consumer that stops after one
batch, and a cancelled context. Every file opened is closed, and the goroutine count
returns to where it was.

**`TestParallelRowGroupPanicIsAnError`:**

- The file's own `ReadAt` panics on a column chunk, read by a worker: the query
  returns an error carrying the panic.
- Every file is closed, and no goroutine is left.

The whole suite runs at `NumCPU` threads by default, so every existing Parquet test
now runs the parallel reader. The new tests ran three times under `-race`.

| tooth | result |
| --- | --- |
| `Next` reads the newest row group, not the oldest | **bites:** every multi-row-group comparison |
| workers never release their file | **bites:** the release tests, and `TestScanParquetFrom`'s every-reader-is-closed |
| no recovery in a worker | **bites:** the test binary dies on the panic |
| no pruning in the parallel path | **bites:** `TestParquetPruningActuallySkips` and `TestScanParquetFrom`'s footers-only case |
| `Close` does not wait for its workers | **bites:** the release tests |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB — with the parallel reader, since the suite runs at every core.
