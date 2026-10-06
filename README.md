# ursus

A (experimental) dataframe library for Go 1.27, modelled on Polars — lazy execution with a query optimizer, Arrow memory layout, SIMD
kernels, and streaming execution that spills to disk rather than falling over. Sort, group-by, join, unique and
partitioned windows spill — by default once a query holds half the memory its container or machine allows — and an
operator that cannot spill fails with an error naming itself.

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

The projection reaches the Parquet reader, the filter becomes a row-group predicate, and the group-by runs on every
core. None of that is visible in the query.

---

## Is this for you?

**It is pure Go.** No cgo, no C++ toolchain, no Python runtime, no sidecar process. It cross-compiles and links into a
static binary like any other Go dependency, and `go get` is the whole install.

That is the reason to pick it, and the cost should be just as plain: **ursus is slower than the serious analytical
engines.** On the h2o.ai benchmark at ten million rows it is roughly 3x Polars; on TPC-H at scale factor 1 it is about
10x. CSV parsing is worse than that. It is not trying to beat Polars or DuckDB, and on current evidence it is not going
to.

**Where that trade is a good one**

- Data that fits comfortably in memory — up to tens of millions of rows, where the difference is half a second against a
  tenth of a second and nobody is waiting.
- A Go service that needs real dataframe work — joins, group-bys, window functions — and cannot take on cgo, a Python
  sidecar, or a large C++ dependency to get it.
- Anywhere the deployment story is worth more than the last multiple of speed.

**Where it is not**

- Interactive analytics over hundreds of millions of rows. Use DuckDB or Polars.
- Anywhere you can link cgo freely: `duckdb-go` is around 10x faster than this and is a binding to a mature engine.
- Anything production-critical today. This is v0.3, the API still moves, and nothing here is promised.

The honest summary is that Go has not had a dataframe library of this shape, and one that is correct and a few times
slower is more useful than none — for the sizes most services actually handle.

---

## Docs

The API reference is on **[pkg.go.dev](https://pkg.go.dev/github.com/advenn/ursus)**, generated from the source —
nothing to host and nothing to keep in sync.

Start with the [runnable examples](./example_test.go): filter, group-by, join, computed columns, whole-frame
aggregation, and reading typed values back out — and, new in 0.3, reading Parquet through any `io.ReaderAt`, a Go
UDF, lists with `Split` and `Explode`, `JoinWhere`, exact Decimal arithmetic, an Enum, a spilling `Unique`, and a List
column written to Parquet. They render beside the methods they document, and `go test` checks each
one's output, so an example that stops being true breaks the build rather than misleading someone quietly.

Beyond that, the doc comments are the documentation. They are unusually long on purpose: each explains why a thing is
the way it is, not only what it does.

---

## Install

```sh
go get github.com/advenn/ursus
```

**Go 1.27 or newer is required.** Two of its changes are load-bearing: generic methods are why `Series[T].Map[U]` and
`df.Column[T](name)` exist at all, and because a generic method still cannot satisfy an interface, every public type is
a concrete struct with polymorphism kept in unexported interfaces.

**`GOEXPERIMENT=simd` is optional.** It switches on the SIMD kernels:

```sh
GOEXPERIMENT=simd go build ./...
```

Without it every kernel falls back to its scalar twin, behind
`//go:build !(goexperiment.simd && amd64)`. That is not a claim — CI runs the entire suite with the experiment off on
every push, and `make test-all` includes an experiment-off leg locally. The flag buys speed, not correctness.

---

## Status

**v0.3** — what changed since v0.2 is in [`CHANGELOG.md`](./CHANGELOG.md). What works today:

|                 |                                                                                                                                                   |
|-----------------|---------------------------------------------------------------------------------------------------------------------------------------------------|
| **Sources**     | Parquet and CSV (read and write, List and Struct included for Parquet), from paths, memory, or any `io.ReaderAt` / stream you open (`ScanParquetFrom`, `ScanCSVFrom`); in-memory frames; Arrow (records and streams in; records out, zero-copy) |
| **Types**       | Bool, Int8–64, Uint8–64, Float32/64, String, Binary, Date, Time, Datetime (unit + zone), Duration, Decimal (128-bit; exact `+ - *` and `sum`, refused past 38 digits; `/` to the nearest Float64; casts to and from every numeric type exact or refused, except to a float, which rounds to the nearest), Enum (built from String, ordered by its categories) |
| **Expressions** | arithmetic, comparison, Kleene three-valued logic, conditionals, casts, null repair, `.str` (incl. `Split` → List) and `.dt` namespaces, 20 aggregates including `Implode`, window functions |
| **Frame ops**   | filter, select, with-columns, sort, top-k, distinct, concat/vstack/hstack, slice/tail/reverse/row-index, drop/rename/drop-nulls, unpivot                   |
| **Joins**       | all seven equi-join kinds with `Validate`, `JoinWhere` and `WhereExists`/`WhereNotExists` (non-equi), as-of join with tolerance and `by` keys, merge-sorted                          |
| **Grouping**    | group-by, `GroupByDynamic`, `Rolling`, calendar-aware intervals                                                                                   |
| **Optimizer**   | predicate pushdown (including through joins), projection pushdown, limit/top-k pushdown, cross-join collapse, constant folding and simplification |
| **Execution**   | order-preserving pipeline parallelism, parallel hash aggregation, and spilling for sort, hash aggregation, hash join, unique and partitioned windows (`WithMemoryLimit` names the rest, which fail rather than spill) |
| **UDFs**        | `MapElements` (per value) and `MapBatches` (per column) — generic methods, so the Go types are inferred from your function                        |

Nested types are partly there: **List and Struct read from Parquet and Arrow and write to Parquet**, with
`Explode`, `Unnest`, a `.list` namespace and `.struct.field()`. An Arrow Map arrives as a list of key/value structs.
Array is not there, and a List of Lists or of Structs is refused by name on both sides.

**Arrow** goes both ways, in pure Go. `df.Record()` and `lf.CollectRecords(ctx)` export without copying;
`ScanArrow` reads any `array.RecordReader` — an IPC stream, Flight, the C Data Interface — and `ScanArrowRecords` reads
records already in memory. Import copies, so nothing you release afterwards can reach a frame:

```go
lf := ursus.ScanArrow(func() (array.RecordReader, error) {
    return ipc.NewReader(bytes.NewReader(stream)) // a new reader every call
})
```

The function is called once to plan the query and once per scan, because a reader cannot be rewound. 38 of Arrow's 45
types map; Decimal256, intervals, unions and run-end encoding are refused by name.

**Object storage** has no client built in: each store wants its vendor's SDK, and ursus is pure Go with one dependency.
`ScanParquetFrom` reads Parquet through any `io.ReaderAt` instead, so a ReadAt over ranged GETs reaches S3, GCS or an
HTTP server — and ursus reads the footer and then only the column chunks the query needs, after statistics have pruned
the row groups it does not:

```go
lf := ursus.ScanParquetFrom([]ursus.ParquetFile{{
    Name: "s3://logs/2026/10/part-0.parquet",
    Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
        return openRanged(ctx, client, "logs", "2026/10/part-0.parquet") // your ReadAt over GetObject with a Range
    },
}})
```

`ScanCSVFrom` takes a stream the same way, and `WriteParquet` and `WriteCSV` write to any `io.Writer`, an upload
included. Built-in stores — `s3://` paths, listing for globs, retries — are planned for 0.4.

The escape hatch is real: a per-element UDF in Go is a function call, not a Python
interpreter round trip, which is the one place this library can beat Polars outright
rather than merely keep up.

```go
lf.Select(ursus.Col("celsius").MapElements("to_fahrenheit", ursus.Float64,
    func(c float64) (float64, error) { return c*9/5 + 32, nil }))
```

Nulls pass through untouched, so your function never receives a zero value it cannot
tell from a real one; use `MapBatches` when the null is the point. The name is
required, and `Explain` shows it. Your function is called from several goroutines at
once, so it must be safe for that.

Not done: built-in object stores (planned for 0.4; the seam above reaches one today), `MapGroups` and `RollingMap`, common subexpression elimination, SQL, `Pivot` — whose output columns are the distinct values of a
column, so its schema would depend on data and no plan node here does; `Unpivot` (melt) ships — and the long tail of
`Expr.Rolling*`, `Upsample`, `Interpolate`
and the trigonometric block.

Version numbers follow Go's own rule for v0: **nothing is promised.** The API is still moving, and the preamble above
says why.

---

## Correctness

```sh
make test-all   # four SIMD widths (512/256/128/0) plus the experiment off
make race       # the whole suite under -race
make levels     # import-level invariants
```

**2944 test cases**, and the matrix is not decoration. Vector width is a *runtime*
property, so a single-width run proves very little: 512-bit gives 8 float64 lanes, which happens to be exactly one
bitmap byte — a coincidence that hides an entire class of sub-byte bitmap bug. The 128-bit leg is where those surface.

Correctness is also checked against other engines. The benchmark suite validates every result against a duckdb
reference, and ursus currently passes **22/22 PDS-H (TPC-H) queries and 15/15 h2o.ai queries** — PDS-H at scale factor
0.1, re-run at every step, and both suites at the published sizes in the v0.3.0 report; every answer ursus gave there
validates.

---

## Benchmarks

Everything that measures ursus lives in [`bench/`](./bench/) — three tiers behind one Makefile, competing against
duckdb, polars, pandas, DataFusion, chDB, Arrow-Go, Gota and QFrame.

```sh
cd bench
make preflight    # refuses to run on a machine without the free memory; that is the feature
make setup
make bench        # gen -> reference answers -> run -> validate -> report
```

See [`bench/README.md`](./bench/README.md) for what is timed and why.

[`bench/results/REPORT.md`](./bench/results/REPORT.md) is checked in. Every result in it is validated against a duckdb
reference, and a disagreement is struck through rather than quietly reported as a fast number.

**How current it is, precisely.** Every table was measured in one session, on one commit, with every engine re-run
together — so the numbers are comparable across engines rather than stitched from different days. That commit is
v0.3.0's, measured on 2026-10-06.

The caveats that apply to *this* run:

- **Every query ran in an 8 GB cgroup**, on a laptop with 6–9 GiB of swap already in use.
- **ursus's h2o `gb10` over CSV was killed at that cap.** It needs about 9 GB, and v0.3.0 spilled only under an explicit
  `WithMemoryLimit`, so it never saw the cgroup. A default budget follows in the next release.
- **PDS-H `q7` regressed** to 3.9 s and 1.68 GB, from 2.4 s and 0.60 GB at step 40. Its date filter stopped being pushed
  below its joins. The fix also follows.
- **Other engines:** chDB timed out on two group-bys and gota ran out of memory on two, under the same cap.

Timings come from a laptop under real conditions, so treat small differences as noise and the ordering as the signal.

**Where it stands, in one place.** Against Polars, on the current report:

|                        |    ursus |  Polars |       |
|------------------------|---------:|--------:|-------|
| h2o.ai, 10M rows       | 2,426 ms |  731 ms | 3.3x  |
| TPC-H SF=1             |   918 ms |   85 ms | 10.8x |
| TPC-H SF=0.1           |    82 ms |   11 ms | 7.5x  |
| TPC-H SF=1 peak memory |  1.68 GB | 0.86 GB | 2.0x  |

Geomeans over queries every engine passed. The gap narrows as the data gets smaller, which is the shape of the trade
described at the top of this file.

Where ursus is behind and why is recorded rather than glossed — every step of construction has an as-built document in [
`context_files/`](./context_files/), including the optimisations that were tried and reverted for being slower.

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

Packages are arranged in strict import levels — `internal/gen/levels` fails the build if a package imports one at or
above its own level, which is what keeps the dependency graph a DAG rather than a suggestion.

### Design notes

[`context_files/`](./context_files/) holds the design documents and an as-built record for every step of construction,
each written to be authoritative over the ones before it. They are unusually candid: they record the defects found, the
measurements taken, the tests that turned out to be vacuous, and the claims that did not survive contact with the code.
Start with the newest.

---

## Licence

[MIT](./LICENSE).
