# Step 487 — as built

**What ursus lacks against Polars 2.1, and SQL wanted.** Taken on 2026-11-03, at
master's `ad9056f`, at the maintainer's request: "do feature analysis, if we need
anymore features implement them. just in case add duckdb compatible sql execution to
list of wanted features". ursus's parity work was measured against Polars 1.44.1;
Polars 2.0 shipped since, and 2.1 is current.

## 1. How

- **Names.** Polars 2.1.0's public API was listed by introspection: every name on
  `Expr`, its nine namespaces, `DataFrame`, `LazyFrame`, `Series`, the two group-bys
  and the top level, 853 names outside `Series` and 233 on it. It was matched by
  name against master's API listing (`internal/gen/api`). Each name left was looked
  up by hand in ursus, under its Go spelling too: `str.to_lowercase` is
  `Str().ToLower`, `sink_batches` is `CollectBatches`, `map_groups` is
  `GroupBy.MapGroups`.
- **Semantics.** The [2.0 upgrade guide](https://docs.pola.rs/releases/upgrade/2/)
  lists what 2.0 changed. Each change an ursus query can reach was run in ursus and
  in Polars 2.1.0.
- **The oracle.** `bench/scripts/oracle_expressions.py` was run again under Polars
  2.1.0, into the scratchpad, and compared with the checked-in
  `testdata/oracle/expressions.json`.

## 2. What did not change

- **The oracle:** all 46 cases answer as they did under 1.44.1, to 1e-12, and the two
  frames are the same. 2.0 deprecates `cut` and `qcut`, for `bin_intervals` and
  `bin_quantiles`, and answers them all the same.
- **2.0's changes ursus already makes:**
  - `&` of a Boolean and an integer is refused;
  - a horizontal concat of unequal heights is refused;
  - `rename_fields` with too few names is refused;
  - an integer beside a `UInt64` is an `Int128`;
  - casts of an integer to a Categorical, and of a scalar to a List, are refused;
  - a frame of no columns keeps its height.
- **2.0's streaming default** leaves a group-by's and a join's row order unspecified
  without `maintain_order`. That is ursus's model since 0.4.

## 3. Where ursus still answers as 1.44.1

Each is a break to change, so each is the maintainer's to decide.

| what | ursus, as 1.44.1 | Polars 2.0 |
| --- | --- | --- |
| `Explode` of an empty list | one row, null | no row (`empty_as_null=True` restores one) |
| a headerless CSV's columns | `column_1`, `column_2`, … | `column_0`, `column_1`, … |
| `Repeat(Col("x"), n)` and `MakeDatetime(Col("y"), …)` | named `repeat` and `datetime` | named after their first input, `x` and `y` |
| `Int64.IsIn(1.5)` | false | refused: "cannot check for Int64 values in List(Float64)" |
| `Cast(String → Date)` | parsed | refused; `str.to_date()` |
| Parquet and Arrow map columns | `List(Struct{key, value})` | the new `Map(key, value)` type |

A refusal in 2.0 against an answer here costs a user nothing. The names, the
explode and the CSV columns change what a query returns.

## 4. What ursus lacks

Of the 853 names, 184 have no ursus feature by name. Most are Python's own:
`to_numpy`, `to_pandas`, `plot`, `style`, `from_*`, `clone`, `rechunk`, `n_chunks`,
`shrink_to_fit`, `collect_async`, `set_random_seed`. ursus takes a seed by argument.
`Series`' 207 are thin by design (step 319): there a frame's expressions answer.
The rest:

**Small, and a typical user reaches for them. Built from step 488:**

| feature | Polars | why |
| --- | --- | --- |
| `Erf`, `Erfc` | `erf`, `erfc` (2.x) | the normal distribution's CDF; Go's `math.Erf` |
| `TruncDiv` | `truncdiv` (2.0) | division toward zero, as Go's and C's; `FloorDiv` floors |
| `Round`, `Ceil`, `Floor`, `Sign` of a Decimal | 2.0: `Decimal(p+1, s)` | refused today: "round() is not defined for Decimal(3, 2)". Rounding money is the first thing done to a Decimal |
| `Name().StripPrefix`, `StripSuffix` | `name.strip_prefix`, `strip_suffix` | `Name().Map` spells it today, in a closure |
| `ScanLines`, `ReadLines` | `scan_lines`, `read_lines` (2.x) | a file of lines as one String column: logs, word lists |

**Wanted, larger. Each needs its own scope:**

| feature | Polars | note |
| --- | --- | --- |
| **SQL, DuckDB-compatible** | `SQLContext`, `sql`, `sql_expr` | **wanted** since this step. It was out by decision (step 156). DuckDB's dialect, parsed into ursus's plan, so SQL and the DSL share the optimizer, the spilling and the budget. DuckDB 1.5.5 is the bench's oracle already, so every answer has a reference. PDS-H's 22 queries, in their SQL, are the first suite |
| `CollectAll`, `ExplainAll` | `collect_all`, `explain_all` | several queries in one run, a shared `Cache` computed once; a service's dashboard |
| the Map type, `List().ToMap()` | `Map` (2.0), `list.to_map` | what 2.0 loads Parquet and Arrow maps as |
| object stores (`s3://`, `hf://`) | `scan_parquet("s3://…")` | out by decision; Polars 2.x's headline: 1.43 TB filtered under 6 GB. ursus reads through `ReaderAt` and `fs.FS` |
| `ApproxQuantile` | `approx_quantile` (2.x) | a quantile in bounded memory; ursus's `Quantile` is exact and spills |
| `Struct().Eval` | `struct.eval` (2.x) | `WithFields` that keeps only what it names |
| `BinIntervals`, `BinQuantiles`, `BinRanks` | 2.0's spellings of `cut` and `qcut`; `bin_ranks` new | `Cut` and `QCut` answer the first two |
| `LazyFrame.Gather`, `DataFrame.IsSorted(by)`, a Parquet file's key-value metadata | `gather`, `is_sorted`, `read_parquet_metadata` | P2 |
| `Meta()`'s `IsScalar`, `IsLengthPreserving`, `IsRowSeparable`; `SetSorted`; a plan serialized; `json_normalize`; `align_frames` | | P3 |

Polars 2.0's `Categorical(ordering=…)` is gone: categoricals sort by their text,
as ursus's do.
