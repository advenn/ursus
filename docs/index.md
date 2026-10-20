---
description: >-
  ursus is a dataframe library for Go (Golang) modelled on Polars: lazy queries with a
  query optimizer, Apache Arrow memory, SIMD kernels and out-of-core execution, in
  pure Go with no cgo.
hide:
  - navigation
  - toc
---

# ursus: DataFrames for Go

<div class="hero" markdown>

<p class="lead">A dataframe library for Go, modelled on Polars. Lazy queries with a
query optimizer, Apache Arrow memory, SIMD kernels, and execution that spills to disk
instead of running out of memory. Pure Go: no cgo, no C++ toolchain, no Python.</p>

[Get started](getting-started/quickstart.md){ .md-button .md-button--primary }
[Coming from Polars](getting-started/coming-from-polars.md){ .md-button }
[API reference](reference/api.md){ .md-button }

</div>

```sh
go get github.com/advenn/ursus
```

--8<-- "Example_home.md"

Nothing runs until `Collect`. The optimizer reads only the columns the query uses,
the group-by runs on every core, and the same query runs under a memory limit by
spilling to disk.

<div class="grid cards" markdown>

-   :material-lightning-bolt:{ .lg .middle } **Lazy and optimized**

    ---

    A query is a plan until it is collected. Filters and projections are pushed
    into the scans and through joins, constants are folded, and a subtree used twice
    runs once.

    [:octicons-arrow-right-24: Lazy and eager](concepts/lazy-and-eager.md)

-   :material-function-variant:{ .lg .middle } **Polars' expressions, in Go**

    ---

    `Col("qty").Mul(Col("price")).Sum().Over(Col("region"))`: the same expressions,
    contexts and names as Polars, checked against Polars' answers on the same inputs.

    [:octicons-arrow-right-24: Expressions](concepts/expressions.md)

-   :material-harddisk:{ .lg .middle } **Out of core**

    ---

    Sort, group-by, join, unique and window functions spill to disk under a memory
    limit and give the answer they give in memory. An operator that cannot spill fails
    with an error naming itself.

    [:octicons-arrow-right-24: Memory and spilling](concepts/memory.md)

-   :material-language-go:{ .lg .middle } **Pure Go**

    ---

    `go get` is the whole install. It cross-compiles and links into a static binary
    like any other Go dependency, and Go functions run inside a query as UDFs.

    [:octicons-arrow-right-24: Installation](getting-started/installation.md)

-   :material-file-table:{ .lg .middle } **Parquet, CSV and Arrow**

    ---

    Read and write Parquet and CSV from files, memory, or any `io.ReaderAt`, with
    row groups pruned by statistics. Arrow records go in and out, zero-copy out.

    [:octicons-arrow-right-24: Quick start](getting-started/quickstart.md)

-   :material-check-decagram:{ .lg .middle } **Checked against other engines**

    ---

    Every TPC-H (PDS-H) and h2o.ai benchmark answer is validated against DuckDB's,
    and the test suite runs at four SIMD widths and with SIMD off.

    [:octicons-arrow-right-24: Benchmarks](benchmarks.md)

</div>

## Is ursus for you?

**It is pure Go,** and that is the reason to pick it. The cost should be just as
plain: **ursus is slower than the serious analytical engines.** On the h2o.ai
benchmark at ten million rows it takes about 1.3 times Polars' time over Parquet and
1.6 times over CSV; on TPC-H at scale factor 1, about 2 times.

**A good fit:**

- data where the speed difference is small in absolute terms: up to tens of millions
  of rows, it is half a second against a tenth of one. Larger data still runs,
  spilling to disk past the memory limit, but there the same ratio is seconds or
  minutes, and a spilled query pays for its disk too;
- a Go service that needs real dataframe work, joins, group-bys and window functions,
  and cannot take on cgo, a Python sidecar or a large C++ dependency to get it;
- anywhere the deployment story is worth more than the last multiple of speed.

**Not a good fit:**

- interactive analytics over hundreds of millions of rows: use DuckDB or Polars;
- anywhere you can link cgo freely: `duckdb-go` is a binding to a mature engine and
  about twice as fast on TPC-H;
- anything production-critical today. ursus is at 0.6, and its API still changes
  between releases, as the [changelog](reference/changelog.md) records.
