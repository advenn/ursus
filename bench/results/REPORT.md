# ursus benchmark results

Produced from `5a721e0` — perf: the key table waits on memory once per chunk of keys, not once per key — on 2026-10-08.

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
| gb1 | 24,441 | 4,486 | 1,961 | 371 | 2,640 | 790 | 422 | 947 | 788 | sum v1 by id1 (100 string groups) |
| gb2 | OOM | 5,124 | 2,089 | 822 | 3,546 | 930 | 518 | 1,075 | 942 | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | OOM | 6,079 | 2,635 | 924 | 4,954 | 1,278 | 802 | TIMEOUT | 1,165 | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 26,802 | 4,745 | 3,140 | 450 | 2,648 | 874 | 570 | 768 | 931 | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 28,628 | 5,487 | 2,825 | 716 | 2,712 | 1,261 | 748 | TIMEOUT | 1,213 | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 29,107 | n/a | 2,899 | 757 | 2,745 | 1,129 | 761 | ERR | 1,139 | median v3, sd v3 by id4, id5 |
| gb7 | n/a | n/a | 2,306 | 884 | 4,936 | 1,121 | 704 | 813 | 1,134 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | n/a | n/a | 2,670 | 1,034 | 3,821 | 1,246 | 1,378 | 1,527 | 1,215 | largest two v3 by id6 (top-n within group) |
| gb9 | n/a | n/a | 2,672 | 946 | 3,489 | 1,006 | 653 | 814 | 1,002 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | n/a | n/a | 7,619 | 3,634 | 15,974 | 2,595 | 1,615 | 6,381 | 2,237 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | n/a | n/a | 3,432 | 1,030 | 7,956 | 1,931 | 1,072 | 2,379 | 1,494 | inner join large to small on integer |
| j2 | n/a | n/a | 3,526 | 1,255 | 8,467 | 2,087 | 1,624 | 3,079 | 1,648 | inner join large to medium on integer |
| j3 | n/a | n/a | 3,655 | 873 | 7,742 | 2,354 | 1,812 | 3,149 | 1,782 | left join large to medium on integer |
| j4 | n/a | n/a | 3,529 | 1,142 | 9,387 | 2,290 | 1,835 | 3,100 | 1,708 | join large to medium on varchar |
| j5 | n/a | n/a | 8,526 | 3,051 | 18,145 | 4,006 | 2,578 | 9,516 | 3,265 | join large to large on integer |
| **geomean** | **27,181** | **5,154** | **3,244** | **989** | **5,355** | **1,483** | **991** | **1,971** | **1,344** | |
| **vs polars** | 27.48x | 5.21x | 3.28x | 1.00x | 5.41x | 1.50x | 1.00x | 1.99x | 1.36x | |
| **queries passed** | 4/15 | 5/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 12/15 | 15/15 | |

Peak resident memory across the suite (GB):

| gota | qframe | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 8.50 | 4.12 | 7.60 | 5.40 | 5.02 | 4.09 | 3.65 | 5.90 | 4.40 |

<details><summary>failures</summary>

- `chdb` **gb3** — timeout: exceeded 600s
- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `gota` **gb2** — oom: no output, exit -9
- `gota` **gb3** — oom: no output, exit -9

</details>

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=parquet

| query | polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| gb1 | 84 | 289 | 35 | 30 | 32 | 337 | 83 | n/a | n/a | sum v1 by id1 (100 string groups) |
| gb2 | 512 | 440 | 110 | 80 | 119 | 721 | 373 | n/a | n/a | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | 629 | 873 | 554 | 453 | 513 | 895 | 477 | n/a | n/a | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 90 | 333 | 95 | 97 | 87 | 344 | 144 | n/a | n/a | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 307 | 730 | 584 | 408 | 478 | 420 | TIMEOUT | n/a | n/a | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 369 | 638 | 433 | 405 | 411 | 691 | ERR | n/a | n/a | median v3, sd v3 by id4, id5 |
| gb7 | 586 | 707 | 416 | 254 | 390 | 770 | TIMEOUT | n/a | n/a | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 706 | 705 | 627 | 809 | 530 | 1,974 | 958 | n/a | n/a | largest two v3 by id6 (top-n within group) |
| gb9 | 488 | 538 | 201 | 161 | 268 | 777 | 306 | n/a | n/a | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 2,603 | 5,174 | 1,641 | 1,179 | 1,251 | 7,958 | 5,039 | n/a | n/a | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 488 | 1,193 | 900 | 475 | 751 | 1,606 | 1,547 | n/a | n/a | inner join large to small on integer |
| j2 | 489 | 1,294 | 1,071 | 477 | 844 | 1,654 | 2,248 | n/a | n/a | inner join large to medium on integer |
| j3 | 354 | 1,316 | 1,440 | 564 | 983 | 2,263 | 2,152 | n/a | n/a | left join large to medium on integer |
| j4 | 557 | 1,291 | 1,057 | 572 | 829 | 2,492 | 1,730 | n/a | n/a | join large to medium on varchar |
| j5 | 2,042 | 4,139 | 2,221 | 1,788 | 1,710 | 4,895 | 7,612 | n/a | n/a | join large to large on integer |
| **geomean** | **475** | **914** | **475** | **339** | **418** | **1,198** | **907** | — | — | |
| **vs polars** | 1.00x | 1.93x | 1.00x | 0.71x | 0.88x | 2.52x | 1.91x | — | — | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 12/15 | 0/15 | 0/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 4.65 | 6.83 | 3.49 | 4.40 | 3.75 | 5.42 | 5.18 | — | — |

<details><summary>failures</summary>

- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `chdb` **gb7** — timeout: exceeded 600s

</details>

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 26 | 22 | 100 | 48 | 24 | n/a | 18 | 88 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 25 | 4 | 22 | 17 | 22 | n/a | 17 | 78 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 27 | 7 | 48 | 30 | 26 | n/a | 18 | 71 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 25 | 9 | 32 | 32 | 21 | n/a | 14 | 51 | · | Order priority checking: EXISTS -> semi join |
| q5 | 28 | 11 | 63 | 37 | 27 | n/a | 19 | 81 | · | Local supplier volume: 6-way join + groupby |
| q6 | 21 | 6 | 16 | 19 | 17 | 30 | 11 | 44 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 29 | 10 | 74 | 69 | 27 | n/a | 34 | 91 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 34 | 12 | 55 | 43 | 29 | n/a | 23 | 103 | · | National market share: 7-way join, conditional aggregate |
| q9 | 43 | 20 | 68 | 71 | 37 | n/a | 29 | 123 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 41 | 9 | 56 | 41 | 32 | n/a | 26 | 79 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 22 | 4 | 16 | 15 | 17 | n/a | 12 | 74 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 26 | 9 | 77 | 43 | 19 | n/a | 16 | 71 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 44 | 27 | 54 | 42 | 31 | n/a | 23 | 64 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 23 | 4 | 19 | 26 | 20 | n/a | 13 | 49 | · | Promotion effect: join + conditional sum ratio |
| q15 | 22 | 5 | 22 | 37 | 18 | n/a | ~~18~~ | ~~96~~ | · | Top supplier: aggregate view + max threshold + join |
| q16 | 25 | 4 | 18 | 14 | 20 | n/a | 13 | 57 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 27 | 7 | 24 | 34 | 20 | n/a | 20 | 69 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 35 | 17 | 57 | 51 | 31 | n/a | 41 | 79 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 26 | 6 | 110 | 47 | 23 | n/a | 19 | 61 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 29 | 9 | 45 | 36 | 24 | n/a | 17 | 79 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 48 | 32 | 145 | 132 | 41 | n/a | 32 | 125 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 24 | 3 | 12 | 13 | 19 | n/a | 11 | 52 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **29** | **9** | **41** | **35** | **24** | **30** | **19** | **73** | — | |
| **vs polars** | 3.31x | 1.00x | 4.71x | 4.01x | 2.76x | 3.42x | 2.18x | 8.38x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 21/22 | 21/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.28 | 0.36 | 0.52 | 0.18 | 0.23 | 0.05 | 0.45 | 0.63 | — |

## PDS-H (TPC-H, 22 queries) — 1 scale, io=parquet

| query | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 458 | 220 | 1,181 | 103 | 208 | 272 | 132 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 117 | 14 | 72 | 42 | 41 | 107 | 43 | n/a | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 274 | 61 | 373 | 87 | 106 | 260 | 92 | n/a | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 314 | 60 | 293 | 67 | 55 | 137 | 62 | n/a | Order priority checking: EXISTS -> semi join |
| q5 | 347 | 102 | 587 | 99 | 153 | 278 | 101 | n/a | Local supplier volume: 6-way join + groupby |
| q6 | 151 | 38 | 131 | 58 | 59 | 111 | 54 | 318 | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 564 | 80 | 691 | 102 | 208 | 295 | 101 | n/a | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 375 | 89 | 441 | 128 | 137 | 464 | 124 | n/a | National market share: 7-way join, conditional aggregate |
| q9 | 625 | 186 | 687 | 215 | 190 | 591 | 210 | n/a | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 332 | 91 | 400 | 149 | 157 | 273 | 128 | n/a | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 95 | 20 | 61 | 38 | 30 | 113 | 34 | n/a | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 374 | 59 | 1,043 | 70 | 90 | 192 | 59 | n/a | Shipping modes and order priority: join + conditional aggregates |
| q13 | 498 | 146 | 443 | 154 | 126 | 230 | 148 | n/a | Customer distribution: left join + count + groupby of a groupby |
| q14 | 189 | 44 | 152 | 89 | 75 | 149 | 85 | n/a | Promotion effect: join + conditional sum ratio |
| q15 | 324 | 31 | 141 | 61 | ~~106~~ | 215 | 59 | n/a | Top supplier: aggregate view + max threshold + join |
| q16 | 101 | 22 | 113 | 52 | 50 | 83 | 49 | n/a | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 404 | 73 | 121 | 104 | 294 | 305 | 177 | n/a | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 587 | 203 | 564 | 161 | 443 | 254 | 167 | n/a | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 418 | 61 | 1,106 | 126 | 115 | 214 | 117 | n/a | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 379 | 129 | 375 | 84 | 121 | 202 | 81 | n/a | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 1,386 | 457 | 2,154 | 233 | 268 | 522 | 236 | n/a | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 105 | 25 | 38 | 48 | 36 | 91 | 50 | n/a | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **310** | **70** | **315** | **91** | **111** | **212** | **92** | **318** | |
| **vs polars** | 4.43x | 1.00x | 4.52x | 1.31x | 1.59x | 3.03x | 1.31x | 4.56x | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 21/22 | 22/22 | 22/22 | 1/22 | |

Peak resident memory across the suite (GB):

| ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 1.02 | 0.91 | 1.87 | 0.44 | 1.48 | 1.43 | 0.41 | 0.10 |

