# Step 400 — as built

**What a query allocates.** B3 of `v0.8-scope.md`.

## 1. What was wrong

`svcprobe alloc` on step 399's build, each of its four queries run fifty times on one
thread over a frame of an Int64 key, a Float64 and a text column, the bytes a query:

| rows | filter, select | group-by | join to 100 rows | sort, head 10 |
| --- | --- | --- | --- | --- |
| 1,000 | 40.1 kB | 57.5 | 87.3 | 11.9 |
| 10,000 | 440.2 | 357.9 | 905.5 | 149.7 |
| 100,000 | 4,202.0 | 807.5 | 5,779.7 | 1,328.5 |

The group-by's answer is 100 rows; it allocated 358 kB at 10,000. Profiled with
`runtime.MemProfileRate` at 512:

- **The group-by's scratch grew from nothing in every query.** Its group ids and its
  resident rows (`groups`, `keep`) were appended from empty a batch at a time, a dozen
  times each, and `keep` listed every row of the batch, which an open sink keeps all
  of: 250 kB of the 358.
- **A text column's slice copied its offsets.** `Column.Slice` said it was O(1) and
  copied the window of its offsets. The in-memory source cut every column of a batch
  to the batch size and then kept the projection's, so a group-by that never read the
  text column paid for its offsets; a join's probe sliced its left batch for each run
  of rows.
- **A constant was copied into a column.** `x*2.0` made a column of 2.0s the batch's
  length before multiplying, as `x+1` and `x-1` did: 15% of the filter's bytes.
- **The probe's selections grew from nothing in every query**, as the group-by's
  did: 265 kB of the join's 905.

And at sixty-four goroutines, each collecting the 10,000-row group-by on one thread
(`svcprobe conc64`), the collector took 14 to 17% of the CPU, at 2.3 to 4.1 GB
allocated a second.

**The Parquet page pool** (step 376) was in no budget: a scan of all sixteen of
lineitem's columns at SF=1 holds up to 105 MiB of pages at once, and an engine's other
queries did not see them. A3 had said the pool is counted in the shared budget, and
step 395 moved it here.

## 2. How

- **The group-by's scratch** (`hashAggSink.Consume`): its group ids are given a
  batch's room at once. `keep` is a frozen sink's alone, the rows it keeps of a batch
  whose new keys it routes; an open sink keeps every row, and does not list them. The
  four ways of keying a batch (integers, packed words, a string's own bytes, encoded)
  each stopped appending to it.
- **A text column's slice is a view** (`Column.Slice`): of its parent's offsets, by
  `memory.SliceBuffer`, as a fixed column's is of its values. The offsets were never
  rebased, so the view holds the values the copy did. Nothing writes a column's
  offsets in place; Arrow's export and the spill read them as they are.
- **The in-memory source projects before it cuts** (`memsrc`): `Next` takes the
  projection's columns and then slices the batch, so it slices only what it hands on.
- **A column and a constant** (`byConst`, `divByConst`): added, subtracted,
  multiplied, and for floats divided, with the constant read once, on either side:
  `addScalarConst` and its kin, written at the start and never called, and a
  subtraction and a division from the left. Float64 adds and multiplies by a
  constant in SIMD (`addF64SIMDConst`, `mulF64SIMDConst`), the constant broadcast to a
  vector. The rest (a floor division, a modulo, a power) still broadcast.
- **The probe's selections** (`joinProbeOp.room`): given the probe batch's rows, up to
  the batch size, before the first match; a join of keys emits about a row for each
  it probes.
- **A scan's pages press its engine** (`execopt.Budget.Pressure`, `scanPages`): a
  Parquet scan opens its files with an allocator of its own over the pool, which
  tells `ScanSpec.Pages` what its page readers hold, and nothing more once it closes:
  a buffer kept past the end is untold, and one given back later is not told again.
  The planner gives it the query's budget's `Pressure`. On an engine's pool those
  bytes count against the room of the other queries and never against this one's:
  like its read-ahead (step 214), they are bytes about to be consumed, which its own
  sinks should not spill for. Told before the query joins, they reach the pool as it
  joins; as it leaves, the pool lets go of them. `EngineStats.Held` counts them. A
  budget on no pool, `WithMemoryLimit`'s, does nothing with them.
- **Left:** `Collect`'s copy, the answer concatenated from its batches into one, a
  third of the filter's bytes and half the join's at 10,000 rows: a frame is one
  batch. A top-k concatenates its keys to compare them, 800 kB of the 100,000-row
  sort's 842. The rest is the answer, taken, computed and copied.

## 3. Measured

`svcprobe alloc`, this build:

| rows | filter, select | group-by | join to 100 rows | sort, head 10 |
| --- | --- | --- | --- | --- |
| 1,000 | 34.9 kB | 36.3 | 66.0 | 11.9 |
| 10,000 | 334.3 | 65.6 | 597.8 | 101.7 |
| 100,000 | 3,126.2 | 73.3 | 4,594.5 | 841.8 |

The 10,000-row group-by allocates 0.18 of what it did, at 100,000 rows 0.09; the
10,000-row join 0.66, the filter 0.76, the sort 0.68.

`svcprobe conc64`, four runs each alternated, at a load of 5 to 8 with another
program on a core:

| | queries a second | the collector's CPU | allocated a second |
| --- | --- | --- | --- |
| step 399 | 6,859 to 8,179 | 14.0 to 15.9% | 2.4 to 2.9 GB |
| here | 9,431 to 11,700 | 7.6 to 10.6% | 606 to 751 MB |

`svcprobe lat`, ten runs each alternated, the order turned after four, the median of
each query's medians:

| rows | | filter, select | group-by | join to 100 rows | sort, head 10 |
| --- | --- | --- | --- | --- | --- |
| 1,000 | all threads | 1.05 | 0.77 | 0.87 | 0.98 |
| | one thread | 0.99 | 0.79 | 0.92 | 0.93 |
| 10,000 | all threads | 0.68 | 0.59 | 0.77 | 1.03 |
| | one thread | 0.74 | 0.60 | 0.72 | 0.95 |
| 100,000 | all threads | 0.82 | 0.52 | 0.75 | 0.92 |
| | one thread | 0.80 | 0.75 | 1.02 | 0.83 |

The geometric mean of the 24 is 0.81, of the one-thread ones 0.83. The 10,000-row
group-by on one thread is 156 µs where 258.

**A scan's pages, counted:** an engine's peak over a scan of all of lineitem's columns
at SF=1, filtered to no rows by a predicate no statistic answers, three runs each:
13.7 MiB on one thread, 52.9 on four, 104.8 on eight, where step 399's engine saw
none; none held once each ended.

PDS-H at SF=0.1 against step 399's runner, four rounds on four threads, at a load of
9 to 11: 1.02; q3 at 1.33 and q18 at 1.17, again six rounds: 0.97 and 0.96.

## 4. Tests

- **`TestAConstantIsReadOnce`** (`kernel`): a Float64 column and a constant, on either
  side, added, subtracted, multiplied and divided, NaN, the infinities, both zeros
  and a subnormal among them, answer bit for bit what the constant copied to a column
  answers; an Int64's and an Int8's, which wrap, added, subtracted and multiplied,
  too; none allocates more than two columns do.
- **`TestDifferentialArithmeticConst`** (`kernel`): the SIMD constant kernels against
  their scalar twins, every length the differential tests use.
- **`TestAStringSliceIsAView`** (`data`): a slice of a String column, and a slice of
  that, read their windows of the parent's offsets, and its rows and nulls.
- **`TestAProjectionIsTakenBeforeTheCut`** (`memsrc`): a projection of two columns of
  three, cut to batches of 4, 4 and 2, its columns in its order and its rows.
- **`TestAProbeTakesItsBatchsRoomAtOnce`** (`physical`): a probe's selections have 300
  rows' room under a batch size of 300, not the 512 doubling reaches.
- **`TestAnOpenSinkListsNoRows`** (`physical`): a group-by by an integer, two, a
  string and an encoded float, each keyed as it should be, lists no row of an open
  sink, and its group ids have 500 rows' room under a batch size of 500.
- **`TestAScansPagesPressTheOtherQueries`** (`execopt`): a query's pages leave the
  other query's room 44 MiB of 64, its own 64; told before it joins, at its join; let
  go of as it leaves; told after, not; a budget on no pool takes none.
- **`TestAScanTellsItsPages`** (`parquet`): a scan, serial and on four threads, read
  whole and after one batch, tells at least a page of 400,000 bytes and holds none
  once closed. **`TestAScansPagesEndWithIt`**: a grown buffer told by its new size; a
  buffer kept past the close untold; one given back later not told.
- **`TestAnEngineCountsAScansPages`** (root): an engine's peak over a Parquet scan of
  200,000 rows that holds nothing is its pages, 3.5 MB on one thread and 5.2 on four;
  none held after.

**Teeth: 19 of 19 bite:** the probe growing its selections; a float's and an integer's
constant broadcast, which only the allocation count bites; the left constant's
subtraction and division turned; the SIMD constant add subtracting; a string slice
copying its offsets, and one row short; an open sink listing its rows; the group ids
grown; a frozen sink feeding every row, which the spill tests bite; a query's own
pages pressing it; pages told before joining lost; a query leaving its pages; pages
reaching the pool unjoined; a closed scan keeping its pages told; a late free told;
a scan telling nothing; the planner not wiring the pages. A closed scan keeping its
pages told and a late free needed `TestAScansPagesEndWithIt`: arrow-go gives every
page back before a scan closes, so a real read never shows them.

**Gate:** the light gate, 28 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1. The internal packages and the root in
full before it.
