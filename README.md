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

## Requirements

**Go 1.27 or newer, and `GOEXPERIMENT=simd` is mandatory** — package `simd` does
not compile without it, so neither does ursus.

```sh
export GOEXPERIMENT=simd
go build ./...
```

Every SIMD kernel has a scalar twin behind `//go:build !goexperiment.simd`, and CI
proves it by running the whole suite with the experiment off. But the *default*
build needs the flag.

Two of Go 1.27's changes are load-bearing here. Generic methods are why
`Series[T].Map[U]` and `df.Column[T](name)` exist at all; and because a generic
method still cannot satisfy an interface, every public type is a concrete struct
with polymorphism kept in unexported interfaces.

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

### A note on using it

The module path is `ursus`, not `github.com/advenn/ursus`, so `go get` will not
work against this repository as-is. It is developed as a self-contained module;
publishing it as an importable package is a deliberate step that has not been
taken yet.

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

**On the published numbers:** `bench/results/REPORT.md` is generated from the last
full harness run and currently predates the parallel-aggregation, sort and
scan work, so it understates the engine. Treat it as a floor, and regenerate with
`make bench` on a quiet machine for a current picture. ursus is not yet as fast as
polars or duckdb; where it is behind and why is recorded rather than glossed —
see [`context_files/`](./context_files/).

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

Not yet chosen. Until one is added, no permission to use, copy or distribute is
granted — see the note in the repository's issues if you need one.
