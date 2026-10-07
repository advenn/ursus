# ursus benchmark results

Produced from `645efda` — docs: the v0.4.0 release candidate — on 2026-10-08.

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
| gb1 | 24,150 | 4,539 | 2,072 | 393 | 2,655 | 785 | 437 | 932 | 810 | sum v1 by id1 (100 string groups) |
| gb2 | OOM | 5,126 | 2,233 | 780 | 3,735 | 930 | 524 | 1,080 | 929 | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | OOM | 6,145 | 2,974 | 932 | 4,996 | 1,210 | 798 | 1,172 | 1,188 | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 27,020 | 4,733 | 3,113 | 450 | 2,683 | 856 | 556 | 1,095 | 897 | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 28,725 | 5,683 | 2,920 | 750 | 2,748 | 1,280 | 738 | 1,235 | 1,248 | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 29,342 | n/a | 2,797 | 732 | 2,753 | 1,120 | 768 | ERR | 1,132 | median v3, sd v3 by id4, id5 |
| gb7 | n/a | n/a | 2,424 | 899 | 4,950 | 1,110 | 715 | 1,099 | 1,143 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | n/a | n/a | 2,651 | 1,029 | 3,906 | 1,249 | 1,191 | 2,276 | 1,243 | largest two v3 by id6 (top-n within group) |
| gb9 | n/a | n/a | 2,663 | 984 | 3,673 | 982 | 641 | 1,136 | 1,017 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | n/a | n/a | 8,409 | 3,323 | 16,557 | 2,669 | 1,623 | 6,207 | 2,231 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | n/a | n/a | 3,404 | 1,029 | 7,686 | 1,927 | 1,220 | 2,476 | 1,480 | inner join large to small on integer |
| j2 | n/a | n/a | 3,543 | 1,130 | 8,237 | 2,042 | 1,789 | 3,139 | 1,746 | inner join large to medium on integer |
| j3 | n/a | n/a | 3,667 | 965 | 7,578 | 2,263 | 1,687 | 3,497 | 1,784 | left join large to medium on integer |
| j4 | n/a | n/a | 3,597 | 1,142 | 9,101 | 2,182 | 1,849 | 3,221 | 1,694 | join large to medium on varchar |
| j5 | n/a | n/a | 10,473 | 3,077 | 18,165 | 4,078 | 2,646 | 9,027 | 3,389 | join large to large on integer |
| **geomean** | **27,232** | **5,212** | **3,375** | **989** | **5,389** | **1,466** | **994** | **2,044** | **1,356** | |
| **vs polars** | 27.54x | 5.27x | 3.41x | 1.00x | 5.45x | 1.48x | 1.01x | 2.07x | 1.37x | |
| **queries passed** | 4/15 | 5/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 14/15 | 15/15 | |

Peak resident memory across the suite (GB):

| gota | qframe | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 8.50 | 3.65 | 8.19 | 5.21 | 5.03 | 4.09 | 3.77 | 5.46 | 4.37 |

<details><summary>failures</summary>

- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `gota` **gb2** — oom: no output, exit -9
- `gota` **gb3** — oom: no output, exit -9

</details>

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=parquet

| query | polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| gb1 | 93 | 494 | 39 | 34 | 33 | 347 | 121 | n/a | n/a | sum v1 by id1 (100 string groups) |
| gb2 | 467 | 811 | 131 | 94 | 122 | 767 | 382 | n/a | n/a | sum v1 by id1, id2 (10,000 string groups) |
| gb3 | 707 | 1,356 | 616 | 519 | 667 | 1,044 | 528 | n/a | n/a | sum v1, mean v3 by id3 (N/100 string groups) |
| gb4 | 105 | 585 | 106 | 107 | 88 | 349 | 161 | n/a | n/a | mean v1, v2, v3 by id4 (100 integer groups) |
| gb5 | 341 | 1,247 | 608 | 461 | 479 | 455 | TIMEOUT | n/a | n/a | sum v1, v2, v3 by id6 (N/100 integer groups) |
| gb6 | 449 | 821 | 468 | 513 | 406 | 864 | ERR | n/a | n/a | median v3, sd v3 by id4, id5 |
| gb7 | 767 | 1,156 | 445 | 285 | 388 | 864 | TIMEOUT | n/a | n/a | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 776 | 1,122 | 695 | 873 | 514 | 2,055 | 954 | n/a | n/a | largest two v3 by id6 (top-n within group) |
| gb9 | 547 | 945 | 205 | 175 | 200 | 824 | TIMEOUT | n/a | n/a | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 2,839 | 6,855 | 1,785 | 1,371 | 1,254 | 10,400 | 3,105 | n/a | n/a | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 521 | 1,960 | 994 | 566 | 715 | 1,737 | 1,655 | n/a | n/a | inner join large to small on integer |
| j2 | 569 | 2,087 | 1,246 | 620 | 846 | 2,042 | 2,289 | n/a | n/a | inner join large to medium on integer |
| j3 | 385 | 2,063 | 1,637 | 672 | 976 | 2,027 | 2,375 | n/a | n/a | left join large to medium on integer |
| j4 | 676 | 2,140 | 1,208 | 585 | 915 | 3,258 | 1,963 | n/a | n/a | join large to medium on varchar |
| j5 | 2,129 | 6,432 | 2,398 | 1,987 | 1,719 | 5,584 | 8,078 | n/a | n/a | join large to large on integer |
| **geomean** | **531** | **1,468** | **525** | **389** | **419** | **1,335** | **1,047** | — | — | |
| **vs polars** | 1.00x | 2.77x | 0.99x | 0.73x | 0.79x | 2.51x | 1.97x | — | — | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | 11/15 | 0/15 | 0/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go | pandas | chdb | gota | qframe |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 4.66 | 6.17 | 3.46 | 4.43 | 3.75 | 5.59 | 5.43 | — | — |

<details><summary>failures</summary>

- `chdb` **gb5** — timeout: exceeded 600s
- `chdb` **gb6** — error: RuntimeError: Code: 46. DB::Exception: Function with name `quantile_cont` does not exist. In scope SELECT id4, id5, quantile_cont(v3, 0.5) AS median_v3, stddev(v3) AS sd_v3 FROM g1 GROUP BY id4, id5 SETTINGS joined_subquery_requires_alias = 0, enable_analyzer = 1. Maybe you meant: ['quantileExact','
- `chdb` **gb7** — timeout: exceeded 600s
- `chdb` **gb9** — timeout: exceeded 600s

</details>

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 72 | 23 | 108 | 85 | 26 | n/a | 20 | 90 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 33 | 4 | 32 | 34 | 25 | n/a | 19 | 78 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 33 | 7 | 47 | 58 | 27 | n/a | 19 | 77 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 29 | 7 | 38 | 49 | 22 | n/a | 17 | 56 | · | Order priority checking: EXISTS -> semi join |
| q5 | 43 | 12 | 64 | 55 | 29 | n/a | 20 | 80 | · | Local supplier volume: 6-way join + groupby |
| q6 | 40 | 5 | 16 | 33 | 18 | 34 | 12 | 47 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 55 | 13 | 80 | 109 | 31 | n/a | 28 | 91 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 44 | 10 | 69 | 66 | 32 | n/a | 24 | 105 | · | National market share: 7-way join, conditional aggregate |
| q9 | 49 | 15 | 79 | 93 | 44 | n/a | 30 | 124 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 51 | 10 | 66 | 65 | 38 | n/a | 25 | 89 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 26 | 5 | 18 | 18 | 26 | n/a | 12 | 86 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 26 | 9 | 88 | 69 | 22 | n/a | 17 | 65 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 41 | 19 | 55 | 48 | 36 | n/a | 24 | 68 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 26 | 4 | 21 | 47 | 21 | n/a | 13 | 58 | · | Promotion effect: join + conditional sum ratio |
| q15 | 30 | 5 | 28 | 68 | 19 | n/a | 18 | ~~99~~ | · | Top supplier: aggregate view + max threshold + join |
| q16 | 37 | 4 | 21 | 16 | 23 | n/a | 12 | 60 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 49 | 7 | 28 | 58 | 23 | n/a | 20 | 80 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 47 | 21 | 70 | 69 | 34 | n/a | 48 | 90 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 52 | 9 | 114 | 82 | 25 | n/a | 19 | 69 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 30 | 8 | 48 | 56 | 26 | n/a | 16 | 93 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 47 | 32 | 155 | 174 | 46 | n/a | 42 | 137 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 27 | 4 | 13 | 15 | 22 | n/a | 14 | 60 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **39** | **9** | **46** | **53** | **27** | **34** | **20** | **78** | — | |
| **vs polars** | 4.45x | 1.00x | 5.33x | 6.13x | 3.12x | 3.90x | 2.30x | 9.03x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 22/22 | 21/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.26 | 0.36 | 0.50 | 0.18 | 0.22 | 0.05 | 0.46 | 0.62 | — |

## PDS-H (TPC-H, 22 queries) — 1 scale, io=parquet

| query | ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 898 | 237 | 1,046 | 137 | 146 | 286 | 131 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 184 | 15 | 76 | 46 | 42 | 132 | 53 | n/a | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 463 | 69 | 382 | 96 | 105 | 276 | 102 | n/a | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 547 | 65 | 330 | 69 | 62 | 159 | 64 | n/a | Order priority checking: EXISTS -> semi join |
| q5 | 529 | 138 | 621 | 102 | 165 | 291 | 99 | n/a | Local supplier volume: 6-way join + groupby |
| q6 | 410 | 50 | 142 | 58 | 64 | 110 | 54 | 346 | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 1,034 | 93 | 772 | 117 | 216 | 286 | 103 | n/a | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 594 | 152 | 489 | 132 | 136 | 514 | 128 | n/a | National market share: 7-way join, conditional aggregate |
| q9 | 1,014 | 196 | 722 | 228 | 199 | 618 | 211 | n/a | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 579 | 105 | 463 | 143 | 173 | 285 | 133 | n/a | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 129 | 25 | 61 | 34 | 31 | 110 | 32 | n/a | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 777 | 73 | 1,032 | 75 | 112 | 199 | 60 | n/a | Shipping modes and order priority: join + conditional aggregates |
| q13 | 532 | 148 | 492 | 167 | 127 | 241 | 156 | n/a | Customer distribution: left join + count + groupby of a groupby |
| q14 | 385 | 52 | 158 | 91 | 77 | 149 | 92 | n/a | Promotion effect: join + conditional sum ratio |
| q15 | 653 | 38 | 147 | 70 | ~~110~~ | ~~234~~ | 61 | n/a | Top supplier: aggregate view + max threshold + join |
| q16 | 137 | 34 | 131 | 54 | 54 | 91 | 53 | n/a | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 605 | 69 | 120 | 103 | 314 | 276 | 104 | n/a | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 932 | 261 | 662 | 177 | 522 | 264 | 160 | n/a | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 927 | 58 | 1,076 | 122 | 117 | 226 | 113 | n/a | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 634 | 148 | 382 | 88 | 136 | 210 | 90 | n/a | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 2,264 | 501 | 2,330 | 243 | 272 | 551 | 239 | n/a | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 176 | 23 | 42 | 49 | 37 | 98 | 49 | n/a | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **528** | **81** | **334** | **96** | **115** | **222** | **92** | **346** | |
| **vs polars** | 6.51x | 1.00x | 4.12x | 1.18x | 1.42x | 2.73x | 1.13x | 4.27x | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 21/22 | 21/22 | 22/22 | 1/22 | |

Peak resident memory across the suite (GB):

| ursus (Go) | polars | pandas | duckdb | datafusion | chdb | duckdb-go | arrow-go |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.97 | 0.92 | 1.86 | 0.44 | 1.47 | 1.56 | 0.41 | 0.10 |

