# ursus benchmark results

Produced from `c6aa77d` — perf: the CSV reader's blocks are sized once and scanned in place — on 2026-10-11.

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
| gb1 | 24,441 | 4,486 | 730 | 441 | 2,640 | 790 | 422 | 947 | 788 | sum v1 by id1 (100 string groups) |
| gb2 | OOM | 5,124 | 1,464 | 944 | 3,546 | 930 | 518 | 1,075 | 942 | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | OOM | 6,079 | 1,673 | 1,117 | 4,954 | 1,278 | 802 | TIMEOUT | 1,165 | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 26,802 | 4,745 | 1,553 | 513 | 2,648 | 874 | 570 | 768 | 931 | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 28,628 | 5,487 | 1,752 | 831 | 2,712 | 1,261 | 748 | TIMEOUT | 1,213 | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 29,107 | n/a | 1,841 | 888 | 2,745 | 1,129 | 761 | ERR | 1,139 | median v3, sd v3 by id4, id5 |
| gb7 | n/a | n/a | 1,668 | 1,024 | 4,936 | 1,121 | 704 | 813 | 1,134 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | n/a | n/a | 1,617 | 1,242 | 3,821 | 1,246 | 1,378 | 1,527 | 1,215 | largest two v3 by id6 (top-n within group) |
| gb9 | n/a | n/a | 1,696 | 1,070 | 3,489 | 1,006 | 653 | 814 | 1,002 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | n/a | n/a | 4,408 | 3,881 | 15,974 | 2,595 | 1,615 | 6,381 | 2,237 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | n/a | n/a | 2,416 | 1,206 | 7,956 | 1,931 | 1,072 | 2,379 | 1,494 | inner join large to small on integer |
| j2 | n/a | n/a | 2,497 | 1,242 | 8,467 | 2,087 | 1,624 | 3,079 | 1,648 | inner join large to medium on integer |
| j3 | n/a | n/a | 2,423 | 927 | 7,742 | 2,354 | 1,812 | 3,149 | 1,782 | left join large to medium on integer |
| j4 | n/a | n/a | 2,537 | 1,298 | 9,387 | 2,290 | 1,835 | 3,100 | 1,708 | join large to medium on varchar |
| j5 | n/a | n/a | 6,282 | 3,402 | 18,145 | 4,006 | 2,578 | 9,516 | 3,265 | join large to large on integer |
| **geomean** | **27,181** | **5,154** | **2,034** | **1,123** | **5,355** | **1,483** | **991** | **1,971** | **1,344** | |
| **vs polars** | 24.20x | 4.59x | 1.81x | 1.00x | 4.77x | 1.32x | 0.88x | 1.75x | 1.20x | |
| **queries passed** | 4/15 | 5/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 12/15 | 15/15 | |

Peak resident memory across the suite (GB):

| gota | qframe | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 8.50 | 4.12 | 7.77 | 5.18 | 5.02 | 4.09 | 3.65 | 5.90 | 4.40 |

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
| gb1 | 85 | 196 | 35 | 30 | 32 | 337 | 83 | n/a | n/a | sum v1 by id1 (100 string groups) |
| gb2 | 504 | 313 | 110 | 80 | 119 | 721 | 373 | n/a | n/a | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | 720 | 844 | 554 | 453 | 513 | 895 | 477 | n/a | n/a | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 100 | 243 | 95 | 97 | 87 | 344 | 144 | n/a | n/a | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 334 | 651 | 584 | 408 | 478 | 420 | TIMEOUT | n/a | n/a | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 421 | 683 | 433 | 405 | 411 | 691 | ERR | n/a | n/a | median v3, sd v3 by id4, id5 |
| gb7 | 647 | 696 | 416 | 254 | 390 | 770 | TIMEOUT | n/a | n/a | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 865 | 601 | 627 | 809 | 530 | 1,974 | 958 | n/a | n/a | largest two v3 by id6 (top-n within group) |
| gb9 | 555 | 556 | 201 | 161 | 268 | 777 | 306 | n/a | n/a | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 2,868 | 3,511 | 1,641 | 1,179 | 1,251 | 7,958 | 5,039 | n/a | n/a | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 544 | 1,250 | 900 | 475 | 751 | 1,606 | 1,547 | n/a | n/a | inner join large to small on integer |
| j2 | 528 | 1,350 | 1,071 | 477 | 844 | 1,654 | 2,248 | n/a | n/a | inner join large to medium on integer |
| j3 | 372 | 1,314 | 1,440 | 564 | 983 | 2,263 | 2,152 | n/a | n/a | left join large to medium on integer |
| j4 | 589 | 1,394 | 1,057 | 572 | 829 | 2,492 | 1,730 | n/a | n/a | join large to medium on varchar |
| j5 | 2,172 | 4,504 | 2,221 | 1,788 | 1,710 | 4,895 | 7,612 | n/a | n/a | join large to large on integer |
| **geomean** | **519** | **832** | **475** | **339** | **418** | **1,198** | **907** | — | — | |
| **vs polars** | 1.00x | 1.61x | 0.92x | 0.65x | 0.81x | 2.31x | 1.75x | — | — | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 12/15 | 0/15 | 0/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 4.73 | 6.87 | 3.49 | 4.40 | 3.75 | 5.42 | 5.18 | — | — |

<details><summary>failures</summary>

- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `chdb` **gb7** — timeout: exceeded 600s

</details>

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 26 | 27 | 100 | 41 | 24 | n/a | 18 | 88 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 25 | 4 | 22 | 10 | 22 | n/a | 17 | 78 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 27 | 8 | 48 | 23 | 26 | n/a | 18 | 71 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 25 | 8 | 32 | 18 | 21 | n/a | 14 | 51 | · | Order priority checking: EXISTS -> semi join |
| q5 | 28 | 14 | 63 | 25 | 27 | n/a | 19 | 81 | · | Local supplier volume: 6-way join + groupby |
| q6 | 21 | 5 | 16 | 13 | 17 | 30 | 11 | 44 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 29 | 11 | 74 | 51 | 27 | n/a | 34 | 91 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 34 | 10 | 55 | 32 | 29 | n/a | 23 | 103 | · | National market share: 7-way join, conditional aggregate |
| q9 | 43 | 19 | 68 | 46 | 37 | n/a | 29 | 123 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 41 | 11 | 56 | 34 | 32 | n/a | 26 | 79 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 22 | 5 | 16 | 8 | 17 | n/a | 12 | 74 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 26 | 8 | 77 | 28 | 19 | n/a | 16 | 71 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 44 | 25 | 54 | 32 | 31 | n/a | 23 | 64 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 23 | 5 | 19 | 21 | 20 | n/a | 13 | 49 | · | Promotion effect: join + conditional sum ratio |
| q15 | 22 | 6 | 22 | 17 | 18 | n/a | ~~18~~ | ~~96~~ | · | Top supplier: aggregate view + max threshold + join |
| q16 | 25 | 4 | 18 | 9 | 20 | n/a | 13 | 57 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 27 | 9 | 24 | 25 | 20 | n/a | 20 | 69 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 35 | 16 | 57 | 30 | 31 | n/a | 41 | 79 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 26 | 6 | 110 | 37 | 23 | n/a | 19 | 61 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 29 | 10 | 45 | 30 | 24 | n/a | 17 | 79 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 48 | 34 | 145 | 54 | 41 | n/a | 32 | 125 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 24 | 4 | 12 | 10 | 19 | n/a | 11 | 52 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **29** | **9** | **41** | **24** | **24** | **30** | **19** | **73** | — | |
| **vs polars** | 3.11x | 1.00x | 4.43x | 2.56x | 2.60x | 3.22x | 2.05x | 7.89x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 21/22 | 21/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.28 | 0.29 | 0.52 | 0.14 | 0.23 | 0.05 | 0.45 | 0.63 | — |

## PDS-H (TPC-H, 22 queries) — 1 scale, io=parquet

| query | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 318 | 242 | 1,181 | 103 | 208 | 272 | 132 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 38 | 17 | 72 | 42 | 41 | 107 | 43 | n/a | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 166 | 70 | 373 | 87 | 106 | 260 | 92 | n/a | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 150 | 59 | 293 | 67 | 55 | 137 | 62 | n/a | Order priority checking: EXISTS -> semi join |
| q5 | 175 | 121 | 587 | 99 | 153 | 278 | 101 | n/a | Local supplier volume: 6-way join + groupby |
| q6 | 96 | 44 | 131 | 58 | 59 | 111 | 54 | 318 | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 331 | 82 | 691 | 102 | 208 | 295 | 101 | n/a | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 216 | 99 | 441 | 128 | 137 | 464 | 124 | n/a | National market share: 7-way join, conditional aggregate |
| q9 | 346 | 208 | 687 | 215 | 190 | 591 | 210 | n/a | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 241 | 111 | 400 | 149 | 157 | 273 | 128 | n/a | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 34 | 21 | 61 | 38 | 30 | 113 | 34 | n/a | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 256 | 73 | 1,043 | 70 | 90 | 192 | 59 | n/a | Shipping modes and order priority: join + conditional aggregates |
| q13 | 357 | 171 | 443 | 154 | 126 | 230 | 148 | n/a | Customer distribution: left join + count + groupby of a groupby |
| q14 | 149 | 48 | 152 | 89 | 75 | 149 | 85 | n/a | Promotion effect: join + conditional sum ratio |
| q15 | 141 | 36 | 141 | 61 | ~~106~~ | 215 | 59 | n/a | Top supplier: aggregate view + max threshold + join |
| q16 | 88 | 26 | 113 | 52 | 50 | 83 | 49 | n/a | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 251 | 78 | 121 | 104 | 294 | 305 | 177 | n/a | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 325 | 221 | 564 | 161 | 443 | 254 | 167 | n/a | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 353 | 61 | 1,106 | 126 | 115 | 214 | 117 | n/a | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 252 | 140 | 375 | 84 | 121 | 202 | 81 | n/a | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 567 | 487 | 2,154 | 233 | 268 | 522 | 236 | n/a | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 60 | 27 | 38 | 48 | 36 | 91 | 50 | n/a | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **180** | **78** | **315** | **91** | **111** | **212** | **92** | **318** | |
| **vs polars** | 2.32x | 1.00x | 4.06x | 1.18x | 1.43x | 2.73x | 1.18x | 4.10x | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 21/22 | 22/22 | 22/22 | 1/22 | |

Peak resident memory across the suite (GB):

| ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.85 | 0.86 | 1.87 | 0.44 | 1.48 | 1.43 | 0.41 | 0.10 |

