# ursus benchmark results

Produced from `ea43952` — docs: say what was re-validated for 0.3, and that the timings were not — on 2026-10-06.

Median wall-clock over the timed iterations, in milliseconds; lower is better.
IO is included in the measurement.

| cell | meaning |
|---|---|
| `n/a` | the engine cannot express this query — see its notes in `config/engines.toml` |
| `·` | not run |
| `OOM` / `TIMEOUT` | stopped by the resource budget |
| `ERR` | the engine failed; hover or see the failures list under each table |
| ~~struck through~~ | disagreed with the duckdb reference. A wrong answer, not a fast one |

**geomean is over the queries that engine passed**, so it is only comparable
between engines with the same coverage — check the `queries passed` row before
reading a speedup.

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=csv

| query | gota | qframe | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| gb1 | 26,501 | 4,810 | 2,349 | 409 | 2,879 | 827 | 542 | 1,194 | 902 | sum v1 by id1 (100 string groups) |
| gb2 | OOM | 5,684 | 2,216 | 828 | 3,863 | 977 | 748 | 1,398 | 1,044 | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | OOM | 6,252 | 2,912 | 952 | 5,280 | 1,277 | 1,423 | 1,435 | 1,402 | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 29,586 | 5,165 | 3,315 | 478 | 2,834 | 998 | 723 | 1,327 | 951 | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 31,792 | 5,937 | 3,334 | 752 | 2,891 | 1,471 | 1,366 | TIMEOUT | 1,337 | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 31,836 | n/a | 3,583 | 794 | 3,181 | 1,207 | 1,047 | ERR | 1,252 | median v3, sd v3 by id4, id5 |
| gb7 | n/a | n/a | 2,584 | 990 | 5,168 | 1,237 | 932 | 864 | 1,226 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | n/a | n/a | 6,102 | 1,095 | 3,996 | 1,466 | 1,417 | 1,580 | 1,335 | largest two v3 by id6 (top-n within group) |
| gb9 | n/a | n/a | 2,978 | 977 | 3,700 | 1,178 | 839 | 1,234 | 1,098 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | n/a | n/a | OOM | 3,625 | 16,683 | 3,342 | 2,508 | 5,573 | 2,430 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | n/a | n/a | 4,121 | 1,106 | 8,324 | 2,134 | 1,598 | 2,714 | 1,677 | inner join large to small on integer |
| j2 | n/a | n/a | 4,396 | 1,397 | 8,818 | 2,417 | 2,493 | 3,435 | 1,893 | inner join large to medium on integer |
| j3 | n/a | n/a | 4,663 | 1,031 | 8,377 | 2,823 | 2,687 | 3,521 | 2,058 | left join large to medium on integer |
| j4 | n/a | n/a | 4,328 | 1,421 | 9,684 | 2,589 | 2,241 | 3,420 | 1,871 | join large to medium on varchar |
| j5 | n/a | n/a | 11,729 | 3,209 | 18,910 | 4,987 | 3,195 | 10,781 | 3,687 | join large to large on integer |
| **geomean** | **29,847** | **5,545** | **3,778** | **1,065** | **5,703** | **1,683** | **1,381** | **2,242** | **1,494** | |
| **vs polars** | 28.03x | 5.21x | 3.55x | 1.00x | 5.36x | 1.58x | 1.30x | 2.11x | 1.40x | |
| **queries passed** | 4/15 | 5/15 | 14/15 | 15/15 | 15/15 | 15/15 | 15/15 | 13/15 | 15/15 | |

Peak resident memory across the suite (GB):

| gota | qframe | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 8.57 | 3.88 | 7.83 | 5.18 | 5.03 | 4.07 | 3.65 | 5.54 | 4.41 |

<details><summary>failures</summary>

- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `gota` **gb2** — oom: no output, exit -9
- `gota` **gb3** — oom: no output, exit -9
- `ursus` **gb10** — oom: no output, exit -9

</details>

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=parquet

| query | polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| gb1 | 98 | 716 | 58 | 33 | 35 | 641 | 271 | n/a | n/a | sum v1 by id1 (100 string groups) |
| gb2 | 561 | 885 | 192 | 105 | 137 | 1,009 | 407 | n/a | n/a | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | 789 | 1,895 | 719 | 635 | 524 | 1,875 | TIMEOUT | n/a | n/a | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 261 | 1,017 | 155 | 156 | 94 | 523 | 125 | n/a | n/a | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 713 | 1,243 | 692 | 512 | 496 | 860 | TIMEOUT | n/a | n/a | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 478 | 1,702 | 590 | 589 | 436 | 1,349 | ERR | n/a | n/a | median v3, sd v3 by id4, id5 |
| gb7 | 743 | 1,320 | 653 | 297 | 409 | 1,365 | TIMEOUT | n/a | n/a | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 901 | 4,805 | 992 | 1,005 | 685 | 4,101 | 1,030 | n/a | n/a | largest two v3 by id6 (top-n within group) |
| gb9 | 678 | 1,131 | 464 | 187 | 224 | 1,450 | 321 | n/a | n/a | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 3,480 | 9,274 | 8,509 | 1,551 | 1,270 | 18,500 | 3,531 | n/a | n/a | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 1,041 | 3,928 | 3,737 | 527 | 757 | 3,137 | 1,955 | n/a | n/a | inner join large to small on integer |
| j2 | 906 | 3,900 | 1,894 | 784 | 934 | 3,509 | 2,605 | n/a | n/a | inner join large to medium on integer |
| j3 | 734 | 4,084 | 2,622 | 647 | 1,057 | 5,885 | 2,453 | n/a | n/a | left join large to medium on integer |
| j4 | 759 | 3,721 | 1,838 | 660 | 908 | 5,106 | 2,085 | n/a | n/a | join large to medium on varchar |
| j5 | 2,833 | 14,854 | 3,934 | 2,485 | 1,919 | 7,701 | 8,468 | n/a | n/a | join large to large on integer |
| **geomean** | **731** | **2,426** | **890** | **436** | **444** | **2,308** | **1,122** | — | — | |
| **vs polars** | 1.00x | 3.32x | 1.22x | 0.60x | 0.61x | 3.16x | 1.54x | — | — | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 11/15 | 0/15 | 0/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 4.77 | 7.61 | 3.46 | 4.37 | 3.73 | 4.82 | 5.20 | — | — |

<details><summary>failures</summary>

- `chdb` **gb3** — timeout: exceeded 600s
- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `chdb` **gb7** — timeout: exceeded 600s

</details>

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 30 | 31 | 170 | 124 | 48 | n/a | 25 | 94 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 29 | 5 | 29 | 34 | 37 | n/a | 28 | 88 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 34 | 8 | 96 | 70 | 34 | n/a | 29 | 79 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 30 | 13 | 51 | 61 | 26 | n/a | 24 | 141 | · | Order priority checking: EXISTS -> semi join |
| q5 | 47 | 11 | 114 | 143 | 32 | n/a | 29 | 96 | · | Local supplier volume: 6-way join + groupby |
| q6 | 24 | 5 | 37 | 60 | 18 | 41 | 21 | 55 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 36 | 32 | 280 | 270 | 33 | n/a | 91 | 109 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 35 | 11 | 154 | 160 | 43 | n/a | 68 | 127 | · | National market share: 7-way join, conditional aggregate |
| q9 | 47 | 18 | 114 | 194 | 44 | n/a | 80 | 133 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 44 | 13 | 105 | 124 | 48 | n/a | 43 | 91 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 23 | 5 | 23 | 25 | 32 | n/a | 21 | 79 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 27 | 11 | 157 | 86 | 23 | n/a | 26 | 64 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 49 | 21 | 112 | 77 | 38 | n/a | 33 | 73 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 24 | 5 | 36 | 58 | 23 | n/a | 19 | 65 | · | Promotion effect: join + conditional sum ratio |
| q15 | 25 | 6 | 45 | 107 | 21 | n/a | 35 | ~~109~~ | · | Top supplier: aggregate view + max threshold + join |
| q16 | 26 | 7 | 33 | 17 | 22 | n/a | 24 | 66 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 30 | 9 | 52 | 86 | 26 | n/a | 31 | 77 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 34 | 19 | 91 | 127 | 35 | n/a | 49 | 95 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 31 | 7 | 214 | 117 | 24 | n/a | 29 | 64 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 40 | 10 | 94 | 74 | 25 | n/a | 25 | 120 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 62 | 40 | 254 | 246 | 75 | n/a | 56 | 175 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 29 | 6 | 21 | 20 | 41 | n/a | 19 | 140 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **33** | **11** | **79** | **82** | **32** | **41** | **33** | **92** | — | |
| **vs polars** | 3.10x | 1.00x | 7.40x | 7.69x | 2.99x | 3.85x | 3.06x | 8.63x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 22/22 | 21/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.16 | 0.25 | 0.46 | 0.18 | 0.11 | 0.05 | 0.41 | 0.57 | — |

## PDS-H (TPC-H, 22 queries) — 1 scale, io=parquet

| query | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 1,454 | 258 | 1,809 | 138 | 181 | 330 | 147 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 342 | 17 | 87 | 52 | 49 | 114 | 51 | n/a | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 812 | 74 | 650 | 129 | 138 | 305 | 103 | n/a | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 642 | 74 | 494 | 82 | 68 | 156 | 71 | n/a | Order priority checking: EXISTS -> semi join |
| q5 | 1,842 | 122 | 1,017 | 107 | 210 | 316 | 120 | n/a | Local supplier volume: 6-way join + groupby |
| q6 | 605 | 48 | 230 | 66 | 77 | 112 | 60 | 360 | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 3,914 | 90 | 1,032 | 132 | 247 | 304 | 106 | n/a | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 2,850 | 97 | 692 | 144 | 154 | 768 | 143 | n/a | National market share: 7-way join, conditional aggregate |
| q9 | 3,274 | 213 | 1,037 | 240 | 250 | 725 | 416 | n/a | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 878 | 102 | 633 | 159 | 250 | 298 | 161 | n/a | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 183 | 25 | 81 | 40 | 35 | 114 | 33 | n/a | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 862 | 71 | 1,752 | 75 | 98 | 216 | 68 | n/a | Shipping modes and order priority: join + conditional aggregates |
| q13 | 695 | 162 | 610 | 182 | 141 | 311 | 172 | n/a | Customer distribution: left join + count + groupby of a groupby |
| q14 | 608 | 53 | 246 | 101 | 91 | 162 | 99 | n/a | Promotion effect: join + conditional sum ratio |
| q15 | 1,082 | 36 | 230 | 79 | ~~124~~ | ~~256~~ | 70 | n/a | Top supplier: aggregate view + max threshold + join |
| q16 | 179 | 49 | 133 | 78 | 57 | 187 | 55 | n/a | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 1,018 | 104 | 171 | 124 | 390 | 330 | 124 | n/a | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 1,559 | 263 | 888 | 173 | 543 | 275 | 184 | n/a | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 1,184 | 68 | 1,558 | 128 | 133 | 275 | 128 | n/a | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 902 | 160 | 736 | 92 | 177 | 262 | 89 | n/a | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 2,779 | 554 | 3,449 | 280 | 319 | 603 | 272 | n/a | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 272 | 25 | 46 | 57 | 46 | 104 | 60 | n/a | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **918** | **85** | **481** | **108** | **135** | **254** | **104** | **360** | |
| **vs polars** | 10.77x | 1.00x | 5.64x | 1.27x | 1.59x | 2.97x | 1.22x | 4.23x | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 21/22 | 21/22 | 22/22 | 1/22 | |

Peak resident memory across the suite (GB):

| ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 1.68 | 0.86 | 1.77 | 0.43 | 1.41 | 1.37 | 0.37 | 0.10 |

