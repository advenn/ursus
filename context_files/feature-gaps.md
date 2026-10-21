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

**Found since:**

| what | where | |
| --- | --- | --- |
| **A generator whose argument is an aggregate of the frame refuses under a memory limit beside a string column.** `WithColumns(IntRange(0, Len(), 1))` stacks the frame beside the generator's branch, a keyless aggregate that reads all its input before it answers, so the frame's side is held meanwhile; at the limit the aggregate, with nothing to spill, refuses, rather than the stack spilling what it holds. An int-only frame's rows are the scan's own buffers, uncharged, so it never showed. A literal end streams, and so does `WithRowIndex` (found at step 287). | internal/physical, the stack and the budget | **refusal** |
| **A float aggregate's last bits depend on the thread count.** Its partial states merge in an order the threads set: of eight probed by key, `Sum`, `Mean`, `Var`, `Std`, `Skew`, `Kurtosis` and `Entropy` differ between one thread and eight, and `Corr` does not. A spilled run equals the one-thread run to the bit. Row order is deterministic, as §10 says (found at step 287). | the accumulators' Merge | **not bit-exact** |
| **A streamed rolling var or std differs from the answer in memory in its last bits.** Over the whole frame under a memory limit, a window's state is rebuilt from each batch's history rows, so its periodic recompute falls at other rows: 35–52% of rows over `sin(i)·1000`. Step 284's skew, kurtosis, cov and corr do too. The streaming test's floats are quarters, whose sums are exact, so it never showed. The fix is to carry the kernel's state across batches, as an ewm's is (found at step 284). | internal/physical/winstream.go, internal/kernel/rolling.go | **not bit-exact** |
| **`Rank` ranks a null, as the smallest value;** Polars leaves a null's rank null and ranks the rest among themselves. `[5, null, 4]` ranks `[3, 1, 2]` here and `[2, None, 1]` there, in a window and in `List().Eval` alike (found at step 268). | internal/kernel/window.go `winRank`, and the streamed rank (`RankNext`) | **wrong answer** |
| **A printed frame padded its cells by bytes,** so a row holding a letter outside ASCII came out a column short: "Zoë" is three columns and four bytes. Found writing the user guide's strings chapter; fixed at step 296, cells padded by their width on a terminal | frame.go | fixed |
| **A typed view of a `Date` or a `Datetime` is its integer.** `df.Column[time.Time]` and `df.At[time.Time]` refuse one, "no typed view for time.Time"; `Column[int32]` gives a Date's days and `Column[int64]` a Datetime's ticks. `Rows[T]` decodes either into a `time.Time`. Polars' `to_list` gives dates and datetimes. For 0.6.7's V3, the eager Series (step 297) | series.go, internal/data | open |
| **A naive `Datetime` is written with a `Z`,** as though it were UTC: `Datetime(s)` prints `2026-03-08T06:30:00Z`, and the CSV writer, which shares `FormatTemporal`, writes it so, which another reader takes as UTC; so does a cast to String (step 298). Polars writes a naive instant with no offset. A change of the CSV format, for 0.6.8's I/O (step 296) | dtype/temporal.go:212 | open |

## 2. The expression model

The structural gap. Each item needs the planner to allow expressions whose length
differs from their input's, or scalars outside a group-by.

| missing | Polars | prio |
| --- | --- | --- |
| **Length-changing expressions:** `Expr.Sort`, `SortBy`, `ArgSort`, `Filter`, `Head`, `Tail`, `Slice`, `Limit`, `Gather`, `GatherEvery`, `Get`, `Unique`, `Reverse`, `DropNulls`, `DropNans`, `Explode`, `Append`, `ExtendConstant`, `Sample`, `Shuffle`, `Rle`, `RleID`, `RepeatBy`, `ArgTrue`, `ArgUnique`. **Built at steps 251–260** for `Sort`, `SortBy`, `Filter`, `Head`, `Tail`, `Slice`, `Gather`, `GatherEvery`, `Get`, `Unique`, `Reverse`, `DropNulls`, `DropNans` and `Explode`, in an aggregation, `Select` and `WithColumns`, with `Unnest` (step 260). Built since: `RepeatBy` (step 263), `ArgSort`, `ArgTrue` and `ArgUnique` (267), `Rle` and `RleID` (281), and `Append`, `ExtendConstant`, `Sample` and `Shuffle` (282). Still missing: `Limit`, Polars' alias of `Head`, `Gather` in `Select`, any of them in `Filter`, and any of them in `.Over`, mapped back onto the partition's rows | all of `Expr` | P1 |
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
| **A List or a Struct column as a key** | `Unique`, `UniqueKeep`, `GroupBy`, `Over` and `Join` each refuse one ("cannot use a List(Int64) column as a key"), as `RleID`, `Rle` and `Mode` do (§9); Polars compares them (found at step 289) | P1 |

## 4. Expression namespaces

### 4.1 `.str` (P0)

- **Missing:**
  - `contains_any`, `find_many`, `extract_groups`, `extract_many`, `replace_many`;
  - `split_exact`, `to_titlecase`, `normalize`;
  - `encode` and `decode` (hex, base64);
  - `json_decode`, `json_path_match`.
- **Built in 0.6.6:**
  - `contains_any`, `find_many`, `extract_groups`, `extract_many`, `replace_many`,
    `split_exact`, `to_titlecase`, `normalize`, `split`'s `inclusive`, and `splitn` as
    a Struct (step 270). Their patterns are constants; Polars takes an expression too;
  - `encode` and `decode`, `to_integer`'s `base`, `dtype` and `strict`, `replace`'s
    `n`, `concat_str`'s `ignore_nulls`, `json_path_match` and `json_decode` (step 271).
    `json_decode` takes its dtype and does not infer one. The JSONPath is `$` then
    `.key`, `['key']`, `[n]` and `..key`, with no wildcard, slice or filter.
- **Partial:**
  - `to_decimal` only through `CastLossy(Decimal(p, s))`, with no inference.

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
- **Built in 0.6.6:**
  - `business_day_count` (step 265);
  - every function listed missing above, with `epoch` and `timestamp` in a unit and
    `fractional` on `second` and every total (step 272).
  - A Polars keyword flag is a name's suffix: `total_seconds(fractional=True)` is
    `TotalSecondsFractional()`, `epoch("ms")` `EpochIn(Milli)`, `epoch("d")`
    `EpochDays()`.
- **Partial:**
  - `replace_time_zone`'s ambiguity policy is one for the whole column, not a column
    of choices; so are `replace`'s and `add_business_days`' roll.

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
- **Built in 0.6.6:** `eval` and `pl.element()`, `filter` and `agg` (step 268);
  `concat`, `shift`, `diff`, `count_matches`, `any`, `all`, `item`, `sample` and
  `sort` with `nulls_last` (step 269). Left: `to_array`, with an Array type.

### 4.4 `.struct` (P1)

- **Missing:** `drop`.
- **Built in 0.6.5:** `Unnest` as an expression (step 260).
- **Built in 0.6.6:** `field` of several names, a regex or `"*"`, and a field named
  after itself (step 275).

### 4.5 Whole namespaces

- **`.cat` (P1):** built on Enum in 0.6.6 (step 273): `get_categories`,
  `starts_with`, `ends_with`, `len_bytes`, `len_chars`, `slice` and `physical`, and
  `Str()` reads an Enum as its text. `to_local` and `is_local` are deprecated in
  Polars 1.44 and are not built; `to`, from the physical indices, is unstable there
  and waits. Categorical itself is §3's.
- **`.arr` (P2):** all of it, `dot` and `to_list` too.
- **`.bin` (P2):** built in 0.6.6 (step 274): `contains`, `starts_with`, `ends_with`,
  `get`, `head`, `tail`, `slice`, `size`, `encode`, `decode` and `reinterpret`, with
  expressions where Polars takes them. `reinterpret` to an Array waits for the Array
  type. `Str()` now refuses a Binary, as Polars' `.str` does.

### 4.6 Top-level functions (P0/P1)

- **Missing:**
  - **generators:** `int_range`, `int_ranges`, `arange`, `linear_space`,
    `date_range`, `date_ranges`, `datetime_range`, `datetime_ranges`, `time_range`;
  - **constructors and folds:** `repeat`, `ones`, `zeros`, `concat_list`,
    `concat_arr`, `fold`, `reduce`, `cum_fold`, `cum_reduce`, `cum_sum_horizontal`;
  - **selection and ordering:** `nth`, `element`, `field`, `arg_where`,
    `arg_sort_by`;
  - **other:** `format`, `business_day_count`,
    `map_groups`, `sql_expr`; the `pl.sum("a")`-style shorthands.
- **Partial:**
  - `date`, `datetime`, `time` and `duration` as Go literals only, not from
    component expressions;
  - `first` and `last` as aggregates only;
  - `row_index` as a frame operation only.

## 5. Computation and aggregation

| missing | prio |
| --- | --- |
| **Statistics:** built: `hist` in step 286; `skew`, `kurtosis` and `approx_n_unique` in step 276; `entropy`, `mode` and Spearman `corr` in step 277 | P1 |
| **Search and hashing:** built: `dot` in step 276; `search_sorted`, `index_of`, `hash`, `peak_min` and `peak_max` in step 279, a search's value a Go value only; `unique_counts` and `value_counts` on an expression in step 280 | — |
| **A window inside a window:** `Col("a").Shift(1).CumSum()` is refused, "compute the inner window into a column with WithColumns first"; Polars nests them (found at step 281) | P1 |
| **Holistic aggregates that do not spill:** `CorrSpearman`'s pairs and `Mode`'s values, a group's, refuse in a group-by past the limit, where `Median`, `Quantile` and `NUnique` spill theirs (found at step 287) | P1 |
| **An aggregate inside an aggregate's input:** `Col("a").Sub(Col("a").Mean()).Abs().Sum()` is refused, in a `Select` or an `Agg`, as "not an aggregate"; Polars takes it (found at step 279) | P1 |
| **Integers as bits:** built: `& \| ^ ~` of integers in step 278, the `bitwise_and/or/xor` aggregates in step 276. Polars has no shifts | — |
| **Aggregate odds and ends:** `agg_groups`. `has_nulls`, `is_empty`, `nan_min` and `nan_max` built in step 276 | P1 |
| **Rolling:** `map`; weights; an integer index (`"2i"` periods) for `_by`; `rolling_rank_by` and Polars' `random` rank; the generic `Expr.rolling(index_column)`. `skew`, `kurtosis`, `rank`, `rolling_cov` and `rolling_corr` built at step 284 | P1 |
| **EWM:** an integer by with a `"2i"` half-life for `ewm_mean_by` and `ewm_sum_by`. `ewm_sum`, `ewm_mean_by` and `ewm_sum_by` built at step 285 | P2 |
| **Other:** `cut`/`qcut` returning Categorical with `include_breaks`. `cumulative_eval` built at step 286. `round_sig_figs`, `cot`, numeric `truncate`, the half-to-even mode and a one-sided `clip` were built at step 283 | P1/P2 |

## 6. Frame operations

`v0.6.7-scope.md` takes this section (step 288).

| missing or partial | prio |
| --- | --- |
| built: `unique(keep=)` with `maintain_order=True`, as `UniqueKeep`, in step 289 | — |
| **A frame `fill_null` strategy's `limit`** (the strategies were built at step 249). A frame's `shift`, `first` and `last` built in step 293; its `null_count`, `sum`, `mean`, `median`, `min`, `max`, `std`, `var`, `quantile` and `product` in step 300. `approx_n_unique` is deprecated on a frame in Polars 1.44.1, and is `Select(All().ApproxNUnique())` | P0/P1 |
| built: `slice` with a negative offset, in step 291 | — |
| built: a frame's `cast`, by name, by type and for every column, in step 298; `match_to_schema` in step 299 | — |
| built: a frame's `sample` and `shuffle` (fraction or n, seed), in step 292 | — |
| **`partition_by`** into frames | P1 |
| **`upsample`**; `interpolate` and `drop_nans` on frames | P1 |
| built: `is_duplicated`, `is_unique` and `n_unique` over several columns, in step 290 | — |
| **`pivot` of several `On` columns, and a lazy pivot** with its columns given | P1 |
| `concat`'s relaxed, horizontal and align modes; `ConcatStrict` meets Int32 and Int64 at Int64, where Polars' vertical refuses them, and refuses Int64 beside Float64, which `vertical_relaxed` meets; `DataFrame.Concat` modes | P1 |
| join: emit a computed key column; `maintain_order` right, left-right and right-left; asof `tolerance` on numeric keys; asof `coalesce`; `JoinWhere` suffix | P1 |
| group-by: `head`, `tail`, `having`, `map_groups`, iterating over groups; an integer index for `GroupByDynamic` and `Rolling`; `.over(order_by)` of an aggregate; mapping `explode` and `join` | P1/P2 |
| `to_dummies`, `unstack`, `gather`, `insert_column`, `extend`, `update`, `glimpse`, `estimated_size`, `hash_rows`, `interpolate_by`, `with_columns_seq`. `gather_every`, `clear`, `is_empty` and `equals` built in step 293 | P2 |
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
| **A count reads every column:** `Select(Len())` over a Parquet scan projects all of them, where Polars reads none (found at step 287) | P1 |
| **Predicate pushdown through group-by keys and window partition keys** | P1 |
| **Expression-level CSE;** clustering of `with_columns`; dropping a computed column nothing reads | P1 |
| **Multi-join reordering** on statistics; sortedness propagation and a merge join | P2 |
| **Parallel sort;** a work-stealing morsel scheduler | P1 |
| `collect_all` and several sinks from one plan; `cache()`; plan serialization | P2 |
| Display settings (rows, width, `glimpse`) | P1 |
| Spill-file compression; approximate aggregates | P2 |
| **A heap bound that trips:** `TestTemporalMediansSpillTheirValues` holds the live heap's growth under 10 MB, a measure of when the collector runs; under test-all's load it read 11 MB once (step 292) and passed fifteen reruns | P2 |
| **SQL** (`SQLContext`, `.sql()`, `sql_expr`) | out by decision |
| GPU, plugins, Python bindings, distributed execution | P3 |

## 9. Where ursus differs from Polars on purpose

These are differences, not gaps; each is documented. A completeness release should
decide whether to keep or switch each one.

- The sum or product of an all-null group is null, not 0 or 1.
- `Max` propagates NaN. Polars' `max` ignores it, and `nan_max` propagates it.
- `n_unique` does not count null; Polars counts it as a value.
- `Round` rounds half away from zero by default; Polars rounds half to even, which
  `RoundHalfToEven` selects (step 283).
- `Round`, `Truncate` and `RoundSigFigs` refuse a Decimal, whose digits Polars rounds
  (step 283).
- `Cot`, like `Tan`, is Go's tangent: within 3 ulps of Polars' glibc one over
  ordinary values, and further near a multiple of π/2, cot(π/2) an absolute 1e-30
  apart (step 283).
- A float bound of `Clip` on an integer column promotes the column, as a fill value
  does; Polars casts the bound to the column's type, so `clip(2.5)` of an Int64 is 2
  there and 2.5 here (step 283).
- A literal has its Go value's type: `Col("a").Add(3)` of an Int32 column is Int64,
  and `Mul(2.5)` of a Float32 one Float64, where Polars' literal takes the column's
  type. A fill meets the column, as Polars' does (`fill.go`'s weak literals).
  Found writing the site's expressions page (step 294).
- `Remove` drops a row whose predicate is null, as `Filter` does; Polars' `remove`
  keeps it (found writing the user guide, step 295).
- A right join keeps the left frame's row order, its unmatched right rows after them,
  and the key where the left frame has it; Polars gives the right frame's order, the
  left's other columns first and the key among the right's (step 295).
- A join on keys of different names keeps both key columns, `JoinLeftOn(a)` and
  `JoinRightOn(b)` giving `a` and `b`; Polars drops the right one.
  `JoinCoalesce(true)` drops it here (step 295).
- `Unpivot` gives each row's columns together, then the next row's; Polars gives
  every row's first column, then every row's second. Its doc chose it: ursus unpivots
  a batch at a time, and Polars' order would depend on the batches (step 296 lists
  it).
- `GroupByDynamic` makes every window of the grid, so a period with no rows is a row
  of `Len` 0; Polars leaves empty windows out. Its doc chose it (step 296 lists it).
- `ToInteger`, `ToDate` and `ToDatetime` turn what does not parse into null; Polars'
  `to_integer` and `to_date` fail by default, `strict=True`. `Strptime` and
  `ToIntegerWith` take strict (step 296).
- A pivot's cell with no value is null under `Sum`, as any empty sum is; Polars' is 0
  (step 296).
- A fill of a whole-number float keeps an integer column's type: `FillNullWith(2.0)`
  and a frame's `FillNull(0.0)` of an Int64 column give Int64, and only a fraction,
  `0.5`, widens it to Float64. Polars widens it for any float (step 297).
- A cast of a float with a fraction to an integer is refused, "not representable",
  and is null when lossy; Polars truncates it toward zero, strict or not: 1.5 is 1,
  −1.7 is −1. ursus's casts are exact or refused; `Truncate(0)` first for Polars'
  answer. Listed by step 298, where a frame's cast inherits it.
- A cast of an instant to text is ISO 8601's, `2026-01-01T00:00:00.000000`, where
  Polars writes a space for the `T`. A naive one gains a `Z`, §1's open row (step
  298).
- `MatchToSchema`'s computed fill meets the schema's type as a column of the frame
  would, widened only as its casts allow, or is refused; Polars 1.44.1 keeps the
  expression's own type, so `missing_columns={"c": pl.col("b")}` of an Int32 `b`
  gives an Int32 `c` under an Int64 schema, and of a String one a String `c` (step
  299).
- `Product` is a Float64 for every number, an integer's too, where Polars keeps an
  integer's type: an Int64 product wraps after about twenty factors, and i128 has no
  multiplication (`agg.go`). A count, `Len`, `Count`, `NullCount`, `NUnique`, is a
  Uint64, where Polars' is a UInt32. Listed by step 300, where a frame's aggregates
  answer so.
- A float `Var` or `Std` is Welford's one pass, shifted by the first value, where
  Polars takes two passes, so they can be an ulp apart on one thread too:
  var([1.5, 2.5, 4.0]) is 1.5833333333333335 here and 1.5833333333333333 there.
  var([1, 2, 4], ddof 0) agrees, 1.5555555555555554 in both (step 300).
- A frame's `Median` of a Decimal answers, as `Expr.Median` does; Polars 1.44.1's
  `DataFrame.median` gives null for one, though its `quantile` answers (step 300).
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
- `MakeDuration` and `FromEpoch` refuse a total past int64, which Polars wraps; a
  duration of columns is named `duration`, where Polars names it `literal` (step 265).
- `ConcatList` refuses a number beside a string, which Polars casts to text (step
  266).
- `ArgSortBy` of several keys is refused in an aggregation, where Polars 1.44.1
  answers with positions past the group's end (step 267).
- An expression is named after its leftmost column, and an Alias, a struct field's
  name or a literal counts only at the root. Polars names it after its leftmost
  input's own name: `Col("a").Alias("x").Add(1)` is `x` there and `a` here,
  `Col("s").Struct().Field("c").Mul(2)` `c` there and `s` here, and
  `Lit(1).Add(Col("a"))` `literal` there and `a` here. A field alone, and each field
  of `Field` of several names, is named after the field, as Polars names it (step
  275).
- `RleID` and `Rle` refuse a Struct, which Polars runs (step 281).
- `RollingSkew` is Polars' to the bit but for about one in a thousand, an ulp apart:
  its m2^1.5 is rounded once, where glibc's pow, Polars', errs by up to 0.52 ulp
  (step 284). `RollingCov` and `RollingCorr` keep Polars' formula on ursus's rolling
  means, so their last bits are not Polars'.
- `Hist`'s category is text, where Polars' is a Categorical, as `Cut`'s labels are
  (step 286).
- `CumulativeEval` is refused in `GroupBy(...).Agg`, as every window is, where Polars
  gives a list a group; and inside `List().Eval` (step 286).
- A frame's `ShiftFill` with a text fill refuses a number column, which Polars makes
  text; `Equals` finds a Date unequal to its day count, which Polars finds equal
  (step 293).
- `EwmMeanBy` and `EwmSumBy` take rows of one instant in the window's order, where
  Polars' unstable sort takes them as it finds them; they take an Int8 or an Int16
  value, which Polars refuses (step 285).
- `RollingRank` with `RollingCenter` ranks a row whose window's last row is null;
  Polars answers null there, checking the end's validity (step 284).
- `Append` with upcast meets numbers only: text and a number are refused, which Polars
  meets as text (step 282). `Sample` and `Shuffle` draw ursus's values, not Polars'.
- `ValueCounts` without `sort` gives the values in the order they first appear, where
  Polars' order is its hash table's (step 280).
- `Entropy` of a group of no value is null, as `Sum`'s is, where Polars' is −0.0.
  `Mode` without `maintainOrder` is sorted, where Polars' order is its hash table's,
  and refuses a Struct or a List, which Polars counts. `CorrSpearman` takes numbers;
  Polars ranks text too (step 277).
- `BaseUtcOffset` and `DstOffset` read the system's zone database, so where it is in
  the rearguard form, as Ubuntu's is, Dublin's and Casablanca's daylight-saving time
  is positive. Polars bundles the main form, where it is negative. Other zones agree
  (step 272).

## 10. What ursus has that most others do not

A completeness release should keep these intact:

- spilling for every blocking operator, with bit-exact answers but a streamed rolling
  var, std, skew, kurtosis, cov or corr (§1);
- a memory budget on by default;
- order-deterministic output at any thread count, but a float aggregate's last bits
  (§1);
- `ursustest` with golden plans;
- validation of every benchmark answer against DuckDB.
