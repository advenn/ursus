# Step 247 — as built

**The 0.6.4 report, and its documents.** No code changed. Everything ran on step
246's code (`659ec39`; `028f0c6` adds documents only).

## 1. Against v0.6.3, alternated

`runner-v063` is v0.6.3's code, built from `git archive v0.6.3`, and `runner-s246`
this report's; both are built with the bench's flags (`GOEXPERIMENT=simd
GOAMD64=v3`). They ran alternately, three rounds of three iterations each.

**PDS-H at SF=1,** on 2026-10-13:

| | v0.6.3 | 0.6.4 | |
| --- | --: | --: | --: |
| geomean wall | 185 ms | 183 ms | **−1.4%** |
| geomean CPU | 928 ms | 932 ms | +0.4% |

0.6.4 is about memory, not speed, and the suite fits in memory: flat is the claim.

| query | wall | | CPU | |
| --- | --: | --: | --: | --: |
| q21 | 522 → 568 ms | +8.8% | 2,324 → 2,490 ms | +7.1% |
| q7 | 357 → 374 ms | +4.8% | 2,024 → 2,019 ms | −0.2% |
| q22 | 67 → 69 ms | +3.0% | 284 → 295 ms | +3.9% |
| q12 | 169 → 174 ms | +3.0% | 1,013 → 1,036 ms | +2.3% |
| q15 | 127 → 130 ms | +2.4% | 740 → 749 ms | +1.2% |
| q4 | 129 → 132 ms | +2.3% | 748 → 743 ms | −0.7% |
| q14 | 153 → 155 ms | +1.3% | 755 → 766 ms | +1.5% |
| q13 | 409 → 414 ms | +1.2% | 1,344 → 1,362 ms | +1.3% |
| q20 | 287 → 286 ms | −0.3% | 1,618 → 1,577 ms | −2.5% |
| q19 | 238 → 236 ms | −0.8% | 1,316 → 1,306 ms | −0.8% |
| q1 | 380 → 376 ms | −1.1% | 2,456 → 2,448 ms | −0.3% |
| q16 | 84 → 83 ms | −1.2% | 246 → 256 ms | +4.1% |
| q10 | 284 → 277 ms | −2.5% | 1,429 → 1,459 ms | +2.1% |
| q2 | 40 → 39 ms | −2.5% | 148 → 145 ms | −2.0% |
| q3 | 198 → 193 ms | −2.5% | 1,178 → 1,163 ms | −1.3% |
| q6 | 109 → 106 ms | −2.8% | 627 → 628 ms | +0.2% |
| q17 | 247 → 239 ms | −3.2% | 1,272 → 1,260 ms | −0.9% |
| q18 | 348 → 332 ms | −4.6% | 1,939 → 1,921 ms | −0.9% |
| q11 | 30 → 28 ms | −6.7% | 139 → 138 ms | −0.7% |
| q9 | 473 → 438 ms | −7.4% | 2,418 → 2,410 ms | −0.3% |
| q5 | 214 → 196 ms | −8.4% | 1,228 → 1,211 ms | −1.4% |
| q8 | 259 → 232 ms | −10.4% | 1,231 → 1,217 ms | −1.1% |

q21 and q7 came out slower and were run again: §1.1.

The session that took these numbers ended before the rest; the scratchpad, where
the runners and logs live, is a RAM disk and did not survive it. The rest was run on
2026-10-14 with the runners built again, alike.

### 1.1 q21 and q7, run again

Two more alternated runs each, on 2026-10-14:

| query | run | v0.6.3 | 0.6.4 |
| --- | --- | --: | --: |
| q21 | 1 | 399 ms | 384 ms |
| q21 | 2 | 447 ms | 448 ms |
| q7 | 1 | 263 ms | 283 ms |
| q7 | 2 | 353 ms | 338 ms |

Each turns over between runs, and the machine's own speed moves more between runs
(q21 at 400 and at 450 ms in both builds) than the builds differ: both are within
noise. No 0.6.4 step touches their plans' in-memory path.

**h2o at ten million rows,** on 2026-10-14, the same way:

| query | v0.6.3 | 0.6.4 | |
| --- | --: | --: | --: |
| gb1, Parquet | 98 ms | 95 ms | −3% |
| gb4, Parquet | 193 ms | 177 ms | −8% |
| gb5, Parquet | 580 ms | 589 ms | +1.6% |
| gb1, CSV | 800 ms | 804 ms | +0.5% |
| gb4, CSV | 1,283 ms | 1,306 ms | +1.8% |
| j1, CSV | 1,997 ms | 1,969 ms | −1.4% |

All within noise, as PDS-H: 0.6.4 changed what happens past a memory limit, and
these ran without one. The machine had about 4 GB available and 4 GB of swap in use,
against 3 GB and 11 GB in 0.6.3's session, which is why gb1 over Parquet takes 98 ms
here and took 335 ms there.

## 2. Against Polars, in one session

**Setup:**

- 0.6.3's raw results were copied to `bench/results/archive-v0.6.3/`;
- `make run ENGINES=ursus,polars` ran over the four suites, each followed by
  `make validate`, then `make report`;
- the laptop had 4 to 7 GB of memory available and 4 GB of swap in use, where 0.6.3's
  session had 3 GB and 11 GB.

**Every answer of ursus and Polars matches DuckDB's reference:** 44, 44, 30 and 30.
The three struck through are other engines' q15, from their earlier session, as in
0.6.3.

| suite | ursus | Polars | ratio | at v0.6.3 |
| --- | --: | --: | --: | --: |
| PDS-H SF=1 | 158 ms | 79 ms | **2.01×** | 1.94× |
| PDS-H SF=0.1 | 20 ms | 9 ms | 2.29× | 1.82× |
| h2o 10M, Parquet | 697 ms | 531 ms | **1.31×** | 1.20× |
| h2o 10M, CSV | 1,784 ms | 1,088 ms | 1.64× | 1.77× |
| PDS-H SF=1, peak memory | 0.83 GB | 0.92 GB | 0.90× | 1.01× |

**What moved, and why it is the session:**

- **The builds did not move:** alternated, 0.6.4 is level with 0.6.3 over PDS-H at both
  scales and over h2o (§1). At SF=0.1 the two took 20.8 and 20.4 ms.
- **Both engines ran h2o faster than in 0.6.3's session,** which was short of memory:
  over Parquet ursus 928 → 697 ms and Polars 774 → 531 ms; over CSV ursus 2,535 →
  1,784 and Polars 1,434 → 1,088. Polars gained more over Parquet, ursus more over
  CSV, so the two ratios moved apart.
- **PDS-H at SF=0.1 ran twice.** The first run, straight after SF=1, gave ursus 28 ms
  and every query slower than the alternated runs a minute later gave the same code:
  q7 122 ms against 46, q8 98 against 29. It was run again alone: 20 ms, against
  Polars' 9.

**The widest gaps:**

- PDS-H SF=1: q12 4.3×, q19 3.5×, q16 2.9×, q10 2.8×, q15 2.8×; ursus is ahead on q21;
- h2o over Parquet: gb4 2.3×, the joins at 1.7 to 1.9×; ursus is ahead on gb2, gb7,
  gb8, gb9 and gb10;
- h2o over CSV: gb4 3.1×, j1 2.1×.

**Memory:**

- ursus's highest peak is gb10 over Parquet, 7.72 GB, where 0.6.3's report had 6.76;
  over CSV 6.44 GB, where it had 8.17. Polars' highest is 5.03 GB;
- with no limit set, a peak is where the garbage collector lets the heap grow, and it
  grew further with more memory free; no 0.6.4 step touches gb10's in-memory path.

## 3. Out of core

Each path 0.6.4 made spill or stream, once without a limit and once under a tenth of
its input. Two million rows of five 8-byte columns, 80 MB; the rolling median's are
5,000 minutes under 16 MB, its twelve million values being what is large. The two
answers are compared by a SHA-256 of their CSV.

| path | step | rows out | no limit: wall, peak | under the limit: wall, peak | spills | same |
| --- | --- | --: | --- | --- | --: | --- |
| a window with an order_by, no partition_by: cum_sum, rank, interpolate | 235–238 | 2,000,000 | 450 ms, 170 MB | 1,669 ms, 12.3 MB (1.54×) | 373 | yes |
| n_unique over one hot key | 241 | 100 | 302 ms, 113 MB | 1,173 ms, 16.8 MB (2.11×) | 8 | yes |
| a median in `.Over()`, no partition_by | 242 | 2,000,000 | 65 ms, 186 MB | 375 ms, 8.5 MB (1.06×) | 4 | yes |
| a keyed rolling sum, an hour by 1,000 keys | 243 | 2,000,000 | 593 ms, 65 MB | 1,144 ms, 8.4 MB (1.05×) | 9 | yes |
| a rolling median, a week over 5,000 minutes | 244 | 5,000 | 340 ms, 120 MB | 1,713 ms, 16.9 MB (1.01×) | 8 | yes |
| top-k, the best half | 245 | 1,000,000 | 1,353 ms, 80 MB | 422 ms, 10.0 MB (1.25×) | 9 | yes |
| a rolling mean by time, no partition_by | 246 | 2,000,000 | 189 ms, 170 MB | 1,025 ms, 10.7 MB (1.33×) | 67 | yes |

The limit is the claim, and it holds: each peaks between 1.01 and 2.11 times it.

**The top-k is faster under the limit than without it,** 422 ms against 1,353. Without
one, the best half of two million rows is a bounded heap of a million; under it, sorted
runs of the best k merge. A heap that holds half its input loses to sorting: for 0.7,
whose theme is speed.

## 4. The documents

- **`bench/results/REPORT.md`,** regenerated from this session.
- **`CHANGELOG.md`:** the 0.6.4 section, where it ends and its known limitations,
  dated for the tag.
- **`README.md`:** the figures, the paragraph on how current they are, the caveats and
  the table.
- **`v0.6.4-scope.md`:** where it stands.

**Gate:** no code changed since step 246's gate.
