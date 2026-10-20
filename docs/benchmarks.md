---
description: >-
  Benchmarks of ursus, the Go dataframe library, against Polars, DuckDB, pandas,
  DataFusion and the Go dataframe libraries Gota and QFrame, on TPC-H (PDS-H) and the
  h2o.ai suite, every answer validated against DuckDB's.
---

# Benchmarks

ursus is slower than Polars and DuckDB, and says by how much. Everything that measures
it lives in [`bench/`](https://github.com/advenn/ursus/tree/master/bench): TPC-H (as
Polars' PDS-H) and the h2o.ai database-like suite, against DuckDB, Polars, pandas,
DataFusion, chDB, Arrow-Go, Gota and QFrame.

**Every answer is validated** against DuckDB's, and an engine that answers wrongly is
struck through in the report rather than reported as a fast number. ursus passes all
22 PDS-H queries and all 15 h2o.ai queries.

## Against Polars

Geometric means over each suite's queries, ursus and Polars run together in one session
on 8 threads:

| Suite | ursus | Polars | ursus / Polars |
|---|---:|---:|---:|
| h2o.ai, 10M rows, Parquet | 697 ms | 531 ms | 1.3× |
| h2o.ai, 10M rows, CSV | 1,784 ms | 1,088 ms | 1.6× |
| TPC-H, scale factor 1 | 158 ms | 79 ms | 2.0× |
| TPC-H, scale factor 0.1 | 20 ms | 9 ms | 2.3× |
| TPC-H SF=1, peak memory | 0.83 GB | 0.92 GB | 0.9× |

Over the releases, ursus's time against Polars' has come down from 3.3× to 1.3× on
h2o.ai over Parquet, and from 10.8× to 2.0× on TPC-H at scale factor 1.

## How they were taken

The figures are v0.6.4's, from a laptop under real conditions, so a small difference is
noise and the ordering is the signal. Later 0.6 releases changed no plan the suites run,
which a byte-for-byte diff of all 37 queries' optimized plans checks at each release,
so the figures stand. The full report, with every engine, every query, the caveats and
peak memory, is
[`bench/results/REPORT.md`](https://github.com/advenn/ursus/blob/master/bench/results/REPORT.md).

To run them yourself:

```sh
cd bench
make preflight   # refuses to run without the free memory it needs
make setup
make bench       # generate, compute DuckDB's answers, run, validate, report
```
