# Step 101 — as built

**The profile 0.4's performance work is ranked by.** This is `v0.4-scope.md` item 1:
"no profile ever measured *why* ursus trails at the scan and join level".

It is a measurement and two flags. No engine code changed.

## 1. How it was taken

- **The Go bench runner gains two flags,** for any engine.
  - `-cpuprofile` covers the timed iterations only: not the warm-up, not the
    setup.
  - `-allocprofile` writes the allocs profile, every allocation by site, after
    them.
- **One targeted query at a time,** never the suite:
  - PDS-H q6, q7, q15, q19, q1 and q9 at SF=0.1, five iterations each;
  - q6 and q7 at SF=1, two iterations;
  - h2o j3 at 2M rows, two iterations;
  - a scratch micro-benchmark for the fixed cost per query.
- **The machine has 8 hardware threads.** "Cores busy" below is CPU time over wall
  time.

## 2. What it says

### 2.1 The Parquet reader is the first cause, by every measure

| query | wall per run | cores busy | reader's share of CPU | GC's share |
| --- | --- | --- | --- | --- |
| q6, SF=1 | 440–456 ms | 2.3 of 8 | **47%** | 20% |
| q7, SF=1 | 1.76–1.94 s | 1.7 of 8 | **41%** | 5% |
| j3, 2M rows | 586–633 ms | 1.8 of 8 | **37%** | — |

- **The reader decodes on one goroutine,** under `reader.mu`. So its share of CPU is
  also, roughly, all of the wall time: in q6, 0.96 s of reader CPU over two runs
  that took 0.9 s.
- **Everything parallel behind it waits.** That is why fewer than 2.3 of 8 cores
  are ever busy.
- **zstd decompression is about a quarter of the reader.** The rest is ursus
  building its columns, including validity built a bit at a time
  (`bitmap.Builder.Append`, 7% of q6).
- **It is 77% of all allocation in q7** (allocs profile; warm-up plus one run, 7.6
  GB):
  - 3.7 GB in `fixedCol.read`'s per-batch value slices, which `data.NewFixed` then
    copies into an Arrow buffer: a second allocation of every value;
  - 2.6 GB in arrow-go's `GoAllocator`, for page buffers that are never reused.

  q6's 20% GC follows from that churn: it allocates 3.4 GB to hold 18 MB live.

### 2.2 The join build is the second, in the join-heavy queries

In q7 the build is **26% of CPU**, almost all of it in `KeyTable.GetOrInsert`, and
serial. The right side always builds (`join.go:1501`), and in q7 that is the larger
side. The probe is 5%. In j3 the build side is small and does not show.

### 2.3 Smaller, and cheap

- **The final `Collect` in j3 is 17% of CPU, serial.** `kernel.Concat` rebuilds
  String columns through `data.NewString` from Go strings, row by row, where
  copying the offsets and characters would do.
- **Filters are 15–24%,** in parallel. A literal is expanded to n elements to
  compare against (`broadcastVals`: 190 MB in q7), and the selection then copies
  every column (`FilterBatch` and `Take`).

### 2.4 Not a cause

- **Fixed cost per query:**
  - 24 µs for a one-row filter and select;
  - 63 µs for a one-row group-by;
  - 93 µs for a one-row Parquet scan.

  The SF=0.1 queries take 45–180 ms. It is not why ursus trails there.
- **The probe** is 5% of q7. Its gather is 24% of j3, but runs in parallel.

## 3. The ranking this gives `v0.4-scope.md`

1. **Item 2, parallel Parquet decode,** with two things the allocs profile adds to
   it:
   - read straight into the column's final buffer, instead of a slice that is
     then copied;
   - build validity a word at a time.

   This is the first lever for every Parquet query.
2. **Item 3, the join build side,** then a parallel build. This is q7, q8, q9 and
   q2.
3. **String concatenation in `Collect` without per-row strings.** Small, and 17% of
   j3.
4. **Item 16, filters without copying.** It is next behind these, not ahead of
   them.
5. **Items 13–15 and 17** (CSE, top-n within a group, median by selection, radix
   group-by) are unchanged in order. This profile did not measure the queries they
   move.

The scope's numbers predated step 89. q7's 43.5× is now about 20×: 1.85 s here,
against Polars' 90 ms in the v0.3.0 report.
