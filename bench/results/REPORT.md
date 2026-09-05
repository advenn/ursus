# ursus benchmark results

Produced from `cd1d78a` — Validate h2o 15/15 in both IO modes; stop the fixture leak — on 2026-09-05.

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

| query | gota | qframe | ursus (Go) | what it exercises |
|---|--:|--:|--:|---|
| gb1 | 29,543 | 4,976 | 2,069 | sum v1 by id1 (large-cardinality string groups) |
| gb2 | 37,320 | 6,212 | 2,535 | sum v1 by id1, id2 (medium-cardinality string groups) |
| gb3 | 39,378 | 6,964 | 3,801 | sum v1, mean v3 by id3 (small-cardinality string groups) |
| gb4 | 32,668 | 5,515 | 3,572 | mean v1, v2, v3 by id4 (large-cardinality integer groups) |
| gb5 | 33,233 | 6,408 | 4,413 | sum v1, v2, v3 by id6 (small-cardinality integer groups) |
| gb6 | 36,481 | n/a | 3,996 | median v3, sd v3 by id4, id5 |
| gb7 | n/a | n/a | 3,156 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | n/a | n/a | 7,688 | largest two v3 by id6 (top-n within group) |
| gb9 | n/a | n/a | 2,893 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | n/a | n/a | 11,408 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | n/a | n/a | 5,940 | inner join large to small on integer |
| j2 | n/a | n/a | 6,505 | inner join large to medium on integer |
| j3 | n/a | n/a | 7,846 | left join large to medium on integer |
| j4 | n/a | n/a | 7,026 | join large to medium on varchar |
| j5 | n/a | n/a | 27,531 | join large to large on integer |
| **geomean** | **34,613** | **5,974** | **5,225** | |
| **queries passed** | 6/15 | 5/15 | 15/15 | |

Peak resident memory across the suite (GB):

| gota | qframe | ursus (Go) |
|--:|--:|--:|
| 9.61 | 3.82 | 7.76 |

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=parquet

| query | polars | ursus (Go) | duckdb | datafusion | duckdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|---|
| gb1 | 97 | 635 | 55 | 37 | 40 | sum v1 by id1 (large-cardinality string groups) |
| gb2 | 627 | 899 | 237 | 102 | 183 | sum v1 by id1, id2 (medium-cardinality string groups) |
| gb3 | 764 | 1,619 | 661 | 538 | 613 | sum v1, mean v3 by id3 (small-cardinality string groups) |
| gb4 | 95 | 913 | 115 | 161 | 98 | mean v1, v2, v3 by id4 (large-cardinality integer groups) |
| gb5 | 377 | 1,234 | 865 | 498 | 566 | sum v1, v2, v3 by id6 (small-cardinality integer groups) |
| gb6 | 476 | 2,220 | 617 | 492 | 515 | median v3, sd v3 by id4, id5 |
| gb7 | 771 | 1,407 | 540 | 279 | 466 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 818 | 4,908 | 705 | 1,255 | 605 | largest two v3 by id6 (top-n within group) |
| gb9 | 668 | 1,321 | 382 | 173 | 256 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 3,603 | 9,235 | 2,318 | 1,386 | 1,457 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 619 | 4,486 | 1,209 | 449 | 850 | inner join large to small on integer |
| j2 | 568 | 5,606 | 1,494 | 569 | 1,465 | inner join large to medium on integer |
| j3 | 411 | 5,668 | 1,899 | 566 | 1,129 | left join large to medium on integer |
| j4 | 710 | 4,595 | 1,293 | 587 | 974 | join large to medium on varchar |
| j5 | 2,502 | 19,699 | 3,223 | 2,284 | 1,936 | join large to large on integer |
| **geomean** | **583** | **2,671** | **672** | **405** | **505** | |
| **vs polars** | 1.00x | 4.58x | 1.15x | 0.69x | 0.87x | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go |
|--:|--:|--:|--:|--:|
| 3.59 | 8.11 | 2.35 | 2.95 | 2.47 |

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 176 | 35 | 237 | 123 | 55 | n/a | 40 | 141 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 72 | 6 | 44 | 40 | 52 | n/a | 51 | 135 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 55 | 18 | 101 | 92 | 48 | n/a | 43 | 143 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 62 | 14 | 61 | 55 | 41 | n/a | 26 | 121 | · | Order priority checking: EXISTS -> semi join |
| q5 | 56 | 26 | 135 | 143 | 62 | n/a | 34 | 157 | · | Local supplier volume: 6-way join + groupby |
| q6 | 32 | 7 | 32 | 56 | 34 | 56 | 33 | 115 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 48 | 24 | 124 | 193 | 71 | n/a | 57 | 172 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 54 | 18 | 131 | 153 | 53 | n/a | 81 | 274 | · | National market share: 7-way join, conditional aggregate |
| q9 | 58 | 43 | 143 | 249 | 83 | n/a | 84 | 196 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 90 | 34 | 125 | 110 | 61 | n/a | 49 | 117 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 36 | 10 | 43 | 36 | 42 | n/a | 50 | 117 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 41 | 38 | 283 | 105 | 52 | n/a | 61 | 93 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 77 | 49 | 194 | 73 | 77 | n/a | 99 | 105 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 45 | 12 | 39 | 57 | 40 | n/a | 29 | 111 | · | Promotion effect: join + conditional sum ratio |
| q15 | 42 | 12 | 40 | 116 | 35 | n/a | 35 | 207 | · | Top supplier: aggregate view + max threshold + join |
| q16 | 36 | 40 | 48 | 23 | 42 | n/a | 22 | 135 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 59 | 21 | 35 | 126 | 42 | n/a | 49 | 144 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 67 | 32 | 169 | 129 | 46 | n/a | 110 | 143 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 46 | 17 | 269 | 135 | 38 | n/a | 44 | 99 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 165 | 20 | 103 | 85 | 50 | n/a | 41 | 150 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 470 | 109 | 473 | 359 | 86 | n/a | 68 | 265 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 196 | 11 | 26 | 19 | 32 | n/a | 29 | 140 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **68** | **21** | **94** | **89** | **50** | **56** | **47** | **143** | — | |
| **vs polars** | 3.19x | 1.00x | 4.41x | 4.17x | 2.33x | 2.61x | 2.20x | 6.68x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 22/22 | 22/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.15 | 0.25 | 0.42 | 0.42 | 0.11 | 0.05 | 0.41 | 0.56 | — |

## PDS-H (TPC-H, 22 queries) — 1 scale, io=parquet

| query | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 1,006 | 254 | 1,361 | 155 | 231 | 348 | 143 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 318 | 30 | 83 | 54 | 53 | 129 | 58 | n/a | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 775 | 113 | 449 | 113 | 119 | 347 | 111 | n/a | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 1,123 | 68 | 398 | 73 | 62 | 157 | 72 | n/a | Order priority checking: EXISTS -> semi join |
| q5 | 1,870 | 133 | 808 | 133 | 185 | 312 | 117 | n/a | Local supplier volume: 6-way join + groupby |
| q6 | 641 | 50 | 184 | 65 | 69 | 132 | 69 | 465 | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 2,439 | 102 | 903 | 126 | 238 | 394 | 118 | n/a | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 2,984 | 121 | 652 | 135 | 155 | 525 | 144 | n/a | National market share: 7-way join, conditional aggregate |
| q9 | 3,778 | 266 | 945 | 228 | 210 | 713 | 233 | n/a | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 960 | 125 | 516 | 163 | 182 | 314 | 158 | n/a | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 332 | 31 | 90 | 40 | 34 | 139 | 36 | n/a | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 1,083 | 81 | 1,272 | 80 | 116 | 205 | 67 | n/a | Shipping modes and order priority: join + conditional aggregates |
| q13 | 722 | 191 | 565 | 171 | 203 | 288 | 159 | n/a | Customer distribution: left join + count + groupby of a groupby |
| q14 | 582 | 105 | 199 | 98 | 90 | 166 | 106 | n/a | Promotion effect: join + conditional sum ratio |
| q15 | 1,147 | 45 | 160 | 80 | 121 | 290 | 68 | n/a | Top supplier: aggregate view + max threshold + join |
| q16 | 281 | 31 | 158 | 55 | 60 | 91 | 56 | n/a | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 1,482 | 79 | 135 | 127 | 356 | 357 | 137 | n/a | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 2,051 | 248 | 711 | 174 | 565 | 274 | 253 | n/a | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 1,689 | 73 | 1,316 | 124 | 118 | 244 | 133 | n/a | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 1,121 | 142 | 461 | 112 | 136 | 250 | 97 | n/a | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 5,084 | 655 | 2,899 | 295 | 300 | 688 | 276 | n/a | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 268 | 47 | 49 | 70 | 54 | 110 | 55 | n/a | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **1,057** | **98** | **405** | **108** | **132** | **253** | **106** | **465** | |
| **vs polars** | 10.73x | 1.00x | 4.11x | 1.10x | 1.34x | 2.57x | 1.08x | 4.72x | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | |

Peak resident memory across the suite (GB):

| ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 3.62 | 0.77 | 1.71 | 0.31 | 1.30 | 1.37 | 0.28 | 0.09 |

