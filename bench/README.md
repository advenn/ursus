# ursus benchmark suite

Everything that measures ursus lives here. Three tiers, one Makefile.

| tier | what it answers | who competes |
|---|---|---|
| `micro` | where does ursus spend time, and did that change? | ursus only, `benchstat` format |
| `h2o` | how does ursus compare on group-by and join? | ursus, polars, pandas, duckdb, datafusion, chdb, duckdb-go, gota, qframe |
| `pdsh` | how does ursus compare on real analytical queries? | the above, plus arrow-go as a floor |

```sh
make preflight          # will refuse to run on a busy machine — that is the feature
make setup              # uv venv + build the Go runners
make bench              # gen -> reference answers -> run -> validate -> report
make bench SF=10        # PDS-H at scale factor 10
make bench SUITE=h2o    # the group-by/join suite at N=1e7
make micro              # ursus-only microbenchmarks
```

Results land in `results/`: `timings.csv` (every iteration), `REPORT.md`,
`dashboard.html`, `plots/`, `summary.json`.

---

## What is measured

**The timed region is scan → materialise, IO included.** For a lazy engine that
means building the plan, reading the file and collecting the result; for an
eager one it means reading the file and everything after. Process startup and
library import are measured separately and reported as `startup_s`, never folded
in — otherwise a Go binary would win on Python's import time rather than on
anything about the query.

**One un-timed warm-up, then N timed iterations** (default 3), median reported.
The page cache is warm for all of them, deliberately and equally. Median rather
than minimum: the minimum rewards whichever engine got the quietest slice of the
machine.

**The query is rebuilt from scratch every iteration.** No plan, frame, file
handle or connection survives between them.

**Writing the answer for validation is outside the timed region.**

**Peak memory is `VmHWM` from `/proc/self/status`**, read inside the engine
process. Go and Python report the same number from the same place.

**Thread count is pinned identically** for every engine — `POLARS_MAX_THREADS`,
`RAYON_NUM_THREADS`, `OMP_NUM_THREADS`, `GOMAXPROCS`, duckdb's `SET threads`.
Left alone, each runtime picks a different default and the benchmark becomes a
comparison of default thread policies.

**Every run is wrapped in a `systemd-run --user --scope`** with `MemoryMax` and
`MemorySwapMax=0` when `MEM_LIMIT` is set. Swapping is not graceful degradation
for a benchmark; it is a silent 100x. An engine that exceeds the budget is
killed and recorded as `oom`, which is a real result.

### Correctness is a gate

duckdb produces the reference answer for every query, through the same runner a
timed duckdb run uses. Every other engine's answer is compared against it: same
shape, same column names, values within `rtol=1e-6` after both sides are sorted
canonically. A mismatch is reported as a wrong answer and its timing is struck
through in the report. A fast wrong answer is not a benchmark result.

Two representation differences are normalised, because they are carriers rather
than values: pandas has no date type so a `DATE` column arrives as a midnight
timestamp, and ursus widens an integer `Sum` to `Int128`, which Parquet can only
carry as `DECIMAL(38, 0)`.

PDS-H answers are compared in full. h2o answers are not — a join at N=1e7
produces tens of millions of rows — so those are compared on row count plus the
sum of a fixed column list, which is the check the h2o benchmark itself applies.
The column list is pinned in `config/suites.toml` rather than derived from each
engine's output, so a difference in which non-key columns an engine carries
through a join cannot become a false mismatch.

---

## The suites

### `pdsh` — PDS-H, the 22 TPC-H queries

Data comes from [`tpchgen-cli`](https://github.com/clflushopt/tpchgen-rs), a pure
Rust dbgen that produces SF10 in seconds. A duckdb pass then normalises it:
money columns `DECIMAL(15,2)` → `DOUBLE`, dates left as `date32`. The cast is
deliberate. ursus's `numericTypes` omits `Decimal`, so the `GroupBy.Sum()`
shorthand skips those columns and decimal arithmetic is unproven; the Python
engines each make a different silent choice about how to represent decimals.
Casting up front means every engine reads byte-identical files and the timings
compare arithmetic rather than type-conversion policy. It is also what
polars-benchmark does.

The SQL is dumped from duckdb's `tpch` extension, which reproduces the
specification text at the validation substitution parameters (`make sql`
regenerates it). One cosmetic patch: q18's unnamed `sum(l_quantity)` is aliased
`col6`, because every answer is matched by column name and asking the dataframe
ports to produce a column called `sum(l_quantity)` would be absurd.

The dataframe ports turn subqueries into joins, which is the only vocabulary a
dataframe API has: `EXISTS` → semi join, `NOT IN` → anti join, a correlated
aggregate → group-by plus equi join, a scalar threshold → a one-row frame cross
joined in so the query stays a single lazy plan. Two rewrites are worth naming:

- **q19** shares `p_partkey = l_partkey` across all three disjuncts, so it is an
  equi join followed by a filter. ursus has no `JoinWhere` (non-equi join) and
  would not benefit from one here.
- **q21** replaces `EXISTS(another supplier on this order)` and
  `NOT EXISTS(another late supplier)` with two distinct-count group-bys. Written
  literally, both need `l2.l_suppkey <> l1.l_suppkey`, which no dataframe API
  can push into a join.

**Scale.** SF1 (≈1 GB, 6M lineitem rows) is the default; `SF=10` is opt-in.
pandas is capped at SF1 in `config/engines.toml` — at SF10 the tables do not fit
on a 16 GB machine however the query is written, and letting it try produces an
OOM rather than a datapoint.

Results here are not comparable to published TPC-H results: the data generator
and the execution harness are both modified, exactly as
[polars-benchmark](https://github.com/pola-rs/polars-benchmark) says of PDS-H.
That project is the prior art for this tier; the queries here were written from
the specification SQL rather than copied from it.

### `h2o` — the db-benchmark group-by and join suite

Ten group-bys and five joins, from
[h2oai/db-benchmark](https://github.com/h2oai/db-benchmark) (now maintained by
DuckDB). The generator is a numpy port of `_data/groupby-datagen.R` and
`_data/join-datagen.R`: the same cardinalities, the same value ranges, the same
0.9 / 0.1 / 0.1 key split that gives the joins their 90% match rate. The
*values* differ — R's Mersenne Twister is not reproducible from numpy — so our
absolute numbers are not comparable to the published h2o leaderboard. Every
engine reads the same generated files, which is what matters for the comparison
here.

Default N is 1e7 (~0.5 GB CSV, ~200 MB Parquet). `make gen-h2o N=1e8` for the
larger set; the join generator requires N ≥ 1e7.

### `micro` — ursus against itself

A separate Go module that reaches ursus through a `replace` directive, so it can
only use the public API. It fills the gaps the root `bench_test.go` leaves:
CSV and Parquet **reading** (not measured anywhere else in the repo), the
`WithBatchSize` sweep, `CollectBatches` streaming, window functions, string
kernels, the as-of join, wide-schema projection pushdown, and spill ratios via
`WithMemoryLimit` + `WithMemoryStats`.

```sh
make micro                 # -benchmem -count=10 -> results/micro.txt
make micro-baseline        # record it as the comparison point
make micro-diff            # benchstat current vs baseline
```

The fixture uses a seeded PRNG with a squared-uniform key distribution rather
than `index % constant`. Modular data has no skew, no repeated hash collisions
and perfectly uniform group sizes, which flatters a hash table.

---

## Engines

Registered in `config/engines.toml`; `enabled = false` keeps one out of the
default run without removing it. Anything an engine genuinely cannot express is
recorded as `unsupported` with the reason — a real fact about that library, not
a gap in this suite.

| engine | notes |
|---|---|
| **ursus** | the subject. Public API only — `internal/...` is not importable from another module, replace or no replace, so the runners exercise what a user would |
| polars | lazy API + `collect()`, the model ursus is built against |
| pandas | fully eager; capped at SF1 and N=1e7 |
| duckdb | also produces the reference answers |
| datafusion | `uv sync --group datafusion` |
| chdb | `uv sync --group chdb`. ClickHouse does not accept the standard TPC-H text: inputs are registered as session views and two settings are appended (`joined_subquery_requires_alias`, `enable_analyzer`), plus `extract(year FROM x)` → `toYear(x)`. All mechanical, all listed in `sql_runner.py` |
| duckdb-go | the honest ceiling for a Go program. Same SQL as the Python duckdb, so the gap between those two rows is the cost of the binding. Needs `make setup-cgo` |
| chdb-go | ClickHouse embedded in Go. Needs libchdb.so plus `make setup-cgo`; off by default |
| arrow-go | **not a competitor.** No aggregate, hash-aggregate or join kernels exist, so only PDS-H q6 is expressible. ursus is built on arrow-go, so the q6 gap between them is what ursus's plan, evaluator and pipeline cost over the layer they sit on, with the Parquet reader and memory format held constant |
| gota | pure Go, eager, **CSV only** — no Parquet reader exists. Six basic group-bys; no expression language, so no `max(v1) - min(v2)`, no window, no PDS-H. Needs `IO=csv` |
| qframe | pure Go, eager, CSV only, faster than gota. `gb1`–`gb5`; its built-in aggregations are sum/max/min, plus avg for float columns only, which is why `gb4` parses two integer columns as floats |

---

## Layout

```
Makefile              the entry point
config/               engine registry, suite definitions, checksum columns
driver/               preflight, cgroup wrapper, runner, validation, report
driver/gen/           dataset generation (tpchgen-cli + duckdb; numpy for h2o)
engines/sql/          one SQL text per query, shared by every SQL engine
engines/py/           polars, pandas, duckdb, datafusion, chdb runners
engines/go/           one module: ursus, arrow-go, gota, qframe (+ cgo behind tags)
engines/go/cmd/repro/ a standalone reproducer for a bug this suite found
micro/                the ursus-only Go microbenchmarks
answers/  data/  results/    generated, gitignored
```

The Go engines are one module with build tags, so the pure-Go set builds with no
cgo and `-tags duckdb,chdb` pulls the heavy engines only when asked. It is a
separate module from ursus so that duckdb-go and chdb-go never enter the root
`go.mod`.

`GOEXPERIMENT=simd` is exported by the Makefile and is mandatory: package `simd`
does not compile without it.

## Adding a query or an engine

A query is a function in `engines/py/queries/<engine>_<suite>.py` or a method in
`engines/go/ursusengine/<suite>.go`, plus a `.sql` file if the SQL engines should
run it. Anything missing from a registry reports `unsupported`, so a
half-finished port never breaks a run.

An engine is an entry in `config/engines.toml` plus a runner that honours the
contract in `engines/py/_common.py` (Python) or `engines/go/engine` (Go): the
same flags in, one JSON object out.
