# preamble from human author

this project is not production ready, and currently it is noticeably slower than other similar tools, like duckdb go or polars py.


# ursus

A Polars-class dataframe library for Go 1.27 — lazy execution with a query
optimizer, Arrow memory layout, SIMD kernels, and streaming execution that spills
to disk rather than falling over.

```go
df, err := ursus.ScanParquet("events.parquet").
    Filter(ursus.Col("price").Gt(5)).
    GroupBy(ursus.Col("region")).
    Agg(
        ursus.Col("price").Sum().Alias("revenue"),
        ursus.Col("qty").Mean().Alias("avg_qty"),
    ).
    Sort(ursus.Desc(ursus.Col("revenue"))).
    Collect(ctx)
```

The projection reaches the Parquet reader, the filter becomes a row-group
predicate, and the group-by runs on every core. None of that is visible in the
query.

---

## Install

```sh
go get github.com/advenn/ursus
```

**Go 1.27 or newer is required.** Two of its changes are load-bearing: generic
methods are why `Series[T].Map[U]` and `df.Column[T](name)` exist at all, and
because a generic method still cannot satisfy an interface, every public type is
a concrete struct with polymorphism kept in unexported interfaces.

**`GOEXPERIMENT=simd` is optional.** It switches on the SIMD kernels:

```sh
GOEXPERIMENT=simd go build ./...
```

Without it every kernel falls back to its scalar twin, behind
`//go:build !(goexperiment.simd && amd64)`. That is not a claim — CI runs the
entire suite with the experiment off on every push, and `make test-all` includes
an experiment-off leg locally. The flag buys speed, not correctness.

---

## Status

**v0.2 is complete.** What works today:

| | |
| --- | --- |
| **Sources** | Parquet and CSV (read and write), in-memory frames |
| **Types** | Bool, Int8–64, Uint8–64, Float32/64, String, Binary, Date, Time, Datetime (unit + zone), Duration, Decimal (128-bit), Enum |
| **Expressions** | arithmetic, comparison, Kleene three-valued logic, conditionals, casts, null repair, `.str` and `.dt` namespaces, 19 aggregates, window functions |
| **Frame ops** | filter, select, with-columns, sort, top-k, distinct, concat/vstack/hstack, slice/tail/reverse/row-index, drop/rename/drop-nulls |
| **Joins** | all seven equi-join kinds with `Validate`, as-of join with tolerance and `by` keys, merge-sorted |
| **Grouping** | group-by, `GroupByDynamic`, `Rolling`, calendar-aware intervals |
| **Optimizer** | predicate pushdown (including through joins), projection pushdown, limit/top-k pushdown, constant folding and expression simplification |
| **Execution** | order-preserving pipeline parallelism, parallel hash aggregation, and spilling for sort, hash aggregation and hash join |

Not done: `JoinWhere` (non-equi join), common subexpression elimination, nested
types (List/Struct/Map), and the long tail of `Expr.Rolling*`, `Upsample`,
`Interpolate` and the trigonometric block.

Version numbers follow Go's own rule for v0: **nothing is promised.** The API is
still moving, and the preamble above says why.

---

## Correctness

```sh
make test-all   # four SIMD widths (512/256/128/0) plus the experiment off
make race       # the whole suite under -race
make levels     # import-level invariants
```

**1386 test cases**, and the matrix is not decoration. Vector width is a *runtime*
property, so a single-width run proves very little: 512-bit gives 8 float64 lanes,
which happens to be exactly one bitmap byte — a coincidence that hides an entire
class of sub-byte bitmap bug. The 128-bit leg is where those surface.

Correctness is also checked against other engines. The benchmark suite validates
every result against a duckdb reference, and ursus currently passes **22/22
PDS-H (TPC-H) queries and 15/15 h2o.ai queries**.

---

## Benchmarks

Everything that measures ursus lives in [`bench/`](./bench/) — three tiers behind
one Makefile, competing against duckdb, polars, pandas, DataFusion, chDB,
Arrow-Go, Gota and QFrame.

```sh
cd bench
make preflight    # refuses to run on a machine without the free memory; that is the feature
make setup
make bench        # gen -> reference answers -> run -> validate -> report
```

See [`bench/README.md`](./bench/README.md) for what is timed and why.

[`bench/results/REPORT.md`](./bench/results/REPORT.md) is checked in. Every result
in it is validated against a duckdb reference, and a disagreement is struck
through rather than quietly reported as a fast number.

**How current it is, precisely.** Every table was measured in one session, on one
commit, with every engine re-run together — so the numbers are comparable across
engines rather than stitched from different days.

The caveat that applies to *this* run: the machine had 9 GiB of swap in use, and
three PDS-H queries (`q1`, `q2`, `q6`) show iteration spreads of up to 2.6x where
they were previously tight. Their medians are inflated by contention — the minimum
of iterations puts them within 7–16% of their old values rather than 29–74% worse.
The published table keeps the median anyway, because switching estimator after
seeing which one flatters you is how a benchmark stops being one.

Timings come from a laptop under real conditions, so treat small differences as
noise and the ordering as the signal.

ursus is not as fast as polars or duckdb. Where it is behind and why is recorded
rather than glossed — see [`context_files/`](./context_files/).

---

## How it is built

```
dtype/          types, schemas, the null contract
i128/           128-bit integers, for Decimal
internal/
  data/         Column and Batch — Arrow layout, three payload shapes
  bitmap/       validity and boolean bitmaps, offset-correct by construction
  kernel/       the compute kernels, SIMD with scalar twins
  expr/         the expression IR
  plan/         logical plan, resolution, the optimizer rules
  physical/     operators, the sink/breaker protocol, spilling
  exec/         the driver
  source/       parquet, csv, memory
ursustest/      assertions for testing code that uses ursus
```

Packages are arranged in strict import levels — `internal/gen/levels` fails the
build if a package imports one at or above its own level, which is what keeps the
dependency graph a DAG rather than a suggestion.

### Design notes

[`context_files/`](./context_files/) holds the design documents and an as-built
record for every step of construction, each written to be authoritative over the
ones before it. They are unusually candid: they record the defects found, the
measurements taken, the tests that turned out to be vacuous, and the claims that
did not survive contact with the code. Start with the newest.

---

## Licence

[MIT](./LICENSE).
