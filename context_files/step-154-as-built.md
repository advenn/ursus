# Step 154 — as built

**Item 9 of `v0.5-scope.md`: the report, at the end of 0.5.** No engine code changed.

## 1. How it was run

**ursus and Polars, every query of all four suites,** as steps 117 and 127 ran
them:

- PDS-H at SF=0.1 and SF=1;
- h2o at ten million rows over Parquet and over CSV;
- `MEM_LIMIT=8G`, a systemd scope with swap off;
- 8 threads, three timed iterations after one untimed warm-up;
- `make run validate`, then `make report`.

**The commit:** `fb7788b`, on 2026-10-09, 17:13 to 17:21, with nothing else of this
project's running.

**Why only the two.** The other engines' code has not changed, and they were most of
step 117's 74 minutes and step 127's 81: chDB's timeouts, and gota over CSV. The
report keeps the latest run of each engine and query, so theirs are step 127's
session, on `5a721e0` the day before. ursus is compared with Polars within one
session, and with the rest across two. The README says so.

**Then `gb10` and `j5` under a 3 GB scope,** ursus alone, one run each, with results
kept apart from the report's (`PATH_RESULTS`): step 150's fold holds more at once,
and this is the case its budget check exists for.

## 2. What it says

**All 74 queries pass, and every answer validates** against DuckDB's: 44 at each PDS-H
size and 30 at each h2o one, counting both engines.

| suite | ursus | Polars | ratio | at step 127 | at v0.3.0 |
| --- | --: | --: | --- | --- | --- |
| h2o 10M, Parquet | 947 ms | 547 ms | 1.73× | 1.93× | 3.32× |
| h2o 10M, CSV | 2,195 ms | 1,116 ms | 1.97× | 3.28× | 3.55× |
| PDS-H SF=1 | 276 ms | 78 ms | 3.56× | 4.43× | 10.8× |
| PDS-H SF=0.1 | 24 ms | 9 ms | 2.55× | 4.01× | 7.5× |
| PDS-H SF=1, peak memory | 1.10 GB | 0.84 GB | 1.31× | 1.12× | 2.0× |

**The target is not met.** §5 set PDS-H SF=1 within 3× of Polars, with q15 and q2
under 4×:

| | ursus | Polars | ratio | at step 127 |
| --- | --: | --: | --- | --- |
| geomean | 276 ms | 78 ms | 3.56× | 4.43× |
| q15 | 163 ms | 38 ms | 4.3× | 10.5× |
| q2 | 69 ms | 14 ms | 4.9× | 8.4× |

Polars' own timings moved between the two sessions: its SF=1 geomean was 70 ms at
step 127.

**The widest gaps left at SF=1:**

| query | ursus | Polars | ratio | at step 127 |
| --- | --: | --: | --- | --- |
| q7 | 581 ms | 81 ms | 7.2× | 7.0× |
| q19 | 404 ms | 68 ms | 5.9× | 6.9× |
| q17 | 446 ms | 86 ms | 5.2× | 5.5× |
| q12 | 332 ms | 66 ms | 5.0× | 6.3× |
| q4 | 321 ms | 64 ms | 5.0× | |
| q2 | 69 ms | 14 ms | 4.9× | 8.4× |
| q16 | 112 ms | 23 ms | 4.9× | |

**The narrowest:** q1 1.4×, q18 1.9×, q20 2.1×, q11 2.2×.

**What step 141's profile said these are made of:**

- **Reading Parquet,** 29 to 57% of every query profiled, of it ZSTD 13 to 29%,
  which has no local fix.
- **The join probe:** its gather and its key lookups that miss the cache, 26% of q17
  and 61% of q2.
- **q7's filters and probe.**

No 0.5 item addressed the reader or the probe directly. They are where 0.6 starts.

**Peak memory at SF=1 rose,** 1.02 to 1.10 GB, against Polars' 0.84:

- step 143 holds a shared subtree's result;
- steps 150 and 151 hold every worker's state, or the build's key tables, at once.

**h2o over CSV moved most:** 3.28× to 1.97×, from step 144's parallel parser and step
150's fold. `gb10` over CSV peaks at 7.28 GB under the 8 GB cap, from 7.60.

**Under a 3 GB container,** at ten million rows, all four pass:

| query | Parquet | CSV |
| --- | --- | --- |
| `gb10` | 3,835 ms, 2.80 GB | 5,678 ms, 2.73 GB |
| `j5` | 4,386 ms, 2.86 GB | 6,887 ms, 2.80 GB |

At v0.4.0 `gb10` peaked at 2.72 GB over Parquet and 2.75 GB over CSV.

## 3. Other engines

Their numbers and failures are step 127's: chDB's timeouts and `gb6`, gota's
out-of-memory group-bys, and DataFusion's and chDB's wrong `q15`, struck through.

**Gate:** none needed; no code changed. The report validates every answer it times.
