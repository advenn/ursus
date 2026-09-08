# ursus benchmark results

Produced from `4e0ce65` — step 40: a KeyTable benchmark, and an optimisation that did not land — on 2026-09-08.

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
| gb1 | 27,152 | 5,936 | 2,192 | 412 | 3,065 | 863 | 480 | 1,038 | 939 | sum v1 by id1 (large-cardinality string groups) |
| gb2 | 33,688 | 6,001 | 2,206 | 854 | 4,169 | 1,046 | 593 | 1,270 | 1,070 | sum v1 by id1, id2 (medium-cardinality string groups) |
| gb3 | 36,114 | 7,162 | 3,097 | 1,037 | 5,667 | 1,375 | 850 | 1,276 | 1,429 | sum v1, mean v3 by id3 (small-cardinality string groups) |
| gb4 | 30,011 | 5,392 | 3,643 | 482 | 3,323 | 1,122 | 671 | 1,184 | 1,071 | mean v1, v2, v3 by id4 (large-cardinality integer groups) |
| gb5 | 33,752 | 6,262 | 3,778 | 792 | 3,135 | 1,407 | 851 | 1,374 | 1,425 | sum v1, v2, v3 by id6 (small-cardinality integer groups) |
| gb6 | 33,954 | n/a | 3,841 | 838 | 3,280 | 1,325 | 871 | ERR | 1,298 | median v3, sd v3 by id4, id5 |
| gb7 | n/a | n/a | 2,748 | 1,041 | 5,657 | 1,257 | 802 | 1,215 | 1,408 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | n/a | n/a | 7,492 | 1,147 | 4,895 | 1,422 | 1,383 | 2,380 | 1,465 | largest two v3 by id6 (top-n within group) |
| gb9 | n/a | n/a | 3,099 | 1,042 | 4,075 | 1,150 | 792 | 1,265 | 1,166 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | n/a | n/a | 10,606 | 4,100 | 13,530 | 2,902 | 1,521 | 4,601 | 2,512 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | n/a | n/a | 4,818 | 1,137 | 8,822 | 2,226 | 1,189 | 2,683 | 1,798 | inner join large to small on integer |
| j2 | n/a | n/a | 5,149 | 1,258 | 9,124 | 2,418 | 1,764 | 3,278 | 1,916 | inner join large to medium on integer |
| j3 | n/a | n/a | 5,307 | 973 | 8,741 | 2,854 | 1,910 | 3,461 | 2,011 | left join large to medium on integer |
| j4 | n/a | n/a | 5,007 | 1,283 | 9,937 | 2,348 | 1,926 | 3,383 | 1,886 | join large to medium on varchar |
| j5 | n/a | n/a | 13,285 | 3,315 | 19,580 | 4,545 | 2,869 | 6,394 | 3,921 | join large to large on integer |
| **geomean** | **32,303** | **6,124** | **4,413** | **1,088** | **6,038** | **1,686** | **1,088** | **2,090** | **1,572** | |
| **vs polars** | 29.70x | 5.63x | 4.06x | 1.00x | 5.55x | 1.55x | 1.00x | 1.92x | 1.44x | |
| **queries passed** | 6/15 | 5/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 14/15 | 15/15 | |

Peak resident memory across the suite (GB):

| gota | qframe | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 9.59 | 4.03 | 8.90 | 5.21 | 5.03 | 4.07 | 3.55 | 6.39 | 4.34 |

<details><summary>failures</summary>

- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','

</details>

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=parquet

| query | polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| gb1 | 99 | 732 | 47 | 45 | 36 | 572 | 89 | n/a | n/a | sum v1 by id1 (large-cardinality string groups) |
| gb2 | 523 | 1,472 | 145 | 102 | 135 | 984 | TIMEOUT | n/a | n/a | sum v1 by id1, id2 (medium-cardinality string groups) |
| gb3 | 778 | 1,668 | 628 | 489 | 541 | 1,172 | 573 | n/a | n/a | sum v1, mean v3 by id3 (small-cardinality string groups) |
| gb4 | 114 | 941 | 99 | 113 | 98 | 437 | 153 | n/a | n/a | mean v1, v2, v3 by id4 (large-cardinality integer groups) |
| gb5 | 397 | 1,263 | 820 | 584 | 604 | 806 | TIMEOUT | n/a | n/a | sum v1, v2, v3 by id6 (small-cardinality integer groups) |
| gb6 | 475 | 2,034 | 496 | 411 | 533 | 1,174 | ERR | n/a | n/a | median v3, sd v3 by id4, id5 |
| gb7 | 718 | 1,645 | 453 | 268 | 434 | 1,385 | TIMEOUT | n/a | n/a | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 852 | 5,598 | 672 | 891 | 550 | 3,241 | 963 | n/a | n/a | largest two v3 by id6 (top-n within group) |
| gb9 | 655 | 1,092 | 241 | 167 | 228 | 1,485 | TIMEOUT | n/a | n/a | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 3,177 | 8,552 | 1,834 | 1,249 | 1,347 | 8,712 | 3,352 | n/a | n/a | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 631 | 3,237 | 1,032 | 426 | 1,011 | 1,905 | 1,887 | n/a | n/a | inner join large to small on integer |
| j2 | 658 | 3,403 | 1,326 | 466 | 968 | 1,886 | 2,308 | n/a | n/a | inner join large to medium on integer |
| j3 | 455 | 3,586 | 1,726 | 494 | 1,122 | 1,663 | 2,504 | n/a | n/a | left join large to medium on integer |
| j4 | 758 | 3,327 | 1,239 | 491 | 976 | 2,774 | 2,062 | n/a | n/a | join large to medium on varchar |
| j5 | 3,035 | 10,372 | 2,462 | 1,940 | 1,896 | 5,271 | 4,462 | n/a | n/a | join large to large on integer |
| **geomean** | **601** | **2,401** | **560** | **366** | **470** | **1,620** | **1,098** | — | — | |
| **vs polars** | 1.00x | 4.00x | 0.93x | 0.61x | 0.78x | 2.70x | 1.83x | — | — | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 10/15 | 0/15 | 0/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 4.65 | 8.75 | 3.44 | 4.31 | 3.70 | 5.02 | 5.86 | — | — |

<details><summary>failures</summary>

- `chdb` **gb2** — timeout: exceeded 600s
- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `chdb` **gb7** — timeout: exceeded 600s
- `chdb` **gb9** — timeout: exceeded 600s

</details>

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 151 | 129 | 414 | 123 | 25 | n/a | 89 | 325 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 94 | 35 | 84 | 42 | 31 | n/a | 55 | 271 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 177 | 108 | 217 | 83 | 29 | n/a | 91 | 415 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 107 | 78 | 109 | 55 | 23 | n/a | 82 | 224 | · | Order priority checking: EXISTS -> semi join |
| q5 | 165 | 133 | 211 | 127 | 32 | n/a | 88 | 400 | · | Local supplier volume: 6-way join + groupby |
| q6 | 81 | 39 | 86 | 53 | 17 | 49 | 54 | 147 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 205 | 109 | 267 | 186 | 31 | n/a | 131 | 360 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 145 | 93 | 183 | 143 | 33 | n/a | 128 | 457 | · | National market share: 7-way join, conditional aggregate |
| q9 | 171 | 170 | 374 | 174 | 40 | n/a | 199 | 509 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 131 | 69 | 233 | 126 | 36 | n/a | 127 | 292 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 77 | 50 | 110 | 28 | 21 | n/a | 47 | 307 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 118 | 60 | 292 | 124 | 24 | n/a | 66 | 233 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 220 | 115 | 318 | 84 | 40 | n/a | 105 | 234 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 106 | 43 | 85 | 211 | 22 | n/a | 56 | 183 | · | Promotion effect: join + conditional sum ratio |
| q15 | 78 | 35 | 64 | 510 | 20 | n/a | 108 | 335 | · | Top supplier: aggregate view + max threshold + join |
| q16 | 82 | 83 | 90 | 113 | 23 | n/a | 41 | 69 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 147 | 49 | 99 | 501 | 26 | n/a | 102 | 82 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 142 | 134 | 163 | 564 | 33 | n/a | 193 | 86 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 176 | 71 | 382 | 734 | 31 | n/a | 93 | 67 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 97 | 104 | 162 | 309 | 27 | n/a | 85 | 84 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 184 | 227 | 450 | 1,494 | 84 | n/a | 143 | 137 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 61 | 24 | 29 | 94 | 36 | n/a | 35 | 54 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **125** | **76** | **162** | **160** | **29** | **49** | **87** | **195** | — | |
| **vs polars** | 1.63x | 1.00x | 2.12x | 2.09x | 0.38x | 0.64x | 1.14x | 2.56x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 22/22 | 22/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.15 | 0.24 | 0.42 | 0.28 | 0.11 | 0.05 | 0.39 | 0.62 | — |

## PDS-H (TPC-H, 22 queries) — 1 scale, io=parquet

| query | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 1,298 | 242 | 1,396 | 145 | 147 | 992 | 137 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 533 | 16 | 80 | 56 | 50 | 850 | 47 | n/a | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 832 | 65 | 434 | 117 | 134 | 1,095 | 107 | n/a | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 677 | 68 | 532 | 79 | 62 | 537 | 72 | n/a | Order priority checking: EXISTS -> semi join |
| q5 | 1,399 | 138 | 738 | 118 | 173 | 1,289 | 104 | n/a | Local supplier volume: 6-way join + groupby |
| q6 | 1,112 | 53 | 156 | 64 | 65 | 536 | 62 | 355 | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 1,882 | 96 | 840 | 127 | 218 | 1,297 | 116 | n/a | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 2,874 | 97 | 543 | 138 | 148 | 1,610 | 140 | n/a | National market share: 7-way join, conditional aggregate |
| q9 | 3,060 | 193 | 1,035 | 237 | 379 | 1,786 | 221 | n/a | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 855 | 114 | 516 | 149 | 205 | 1,297 | 142 | n/a | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 182 | 23 | 89 | 38 | 35 | 541 | 33 | n/a | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 924 | 80 | 1,388 | 74 | 122 | 787 | 89 | n/a | Shipping modes and order priority: join + conditional aggregates |
| q13 | 814 | 213 | 546 | 170 | 161 | 839 | 173 | n/a | Customer distribution: left join + count + groupby of a groupby |
| q14 | 574 | 65 | 165 | 104 | 87 | 761 | 91 | n/a | Promotion effect: join + conditional sum ratio |
| q15 | 1,071 | 46 | 267 | 74 | 114 | 821 | 62 | n/a | Top supplier: aggregate view + max threshold + join |
| q16 | 142 | 30 | 160 | 142 | 52 | 266 | 53 | n/a | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 1,018 | 93 | 147 | 200 | 329 | 363 | 111 | n/a | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 1,563 | 247 | 748 | 177 | 483 | 245 | 180 | n/a | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 1,203 | 76 | 1,326 | 123 | 121 | 287 | 124 | n/a | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 921 | 162 | 428 | 92 | 156 | 231 | 88 | n/a | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 5,097 | 568 | 3,268 | 309 | 1,390 | 576 | 380 | n/a | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 255 | 28 | 46 | 75 | 206 | 103 | 65 | n/a | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **936** | **86** | **411** | **114** | **148** | **627** | **101** | **355** | |
| **vs polars** | 10.84x | 1.00x | 4.76x | 1.32x | 1.71x | 7.26x | 1.17x | 4.11x | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | |

Peak resident memory across the suite (GB):

| ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 2.26 | 0.81 | 1.76 | 0.31 | 1.32 | 1.47 | 0.28 | 0.10 |

