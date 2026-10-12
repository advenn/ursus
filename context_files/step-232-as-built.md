# Step 232 — as built

**The 0.6.3 report, and its documents.** No code changed. Everything ran on
`d661e79`, step 231's code, on 2026-10-12.

## 1. Against v0.6.2, alternated

`runner-v062` is v0.6.2's code, built from `git archive v0.6.2`, and `runner-s231`
this report's; both are built with the bench's flags (`GOEXPERIMENT=simd
GOAMD64=v3`). They ran alternately, three rounds of three iterations each.

**PDS-H at SF=1:**

| | v0.6.2 | 0.6.3 | |
| --- | --: | --: | --: |
| geomean wall | 209 ms | 193 ms | **−7.6%** |
| geomean CPU | 894 ms | 914 ms | +2.2% |

The widest moves by wall time: q11 −30%, q4 −22%, q15 −17%, q21 −16%, q12 −15%,
q6 −14%, q17 −14%, q20 −13%. CPU rose by 2%: the Parquet reader decodes ahead of
the head (step 214), where it waited.

**Four queries came out slower in that run:** q3 +10.5%, q5 +7.1%, q18 +5.0% and
q10 +4.6%. Run again:

- q5, q10 and q18 were within noise, −4%, −2% and −2%;
- q3 swings between two speeds, about 830 and 1,100 ms of CPU, in both builds.
  Over two more alternated runs it was 190 against 187 ms, then 216 against 186.

**h2o over Parquet:** gb1 −31% wall, gb4 −24%, gb5 −8%, from 335, 389 and 900 ms.
**h2o over CSV,** whose reader 0.6.3 did not touch: gb1 −2%, gb4 −9%, j1 −8%.

**Peak memory,** from the runs below against 0.6.2's raw results: q1 at SF=1
188 → 243 MB, gb4 over Parquet 167 → 185 MB. The read-ahead holds decoded batches
the head has not reached.

**The scope's claims:**

| claim | measured | |
| --- | --- | --- |
| PDS-H SF=1 geomean wall at least 7% less | −7.6% | **met** |
| no query slower beyond about 5% | the four slower run again within noise | **met** |
| gb4 and q1 peak memory within +100 MB | +18 and +54 MB | **met** |
| h2o gb4 at least 35% less wall time | −24% | **not met** |

gb4's claim was set before step 213's traces. Step 214 found gb4 bound by this
laptop's four physical cores once the reader ran ahead, so its time comes only from
removing work. Step 215 removed some, the 128-bit additions of a narrow integer's
mean.

## 2. Against Polars, in one session

**Setup:**

- v0.6.2's raw results were copied to `bench/results/archive-v0.6.2/`;
- `make run ENGINES=ursus,polars` ran over the four suites;
- then `make validate` over each, and `make report`.

**Every answer of ursus and Polars matches DuckDB's reference:** 44, 44, 30 and 30.
The three struck through are other engines' q15, from their earlier session.

| suite | ursus | Polars | ratio | at v0.6.2 |
| --- | --: | --: | --: | --: |
| PDS-H SF=1 | 148 ms | 76 ms | **1.94×** | 2.07× |
| PDS-H SF=0.1 | 18 ms | 10 ms | 1.82× | 2.76× |
| h2o 10M, Parquet | 928 ms | 774 ms | **1.20×** | 1.52× |
| h2o 10M, CSV | 2,535 ms | 1,434 ms | 1.77× | 1.65× |
| PDS-H SF=1, peak memory | 0.82 GB | 0.81 GB | 1.01× | 0.93× |

**The machine was short of memory, and h2o shows it.**

- **Available memory:**
  - PDS-H ran with about 3 GB available and 11 GB of swap in use;
  - h2o over Parquet with about 3 GB, after the session's old runner binaries were
    deleted from the RAM-backed scratchpad (the maintainer's call);
  - over CSV with about 6.5 GB.
- **Both engines ran h2o slower than in 0.6.2's session,** and by different
  amounts:
  - over Parquet, ursus 822 → 928 ms, Polars 541 → 774;
  - over CSV, ursus 1,764 → 2,535 ms, Polars 1,071 → 1,434.
- **So the h2o ratios measure this session.** Parquet's 1.20× flatters ursus, and
  CSV's 1.77× does not. The A/B above, alternated, is the measure of what 0.6.3
  changed: it is faster than 0.6.2 over both.
- **At SF=0.1,** ursus moved from 24.1 to 18 ms, and Polars from 8.7 to 10 ms.

**The widest PDS-H gaps at SF=1:** q7 3.9×, q19 3.3×, q16 3.2×, q15 3.1×, q17 2.9×.
**Over h2o Parquet:** the joins at 1.6 to 2.0×, gb1 and gb4 at 1.7×. ursus is ahead
on gb8, gb9 and gb10, and level on gb2, gb3, gb5 and gb7.

**Memory:**

- ursus's highest peak is gb10 over CSV, 8.17 GB, where 0.6.2's report had 9.11 GB;
  Polars' highest is 5.13 GB;
- over Parquet, gb10 peaked at 6.76 GB, as in 0.6.2.

## 3. The documents

- **`bench/results/REPORT.md`,** regenerated from `d661e79`.
- **`CHANGELOG.md`:**
  - the 0.6.3 section, its "where it ends" and its known limitations;
  - it is headed "untagged" until a tag is asked for.
- **`README.md`:** the figures, the paragraph on how current they are, and the
  table.
- **`v0.6.3-scope.md`:** "where it stands".
