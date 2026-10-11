# Step 208 — as built

**The 0.6.2 report, and its documents.** No code changed.

- **PDS-H** ran on `3aaf333`, step 207's code, on 2026-10-11.
- **h2o** was stopped for low memory that evening, and re-run on `2cc9012`, step
  211's code, on 2026-10-12, when the maintainer had freed memory.
- Steps 209 to 211 touch windows and the CSV reader, which PDS-H does not use, so the
  PDS-H figures stand for `2cc9012` too. `REPORT.md` is stamped with `2cc9012`.

## 1. Against v0.6.1, alternated

`runner-s200` is v0.6.1's code, and `runner-s207` this report's; both are built with
the bench's flags. They ran alternately over PDS-H at SF=1, three rounds, three
iterations each.

| | v0.6.1 | 0.6.2 | |
| --- | --: | --: | --: |
| geomean CPU | 813 ms | 743 ms | **−8.6%** |
| geomean wall | 164 ms | 160 ms | −2.5% |
| q7 | 2,189 ms CPU, 371 ms wall | 1,685, 327 | **−23%**, −12% |
| q21 | 2,247 ms CPU, 513 ms wall | 1,741, 457 | **−23%**, −11% |

**The widest moves,** CPU then wall:

| query | CPU | wall | from |
| --- | --: | --: | --- |
| q4 | −23% | −12% | the dense build (204) |
| q7 | −23% | −12% | the bitmap filter (202) |
| q21 | −23% | −11% | the bitmap, the dense build |
| q17 | −19% | −10% | the probe's index (203) |
| q8 | −17% | +2% | the bitmap |
| q11 | −16% | −4% | |
| q5 | −15% | −10% | the bitmap |
| q2 | −12% | +3% | |
| q9 | −9% | −6% | |

The rest are within 6% either way.

**The scope's claims.** It asked for q7 and q21 at least 20% faster, and PDS-H at
SF=1 at least 5% faster:

- **by CPU, all three are met:** q7 and q21 −23%, the geomean −8.6%;
- **by wall time, none is:** q7 −12%, q21 −11%, the geomean −2.5%.

0.6.1's claims were judged by wall time, and by that measure this release falls
short. The likely reason, not measured: the work removed was the hash work of
joins, a lookup or an insert a row, which ran on every core at once. A query's time
is bounded by the longest path through its pipelines, where Parquet decoding and
ZSTD still dominate (step 201's profiles put Parquet at 31 to 67% of the queries the
scope targeted). Removing parallel work cuts CPU by more than it cuts that path.

## 2. Against Polars, in one session

`make run ENGINES=ursus,polars` over the four suites, after v0.6.1's raw results
were copied to `bench/results/archive-v0.6.1/`; then `make validate` and
`make report`. **Every answer of ursus and Polars matches DuckDB's reference.** The
one disagreement of 155 is DataFusion's q15, from its earlier session, struck
through as in 0.6.1's report.

| suite | ursus | Polars | ratio | at v0.6.1 |
| --- | --: | --: | --: | --: |
| PDS-H SF=1 | 158.6 ms | 76.5 ms | **2.07×** | 2.16× |
| PDS-H SF=0.1 | 24.1 ms | 8.7 ms | 2.76× | 2.43× |
| h2o 10M, Parquet | 822 ms | 541 ms | 1.52× | 1.51× |
| h2o 10M, CSV | 1,764 ms | 1,071 ms | **1.65×** | 1.70× |
| PDS-H SF=1, peak memory | 0.78 GB | 0.84 GB | 0.93× | 1.02× |

- **At SF=0.1** ursus moved from 24.6 to 24.1 ms, and Polars from 10.1 to 8.7 ms.
- **The widest PDS-H gaps at SF=1** are q15 3.6×, q19 and q16 3.5×, then q12, q17 and
  q2 at 2.9×. q7, 3.6× at v0.6.1, is no longer among them.
- **The machine:** PDS-H ran with about 3 GB of memory available and 12 GB of swap in
  use; h2o with about 6 to 7 GB available. No cgroup was set.

## 3. gb10's peak, and why it is not a regression

ursus's h2o peaks rose to 9.11 GB over CSV and 6.76 GB over Parquet, both from
`gb10`, where v0.6.1's report had 6.32 and 6.20 GB. Checked:

- **The engine holds the same.** The runner's accounted bytes are identical in both
  sessions, 3.70 GB over CSV and 3.24 GB over Parquet, and so is the total
  allocation, 38.8 and 35.8 GB. Only the Go heap's size when sampled differs.
- **No code on gb10's path changed** since v0.6.1: it groups by six keys, which keep
  the encoded path step 205 left, and it has no join.
- **Run once each in one session, over Parquet,** v0.6.1's runner and every step's
  since peaked anywhere from 5.30 to 6.60 GB, in no order of code: step 204's runner
  highest, step 207's lowest, HEAD's 5.43 GB against v0.6.1's 5.59 GB. The collector
  ran 34 to 39 times a run.

gb10's peak is noisy by about ±0.6 GB from the collector's timing, and the report's
9.11 GB over CSV is one high sample. It is reported as measured.

## 4. The documents

- **`CHANGELOG.md`:** 0.6.2's section is a candidate, not tagged, with "Where it
  ends" and its known limitations.
- **`README.md`:** the report's provenance and its table, with a column for v0.6.1.
- **`v0.6.2-scope.md`:** where it stands.

The tags for v0.6.1 and v0.6.2 wait to be asked for.
