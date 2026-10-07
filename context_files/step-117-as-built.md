# Step 117 — as built

**The 0.4 benchmark report.** Every engine, both suites, all four sizes, in one
session on the v0.4.0 candidate, `645efda`. No engine code changed.

## 1. How it was run

The same settings as the v0.3.0 report:

- `make run` and `make validate` for PDS-H at SF=0.1 and SF=1, and for h2o at 10
  million rows over Parquet and over CSV;
- `MEM_LIMIT=8G`, which runs each query in a systemd scope with swap off;
- 8 threads, three timed iterations after one untimed warm-up, and a 600 s timeout
  per query.

The machine was a 15 GiB laptop with an IDE still open and 4 GiB of swap already in
use. Preflight passed for every suite.

The run took 74 minutes. Most of it went to other engines: chDB's three timeouts, and
gota over CSV.

## 2. What it says

| suite | ursus | Polars | ratio | at v0.3.0 |
| --- | --: | --: | --- | --- |
| h2o 10M, Parquet | 1,468 ms | 531 ms | 2.77× | 3.32× |
| h2o 10M, CSV | 3,375 ms | 989 ms | 3.41× | 3.55× |
| PDS-H SF=1 | 528 ms | 81 ms | 6.51× | 10.8× |
| PDS-H SF=0.1 | 53 ms | 9 ms | 6.13× | 7.5× |
| PDS-H SF=1, peak memory | 0.97 GB | 0.92 GB | 1.05× | 2.0× |

Geomeans are over each engine's passed queries.

- **Correctness:** ursus passed every query of all four suites, and all 74 of its
  answers validate against duckdb's.
- **gb10 over CSV now passes,** at 8.4 s and 6.9 GB; v0.3.0 was killed on it.
- **The target was not met.** `v0.4-scope.md` §5 set PDS-H SF=1 within 4× of Polars.
  It is 6.5×.
- **Where the gap is:** the queries that scan and filter `lineitem`, which is nearly
  all of them. q6 does little else and is 8.2×; q15 17×, q19 16×, q12 11×.
- **h2o:** the 100-group aggregations are the worst. Polars finishes gb1 in 92 ms and
  gb4 in 105 ms, ursus in 494 and 585. Both are mostly a scan.
- **Memory:** h2o `j5` over CSV peaked at 8.19 GB under the 8 GB cap. The budget was
  4 GB; the rest is the Go heap's slack, which `SetProcessMemoryLimit` bounds and the
  runner does not call.

**Other engines' failures,** each in the report:

- chDB timed out on gb5, gb7 and gb9 over Parquet, and failed gb6 in both h2o suites,
  on a function it does not have.
- gota ran out of memory on gb2 and gb3.
- chDB, and DataFusion at SF=1, answered q15 with no rows.

## 3. What changed in the tree

- **`bench/results/REPORT.md`,** regenerated.
- **`README.md`:** the benchmark section's commit, date and caveats, the table of where
  ursus stands with v0.3.0's ratios beside it, and the speed claims at the top: TPC-H
  "about 10x" is now 6.5x, and `duckdb-go` "around 10x faster" is now around 6x.
- **`CHANGELOG.md` and `v0.4-scope.md` §7:** the measured numbers replace "the full
  report has not been run", and §7 says the target was not met.

## 4. What it ranks next

The scan and the filter. That is the same finding as step 101's profile, now measured
across the suite: the queries that do little but scan `lineitem`, or h2o's ten
million rows, are the ones furthest behind.
