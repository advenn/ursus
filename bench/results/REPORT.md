# ursus benchmark results

Produced from `8bb1ad1` — Rewrite min/max to flat typed storage; publish the benchmark report — on 2026-09-04.

> **These timings predate four commits and understate the engine by roughly
> 2.3x.** `7aab95b` rewrote the CSV reader's string path, `9afc578` replaced the
> group-key hash table in six operators, and `921fc38` rewrote the radix sort's
> inner loop. A one-iteration ursus-only re-run of h2o at `921fc38` puts the
> geomean at 2,801 ms against the 6,470 ms below — and gb8 at 5,646 ms against
> 27,973 ms, a 4.95x change on the suite's worst query. Those are single
> iterations on a loaded machine, so treat them as indicative; the tables below
> are the last coherent multi-engine measurement and are left as they were.
>
> The ANSWERS are current as of `921fc38`: 22/22 against the duckdb reference on
> PDS-H at SF=0.1 and SF=1, and 15/15 on h2o in both parquet and CSV.
> Refreshing the timings needs a full multi-engine run.

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

| query | gota | qframe | what it exercises |
|---|--:|--:|---|
| gb1 | 30,429 | 7,535 | sum v1 by id1 (large-cardinality string groups) |
| gb2 | 37,237 | 8,365 | sum v1 by id1, id2 (medium-cardinality string groups) |
| gb3 | 37,633 | 10,794 | sum v1, mean v3 by id3 (small-cardinality string groups) |
| gb4 | 31,238 | 6,451 | mean v1, v2, v3 by id4 (large-cardinality integer groups) |
| gb5 | 39,280 | 8,069 | sum v1, v2, v3 by id6 (small-cardinality integer groups) |
| gb6 | 52,895 | n/a | median v3, sd v3 by id4, id5 |
| **geomean** | **37,470** | **8,125** | |
| **queries passed** | 6/6 | 5/6 | |

Peak resident memory across the suite (GB):

| gota | qframe |
|--:|--:|
| 9.54 | 3.37 |

## h2o.ai db-benchmark (groupby + join) — 1e+07 rows, io=parquet

| query | polars | ursus (Go) | duckdb | datafusion | duckdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|---|
| gb1 | 160 | 1,885 | 86 | 216 | 136 | sum v1 by id1 (large-cardinality string groups) |
| gb2 | 951 | 3,281 | 260 | 453 | 605 | sum v1 by id1, id2 (medium-cardinality string groups) |
| gb3 | 1,435 | 6,739 | 1,825 | 1,096 | 1,182 | sum v1, mean v3 by id3 (small-cardinality string groups) |
| gb4 | 178 | 1,696 | 254 | 345 | 208 | mean v1, v2, v3 by id4 (large-cardinality integer groups) |
| gb5 | 836 | 5,082 | 1,596 | 965 | 1,066 | sum v1, v2, v3 by id6 (small-cardinality integer groups) |
| gb6 | 859 | 3,248 | 1,058 | 1,235 | 1,059 | median v3, sd v3 by id4, id5 |
| gb7 | 1,222 | 2,360 | 1,097 | 755 | 1,172 | max v1 - min v2 by id3 (range over small groups) |
| gb8 | 1,371 | 27,973 | 1,561 | 2,054 | 1,356 | largest two v3 by id6 (top-n within group) |
| gb9 | 1,077 | 4,432 | 509 | 309 | 658 | regression: sum(v1*v2)/... by id2, id4 |
| gb10 | 5,812 | 23,405 | 4,138 | 3,033 | 3,428 | sum v3, count by id1..id6 (six-key grouping) |
| j1 | 1,144 | 6,842 | 3,707 | 995 | 2,055 | inner join large to small on integer |
| j2 | 1,042 | 8,253 | 2,619 | 821 | 2,167 | inner join large to medium on integer |
| j3 | 692 | 10,477 | 9,938 | 1,095 | 1,502 | left join large to medium on integer |
| j4 | 1,007 | 8,503 | 6,096 | 1,552 | 1,194 | join large to medium on varchar |
| j5 | 4,607 | 36,235 | 9,128 | 5,141 | 3,044 | join large to large on integer |
| **geomean** | **1,007** | **6,470** | **1,475** | **949** | **1,047** | |
| **vs polars** | 1.00x | 6.42x | 1.46x | 0.94x | 1.04x | |
| **queries passed** | 15/15 | 15/15 | 15/15 | 15/15 | 15/15 | |

Peak resident memory across the suite (GB):

| polars | ursus (Go) | duckdb | datafusion | duckdb-go |
|--:|--:|--:|--:|--:|
| 3.48 | 5.35 | 2.32 | 2.96 | 2.45 |

## PDS-H (TPC-H, 22 queries) — 0.1 scale, io=parquet

| query | duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go | what it exercises |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| q1 | 176 | 35 | 237 | 158 | 55 | n/a | 40 | 141 | n/a | Pricing summary: filter + 2-key groupby with 8 aggregates + sort |
| q2 | 72 | 6 | 44 | 48 | 52 | n/a | 51 | 135 | · | Minimum cost supplier: correlated min() rewritten as groupby + join |
| q3 | 55 | 18 | 101 | 116 | 48 | n/a | 43 | 143 | · | Shipping priority: 3-way join, filter, groupby, top-10 |
| q4 | 62 | 14 | 61 | 123 | 41 | n/a | 26 | 121 | · | Order priority checking: EXISTS -> semi join |
| q5 | 56 | 26 | 135 | 245 | 62 | n/a | 34 | 157 | · | Local supplier volume: 6-way join + groupby |
| q6 | 32 | 7 | 32 | 100 | 34 | 56 | 33 | 115 | · | Forecasting revenue change: single-table filter + sum (scan-bound) |
| q7 | 48 | 24 | 124 | 282 | 71 | n/a | 57 | 172 | · | Volume shipping: 5-way join, date extraction, 3-key groupby |
| q8 | 54 | 18 | 131 | 237 | 53 | n/a | 81 | 274 | · | National market share: 7-way join, conditional aggregate |
| q9 | 58 | 43 | 143 | 366 | 83 | n/a | 84 | 196 | · | Product type profit measure: 6-way join, substring match, groupby |
| q10 | 90 | 34 | 125 | 150 | 61 | n/a | 49 | 117 | · | Returned item reporting: 4-way join, groupby, top-20 |
| q11 | 36 | 10 | 43 | 47 | 42 | n/a | 50 | 117 | · | Important stock identification: scalar-subquery threshold via two collects |
| q12 | 41 | 38 | 283 | 151 | 52 | n/a | 61 | 93 | · | Shipping modes and order priority: join + conditional aggregates |
| q13 | 77 | 49 | 194 | 98 | 77 | n/a | 99 | 105 | · | Customer distribution: left join + count + groupby of a groupby |
| q14 | 45 | 12 | 39 | 93 | 40 | n/a | 29 | 111 | · | Promotion effect: join + conditional sum ratio |
| q15 | 42 | 12 | 40 | 150 | 35 | n/a | 35 | 207 | · | Top supplier: aggregate view + max threshold + join |
| q16 | 36 | 40 | 48 | 38 | 42 | n/a | 22 | 135 | · | Parts/supplier relationship: anti join + n_unique groupby |
| q17 | 59 | 21 | 35 | 157 | 42 | n/a | 49 | 144 | · | Small-quantity-order revenue: correlated avg -> groupby + join + filter |
| q18 | 67 | 32 | 169 | 276 | 46 | n/a | 110 | 143 | · | Large volume customer: having-subquery -> semi join, top-100 |
| q19 | 46 | 17 | 269 | 215 | 38 | n/a | 44 | 99 | · | Discounted revenue: equi join on partkey then an OR-of-conjunctions filter |
| q20 | 165 | 20 | 103 | 117 | 50 | n/a | 41 | 150 | · | Potential part promotion: nested correlated subqueries -> groupby + semi joins |
| q21 | 470 | 109 | 473 | 595 | 86 | n/a | 68 | 265 | · | Suppliers who kept orders waiting: self semi join + self anti join |
| q22 | 196 | 11 | 26 | 28 | 32 | n/a | 29 | 140 | · | Global sales opportunity: phone prefix substring + scalar avg + anti join |
| **geomean** | **68** | **21** | **94** | **134** | **50** | **56** | **47** | **143** | — | |
| **vs polars** | 3.19x | 1.00x | 4.41x | 6.27x | 2.33x | 2.61x | 2.20x | 6.68x | — | |
| **queries passed** | 22/22 | 22/22 | 22/22 | 22/22 | 22/22 | 1/22 | 22/22 | 22/22 | 0/22 | |

Peak resident memory across the suite (GB):

| duckdb | polars | pandas | ursus (Go) | duckdb-go | arrow-go | datafusion | chdb | chdb-go |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.15 | 0.25 | 0.42 | 0.52 | 0.11 | 0.05 | 0.41 | 0.56 | — |

