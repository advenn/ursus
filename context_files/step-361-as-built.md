# Step 361 — as built

**A sort on several threads:** the radix passes, the comparator's merge sort, the
rows' places in their batches and the output's gather each split across the query's
threads. The permutation is the serial sort's exactly, ties included.

## 1. How

**`kernel.ArgSortColumnsN(cols, specs, n, workers)`** is `ArgSortColumns` on up to
`workers` goroutines. On one worker, or fewer than 65,536 rows (`parallelMin`), it is
`ArgSortColumns`.

- **The radix passes** (`radixSortedN`). The sorter gained `workers`, and
  `radixSorted` hands a column of 65,536 rows or more to its parallel twin. The rows
  are cut into a chunk a worker:
  - the keys are gathered into the present order, a chunk a goroutine;
  - one pass counts every byte's histogram of every chunk. Their sum says which bytes
    take one value, which are skipped as before, and they are the first round's
    chunk histograms; a later round counts its byte again;
  - chunk w's first place in bucket v is past every row of the buckets below v and
    of chunks 0 to w−1 in v. So a bucket holds its rows in the order they were
    read, as one goroutine's pass leaves them, and the sort stays stable;
  - each chunk scatters its rows on its own goroutine.
- **The merge sort** (`ArgSortN`), for a key the radix cannot take, a String's:
  - each worker stably sorts a run of the rows;
  - runs are merged in pairs, level by level, between two buffers; an odd run out is
    copied across;
  - each merge is cut into pieces a goroutine. A piece's start in each run is found
    by binary search, merge path's co-rank (`coRank`);
  - a tie is taken from the left run, whose rows come first, in the co-rank as in the
    merge, so the merge is the stable sort's.

**`sortSink`** gained `threads`, set by `planSort` from the query's. The sorts a
join's order, a sorted window and a temporal group build stay serial.

- `order` sorts by `ArgSortColumnsN`.
- **The rows' places in their batches.** A row's global index was found in its batch
  by binary search, a third of a serial sort of a million rows. When every batch but
  the last holds a power of two's rows, as a scan's do, the batch is the index
  shifted, the last batch taking the rest (`uniformShift`). Otherwise the search
  stays. Either way, a piece of the rows a goroutine (`eachPiece`).
- **The output's gather** (`sortedBufferRun.nextAhead`). On several threads a window
  of as many batches is gathered ahead, each on its own goroutine with a channel of
  one, and they are handed out in order. A gather's panic is its error, in its
  turn. `Close` waits for the window.

## 2. Tests

- **`TestParallelSortMatchesSerial`** (kernel), `parallelMin` lowered to 64:
  - `ArgSortColumnsN` on 2, 3, 4 and 7 workers gives `ArgSortColumns`' permutation;
  - at 64, 65, 127, 1,000, 4,099 and 70,000 rows;
  - integers of few values and of many, floats with NaN, infinities and −0, nulls,
    a Uint8, strings, three keys and two;
  - ascending and descending, nulls first and last.
- **`TestCoRankSplitsAMergeAsTheMergeDoes`:** every prefix of 300 random merges, ties
  among them, is the co-rank's.
- **`TestASortFindsEachRowsBatch`** (physical): batches of 8, 8, 8, 3; of 8, 8, 8, 20;
  of 8, 5, 8, 8; of 6; of 16 alone; of one row; on one thread and on four, against
  the stable sort.
- **`TestASortsGatherAheadKeepsItsErrors`:** a gather ahead that fails, by an error
  and by a panic, fails in its turn, after the batches before it.
- **`TestSortOnThreadsMatchesOneThread`:** 150,000 rows, on four threads against
  one:
  - five sorts: an integer, a float descending with its nulls last, a string then an
    integer, a distinct integer, and the top 5,000;
  - batches of 8,192, 1,000 and 70,000 rows;
  - each again past a 1 MiB limit, spilled.

**Teeth: 18 of 21 bite.**

- **The radix passes:**
  - the offsets chunk-major;
  - a later round counted by the first pass;
  - a byte that varies skipped;
  - every chunk given chunk 0's offsets;
  - the keys not gathered in the present order.
- **The merge sort:** a tie taken from the right, in the co-rank and in the merge; an
  odd run not copied; a piece's end its start.
- **The mapping:** a shift for a batch of any size; a short middle batch let through;
  a long last batch shifted past; a piece one row short; a searched piece written at
  the start.
- **The gather:** the window handed out last first; a row skipped between batches; a
  gather's error lost; its panic not recovered.

The three silent ones turn the parallelism off and change no answer: the radix's
dispatch, the threads never reaching the sort, the tail gathered serially. The
timings below are their test.

## 3. Performance

A frame of a million rows in memory, median of seven, with the machine under
other work (a load of 5), Polars 1.44.1 measured alongside:

| | before | 1 thread | 4 threads | 8 threads | Polars |
| --- | --- | --- | --- | --- | --- |
| distinct Int64s | 99–107 ms | 69 | 36 | 30 | 18.9 |
| distinct Int64s and a Float64 | 125–143 | 97 | 63 | 48 | 44.0 |
| distinct strings of 8 bytes | one thread's | 963–1,430 | 445 | 291 | 64.1 |

Polars sorted the integers in 9.9 ms and the pair in 31 on a quiet machine.

- **One thread** gained a third from the mapping's shift alone. The binary search was
  280 ms of seven runs' profile.
- **Several threads:** a profile of four shows the radix scatter, a quarter of its
  time, the gather of the Int64s a sixth, and the keys' concatenation a tenth, which
  is serial.
- **The strings** are Go's stable sort: its in-place merge rotates, and each
  comparison calls two closures and reads two offsets. The merge's split cuts the
  time by five on eight threads, but the gap to Polars is the comparison's. A radix
  pass on each string's first eight bytes is recorded in feature-gaps §8.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. The other widths, the scalar build and the race detector are CI's.