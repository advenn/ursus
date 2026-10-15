# What ursus lacks, against established dataframe libraries

**Taken on 2026-10-14, at `028f0c6`** (after v0.6.4's step 246), at the maintainer's
request: everything a user of Polars, pandas or DuckDB would reach for that ursus
does not have today.

**How:**

- four read-only surveys checked every item of
  [`dataframe-features.md`](./dataframe-features.md), the Polars-shaped checklist,
  against the public API (`go doc`, the source, the internals where needed);
- each also listed what current Polars, pandas, DuckDB and DataFusion offer beyond
  that checklist;
- a claim here was verified by finding the symbol, or its absence;
- the silent `FillNull` bug in §1 was reproduced by hand.

**The shape of it:**

- the core is broad and correct: types, null and NaN semantics, operators, math,
  joins of every kind, group-by, windows, rolling and dynamic groups, CSV and Parquet,
  spilling everywhere;
- **the largest gap is structural: an expression must keep its column's length.**
  There is no `Expr.Sort`, `Filter`, `Head`, `Slice`, `Gather` or `Unique`, and an
  aggregate is refused in `Select` and `WithColumns`;
- after that, breadth: three missing namespaces (`.arr`, `.bin`, `.cat`), the
  generators (`int_range`, `date_range`), `list.eval`, JSON and IPC I/O, hive
  partitions, selectors, and a frame-level surface (sample, partition_by, frame
  cast) that pandas and Polars users expect;
- SQL is out by an earlier decision.

Priorities are the checklist's (P0 must exist, P1 Polars-class, P2 long tail, P3
maybe never), or inferred where it gives none.

---

## 1. Bugs and stale documents found on the way

Fix these before anything new. **Step 249 fixed every row** (see its record).

| what | where | |
| --- | --- | --- |
| **`lf.FillNull(ursus.FillForward)` fills every null with the constant 2.** `FillStrategy` is a `uint8`, so it satisfies `Operand`, and the frame-level `FillNull` takes it as a literal value. Reproduced: `[1, null, null, 7]` → `[1, 2, 2, 7]`. | frameops.go:352, fill.go:220 | **silent wrong answer** |
| `DynamicOptions`' doc says `Label`, `StartBy` and `IncludeBoundaries` produce an error; all three work | groupby.go:12–22 | stale doc |
| `MapExplode` is documented as a working strategy; it is refused at planning | window.go:18–21 | stale doc |
| `JoinWhere`'s doc names `JoinSuffix`; it takes no options | join.go:221, 235 | doc and API |
| `lazy.go:304` says sort and join stay serial; the join's build and probe are parallel | lazy.go:304 | stale doc |
| The spill format's header says the Parquet writer refuses Int128 and temporal types, and that there are no List or Struct columns | internal/spill/spill.go:5–12 | stale doc |
| `cut.go:38` says an Enum cannot yet be built; `Cast(String → Enum)` builds one | cut.go:38 | stale doc |
| `LazyFrame.Count(ctx)` returns the row count; Polars' `count()` returns each column's non-null count | lazy.go:651 | name trap |
| `Expr.Explode`, `Expr.Unnest`, `Expr.DropNulls` and `Expr.DropNans` exist only to refuse | fill.go:196–320 | API trap |

## 2. The expression model

The structural gap. Each item needs the planner to allow expressions whose length
differs from their input's, or scalars outside a group-by.

| missing | Polars | prio |
| --- | --- | --- |
| **Length-changing expressions:** `Expr.Sort`, `SortBy`, `ArgSort`, `Filter`, `Head`, `Tail`, `Slice`, `Limit`, `Gather`, `GatherEvery`, `Get`, `Unique`, `Reverse`, `DropNulls`, `DropNans`, `Explode`, `Append`, `ExtendConstant`, `Sample`, `Shuffle`, `Rle`, `RleID`, `RepeatBy`, `ArgTrue`, `ArgUnique`. **Built at steps 251–260** for `Sort`, `SortBy`, `Filter`, `Head`, `Tail`, `Slice`, `Gather`, `GatherEvery`, `Get`, `Unique`, `Reverse`, `DropNulls`, `DropNans` and `Explode`, in an aggregation, `Select` and `WithColumns`, with `Unnest` (step 260). Still missing: the rest of the list, `Gather` in `Select`, any of them in `Filter`, and any of them in `.Over`, mapped back onto the partition's rows | all of `Expr` | P1 |
| **Aggregates in `Select` and `WithColumns`:** `Select(Col("x").Sum())` for a frame-wide reduction, and `x - x.mean()` broadcast without `.Over()`. **Built at step 250.** | the default context rules | P0/P1 |
| **Expression-valued arguments:** `IsIn(expr or list column)`; `List().Get(expr)`, `List().Contains(expr)`; `Str().Contains/Replace/Split` with an expression pattern; `Dt().OffsetBy(expr)`. **Built at steps 254–256,** but `IsIn(Col("b").Implode())`, 0.7's scalar subqueries | most namespace methods | P1 |
| **List and Series literals:** `Lit([]int64{...})`. **List literals built at step 257;** Series literals missing | `pl.lit([..])`, `pl.Series` | P1 |
| **Selectors** with set algebra (union, difference, complement), dtype families (temporal, numeric, any Datetime or Decimal or List), name patterns (`starts_with`, `contains`), `first`, `last`, `by_index`. Today: `Col`, `ColRegex`, `ColDType` (exact types), `All`, `Exclude` | `polars.selectors` | P1 |
| **`name.*`:** `keep`, `to_lowercase`, `to_uppercase`, `replace`, `prefix_fields`, `suffix_fields`, `map_fields`. Today: `Prefix`, `Suffix`, `MapName` | `Expr.name` | P2 |
| **`meta.*` and serialization:** `root_names`, `output_name`, `is_column`, `is_literal`, `has_multiple_outputs`, `tree_format`; expressions and plans to and from bytes | `Expr.meta`, `serialize` | P2 |
| **`Expr.Pipe`, `Expr.Where`, `Expr.Inspect`** | | P2 |

## 3. Types

| missing | note | prio |
| --- | --- | --- |
| **Categorical** | only Enum exists; `TypeCategorical` is reserved | P1 |
| **Array** (fixed-size list) | the type constructor exists; no storage, kernels or Arrow export | P1 |
| **Large strings and binary** (64-bit offsets, `BinaryOffset`) | a String column holds at most 2 GiB of characters | P1 |
| **String view layout** | Arrow's view types are converted on import | P2 |
| **UInt128, Float16, Object, Unknown** | Float16 is widened to Float32 on import | P2 |
| **Dictionary, run-end, union, ListView** in memory | decoded or refused on import | P2 |
| `Expr.ToPhysical`, `Reinterpret`, `ShrinkDtype` | `Cast(dt.Physical())` works for temporal types only | P2 |

## 4. Expression namespaces

### 4.1 `.str` (P0)

- **Missing:**
  - `contains_any`, `find_many`, `extract_groups`, `extract_many`, `replace_many`;
  - `split_exact`, `to_titlecase`, `normalize`;
  - `encode` and `decode` (hex, base64);
  - `json_decode`, `json_path_match`.
- **Partial:**
  - `splitn` returns a List, not a Struct;
  - `to_integer` has no `base` or `strict`;
  - `to_decimal` only through `CastLossy(Decimal(p, s))`, with no inference;
  - `replace` has no count `n`;
  - `split` has no `inclusive`;
  - `concat_str` has no `ignore_nulls`.

### 4.2 `.dt` (P0/P1)

- **Missing:**
  - `iso_year`, `century`, `millennium`;
  - `is_business_day`, `add_business_days`, `business_day_count`;
  - `replace` (set a component);
  - `base_utc_offset`, `dst_offset`;
  - `with_time_unit`;
  - `total_milliseconds`, `total_microseconds`, `total_nanoseconds`.
- **Built in 0.6.5:** `dt.date()` and `dt.time()`, the local date and time (step
  249).
- **Partial:**
  - `epoch` in whole seconds only;
  - `timestamp` and `from_epoch` only through casts;
  - `second(fractional=)`;
  - `replace_time_zone`'s ambiguity policy is one for the whole column, not a column of choices.

### 4.3 `.list` (P1)

- **Missing:**
  - `eval` and `pl.element()`;
  - `filter`, `agg`, `concat`;
  - `shift`, `diff`;
  - `count_matches`, `sample`;
  - `any`, `all`, `item`;
  - `to_array`.
- **Partial:**
  - `sort` has no `nulls_last`;
  - `get` has no `null_on_oob` (always null);
  - `n_unique` does not count null;
  - the sum of an empty list is null, where Polars gives 0.
- **Built in 0.6.5:** `median`, `std`, `var` and `gather_every` (step 253); `Explode` as
  an expression (259).

### 4.4 `.struct` (P1)

- **Missing:** `drop`; `field` of several names or a regex.
- **Built in 0.6.5:** `Unnest` as an expression (step 260).

### 4.5 Whole namespaces

- **`.cat` (P1):** `get_categories`, `starts_with`, `ends_with`, `len_bytes`,
  `len_chars`, `physical`, `to_local`. `Str()` refuses an Enum.
- **`.arr` (P2):** all of it, `dot` and `to_list` too.
- **`.bin` (P2):** `contains`, `starts_with`, `ends_with`, `get`, `head`, `tail`,
  `slice`, `size`, `encode`, `decode`, `reinterpret`.

### 4.6 Top-level functions (P0/P1)

- **Missing:**
  - **generators:** `int_range`, `int_ranges`, `arange`, `linear_space`,
    `date_range`, `date_ranges`, `datetime_range`, `datetime_ranges`, `time_range`;
  - **constructors and folds:** `repeat`, `ones`, `zeros`, `concat_list`,
    `concat_arr`, `fold`, `reduce`, `cum_fold`, `cum_reduce`, `cum_sum_horizontal`;
  - **selection and ordering:** `nth`, `element`, `field`, `arg_where`,
    `arg_sort_by`;
  - **other:** `format`, `rolling_corr`, `rolling_cov`, `business_day_count`,
    `map_groups`, `sql_expr`; the `pl.sum("a")`-style shorthands.
- **Partial:**
  - `date`, `datetime`, `time` and `duration` as Go literals only, not from
    component expressions;
  - `first` and `last` as aggregates only;
  - `row_index` as a frame operation only.

## 5. Computation and aggregation

| missing | prio |
| --- | --- |
| **Statistics:** `skew`, `kurtosis`, `entropy`, `mode`, `hist`, `approx_n_unique`; Spearman `corr` | P1 |
| **Search and hashing:** `search_sorted`, `index_of`, `hash`, `unique_counts`, `value_counts` on an expression, `peak_min`, `peak_max`, `dot` | P1 |
| **Integers as bits:** `& \| ^ ~` and shifts; `bitwise_and/or/xor` aggregates (`And`/`Or`/`Xor` take Booleans only) | P1 |
| **Aggregate odds and ends:** `has_nulls`, `is_empty`, `nan_min`, `agg_groups` | P1 |
| **Rolling:** `skew`, `kurtosis`, `rank`, `map`; weights; an integer index (`"2i"` periods) for `_by`; `rolling_corr`, `rolling_cov`; the generic `Expr.rolling(index_column)` | P1 |
| **EWM:** `ewm_mean_by`, `ewm_sum`, `ewm_sum_by` | P1 |
| **Other:** `cumulative_eval`, `round_sig_figs`, `cot`, numeric `truncate`, a rounding mode (half-to-even), a one-sided `clip`, `cut`/`qcut` returning Categorical with `include_breaks` | P1/P2 |

## 6. Frame operations

| missing or partial | prio |
| --- | --- |
| **`unique(keep=last/any/none)`**; only first today | P0 |
| **Frame `fill_null(strategy, limit)`**, which is the §1 bug's real fix; frame `null_count`, `std`, `var`, `product`, `shift`, `cum_*` shortcuts | P0/P1 |
| `slice` with a negative offset | P0 |
| **`cast` of a frame** (a map of columns, or by dtype); `match_to_schema` | P1 |
| **`sample`, `shuffle`** (fraction or n, seed) | P1 |
| **`partition_by`** into frames | P1 |
| **`upsample`**; `interpolate` and `drop_nans` on frames | P1 |
| **`is_duplicated`, `is_unique` and `n_unique` over several columns** | P1 |
| **`pivot` of several `On` columns, and a lazy pivot** with its columns given | P1 |
| `concat` align modes and a type-strict vertical mode; `DataFrame.Concat` modes | P1 |
| join: emit a computed key column; `maintain_order` right, left-right and right-left; asof `tolerance` on numeric keys; asof `coalesce`; `JoinWhere` suffix | P1 |
| group-by: `head`, `tail`, `having`, `map_groups`, iterating over groups; an integer index for `GroupByDynamic` and `Rolling`; `.over(order_by)` of an aggregate; mapping `explode` and `join` | P1/P2 |
| `to_dummies`, `unstack`, `gather`, `gather_every`, `insert_column`, `extend`, `update`, `clear`, `is_empty`, `equals`, `glimpse`, `estimated_size`, `hash_rows`, `interpolate_by`, `with_columns_seq` | P2 |
| `unpivot` without `On` (every non-index column); `transpose` of mixed String and number, with names from a column | P2 |
| **An eager `Series` with operations:** it is a read-only view (`Get`, `Values`, `Map`, `Fold`); sort, unique, head, fill and aggregates go through a frame | P1 |
| Build a frame from Go structs (`FromRows[T]`); a lazy row iterator, `to_dicts`, `rows_by_key` | P1 |

## 7. I/O

| missing or partial | prio |
| --- | --- |
| **JSON and NDJSON:** read, scan, write, sink | P1 |
| **Arrow IPC built in:** scan or read a file or stream, write, sink. Today it is arrow-go glue through `ScanArrow` and `Record()` | P1 |
| **Hive partitioning** on read (keys, types, dates); **partitioned writes** by key | P1 |
| **Multi-file schema evolution:** `missing_columns`, `extra_columns`, union by name; `include_file_paths`; `**` globs | P1 |
| **Schema overrides on Parquet and Arrow scans;** CSV date inference; compressed CSV input (gzip, zstd); CSV writer quote style and date and float formats | P1 |
| **Cloud object stores** by URI (`s3://`, `gs://`, `https://`), with listing, credentials and retries. Today you bring a `ReaderAt` | P2 |
| **Parquet:** page index and bloom filters (read and write), compression level, per-column options, a metadata cache | P2 |
| Avro write; Excel; Delta Lake; Iceberg; databases through ADBC | P2/P3 |
| Predicate pushdown into CSV and Arrow scans | P2 |

## 8. Engine and tooling

These belong to a performance release more than to features.

| missing or partial | prio |
| --- | --- |
| **`profile()` / EXPLAIN ANALYZE:** each operator's time and rows; a physical-plan explain; `Explain` honouring `OptFlags` | P1 |
| **Slice pushdown** past projections, unions and joins, and of an offset slice | P1 |
| **Predicate pushdown through group-by keys and window partition keys** | P1 |
| **Expression-level CSE;** clustering of `with_columns`; dropping a computed column nothing reads | P1 |
| **Multi-join reordering** on statistics; sortedness propagation and a merge join | P2 |
| **Parallel sort;** a work-stealing morsel scheduler | P1 |
| `collect_all` and several sinks from one plan; `cache()`; plan serialization | P2 |
| Display settings (rows, width, `glimpse`) | P1 |
| Spill-file compression; approximate aggregates | P2 |
| **SQL** (`SQLContext`, `.sql()`, `sql_expr`) | out by decision |
| GPU, plugins, Python bindings, distributed execution | P3 |

## 9. Where ursus differs from Polars on purpose

These are differences, not gaps; each is documented. A completeness release should
decide whether to keep or switch each one.

- The sum or product of an all-null group is null, not 0 or 1.
- `Max` propagates NaN. Polars' `max` ignores it, and `nan_max` propagates it.
- `n_unique` does not count null; Polars counts it as a value.
- `Round` rounds half away from zero; Polars rounds half to even.
- `Div` and `Pow` always return a float.
- `Cut` returns String, not Categorical.
- An expression's `TopK` and `BottomK` return a List.
- `Time` takes a unit; Polars' is nanoseconds.
- `GroupByDynamic` with keys puts the index column first and the keys after it;
  Polars puts the keys first (found at step 251).
- `List().Get` is null out of range, with a constant or an expression index; Polars'
  `list.get` errs unless `null_on_oob`. A float index is refused; Polars takes it
  (step 254).
- `Str().Replace` and `ReplaceAll` with a null value a row give null; Polars keeps
  the string as it was (step 255).
- A `Select` of literals alone is a row for each of the frame's rows; Polars' is one
  row. A `Select` of aggregates, with or without literals, is one row, as Polars'
  (step 250).
- `DropNans` of a column that is not a float is refused; Polars returns it as it was
  (steps 253 and 258).
- `Explode` of a column that is not a List is refused; Polars returns it as it was
  (step 259).
- `IntRange` and `IntRanges` refuse a float argument, which Polars casts; a null
  argument to a scalar generator gives no rows, where Polars refuses it; a literal
  integer in `Repeat` is Int64, where Polars makes it Int32 (step 263).

## 10. What ursus has that most others do not

A completeness release should keep these intact:

- spilling for every blocking operator, with bit-exact answers;
- a memory budget on by default;
- order-deterministic output at any thread count;
- `ursustest` with golden plans;
- validation of every benchmark answer against DuckDB.
