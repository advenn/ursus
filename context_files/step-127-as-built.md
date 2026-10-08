# Step 127 — as built

**The report, re-run after the performance round, on an otherwise idle machine.**
No engine code changed.

## 1. Why it was re-run

Step 117's report had two faults:

- **It understated ursus.** This session's reviewers were grepping the repository
  and arrow-go's sources while ursus's PDS-H and h2o-Parquet runs went (step 123 §4).
- **It predated steps 123–126,** which changed ursus's numbers more than any step
  since step 102.

## 2. How

- **The same command and settings as step 117:** every engine, PDS-H at SF=0.1 and
  SF=1, h2o at ten million rows over Parquet and over CSV, under an 8 GB scope with
  swap off, on 8 threads, with three timed iterations after a warm-up.
- **Commit:** `5a721e0`.
- **The machine:** nothing else of this project's ran alongside it, no build, no test,
  no reviewer. The IDE stayed open.
- **The run:** 03:20 to 04:41.

## 3. What it says

| suite | ursus | Polars | ratio | step 117 | v0.3.0 |
| --- | --: | --: | --- | --- | --- |
| h2o 10M, Parquet | 914 ms | 475 ms | 1.93× | 2.77× | 3.32× |
| h2o 10M, CSV | 3,244 ms | 989 ms | 3.28× | 3.41× | 3.55× |
| PDS-H SF=1 | 310 ms | 70 ms | 4.43× | 6.51× | 10.8× |
| PDS-H SF=0.1 | 35 ms | 9 ms | 4.01× | 6.13× | 7.5× |
| PDS-H SF=1, peak memory | 1.02 GB | 0.91 GB | 1.12× | 1.05× | 2.0× |

**ursus passed all 74 queries,** and every answer validates.

**The target is nearly met:** §5's was PDS-H SF=1 within 4× of Polars, and it is
4.43×.

**The widest gaps left at SF=1:**

| query | ursus | Polars | ratio | why |
| --- | --: | --: | --- | --- |
| q15 | 324 ms | 31 ms | 10.5× | it computes one `lineitem` group-by twice; subplan reuse is the CSE item, moved to 0.5 |
| q2 | 117 ms | 14 ms | 8.4× | |
| q7 | 564 ms | 80 ms | 7.0× | |
| q19 | 418 ms | 61 ms | 6.9× | |
| q12 | 374 ms | 59 ms | 6.3× | |

**The narrowest:** q1 at 2.1×; q18, q20 and q21 at about 3×.

**h2o over Parquet:**

- gb2 and gb8 now run at Polars' speed;
- gb7 and gb9 are within 1.2×;
- the widest are gb4 and j3 at 3.7×, and gb1 at 3.4×.

**Memory:** ursus's highest peak is h2o `gb10` over CSV, 7.60 GB under the 8 GB cap,
against a 4 GB query budget; the rest is the Go heap's slack.

**Other engines:**

- DataFusion answered q15 with no rows at both scales, and chDB at SF=0.1;
- chDB timed out on three h2o group-bys, and failed `gb6`;
- gota ran out of memory on two group-bys.

## 4. What changed in the tree

- **`bench/results/REPORT.md`,** regenerated.
- **`README.md`:**
  - its benchmark section: the commit, the caveats, and the table;
  - the speed claims at the top: h2o "about 2x Polars over Parquet and 3x over CSV",
    TPC-H "about 4.5x";
  - duckdb-go "around 3.5x faster".
- **`CHANGELOG.md`'s highlights and limitations,** and `v0.4-scope.md` §7.
