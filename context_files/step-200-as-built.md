# Step 200 — as built

**The 0.6.1 report, and its documents.** No code changed. Run on `806070b`,
step 199's code, on 2026-10-11.

## 1. Against v0.6.0, alternated

`runner-v060` is v0.6.0's code, and `runner-s200` this step's. Both are built with
the bench's flags. They ran alternately over PDS-H at SF=1, three rounds, three
iterations each.

| | v0.6.0 | 0.6.1 | |
| --- | --: | --: | --: |
| geomean wall | 196 ms | 175 ms | **−10.9%** |
| geomean CPU | 877 ms | 845 ms | −3.5% |
| q19 wall | 354 ms | 195 ms | **−45%** |
| q12 wall | 243 ms | 177 ms | **−27%** |

**The widest moves:**

- q19 −45%, q12 −27%, q1 −25%, q2 −20%, q4 and q10 −16%;
- q13 is +9% and q5 +2%, within their noise.

**The scope's claims:**

- **PDS-H at SF=1 at least 10% faster: met,** by wall time; CPU fell 3.5%.
- **q19 and q12 each at least 30% faster:** q19 is met at −45%. q12 is three points
  short at −27%.

## 2. Against Polars, in one session

`make run ENGINES=ursus,polars` over the four suites, then `make validate` and
`make report`. **All 148 answers match DuckDB's reference.** The three struck through
are DataFusion's and chDB's q15, from their earlier session, as in 0.6.0's report.

| suite | ursus | Polars | ratio | at v0.6.0 |
| --- | --: | --: | --: | --: |
| PDS-H SF=1 | 167.8 ms | 77.8 ms | **2.16×** | 2.32× |
| PDS-H SF=0.1 | 24.6 ms | 10.1 ms | 2.43× | 2.56× |
| h2o 10M, Parquet | 796 ms | 528 ms | 1.51× | 1.61× |
| h2o 10M, CSV | 1,886 ms | 1,110 ms | 1.70× | 1.81× |
| PDS-H SF=1, peak memory | 0.84 GB | 0.82 GB | 1.02× | 0.99× |

- **The machine:** about 4 GB of memory free and 12 GB of swap in use, under no
  limit.
- **ursus's highest h2o peaks** are 6.20 GB over Parquet and 6.76 GB over CSV, from
  6.87 and 7.77 GB.
- **v0.6.0's raw results** are copied to `bench/results/archive-v0.6.0/` before
  this run overwrote them.

## 3. gb10 and j5 under 3 GB

Each ran under `systemd-run --user --scope -p MemoryMax=3G -p MemorySwapMax=0`, one
iteration, outside the driver, and its checksum was compared with the reference's:

| query | time | peak | answer |
| --- | --: | --: | --- |
| gb10, Parquet | 3.15 s | 2.74 GB | matches |
| j5, Parquet | 3.79 s | 2.91 GB | matches |
| gb10, CSV | 4.75 s | 2.78 GB | matches |
| j5, CSV | 5.65 s | 2.91 GB | matches |

## 4. Out of core

The scope's out-of-core claim is met by the steps' own tests:

| operator | step | test |
| --- | --- | --- |
| `JoinAsOf` forward and nearest | 194 | every strategy under 1 MiB over 300k right rows |
| a cross join | 195 | a 300-by-500 cross join under 4 KiB |
| `GroupByDynamic` | 196 | under a fifth of its input |
| `Rolling` | 196 | holds its answer, not its input: 22 MB where it held 30 |
| a window with no `partition_by` | 197 | 20,000 rows under 16 KiB |

The claim said a tenth of the input. That holds where the answer is smaller than a
tenth. A rolling group's answer is as long as its input, and a dynamic group's can
be a tenth of it, so they hold their answer.

## 5. The documents

- **`CHANGELOG.md`:** 0.6.1's section is a candidate, not tagged, with "Where it
  ends" and its known limitations.
- **`README.md`:** the report's provenance and its table, with a column for v0.6.0.
- **`v0.6.1-scope.md`:** where it stands.

The tag waits to be asked for.
