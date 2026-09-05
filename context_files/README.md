# context_files

Background material for building **ursus** — a Polars-class dataframe library for Go:
expression DSL, lazy execution with a query optimizer, streaming/larger-than-RAM
execution, Arrow memory, SIMD kernels.

| File | What it is | Read it when |
| --- | --- | --- |
| [`step-23-as-built.md`](./step-23-as-built.md) | **Start here.** `map[string]int32` replaced by `kernel.KeyTable` in the six operators that identify groups — group-by, its spilling twin, both join build sinks, and the window sink. Every affected benchmark faster (1.04–1.18x) and the string group-by allocating **17.7x less**. Read it mainly for the measurement lesson: the first before/after said the change was a 1.2–1.4x REGRESSION, and it was machine drift — the same unchanged code measured 18% apart between two runs, and only matched 8-sample runs against a stashed tree settled it. Also: a teeth check that could not fire and why that is the honest result, a `Merge` that ranged a Go map and so was nondeterministic, and a release path that freed the accounting but not the memory. | Always. Authoritative over everything below. |
| [`step-22-as-built.md`](./step-22-as-built.md) | The CSV reader's per-value string allocation, removed the way step 18 removed Parquet's: **3,177,463 allocations per scan become 25,310**, memory halved, the scan ~2x faster. Read it for why the plan's "remaining ~1M allocations" turned out not to exist — the benchmark fixture reads its timestamp column as a String, which reading the *benchmark* rather than the reader is what showed — and for a teeth check whose first version was not a bug at all. Also: a 34.6 MB compiled test binary tracked since the initial commit, and three false statements in files people can now `go get`. (Step 21 was packaging — the module rename to `github.com/advenn/ursus`, `bench.yml`, the MIT licence — and has no as-built; it is in commits `324a9ce` and `fd89262`.) | Always. Authoritative over everything below. |
| [`step-20-as-built.md`](./step-20-as-built.md) | `extremumAcc` rewritten from one heap `*data.Column` per group to flat typed slices — h2o gb7 goes from 23x slower than polars to 1.6x, and from 1.91 GB to 0.23 GB. **h2o now validates 15/15**, so nothing in the report is struck through for the first time. Also: a test of mine that was vacuous until rewritten, a CI guard whose first version would have failed under `bash -e`, and `make run` silently timing a stale binary. | Always. Authoritative over everything below. |
| [`step-19-as-built.md`](./step-19-as-built.md) | gb7's wrong answer, fixed — and the cause was a bug in **arrow-go**, not ursus: an operator-precedence typo in `alignedBitmapOp` made `BitmapAnd`/`Or`/`AndNot` silently drop the last EIGHT bits of any result whose source had a non-zero offset. Read it for the bisect that overturned the obvious suspect, and for why a test that already swept unaligned offsets could not see it. h2o is now 15/15. | Always. Authoritative over everything below. |
| [`step-18-as-built.md`](./step-18-as-built.md) | The sort and the per-row string allocation — the first step scoped by a PROFILER rather than a design doc. `ArgSort` was 42% of hot-path CPU and 99.85% of that was `sort.SliceStable`; the fix was noticing that `OrderKeyF64` already produced exactly the key a radix sort wants and was being recomputed on every comparison. Window rank 5.1x, sort 3.2x, and 51x fewer allocations in the Parquet scan. Also: how the baseline had to be recorded twice, and two teeth checks that could not fire. | Always. Authoritative over everything below. |
| [`step-17-as-built.md`](./step-17-as-built.md) | Parallel hash aggregation — `Sink.Merge` runs in production for the first time, sixteen steps after it was written. The group-id remap it had been waiting for, two gates (order-insensitivity and no memory limit) that decline rather than approximate, and the first step driven by a MEASUREMENT: `bench/` holds a full TPC-H + h2o harness whose report was stale by twenty queries. 4.76x in memory, 1.0–2.8x end to end, and a documented reason why one query gains nothing. Also finds a pre-existing wrong answer. | Always. Authoritative over everything below. |
| [`step-16-as-built.md`](./step-16-as-built.md) | The optimizer's second half: constant folding, boolean identities, dead-filter removal and identity-`Project` removal, behind the `SimplifyExprs` flag that had been declared and unread since v0.1. Why folding is INJECTED through a `ConstEvaluator` interface rather than imported — `internal/plan` still has no Arrow dependency, and a test now asserts it. Two claims the design doc got wrong and the code disproves: the rule needs `Once`, not `UntilStable`, and division by zero is not the folding hazard. | Always. Authoritative over everything below. |
| [`../bench/`](../bench/) | The benchmark harness, a **separate Go module** plus a uv/Python driver. PDS-H (TPC-H, 22 queries) and h2o.ai (10 groupby + 5 join) against duckdb, polars, pandas, DataFusion, chDB, Arrow-Go, Gota and QFrame, with generated data, duckdb reference answers and a validator that strikes through any engine that disagrees. `make bench` runs the lot; `make report` rebuilds [`results/REPORT.md`](../bench/results/REPORT.md) from `timings.csv` without re-running anything. | Before claiming ursus is fast or slow at anything. It is also the only thing in the repo that checks ursus against another engine's ANSWERS — step 17 found a wrong answer that way. Keep REPORT.md regenerated: it was stale by twenty queries for several steps. |
| [`design/`](./design/) | The **pre-implementation** design: [`foundation.md`](./design/foundation.md) (2,752 lines — memory, dtypes, Arrow layout, the null contract, SIMD kernels), [`logical.md`](./design/logical.md) (2,177 — the expression IR, plan nodes, resolution, and the `Rule`/`LocalRule`/`RunMode` optimizer framework), [`physical.md`](./design/physical.md) (2,584 — operators, the sink/breaker protocol, morsel parallelism). Each ends with a "what I deliberately left out, and where it slots in" table that predicted where joins, windows, spilling and nested types would attach. | Implementing something the design specified but nobody has built yet. **The as-builts are authoritative over these wherever they disagree** — §12's `LocalRule` arrived in step 16 with a different signature, and its claim that folding needs `UntilStable` is wrong. |
| [`step-15-as-built.md`](./step-15-as-built.md) | **v0.2 is complete.** The as-of join — a LEFT join whose match rule is "nearest", which is why it reuses JoinLayout and gatherOut and adds one search function. Temporal promotion within a kind, which its canonical case needed and which makes a TimeUnit.Finer doc comment true after thirteen steps. Plus MergeSorted and the nine projection-pushdown arms, deferred in steps 11 through 14. | Always. Authoritative over everything below. |
| [`step-14-as-built.md`](./step-14-as-built.md) | Calendar intervals and temporal grouping: `Interval`/`Every`, `GroupByDynamic` and `Rolling`. Why a sorted index turns every window into a row RANGE — which is what lets both operators reuse all nineteen accumulators unchanged — and why month arithmetic must clamp where `time.Date` normalises. Parquet finally reads and writes Int128 and the temporal types, so a spilling group-by can write its own output and `.dt` is reachable from Parquet at all. | Before the earlier steps. Authoritative over them. |
| [`step-13-as-built.md`](./step-13-as-built.md) | The spilling hash join, which completes v0.2's streaming line. Why step 12's residency freeze does not transfer — measured at 4.9x the limit with zero spills — and what a hybrid hash join replaces it with. Four silent wrong answers removed, the join's hash table finally on the memory ledger (it was under-reporting by 3.2x), and eleven invariance tests that compared ten rows of their output. | Before the earlier steps. Authoritative over them. |
| [`step-12-as-built.md`](./step-12-as-built.md) | Spilling hash aggregation: the residency freeze, radix partitioning of the keys admitted after it, recursion on skew with a per-level seed, and `MaintainOrder` — a flag that had been assigned, rendered and never read for four steps. Why `Accumulator.Merge` is deliberately never called, why the refusal for `quantile`/`median`/`n_unique` is exact rather than heuristic, and the budget bug whose error message blamed skew on uniform data. `distinctOp` finally has an account. | Before the earlier steps. Authoritative over them. |
| [`step-11-as-built.md`](./step-11-as-built.md) | Expression completeness: the maths family (`Pow`, `Sqrt`, `Round`, `Clip`, `Sign`, `Floor`…) and null repair (`FillNull`, `FillNan`, `ForwardFill`, `Diff`). Weak literals, so filling an Int32 column with 0 leaves it Int32. Two plan-accepts/kernel-rejects holes closed — `Sum().Abs()` and `Sum().Cast(String)` both used to fail at execution — plus eight name tables whose unknown-constant fallback was unreachable. | Before the earlier steps. Authoritative over them. |
| [`step-10-as-built.md`](./step-10-as-built.md) | Bounded memory and external sort: allocation-identity accounting, `WithMemoryLimit`/`WithSpillDir`/`WithMemoryStats`, the `internal/spill` run format (the one that can serialise Int128), a cross-run comparator and a stable k-way merge. Why `Sink.Merge` was never the spilling seam, and why the top-k guard turned out to be a memory guard rather than a correctness one. | Before the earlier steps. Authoritative over them. |
| [`step-9-as-built.md`](./step-9-as-built.md) | The frame layer: Concat/VStack/HStack, Slice/Tail/Reverse/WithRowIndex, Drop/Rename/DropNulls, TopK/BottomK, the GroupBy shorthands and `DataFrame.Lazy()`. A dead top-k path woken up, a `Sort` arm added to projection pushdown, and three pre-existing panics/errors on empty and null-literal results. | Before the earlier steps. Authoritative over them. |
| [`step-8-as-built.md`](./step-8-as-built.md) | Window functions. Why the "nested scope body" mechanism was the wrong tool and hiding the aggregated child would have let projection pushdown prune it away; the additive `HasUnboundAgg` scoping; one ArgSort for all partitions; and `expr.Rebuild`, which removed the third copy of a rewrite walk. | Before the earlier steps. Authoritative over them. |
| [`step-7-as-built.md`](./step-7-as-built.md) | Conditionals (`When/Then/Otherwise`, `Coalesce`), ten aggregates (`Std`, `Var`, `Median`, `Quantile`, `Product`, `ArgMin/Max`, `Any`, `AllTrue`, `NullCount`) and `IsIn`. Why a conditional could not be sugar, why `Agg.String()` hiding its parameters was a correctness bug, and a merge test that could not fail. | Before the earlier steps. Authoritative over them. |
| [`step-6-as-built.md`](./step-6-as-built.md) | The `.str` and `.dt` namespaces, and six defects that made the temporal type family advertised-but-broken — including a `CastTimeUnit` that silently turned an instant into the year 55,974,289. The `expr.Call` node every future namespace reuses, and one parse/format core shared by four callers. | Before the earlier steps. Authoritative over them. |
| [`step-5-as-built.md`](./step-5-as-built.md) | v0.1 complete: predicate pushdown through joins, and order-preserving in-memory parallelism (2.97x on a pipeline). Why the ordering obligation had five silent dependants, what the design docs' "parallelism is free" claim was actually worth, and three pre-existing defects the audit surfaced. | Before the earlier steps. Authoritative over them. |
| [`step-4-as-built.md`](./step-4-as-built.md) | Joins: all seven equi-join kinds plus `Validate`. The output-schema/collision algorithm and why it has exactly one definition, the first two-child plan node, null keys as the opposite of GroupBy's rule, and two live bugs found on the way. | Before the earlier steps. Authoritative over them. |
| [`step-3-as-built.md`](./step-3-as-built.md) | IO: CSV and Parquet, readers and writers. The scan contract's soundness fix (`Exact`/`Inexact`/`Unsupported`), read set vs emit set, Decimal, three arrow-go statistics traps, and the literal-expression crash three of six operators shared. | Before the earlier steps. Authoritative over them. |
| [`step-2-as-built.md`](./step-2-as-built.md) | The analytics core: GroupBy/Agg, Sort, WithColumns, Unique, predicate pushdown. The four places ursus deliberately diverges from Polars on null semantics, the Int128 decision and its blast radius, and the nine defects a pre-planning start introduced. | Before the vision docs. Authoritative over them. |
| [`step-1-as-built.md`](./step-1-as-built.md) | The walking skeleton: the package map and import levels, the ~24 corrections made to the vision docs, and three things learned only by building (including a real Go 1.27 `simd` bug). | Before the vision docs. Authoritative over them. |
| [`go-1.27-release-notes.md`](./go-1.27-release-notes.md) | Full Go 1.27 change docs. Generic methods, `simd`/`simd/archsimd`, runtime and stdlib changes, upgrade gotchas. | Writing any Go in this repo. §1.1 (generic methods) and §4 (`simd`) are the load-bearing parts. |
| [`dataframe-landscape.md`](./dataframe-landscape.md) | Ecosystem research: the three-layer stack, per-library analysis (pandas, Polars, Arrow, DataFusion, DuckDB, Ibis, Narwhals, chDB, …), the state of Go dataframes, and why Go 1.27 changes the calculus. | Deciding *what kind of thing* ursus is, or justifying a design choice. |
| [`dataframe-features.md`](./dataframe-features.md) | Exhaustive feature taxonomy of modern dataframe libraries, with P0–P3 priorities. Type system, Arrow layout, expression/context model, the full operation catalogue, joins, grouping, optimizer passes, streaming, I/O, SQL. | Scoping a milestone, or checking whether a feature is on the list. |
| [`ursus-api.md`](./ursus-api.md) | The proposed Go API: package layout, `DataType`/`Schema`, `Series[T]`, `Expr` + namespaces, selectors, `LazyFrame`, grouping/windows, joins, I/O, execution, UDFs, the SIMD kernel layer, SQL, and a Polars→ursus naming map. | Writing or reviewing any public API. |

## Load-bearing constraints

- **A generic method cannot implement an interface method** (Go 1.27). Public types are
  therefore concrete structs; polymorphism lives in unexported interfaces.
- **Errors are deferred**, sticky on `*LazyFrame`, surfaced at `Collect`.
- **`context.Context` at every execution boundary**, never in builder methods.
- **Every SIMD kernel has a scalar twin** behind `//go:build !goexperiment.simd`, with a
  differential test asserting identical results.

## Verified on this machine (go1.27.0 linux/amd64, AVX-512)

`ursus-api.md` §18 records four checks that were actually run, not assumed:

- ✅ `interface{ Expr | Literal }` compiles — so `Col("a").Gt(5)` needs no `Lit(...)`.
- ✅ Generic methods work on generic types — `Series[T].Map[U]` infers `U`.
- ✅ Generic methods genuinely cannot implement interfaces (compiler error captured).
- ✅ `GOEXPERIMENT=simd`: 512-bit vectors, not emulated; the `LoadPart`/`StorePart`
  loop and `Greater`→`Masked` null handling work as documented.

⚠️ **Every module and scratch test here must declare `go 1.27` in its `go.mod`.** Without
it the toolchain silently selects an older Go and generic methods fail with
`syntax error: method must have no type parameters`.

⚠️ **Use `go fmt`, never bare `gofmt`.** Under goenv, `gofmt` on PATH is the globally
pinned Go (1.26.2), which cannot parse generic methods and fails with
`method must have no type parameters` — a message that reads exactly like a compiler
error. `go fmt` routes through the toolchain `go.mod` selects.

⚠️ **`simd.LoadXxxPart`'s returned count is wrong under `GODEBUG=simd=0`** — it returns
`len(s)` instead of the lanes actually loaded, so the documented `i += n` loop idiom
silently skips elements. Derive the step from `v.Len()` or `StorePart`'s return
instead. See [`step-1-as-built.md`](./step-1-as-built.md) §3.1.

## Still unresolved

`ursus-api.md` §17 items 2–8: Arrow ownership (`Retain`/`Release` exposure), `Series[T]`
vs erased `Column`, chunked vs contiguous storage, string layout (offsets vs
`StringView`), how much of the eager façade to generate, and categorical
string-cache scoping.

Spilling is **done for all three of v0.2's operators**: sort (step 10), hash
aggregation (step 12) and hash join (step 13). The window, reverse, hstack, unique
and tail sinks are accounted and refused rather than spilled, and bounding them needs
the chunked `Column` above. The sort's own peak is `budget + runs × batch size` — a
single-pass k-way merge holds one batch per run — so a multi-pass merge that bounds
the fan-in is open too (step 11 §9). Two shapes still refuse by design, and both are
the same rule: partitioning divides the KEY SPACE, so it cannot help a group-by whose
`quantile`/`median`/`n_unique` state is O(rows) within one key (step 12 §5), and it
cannot help a cross join, which has no key, or one join key with more build rows than
the limit (step 13 §3).

**v0.2 is complete after step 15** — all eight items of `dataframe-features.md` §14's
line. The projection-pushdown arms and the `*Reverse` predicate barrier, deferred in
steps 11 through 14, landed with it; a group-by over a forty-column table now reads
two. **Step 16** closed `SimplifyExprs`, the last of `design/logical.md` §12's four
optimizer passes bar CSE.

**Step 17 made the engine parallel where it was measurably serial.** A hash
aggregation now runs on N sinks whose partial tables are merged — the first
production caller of `Sink.Merge` — gated on order-insensitivity and on there being
no memory limit. Sort and join breakers are still serial, for reasons step 17 §9
records, and the join PROBE is what actually dominates a large join.

**Step 18 attacked the profile.** The sort was 42% of hot-path CPU and now runs on
precomputed order keys with no comparator at all; the Parquet reader stopped
allocating one Go string per value. Window rank is 5.1x faster, sort 3.2x, and a
full scan makes 51x fewer allocations. What is left of the gap to polars is
recorded in step 18 §6 — the CSV reader still has the defect Parquet just lost,
string kernels still allocate per value, string SORT keys still take the
comparator fallback, and the six `map[string]int32` tables are ~11% of CPU.

**Step 19 fixed the wrong answer, and it was not where anyone expected.** h2o gb7's
96 spurious NULLs came from `arrow-go v18.7.0`, whose `alignedBitmapOp` computes
`endMask := (lOffset + length%8)` where it means `(lOffset + length) % 8` — so
`BitmapAnd`, `BitmapOr` and `BitmapAndNot` dropped the last eight bits of any
result whose source had a non-zero bit offset. That is every batch after the
first. `internal/bitmap` now implements those ops on its own `Words`/`AppendBits`
machinery and **h2o validates 15/15**. The suspected cause — `assembleRows`
discarding a validity bitmap it computes — was innocent; step 19 §2 has the
bisect.

**Step 20 removed `extremumAcc`'s per-group `*data.Column`**, which was the last
large measured gap: gb7 went 28.5 s → 2.0 s and 1.91 GB → 0.23 GB, from 23x
polars to 1.6x. The clearest remaining allocation win is now **the CSV reader**,
which still makes 3,177,442 allocations per `ScanCSV` — the exact defect Parquet
lost in step 18, fixable with the same `data.NewStringParts`. After that: the six
`map[string]int32` hash tables (~11% of CPU), the serial join probe (j1–j5 at
6–15x), and string sort keys still on the comparator fallback.

Then, roughly in order: **`JoinWhere`**, the one §7 join with no implementation and
no prerequisite left; **the join probe and parallel join build**; **CSE**, the last
of `design/logical.md` §12's four, which needs a cost model ursus has no cardinality
estimates for; **radix-partitioned aggregation**, which is what would make gb10
(one group per row) gain anything at all and what would let spilling and parallelism
coexist; and **`SetSorted`/`IsSorted`**.

On `Sink.Merge`, restated because the old line was wrong for two steps. There are
**seven** `Merge` methods, not five — steps 14 and 15 each added one without
updating the count. **Three are refusals**, not implementations: `windowSink`,
`temporalSink` and `asOfBuildSink` all return an internal error, and all three for
the same reason (a partial sink does not know the global order its grid or its key
search depends on). Of the **four** real ones — `hashAggSink`, `joinBuildSink`,
`sortSink`, `reverseSink` — only `hashAggSink` is called in production, and
**`reverseSink` still has no test at all**, though it is the one with
counterintuitive PREPEND semantics: its own comment records that appending, "which
is right for sortSink, would silently produce a partially-reversed frame".

Then the expression long tail: the 25 `Expr.Rolling*` methods (which need a frame
concept `WinParams` does not have), `Upsample`/`DateRange`, `Interpolate`, the
trigonometric and bit-count blocks, the twelve missing aggregates, and `Expr`-level
selection (§5.6, 45 of 54). Plus two format gaps: the `CanCast`/kernel disagreement on
numeric ↔ Decimal, and `Duration` in Parquet, where arrow-go panics rather than
returning an error.
