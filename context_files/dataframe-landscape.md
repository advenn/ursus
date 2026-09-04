# The DataFrame Landscape — research notes

Purpose: understand what exists, why it exists, and precisely which gap `ursus` fills.
Companion docs: [`dataframe-features.md`](./dataframe-features.md) (feature taxonomy),
[`ursus-api.md`](./ursus-api.md) (proposed Go API).

---

## 1. The stack has three separable layers

Modern "dataframe" is not one thing. It is three layers that different projects
implement at different depths. Knowing which layer you are building at is the single
most important architectural decision.

```
┌─────────────────────────────────────────────────────────┐
│ L3  API / frontend                                      │  pandas, Polars API,
│     - expression DSL or SQL                             │  Ibis, Narwhals,
│     - what users type                                   │  DataFusion DataFrame API
├─────────────────────────────────────────────────────────┤
│ L2  Query engine                                        │  Polars engine,
│     - logical plan → optimizer → physical plan          │  DataFusion, DuckDB,
│     - operators: scan, filter, project, join, agg, sort │  Velox, ClickHouse
│     - parallelism, spilling, streaming                  │
├─────────────────────────────────────────────────────────┤
│ L1  Memory format + kernels                             │  Apache Arrow,
│     - columnar layout, validity bitmaps, buffers        │  arrow2, Vortex,
│     - vectorized compute kernels (often SIMD)           │  Arrow compute
└─────────────────────────────────────────────────────────┘
```

`ursus` must implement **all three** in Go, because L1 exists in Go (`arrow-go`) but
L2 does not exist at all, and L3 exists only as toys.

---

## 2. Library-by-library

### pandas — the incumbent (L3 + weak L2 + weak L1)

- **Model:** eager, row-labelled (the `Index`), NumPy-backed (increasingly Arrow-backed).
- **Why it won:** ubiquity, notebook ergonomics, the `Index` for time series alignment.
- **Why it is "old school":**
  - Eager: every operation materializes. No optimizer, no pushdown, no fusion.
  - Single-threaded for almost everything.
  - Memory-hungry: object dtype for strings (historically), copies everywhere,
    `SettingWithCopyWarning` as a symptom of unclear ownership semantics.
  - The `Index` is a source of enormous accidental complexity (alignment surprises,
    MultiIndex, `reset_index()` rituals).
  - API surface is huge and inconsistent (`apply` vs `map` vs `applymap` vs `transform`;
    `axis=0/1` everywhere; 15 ways to select).
- **Lesson for ursus:** *no row index.* Polars deliberately dropped it and it was correct.
  Row order is a property, not an addressable label space.

### Polars — the reference design (L1 + L2 + L3, Rust)

- **Model:** columnar, Arrow-compatible memory, **expression DSL**, eager + lazy APIs,
  multithreaded, streaming engine for larger-than-RAM.
- **Key ideas we are copying:**
  1. **Expressions are values.** `col("a") / col("b")` builds a tree; it does nothing
     until placed in a *context*. This is the whole design.
  2. **Contexts:** `select`, `with_columns`, `filter`, `group_by().agg()`. Each context
     has different length/broadcast rules.
  3. **Expression expansion:** one expression can name many columns
     (`col("a","b")`, regex, by dtype, selectors) and fan out into parallel sub-plans.
  4. **Lazy by default for serious work** — `scan_*` returns a `LazyFrame`, the optimizer
     pushes predicates/projections into the scan, and the file reader only reads the
     row groups and columns actually needed.
  5. **Streaming engine** (`engine="streaming"`) processes in morsels/batches so the
     working set is bounded; unsupported operators fall back to in-memory transparently.
  6. **Zero-copy Arrow interop** in and out.
- **Weaknesses to improve on:**
  - Python API leans hard on operator overloading and kwargs — neither exists in Go, so
    our surface must be redesigned rather than transliterated.
  - Streaming coverage is incomplete and the fallback is silent; users discover
    memory blowups at runtime. **We should make streamability introspectable and
    optionally strict** (error instead of silent fallback).
  - No cancellation story. Go's `context.Context` is a real advantage here.
- **Legal note:** method *names* and API shapes are not copyrightable subject matter in
  any meaningful sense (`Google v. Oracle`, 2021, held that reimplementing Java's API
  declarations for a new platform was fair use). Polars is MIT-licensed regardless.
  **Do not copy Rust source.** Copying names, semantics, and doc phrasing conventions is
  fine and is exactly what Narwhals, Ibis, and cuDF already do openly.

### Apache Arrow — the memory format (L1)

Not a dataframe library; the *substrate*. Its value is that a Parquet reader, a query
engine, a GPU library, and a Go program can all point at the same bytes.

Layout essentials an implementer must honor (from the columnar spec):

- **Buffers are 8-byte aligned minimum, 64-byte recommended** — 64 is what lets SIMD
  loops run without a scalar prologue.
- **Validity bitmap**, LSB-numbered, `ceil(len/8)` bytes, `1` = valid. Omittable when
  `null_count == 0`.
- **Fixed-width primitives:** one values buffer, `width * len` bytes.
- **Variable-size binary/utf8:** offsets buffer of `len + 1` monotonically increasing
  ints (32-bit `Binary`/`Utf8`, 64-bit `LargeBinary`/`LargeUtf8`) + data buffer.
- **View layout (Arrow ≥ 1.4):** 16-byte view structs. Strings ≤ 12 bytes are inlined;
  longer ones store `{length, 4-byte prefix, buffer index, offset}`. The 4-byte prefix
  makes most comparisons resolve without touching the data buffer. This is the Umbra
  design and it is a large win for string-heavy work.
- **List:** offsets + child array. **ListView:** offsets + *sizes*, allowing out-of-order
  and shared child ranges. **FixedSizeList:** no offsets, slot `j` is `[j*N, (j+1)*N)`.
- **Struct:** one equal-length child per field; parent validity ANDs with child validity.
- **Union:** dense (types buffer + offsets + children) vs sparse (types buffer, all
  children full length).
- **Dictionary encoding:** integer indices + dictionary array. Null count comes from the
  indices only.
- **Run-end encoding (≥ 1.3):** `run_ends` (monotonic) + `values`. Random access is
  `O(log n)` via binary search — great for sorted/low-cardinality columns.
- **RecordBatch:** equal-length arrays + schema. Buffers serialize in depth-first
  pre-order. Multiple batches = chunked dataset.
- **IPC:** 8-byte-aligned encapsulated messages, `0xFFFFFFFF` continuation marker,
  flatbuffer metadata, then body. File format adds `ARROW1` magic + footer with offsets.

### Apache DataFusion — engine-as-a-library (L2 + L3, Rust)

- "A very fast, extensible query engine for building data-centric systems in Rust,
  using the Apache Arrow in-memory format."
- **Architecture:** SQL/DataFrame frontend → `LogicalPlan` → optimizer → `ExecutionPlan`
  (physical) → vectorized, multithreaded, **streaming** execution over partitions.
- **Optimizer:** expression coercion and simplification, projection and filter pushdown,
  sort- and distribution-aware optimizations, automatic join reordering.
- **Extensibility is the product:** `TableProvider` (custom sources), `ObjectStore`
  (custom storage), scalar/aggregate/window/table UDFs, custom optimizer passes,
  alternate query languages, Substrait plan exchange.
- **Lesson for ursus:** separating `LogicalPlan` from `PhysicalPlan` with a rule-based
  optimizer between them is the proven structure. Also: make `TableProvider` and
  `ObjectStore` equivalents public from day one so users can plug in their own sources.

### DuckDB — in-process OLAP SQL (L1 + L2, C++)

- Embedded, no server process. Columnar-**vectorized** engine: queries are interpreted
  but a *vector* (batch) of values is processed per operation, amortizing interpretation
  overhead. Push-based execution model.
- Larger-than-memory via external/spilling operators.
- Native single-file format with secondary indexes; also reads Parquet/lakehouse formats.
- **Zero-copy Arrow / Pandas integration** — queries run directly on foreign memory.
- Extension mechanism for new types, functions, file formats, and SQL syntax.
- **Why Go users reach for it today:** cgo bindings exist and it works. **Why it is not
  the answer:** it is SQL. You lose type safety, composability, and Go-native
  refactoring; you get string-building, cgo overhead, and a C++ dependency in your build.

### Ibis — portable frontend (L3 only, Python)

- Deferred expression API over **20+ backends** (DuckDB default, Polars, DataFusion,
  BigQuery, Snowflake, Databricks, PySpark, Flink, Postgres, MySQL, Oracle, SQLite,
  ClickHouse, Druid, Trino, Exasol).
- Compiles expressions to backend-native SQL via **SQLGlot**; you can inspect the SQL and
  mix hand-written SQL with the dataframe API.
- **Lesson:** an expression IR that is *serializable and retargetable* is valuable on its
  own. If our logical plan is a clean, serializable IR, someone can later compile it to
  SQL and push it into Postgres/ClickHouse. Design for that even if we do not build it.

### Narwhals — compatibility shim (L3 only, Python, zero dependencies)

- "An extremely lightweight and extensible compatibility layer between dataframe
  libraries." Wraps existing frames in **a subset of the Polars API**.
- Full API support: cuDF, Modin, pandas, Polars, PyArrow. Lazy-only: Dask, DuckDB, Ibis,
  PySpark, SQLFrame, Daft.
- Ships a general API plus a **stable API** with a backwards-compatibility guarantee.
- **Lesson:** the Polars expression API has become the *de facto* portable dataframe
  interface. Adopting its vocabulary means our users already know it, and any future
  Go↔Python bridge is a mechanical mapping. It also confirms the API subset that
  actually matters — Narwhals implements the useful ~20%, and their choice of what to
  include is a good MVP scope signal.

### chDB / ClickHouse — embedded OLAP (L2, C++)

- chDB is ClickHouse-as-a-library: a full ClickHouse engine in-process, SQL frontend.
- ClickHouse's relevant innovations: aggressive vectorization, specialized codecs
  (Delta, DoubleDelta, Gorilla, T64, LZ4/ZSTD per column), sparse primary indexes,
  `MergeTree` sorted-by-key storage, and extremely fast `GROUP BY` via specialized
  hash table variants chosen by key cardinality/type.
- **Lesson:** *pick hash table implementations per key type.* A `GROUP BY` over a small
  `UInt8` key should be a direct-mapped array, not a hash map. Cardinality estimation
  driving strategy selection is a real, large win (Polars does this too — see its
  "cardinality estimation" optimizer pass).

### The rest, briefly

| Project | Layer | Idea worth stealing |
| --- | --- | --- |
| **Dask** | L2/L3 | Partitioned frames + task graph; scale-out beyond one machine. Also: the *pain* of partition-alignment shows why a single-node engine should own its own partitioning. |
| **Modin** | L3 | Drop-in pandas API over Ray/Dask. Shows API compatibility alone attracts users. |
| **Vaex** | L2 | Memory-mapped out-of-core + lazy "virtual columns" computed on access. Memory-mapping Arrow IPC files is nearly free larger-than-RAM support. |
| **cuDF** | L1/L2 | GPU columnar. Confirms that a clean expression IR retargets to new hardware. |
| **Daft** | L2/L3 | Rust engine, Python frontend, multimodal columns (images, tensors, URLs). Interesting extension-type story. |
| **Spark** | L2/L3 | Catalyst optimizer; the canonical logical→physical rule-based optimizer literature. Whole-stage codegen. |
| **Velox** | L1/L2 | Meta's C++ vectorized execution library; the "engine as reusable component" thesis, same as DataFusion. |
| **Vortex** | L1 | Next-gen compressed columnar format; random access into *compressed* data. Watch as a future storage target. |

---

## 3. The Go situation — why `ursus` should exist

### What exists today

| Library | Status | Assessment |
| --- | --- | --- |
| [`go-gota/gota`](https://github.com/go-gota/gota) | Most-starred, largely stalled | Eager, `interface{}`-boxed values, no Arrow, no lazy, no parallelism. The authors themselves note it is "difficult to build a 1:1 implementation of an untyped API in a typed language," which "led to bloated, hard to maintain code." |
| [`go-gota/gota2`](https://github.com/go-gota/gota2) | Rewrite using Go 1.18 type parameters | Acknowledges the generics fix. Still eager, still no Arrow, still no engine. |
| [`tobgu/qframe`](https://github.com/tobgu/qframe) | Immutable frames | Best API of the old guard and faster than gota in benchmarks. Arrow support is listed as a *wanted contribution*, i.e. absent. |
| `rocketlaunchr/dataframe-go` | Maintenance mode | Eager, generics-free, small. |
| `gandalff` | Newer, small | Data wrangling, not an engine. |
| [`apache/arrow-go`](https://pkg.go.dev/github.com/apache/arrow-go/v18/arrow) | **Healthy and complete at L1** | Full type system, builders, `RecordBatch`, `Table`, `Chunked`, dictionary + REE + view types, extension types, IPC, Parquet, Flight. **This is our foundation.** |

**Nobody has stable APIs. None of the dataframe libraries are Arrow-integrated. None has
a query optimizer. None does lazy execution. None handles larger-than-RAM.** The gap is
not "a better gota" — the gap is **L2 does not exist in Go at all.**

### What Go people actually do (the pain we are solving)

1. **Shell out to Python.** Marshal to Parquet/CSV, run a Python process, read results
   back. Two languages, two dependency trees, two deploy stories, serialization on both
   ends. *(This is exactly the reported experience motivating this project.)*
2. **cgo DuckDB.** Works, but it is SQL-in-strings, a C++ toolchain dependency, cgo call
   overhead, and no compile-time checking of column names or types.
3. **Hand-rolled loops over `[]struct{}`.** Fine until it is 50 GB, or until you need a
   group-by with five aggregations and a window function.

### Why Go 1.27 specifically changes the calculus

| Go 1.27 feature | What it unlocks for us |
| --- | --- |
| **Generic methods** (`func (df *DataFrame) Column[T any](name string) ...`) | Typed accessors *as methods* rather than package-level functions. `Series[T]`, typed row scanning, typed UDFs, typed literals — all fluent instead of `ursus.Column[int64](df, "x")`. This is the ergonomic difference between a library people like and one they tolerate. |
| **`simd` package** (portable, vector-size-agnostic, `GOEXPERIMENT=simd`) | Compute kernels written once, running on AVX-512/AVX2/SSE/Neon/wasm128 and *emulated* elsewhere. No assembly, no per-arch build tags for the portable path. |
| **`simd/archsimd`** | Hand-tuned kernels for the hot 5% (bitmap ops, string prefix compare, hash) where a fixed 512-bit width is worth arch-specific code. |
| **Size-specialized malloc** (~30% cheaper small allocations) | Helps expression-node and builder churn. |
| **Goroutine leak profile** (`/debug/pprof/goroutineleak`) | An engine with per-morsel goroutines and channel pipelines *will* leak on cancellation paths. This makes those bugs findable. |
| **`encoding/json/v2` + `jsontext`** | Fast, correct NDJSON scanning built in, with token-level access for schema inference. |
| **Faster `compress/flate`** | Parquet/IPC compression paths get free speedup. |

### The hard constraint to design around

> **A generic method cannot implement an interface method, and interface methods cannot
> declare type parameters.** (Go 1.27 language section.)

Consequence: **user-facing types must be concrete structs, not interfaces.** If `Expr`
were an interface, `Expr` could never gain a generic method. Design:

```go
type Expr struct { node exprNode }   // concrete struct, generic methods OK
type exprNode interface { ... }      // unexported interface, non-generic methods only
```

The same applies to `DataFrame`, `LazyFrame`, and `Series[T]`. Polymorphism lives in
unexported interfaces; the public surface is concrete.

---

## 4. Design conclusions carried into `ursus`

1. **Build on `arrow-go`.** Do not invent a memory format. Zero-copy in/out of Arrow is
   a feature, not an afterthought.
2. **Copy the Polars conceptual model** — expressions, contexts, expansion, lazy-first —
   because it is correct and because Narwhals proved it is the portable vocabulary.
3. **Redesign the *syntax*** for Go: no operator overloading, no kwargs, no dynamic
   dispatch on Python types. Methods, option structs, functional options, generics.
4. **Logical plan → optimizer → physical plan**, DataFusion-style, with the logical plan
   as a clean serializable IR (enables future SQL pushdown / distributed execution).
5. **Streaming is a first-class execution mode, not a flag** — and it must be
   *introspectable* (`Explain`) and optionally *strict* (fail rather than silently
   buffer the world).
6. **`context.Context` everywhere at the execution boundary.** Cancellation and deadlines
   are things Polars cannot offer and Go users expect.
7. **Errors are values, deferred.** Lazy chains accumulate a sticky error surfaced at
   `Collect` — the `bufio.Scanner` / `sql.Rows` idiom — so chaining stays clean.
8. **Iterators (`iter.Seq2`) for batch output**, so streaming results compose with the
   rest of Go.

---

## Sources

- [Go 1.27 release notes](https://go.dev/doc/go1.27) — see [`go-1.27-release-notes.md`](./go-1.27-release-notes.md)
- [Polars documentation](https://docs.pola.rs/)
- [Apache Arrow columnar format specification](https://arrow.apache.org/docs/format/Columnar.html)
- [Apache Arrow Go (`arrow-go/v18`)](https://pkg.go.dev/github.com/apache/arrow-go/v18/arrow)
- [Apache DataFusion](https://datafusion.apache.org/user-guide/introduction.html)
- [Why DuckDB](https://duckdb.org/why_duckdb)
- [Ibis](https://ibis-project.org/)
- [Narwhals](https://narwhals-dev.github.io/narwhals/)
- [gota](https://github.com/go-gota/gota), [gota2](https://github.com/go-gota/gota2), [qframe](https://github.com/tobgu/qframe)
- [DataFrames in Go with gota, qframe, and dataframe-go](https://www.mungingdata.com/go/dataframes-gota-qframe/)
