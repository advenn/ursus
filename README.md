# ursus

[![Go Reference](https://pkg.go.dev/badge/github.com/advenn/ursus.svg)](https://pkg.go.dev/github.com/advenn/ursus)
[![CI](https://github.com/advenn/ursus/actions/workflows/ci.yml/badge.svg)](https://github.com/advenn/ursus/actions/workflows/ci.yml)
[![Docs](https://img.shields.io/badge/docs-advenn.github.io%2Fursus-1f2d3d)](https://advenn.github.io/ursus/)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

**ursus is a dataframe library for Go (Golang), modelled on Polars** — lazy execution with a query optimizer, Apache
Arrow memory, SIMD kernels, and streaming execution that spills to disk rather than falling over. Pure Go, no cgo;
experimental, at 0.6.

**Documentation: [advenn.github.io/ursus](https://advenn.github.io/ursus/)** — a quick start, the concepts, a guide for
Polars users, versioned by release, with every example run by the test suite.

Sort, group-by, join, unique, every
window function and the temporal groups spill or stream — by default once a process's queries together hold half the
memory its container or machine allows, on Linux, macOS and Windows, a budget they share (an `Engine`'s) — and an
operator that cannot spill fails with an error naming itself. Under that default budget ursus also sets the Go runtime's
soft memory limit near the same ceiling, so the heap's own slack stays under it too; `GOMEMLIMIT`, or
`ursus.LeaveProcessMemoryLimit()`, keeps a program's own choice.

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
engines.** On the h2o.ai benchmark at ten million rows it is about 1.3x Polars over Parquet and 1.6x over CSV; on
TPC-H at scale factor 1 it is about 2x. It is not trying to beat Polars or DuckDB, and on current evidence it is not going
to.

**Where that trade is a good one**

- Data where the speed difference is small in absolute terms — up to tens of millions of rows, where it is half a
  second against a tenth of one and nobody is waiting. Larger data still runs, spilling to disk past the memory limit,
  but there the same ratio is seconds or minutes, and a spilled query pays for its disk too.
- A Go service that needs real dataframe work — joins, group-bys, window functions — and cannot take on cgo, a Python
  sidecar, or a large C++ dependency to get it.
- Anywhere the deployment story is worth more than the last multiple of speed.

**Where it is not**

- Interactive analytics over hundreds of millions of rows. Use DuckDB or Polars.
- Anywhere you can link cgo freely: `duckdb-go` is around 2x faster than this on TPC-H and is a binding to a mature
  engine.
- Anything production-critical today. This is v0.6, the API still moves, and nothing here is promised.

The honest summary is that Go has not had a dataframe library of this shape, and one that is correct and a few times
slower is more useful than none — for the sizes most services actually handle.

---

## Docs

The guide is at **[advenn.github.io/ursus](https://advenn.github.io/ursus/)**: a quick start, the concepts, a page for
Polars users, and benchmarks, versioned by release. Every code block on it is a Go example in
[`docs/examples`](./docs/examples/) that `go test` runs, its output the one shown.

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

**v0.6** — what changed since v0.5 is in [`CHANGELOG.md`](./CHANGELOG.md), and what 0.6 set out to do,
and did, in [`v0.6-scope.md`](./context_files/v0.6-scope.md). What works today:

|                 |                                                                                                                                                   |
|-----------------|---------------------------------------------------------------------------------------------------------------------------------------------------|
| **Sources**     | Parquet, CSV (gzip, zlib or Zstandard too), JSON, NDJSON and Arrow IPC, read and written, from paths, memory, or any `io.ReaderAt` / stream you open; directories, `**` globs and hive partitions, read and written, with Parquet schema evolution; databases read and written through `database/sql`; in-memory frames and Go values (`FromRows`, `Rows`, `ToMaps`); Arrow (records and streams in; records out, zero-copy) |
| **Types**       | Bool, Int8–64, Uint8–64, Float32/64, String and Binary (past 2 GiB too), Date, Time, Datetime (unit + zone), Duration, Decimal (128-bit; exact `+ - *` and `sum`, refused past 38 digits; `/` to the nearest Float64; casts to and from every numeric type exact or refused, except to a float, which rounds to the nearest), Enum (built from String, ordered by its categories), Categorical, List, Array (fixed width, the `Arr()` namespace) and Struct, each of the last three a key too |
| **Expressions** | Polars' contexts: an aggregate is one value in `Select`, `WithColumns` and `Filter`; `Expr.Filter` is an aggregate's input; `Sort`, `SortBy`, `Head`, `Tail`, `Slice`, `Reverse`, `Unique`, `DropNulls`, `DropNans`, `Gather`, `GatherEvery`, `Get`, `Explode`, `ArgSort`, `Mode`, `ValueCounts`, `Rle`, `Append`, `SampleN` and `Hist` are a list a group in an aggregation and a column of their own length in `Select` and `WithColumns`; `Unnest` and `Struct().Field` of several names are a column a field; arguments take expressions, and a Go slice is a List literal. Generators (`IntRange`, `LinearSpace`, `Repeat`, the date, datetime and time ranges, `MakeDate` and its kin), folds (`Fold`, `Reduce`, `CumFold`, `ConcatList`, `Format`), selection by position (`Nth`, `ArgWhere`), and `List().Eval` over `Element()`. Then arithmetic, comparison, Kleene three-valued logic and the bits of integers, conditionals, casts, null repair, value maps (`Replace`, `ReplaceStrict`, `ReplaceOr`), rounding with a mode, `RoundSigFigs` and `Truncate`, trigonometric and hyperbolic functions, bit counts, `Hash`, `SearchSorted` and `IndexOf`, `ConcatStr`, `Struct`, `Max`/`Min`/`Sum`/`Mean`/`Any`/`AllHorizontal`, `Cut`/`QCut`, the `.str` (incl. `Split`, `SplitExact`, `ContainsAny` and `ReplaceMany` over many patterns, `Normalize`, `Encode`, `JsonDecode` and `Strptime` with strftime formats), `.dt` (incl. `Date`, `Time`, `Strftime`, `OffsetBy`, `Round`, `ConvertTimeZone`, `Replace`, `IsBusinessDay`, `AddBusinessDays` and the fractional totals), `.list` (incl. `Join`, `NUnique`, `ArgMin`/`ArgMax`, the set operations, `Gather`, `ToStruct`, `Shift`, `Diff`, `SampleN`), `.cat` (on an Enum), `.bin` and `.struct` namespaces, the aggregates (incl. `Implode`, `TopK`, `BottomK`, `Corr`, `Cov`, `CorrSpearman`, `Skew`, `Kurtosis`, `Entropy`, `MinBy`, `MaxBy`, the bitwise reductions and `ApproxNUnique`), and window functions (incl. `Rolling*` with `RollingMedian`, `RollingQuantile`, `RollingSkew`, `RollingKurtosis`, `RollingRank`, `RollingCov` and `RollingCorr`, `Rolling*By` by time, `Ewm*` with `EwmSum` and `EwmMeanBy`, `RleID`, `Interpolate`, `CumulativeEval`, and any body around an aggregate inside `.Over`) |
| **Frame ops**   | filter, select, with-columns, sort, top-k, distinct (`UniqueKeep`, duplicates over columns), concat in its modes/vstack/hstack, slice/tail/reverse/row-index, drop/rename/drop-nulls, a frame's cast, fills, aggregates and sample, pivot, unpivot, transpose, `PartitionBy`, `Upsample`, `ToDummies`, selectors, `ValueCounts`, `Describe`, a `Series` with operations |
| **Joins**       | all seven equi-join kinds with `Validate` and `maintain_order`, `Update`, `JoinWhere` and `WhereExists`/`WhereNotExists` (non-equi), as-of join with a tolerance, `by` keys and coalesce, merge-sorted |
| **Grouping**    | group-by with `Having`, a group's head and tail, and its groups as frames; `GroupByDynamic`, `Rolling`, calendar-aware intervals                  |
| **Optimizer**   | predicate pushdown (including through joins, a group-by's keys and a window's partition keys), projection pushdown (a count reads no column), limit/top-k pushdown through what keeps rows, cross-join collapse, constant folding and simplification, a shared subtree run once |
| **Execution**   | order-preserving pipeline parallelism, Parquet row groups decoded and CSV parsed in parallel, parallel hash aggregation with a partitioned fold and a frame's aggregate a column at a time, `Profile` of each node, a partitioned parallel join build, a sort and a unique on several threads, text sorted by radix, inner joins that hash their smaller input, and spilling for sort and top-k, hash aggregation (a median's, quantile's and n_unique's values too), hash join, unique, every window function over one huge partition too, keyed `Rolling` and `GroupByDynamic`, tail and reverse (`WithMemoryLimit` names the rest, which fail rather than spill) |
| **UDFs**        | `MapElements` (per value) and `MapBatches` (per column) — generic methods, so the Go types are inferred from your function                        |

Nested types are partly there: **List and Struct read from Parquet and Arrow and write to Parquet**, with
`Explode`, `Unnest`, a `.list` namespace and `.struct` (`Field`, `WithFields`, `RenameFields`, `JsonEncode`). An Arrow Map arrives as a list of key/value structs.
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
included. Built-in stores — `s3://` paths, listing for globs, retries — are out by decision; the seam is the way in.

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

Not done: built-in object stores and SQL, both out by decision (the seam above reaches a store today); `MapGroups`
and `RollingMap`; a lazy `Pivot`, whose output columns are the distinct values of a column, so its schema would depend on
data and no plan node here does (`Unpivot` ships, and `DataFrame.Pivot` and `Transpose` eagerly); `Upsample`; `Sample` and
`List().Eval`; a rolling rank, and a rolling window by an integer `by`; Spearman's correlation;
Arrow IPC files and NDJSON. [`v0.6-scope.md`](./context_files/v0.6-scope.md) says why each waits, and
[`feature-gaps.md`](./context_files/feature-gaps.md) lists everything Polars, pandas and DuckDB have that ursus does
not, which the rest of 0.6 works through.

**ursus is on [ZeroVer](https://0ver.org): 0.x, and a stable 1.0 is not expected yet.** Version numbers follow Go's
rule for v0, so a minor version may rename, remove or reshape an API when the result is better, with no deprecation
period. The changelog lists each release's breaks with their replacements.

---

## Correctness

```sh
make test-all   # four SIMD widths (512/256/128/0) plus the experiment off
make race       # the whole suite under -race
make levels     # import-level invariants
```

**3240 test cases**, and the matrix is not decoration. Vector width is a *runtime*
property, so a single-width run proves very little: 512-bit gives 8 float64 lanes, which happens to be exactly one
bitmap byte — a coincidence that hides an entire class of sub-byte bitmap bug. The 128-bit leg is where those surface.

Correctness is also checked against other engines. The benchmark suite validates every result against a duckdb
reference, and ursus currently passes **22/22 PDS-H (TPC-H) queries and 15/15 h2o.ai queries** — PDS-H at scale factor
0.1, re-run at every step, and both suites at the published sizes in the report below; every answer ursus gave there
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

**How current it is, precisely.** ursus and Polars were re-run together, every query of all four suites, on step 246's
code, `659ec39`, on 2026-10-14 (step 247). v0.6.5 to v0.6.8 changed no plan the suites run: the optimized plans of all
37 queries are byte-identical to v0.6.4's, v0.6.5's, v0.6.6's and then v0.6.7's, and every answer still matches DuckDB's
(steps 261, 287, 364 and 374). v0.6.7 changed the sort beneath them, and PDS-H at SF=0.1 took 0.95 to 0.98 of v0.6.6's
time; v0.6.8 the strings, the scans and the aggregates, and it took 1.00 of v0.6.7's, so the figures stand. v0.6.8's CSV
reader is faster than they say: h2o's `gb1` over CSV took 0.78 of the time before it. The other engines' numbers are
from an earlier session, every engine together on `5a721e0` on 2026-10-08 (step 127): their code has not changed, and
re-running them would have taken the laptop for an hour and more. So ursus is compared with Polars within one session,
and with the rest across sessions.

The caveats that apply to *this* run:

- **No cgroup was set,** where v0.5.0's report ran every query in an 8 GB one. It ran on 8 threads, on a laptop with
  4 to 7 GB of memory available and 4 GB of swap in use by IDEs and the rest. Nothing else of this project's ran
  alongside it.
- **The session, not the code, moved the ratios.** Alternated against v0.6.3's code, both built alike, 0.6.4 is level:
  PDS-H at SF=1 1.4% less wall time, at SF=0.1 2% less, h2o within noise. 0.6.4 changed what happens past a memory
  limit, and these run without one. Both engines ran h2o faster than in v0.6.3's session, which was short of memory:
  over Parquet ursus 928 to 697 ms and Polars 774 to 531, over CSV 2,535 to 1,784 and 1,434 to 1,088.
- **PDS-H at SF=0.1 ran twice:** the first run, straight after SF=1, was slower on every query than the same code a
  minute later, and was run again alone.
- **ursus passed every query of all four suites,** and every answer validated against duckdb's.
- **ursus's highest peak is h2o `gb10` over Parquet, 7.72 GB,** with no limit set, and 6.44 GB over CSV; Polars'
  highest is 5.03 GB. With no limit a peak is where the garbage collector lets the heap grow, and it moves with the
  memory free: v0.6.3's report had 6.76 and 8.17 GB.
- **Out of core,** each path v0.6.4 made spill or stream, under a tenth of its input, gives the answer it gives
  without a limit, peaking at 1.01 to 2.11 times the limit; step 247 has the table.
- **Other engines:** chDB timed out on two group-bys and failed `gb6` in both h2o suites; gota ran out of memory on
  two group-bys; DataFusion, and chDB at SF=0.1, answered PDS-H `q15` wrongly, and are struck through.

Timings come from a laptop under real conditions, so treat small differences as noise and the ordering as the signal.

**Where it stands, in one place.** Against Polars, on the current report, with v0.6.3's, v0.6.2's, v0.6.1's,
v0.6.0's, v0.5.0's, v0.4.0's and v0.3.0's for comparison:

|                           |    ursus |   Polars |       | at v0.6.3 | at v0.6.2 | at v0.6.1 | at v0.6.0 | at v0.5.0 | at v0.4.0 | at v0.3.0 |
|---------------------------|---------:|---------:|-------|----------:|----------:|----------:|----------:|----------:|----------:|----------:|
| h2o.ai, 10M rows, Parquet |   697 ms |   531 ms | 1.3x  |      1.2x |      1.5x |      1.5x |      1.6x |      1.7x |      1.9x |      3.3x |
| h2o.ai, 10M rows, CSV     | 1,784 ms | 1,088 ms | 1.6x  |      1.8x |     1.65x |      1.7x |      1.8x |      2.0x |      3.3x |      3.6x |
| TPC-H SF=1                |   158 ms |    79 ms | 2.0x  |      1.9x |      2.1x |      2.2x |      2.3x |      3.6x |      4.4x |     10.8x |
| TPC-H SF=0.1              |    20 ms |     9 ms | 2.3x  |      1.8x |      2.8x |      2.4x |      2.6x |      3.1x |      4.0x |      7.5x |
| TPC-H SF=1 peak memory    |  0.83 GB |  0.92 GB | 0.9x  |      1.0x |      0.9x |      1.0x |      1.0x |      1.3x |      1.1x |      2.0x |

Geomeans over each engine's passed queries; ursus and Polars pass every one. v0.6.4's code is level with v0.6.3's when
the two are alternated, so the moves against Polars are the session's; see the caveats above. v0.6.3's h2o figures came
from a session short of memory that slowed both engines.

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
