# Step 408 — as built

**The 0.7 report, and its documents.** It ran on 0.7's code, `16e617a`, the last
commit before 0.8's scope (step 392), and changed no code.

0.7 is **performance,** the Parquet read first (`v0.7-scope.md`, step 375). Steps 376
to 391 built it:

- **the Parquet read (376–378, 380):** pages decompressed into reused buffers; the
  columns beside a text predicate the reader judges read at its kept rows; a
  dictionary's text marked and emitted by the kept rows; zstd's floor recorded;
- **the operators (379, 381, 384, 391):** a join's text gathered by runs; integer
  keys packed into words, a group-by's and a join's;
- **`feature-gaps.md` §8's items (382–390):** a filter of keys below a group-by or a
  window; `Upsample`'s grid joined in its order; a large top-k that sorts; a window
  in an aggregation over each group's list; a sink's windows at once, and its
  aggregate windows handed back batch by batch; a CSV file's floats in one pass and
  `Collect`'s copy on the query's threads; the CSV writer on the query's threads.

## 1. Against v0.6.8

**Setup:**

- `runner-070` was built from `git archive 16e617a`, `runner-068` from `git archive
  v0.6.8`, both `GOEXPERIMENT=simd GOAMD64=v3` as `bench/Makefile` builds one;
- each query ran on both, alternating, their order swapped each round, on four
  threads;
- the laptop had 3 to 7 GB of memory available and 14 to 19 GB of swap in use.

| suite | rounds × iterations | v0.6.8 | 0.7 | ratio |
| --- | --- | --: | --: | --: |
| PDS-H SF=1 | 2 × 5 | 4,768 ms | 4,465 ms | **0.94** |
| PDS-H SF=0.1 | 4 × 10 | 594 ms | 541 ms | **0.91** |
| h2o 10M, Parquet | 2 × 3 | 14,962 ms | 13,843 ms | **0.93** |
| h2o 10M, CSV | 2 × 3 | 23,847 ms | 23,836 ms | **1.00** |

The sums of the queries' medians.

**The widest moves:**

- PDS-H at SF=1: q12 0.74, q19 0.76 and q10 0.81, the Parquet read's (377, 378);
- at SF=0.1: q19 0.71, q12 0.76, and every query 0.99 or less;
- h2o over Parquet: the joins, j1 0.73, j4 0.79 and j2 0.84, whose answers are
  millions of rows (379, 389); gb6 0.81, its keys packed (381).

**Over CSV, level.** The read is most of each query's time, and 0.7's change to it,
a float of 17 digits parsed in one pass (389), does not reach h2o's `v3`, of eight
digits, which v0.6.8 parsed in one already.

**Run again, the four that read above 1.05,** beside controls:

| query | first | again | rounds × iterations | controls |
| --- | --: | --: | --- | --- |
| q1, SF=1 | 1.16 | 0.95 | 4 × 5 | q13 0.99, q18 0.96 |
| gb9, Parquet | 1.10 | 1.01 | 4 × 5 | gb2, gb3, gb7 0.99 |
| gb1, CSV | 1.33 | 0.87 | 4 × 3 | gb5 0.92, gb6 0.89, gb7 1.05 |
| gb10, CSV | 1.08 | 0.92 | 4 × 2 | |

**On eight threads,** six SF=1 queries, three rounds of five: 0.96 in all; q1 0.98,
q2 0.77, q3 0.93, q7 0.93, q9 0.97, q18 0.97.

**Memory:** `gb10`'s peak over Parquet 8.08 GB on v0.6.8 and 7.86 on 0.7, over CSV
9.16 and 8.34, and 7.98 and 7.94 run again; `j5` 5.5 to 6.7 GB on both.

## 2. Against Polars, in one session

**Setup:**

- v0.6.4's results (step 247's session) were copied to
  `bench/results/archive-v0.6.4/`;
- the driver ran directly, not through `make run`, which builds `bin/runner` from the
  working tree, master with 0.8's steps: `PATH_GOBIN` named a directory whose
  `runner` is `runner-070`;
- `run --engines ursus,polars --threads 8 --iterations 3` over the four suites, each
  followed by `validate`, then `report`. The report names the commit `HEAD` is, so
  its provenance line was corrected by hand to `16e617a`;
- the laptop had about 6 GB of memory available and 17 GB of swap in use by IDEs and
  browsers. A test suite of another project's, one core's worth, was running at
  14:32, after the session (14:23 to 14:30), and the work it belonged to had been
  going since 14:03.

**Every answer of ursus's and Polars' matches DuckDB's:** 44, 44, 30 and 30. The
three struck through are other engines' q15, from their earlier session, as in
v0.6.4's report.

| suite | ursus | Polars | ratio | at v0.6.4 |
| --- | --: | --: | --: | --: |
| PDS-H SF=1 | 178 ms | 91 ms | **1.96×** | 2.01× |
| PDS-H SF=1, alternated | 167 ms | 92 ms | 1.82× | |
| PDS-H SF=0.1 | 17 ms | 9 ms | **1.87×** | 2.29× |
| h2o 10M, Parquet | 670 ms | 563 ms | **1.19×** | 1.31× |
| h2o 10M, CSV | 1,505 ms | 1,221 ms | **1.23×** | 1.64× |
| h2o 10M, CSV, alternated | 1,352 ms | 1,126 ms | 1.20× | |
| PDS-H SF=1, highest peak | 0.84 GB | 0.84 GB | 1.00× | 0.90× |

**PDS-H at SF=1 ran twice.** The first run gave 188 ms against 85, 2.21×: ursus's q1
425 ms, q2 95, q3 372 and q18 609, where the eight-thread A/B a minute later gave 0.7
434, 36, 224 and 350 some minutes later, and Polars' were level with v0.6.4's
session. The second run,
the report's, gave 178 against 91. Then the two engines were alternated query by
query (`vspl408.py`), three rounds of five on eight threads: 167 against 92, 1.82×.
h2o over CSV, the largest move, was alternated so too, two rounds of three: 1.20×,
where the session gave 1.23×.

**What moved since v0.6.4's session:**

- ursus over CSV 1,784 → 1,505 ms, over Parquet 697 → 670, PDS-H at SF=0.1 20 → 17;
- Polars over CSV 1,088 → 1,221 and over Parquet 531 → 563, and both engines slower
  at SF=1, ursus 158 → 178 and Polars 79 → 91: the laptop is busier than it was;
- Polars' code is the same, and its geomeans moved 0 to 15% between the sessions:
  the ratios carry that much of the session. The code's own move is §1's, 0.91 to
  1.00 of v0.6.8's time, and v0.6.8's CSV reader's, `gb1` over CSV 0.78 of its time
  (373).

**The widest gaps:**

- PDS-H at SF=1: q15 3.1×, q13 2.9×, q17 2.8×, q19 2.8×, q14 2.7×; ursus is ahead
  on q21, 0.87×;
- h2o over Parquet: gb4 2.2×, j3 1.8×, gb5, gb6 and j4 1.5×; ursus is ahead on gb2
  0.62×, gb8 0.77×, gb7 0.88×, gb10 0.89×, gb9 0.95× and gb3 0.98×;
- h2o over CSV: gb1 2.3×, gb4 1.7×, j3 1.5×; ursus is ahead on j4 0.82× and gb10
  0.88×.

**Memory:**

- ursus's highest peak is gb10 over CSV, 7.24 GB, and over Parquet 5.57, where
  v0.6.4's report had 6.44 and 7.72; Polars' 5.21 and 4.71;
- over the queries, ursus's peaks' geomean is below Polars': 0.22 GB against 0.36 at
  SF=1, 0.68 against 1.6 over Parquet.

**Eight threads are slower than four on this laptop,** four cores of two threads
each: ursus's q1 at SF=1 took about 345 ms on four and 435 on eight, v0.6.8's alike.

## 3. What 0.7 leaves

As the CHANGELOG's known limitations say:

- **measured and left:** expression-level CSE (388); a CSV file read a column at a
  time (389); zstd, about 170 ms of q14's bare scan (380); `Upsample`, about nine
  times Polars' time (383, 391); a window's partitions on one thread (386, 391);
- **not measured:** S1, a work-stealing scheduler, which the scope made depend on a
  profile showing threads idle. The eight-thread figure above is a reason to measure
  it.

## 4. The documents

- **`bench/results/REPORT.md`:** regenerated from the session, its provenance line
  naming `16e617a`.
- **`README.md`:** the summary's ratios, how current the report is, its caveats, and
  the table of where it stands with a column for v0.6.4's.
- **`docs/benchmarks.md`:** the table and how it was taken.
- **`CHANGELOG.md`:** 0.7's section dated for the report, with where it ends and its
  known limitations; 0.8's notes step 408 as 0.7's report.
- **`v0.7-scope.md`:** where it stands.

**The tag, v0.7.0, is this commit,** off master, on the branch `release-0.7`: master
has 0.8's steps 392 to 407 above 0.7's code, so 0.7 alone is `16e617a` with this
step's documents. With them come step 407's fixes of what was stale in the README
and was so at 0.7 too: its version, its three dependencies, the nested types and what
is not done; and the installation page's dependencies and the site's version.

**Gate:** no code changed; `16e617a`'s CI passed, every width, the scalar build and
the race detector.
