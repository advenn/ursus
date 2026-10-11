# Step 189 — as built

**The 0.6 report:** the run `v0.6-scope.md` left for the end. It has three parts:

- ursus and Polars in one session, every query of all four suites;
- ursus against v0.5.0, built alike and alternated;
- h2o's `gb10` and `j5` under 3 GB.

It was run on `c6aa77d`, step 188, on 2026-10-11.

## 1. The machine

The laptop has 8 threads and 15.3 GB of memory. During the run:

- 5.6 GB of memory was available and 10.1 GB of swap was in use, by an IDE and the
  rest of a working session;
- the preflight passed each suite with that warning;
- nothing else of this project's ran alongside.

**No cgroup was set.** v0.5.0's report ran every query in an 8 GB cgroup with swap
off. This one ran without a limit, as `make run` does by default. The 3 GB runs in
§4 are where a limit was set.

## 2. ursus and Polars, in one session

`make run ENGINES=ursus,polars` for each suite, then `make validate`, then
`make report`. **All 148 answers match DuckDB's reference.** Each figure is a
geomean over 22 or 15 queries, of each query's median over three timed iterations.

| suite | ursus | Polars | ratio | at v0.5.0 |
| --- | --: | --: | --: | --: |
| PDS-H SF=1 | 180 ms | 77.6 ms | **2.32×** | 276 ms, 3.56× |
| PDS-H SF=0.1 | 23.6 ms | 9.2 ms | **2.56×** | 29.0 ms, 3.09× |
| h2o 10M, Parquet | 832 ms | 519 ms | **1.61×** | 947 ms, 1.73× |
| h2o 10M, CSV | 2,034 ms | 1,123 ms | **1.81×** | 2,195 ms, 1.97× |
| PDS-H SF=1, peak memory | 0.85 GB | 0.86 GB | **0.99×** | 1.10 GB, 1.31× |

- **Polars read 77.6 ms at SF=1,** within its earlier range of 70 to 97 ms. So this
  session was not a fast one for Polars.
- **ursus's peaks without a limit:** h2o `gb10` reached 6.87 GB over Parquet and
  7.77 GB over CSV, and `j5` reached 5.67 and 6.87 GB. Polars' were 4.56 to
  5.18 GB.
- **The other engines' columns** in `REPORT.md` are from step 127's session, as at
  v0.5.0. DataFusion and chDB answer q15 wrongly there, and are struck through.

## 3. ursus against v0.5.0, alternated

- **The builds:** v0.5.0's source, from `git archive v0.5.0`, and HEAD, both built
  with the bench's flags, `GOEXPERIMENT=simd` and `GOAMD64=v3`.
  - `runner-v050`, which the per-step comparisons used, had been built with neither.
    So had the per-step runners, which kept those comparisons consistent among
    themselves.
- **The method:** `abcpu.py` runs the two runners alternately, three rounds a query.
  It takes the median round's CPU time from rusage, divided by the iterations
  including the warm-up, and the median wall time.

| suite | CPU, v0.5.0 → now | wall, v0.5.0 → now |
| --- | --: | --: |
| PDS-H SF=1, 22 queries, 3 iterations | 1,326 → 898 ms, **−32.2%** | 274 → 199 ms, **−27.1%** |
| h2o 10M Parquet, 15 queries, 1 iteration | 5,295 → 4,865 ms, −8.1% | 928 → 846 ms, −8.9% |
| h2o 10M CSV, gb1, gb4, gb7, j1, 1 iteration | 11,637 → 10,720 ms, −7.9% | 1,672 → 1,509 ms, −9.8% |

**PDS-H, query by query:**

- q21 −65% CPU, from runtime filters;
- q11 −59% and q2 −55%;
- q17 −47%, q5 −42%, q16 −42%;
- the rest fell by 14 to 38%;
- the exceptions: q1 −4%, q22 ±0% CPU (wall −46%), and q4 +8% CPU (wall −44%).
  Semi and anti joins now build in parallel, trading CPU for wall time.

**h2o, query by query:**

- **the group-bys by one integer key moved:** gb4 −29% CPU, gb8 −16%, gb5 −12%, and
  the joins by 2 to 15%;
- gb1, by one string key, moved −7%;
- **the others sit within 2%:** gb2, gb3, gb6, gb7, gb9 and gb10. Their keys are
  strings, several columns, or both, and gb6's aggregate is a median;
- 0.6's speed work went to joins, single integer keys and Parquet, and most of h2o's
  group-bys are none of those.

## 4. gb10 and j5 under 3 GB

Each ran under `systemd-run --user --scope -p MemoryMax=3G -p MemorySwapMax=0`, as
the driver wraps a limited run. They ran outside the driver, so that `timings.csv`,
whose report keeps each query's latest run whatever its limit, keeps the unlimited
ones. One iteration each:

| query | time | peak | answer |
| --- | --: | --: | --- |
| gb10, Parquet | 3.30 s | 2.77 GB | matches |
| j5, Parquet | 4.00 s | 2.92 GB | matches |
| gb10, CSV | 5.12 s | 2.83 GB | matches |
| j5, CSV | 6.16 s | 2.91 GB | matches |

The reference for these is a checksum: rows and the sums of `cnt` and `v3`, or of
`v1` and `v2`. Each was computed from the runner's full answer and matches to 1e-9.
v0.5.0 peaked at 2.73 to 2.86 GB in the same container.

## 5. What 0.6 claims

The scope's claim: **within 3× of Polars on PDS-H at SF=1, in one session, and at
least 20% faster than v0.5.0 on it.**

- **Within 3× of Polars:** 2.32×. **Met.**
- **20% faster than v0.5.0,** alternated: −27.1% wall and −32.2% CPU. **Met.**
- **Beyond the claim:** every suite is now closer to Polars than at v0.5.0. h2o moved
  by about 9% against v0.5.0, short of what PDS-H moved, for the reason §3 gives.

## 6. What it touched

- **`bench/results/REPORT.md`,** regenerated.
- **v0.5.0's raw results are kept:** `bench/results/archive/`, ignored, holds its
  `raw/`, `timings.csv`, `summary.json`, `validation.csv` and `REPORT.md`, copied
  before the run.
- **The README's report section and its table,** with a column for v0.5.0.
- **The CHANGELOG,** with these figures in "Where it ends".
- **The CHANGELOG's Known limitations,** the release's last section, written
  afresh from v0.5.0's:
  - `MergeSorted` and backward `JoinAsOf` leave the operators that do not spill;
  - what 0.6 built leaves "Not built", and what its features leave out joins it;
  - the default budget off Linux, and the open audit rows, are gone;
  - the differences from Polars that steps 175, 177, 174 and 187 recorded are listed;
  - speed and memory are from §2.
- **The README's "Not done" list** matches the CHANGELOG's "Not built" again, as
  step 166 left them.
- **No code changed.**
