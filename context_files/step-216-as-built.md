# Step 216 — as built

**S2 of `v0.6.3-scope.md`, a pool of ZSTD decoders: measured, and not built.**

## 1. The evidence

arrow-go decodes every ZSTD page through one decoder for the whole process, at
klauspost's default concurrency, `min(GOMAXPROCS, 4)`.

- **Before step 214,** the trace showed no waiting there: the read-ahead window let
  only about two decodes run at once.
- **After it,** h2o j3's block profile on step 215's runner showed decodes waiting in
  `zstdCodec.DecodeWithError`: 0.84 s across two runs of about 1.3 s, a third of a
  worker on average.

## 2. What was tried

A codec of pooled decoders, each of concurrency one, taken for one decode and given
back:

- encoding and streaming forwarded to arrow-go's codec;
- registered in `init` over arrow-go's own codec only.

The Parquet tests passed through it.

## 3. Measured, step 215's runner against it

Alternated three rounds:

| query | CPU | wall |
| --- | --: | --: |
| h2o j3, Parquet | 9,826 → 9,619 ms, −2% | 1,322 → 1,285 ms, −3% |
| h2o j1, Parquet | 7,595 → 7,463 ms, −2% | 1,086 → 1,061 ms, −2% |
| PDS-H q1, SF=1 | 2,521 → 2,569 ms, +2% | 455 → 459 ms, +1% |
| PDS-H q17, SF=1 | 1,228 → 1,260 ms, +3% | 254 → 257 ms, +1% |

All within noise.

- **The laptop's four physical cores are saturated** once the reader runs ahead,
  step 214 found. A decode that no longer waits only takes a core from other work.
- **The cap would matter on a machine with more cores than this one,** and is left
  for when the report can run on one.
- **Not built,** by the rule every speed step keeps: a gain that does not show is not
  kept. The code is kept outside the tree, in the session's scratch, not committed.

## 4. Also measured: the probe's queue depth

h2o j3's block profile also showed the parallel probe's workers waiting, on jobs
and on their lanes, which `Next` reads in turn. Run at a depth of 8 for 2
(`parQueueDepth`), j3, j1 and gb4 were unchanged: 1,139 → 1,142, 1,036 → 1,054,
and 242 → 245 ms of wall time. The waiting is idle workers, not a bound; the depth
stays 2.

**What j3 spends instead:** ZSTD 22% of its CPU, gathering the build side's columns
(`kernel.Take`, strings most) 22%, copies 13%. Avoiding the gather is late
materialisation, a larger item than this release's.
