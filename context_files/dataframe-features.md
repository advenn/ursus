# Feature Taxonomy of Modern DataFrame Libraries

An exhaustive catalogue of what a "complete" modern dataframe library provides, using
Polars as the reference implementation (its API index is the most complete public
enumeration of the problem space). Every list here is a **requirements checklist for
`ursus`**, annotated with priority.

Priority key: **P0** = must exist for v0.1 to be useful · **P1** = needed for
"Polars-class" · **P2** = completeness / long tail · **P3** = maybe never.

Companion docs: [`dataframe-landscape.md`](./dataframe-landscape.md) ·
[`ursus-api.md`](./ursus-api.md) · [`go-1.27-release-notes.md`](./go-1.27-release-notes.md)

---

## 1. Type system

### 1.1 Physical types (Polars `DataType` enum, verified against Rust docs)

| Category | Types | Priority |
| --- | --- | --- |
| Boolean | `Boolean` (bit-packed) | P0 |
| Signed int | `Int8`, `Int16`, `Int32`, `Int64`, `Int128` | P0 (128: P2) |
| Unsigned int | `UInt8`, `UInt16`, `UInt32`, `UInt64`, `UInt128` | P0 (128: P2) |
| Float | `Float16`, `Float32`, `Float64` | P0 (f16: P2) |
| Decimal | `Decimal(precision usize, scale usize)` — 128-bit backing | P1 |
| String | `String` (UTF-8, variable length) | P0 |
| Binary | `Binary`, `BinaryOffset` | P1 |
| Temporal | `Date` (i32 days since epoch), `Time` (i64 ns since midnight), `Datetime(TimeUnit, Option<TimeZone>)`, `Duration(TimeUnit)` | P0 |
| Nested | `List(Box<DataType>)` (variable len), `Array(Box<DataType>, usize)` (fixed len), `Struct(Vec<Field>)` | P1 |
| Dictionary-ish | `Categorical(Categories, CategoricalMapping)` — runtime-inferred; `Enum(FrozenCategories, ...)` — predeclared | P1 |
| Special | `Null`, `Object(&'static str)`, `Unknown(UnknownKind)` | P0 (`Null`), P2 (`Object`) |

`TimeUnit` ∈ {nanoseconds, microseconds, milliseconds}. Arrow additionally has seconds.

### 1.2 Semantics that must be nailed

- **Null ≠ NaN.** Null is "absent"; NaN is a float value. They need separate predicates
  (`is_null` / `is_nan`) and separate fill operations (`fill_null` / `fill_nan`).
  Nearly every naive dataframe library gets this wrong.
- **Float ordering:** Polars deviates from IEEE 754 for sorting/grouping — NaN compares
  *equal to* other NaNs and *greater than* all non-NaN values. Necessary for total order
  in sorts and hash equality in group-by. Document the deviation explicitly.
- **Null propagation in arithmetic:** `null + 1 == null`. In aggregations, nulls are
  *skipped* by default (`sum` of `[1, null, 2]` is `3`), matching SQL.
- **Null in comparisons:** `null == null` is `null` (three-valued logic), but a separate
  `eq_missing` / `ne_missing` treats nulls as equal. Both are required.
- **Null in joins:** `nulls_equal` flag decides whether null keys match.
- **Null in `group_by`:** null forms its own group.
- **Logical vs physical type:** `Categorical` is physically `UInt32` indices; `Date` is
  physically `Int32`. Need `to_physical()` to drop to the storage representation and a
  `Field` carrying the logical type.

### 1.3 Schema

- Ordered `name → DataType` mapping. Column names are **unique** and **ordered**.
- Inferable from data with an override hook; `schema_overrides` on all readers.
- **Schema resolution must be possible without executing the plan** — this is what makes
  `LazyFrame.collect_schema()` and IDE-time validation work. Every operator needs a
  `Schema → Schema` function independent of data.
- Schema-related utilities: `match_to_schema` (reorder/cast to a target),
  `cast` (whole frame), `collect_schema`.

---

## 2. Memory layout (Apache Arrow)

Requirements for the storage layer, from the Arrow columnar spec:

- **Alignment:** buffers 8-byte aligned minimum, **64-byte recommended** — this is what
  removes the scalar prologue from SIMD loops. Pad lengths to 64-byte multiples too.
- **Validity bitmap:** LSB-numbered, `ceil(len/8)` bytes, `1` = valid, omittable when
  `null_count == 0`. Null slots may contain arbitrary bytes.
- **Fixed-width:** single values buffer.
- **Variable binary/utf8:** `len + 1` monotonically increasing offsets (i32 or i64) +
  data buffer. First offset normalizes to 0.
- **View layout:** 16-byte views; ≤ 12-byte strings inlined; longer store
  `{len, 4-byte prefix, buffer idx, offset}`. The prefix short-circuits most comparisons.
  **High value for string-heavy analytics — plan for it.**
- **List:** offsets + child. **ListView:** offsets + sizes (out-of-order / shared child
  ranges). **FixedSizeList:** slot `j` occupies child `[j*N, (j+1)*N)`.
- **Struct:** equal-length children; effective validity = parent AND child.
- **Union:** dense (types + offsets + children) / sparse (types + full-length children).
- **Dictionary:** integer indices + dictionary array; null count from indices only.
- **Run-end encoded:** monotonic `run_ends` + `values`; random access `O(log n)`.
- **Chunking:** a column is a *sequence* of arrays (`ChunkedArray`). Operations must
  handle chunk boundaries; `rechunk()` consolidates. Chunk-awareness is what makes
  `concat`/`vstack` O(1).
- **Slicing is zero-copy:** offset + length view over shared buffers.
- **Reference counting / ownership:** `arrow-go` uses explicit `Retain`/`Release`.
  We must decide whether to expose that or hide it behind GC-managed wrappers.

---

## 3. Core data structures

| Structure | Definition | Notes |
| --- | --- | --- |
| **Series** | 1-D, homogeneously typed, named, nullable, chunked | Polars: `Series`. Has ~350 methods, mostly mirroring `Expr`. |
| **DataFrame** | Ordered set of equal-length, uniquely named Series | **No row index.** Row order is a property, not a label space. |
| **LazyFrame** | An unexecuted query plan producing a DataFrame | The primary API for real work. |
| **Expr** | A lazy, composable description of a column transformation | The central abstraction. |
| **GroupBy / LazyGroupBy** | Intermediate handle between `group_by` and `agg` | |
| **Schema** | Ordered name→dtype map | Must be computable without data. |

**Design note:** Polars maintains three near-parallel APIs (`DataFrame`, `LazyFrame`,
`Series`/`Expr`) with large duplication. `LazyFrame` has ~90 methods, `DataFrame` ~130,
`Series` ~350, `Expr` ~500. **We should generate the eager API from the lazy one**
(`df.Select(...)` ≡ `df.Lazy().Select(...).Collect()`) to avoid triple maintenance.

---

## 4. The expression / context model  ← *the heart of the design*

### 4.1 Expressions

An expression is a **lazy description of a transformation**, not a computation:

```python
bmi = pl.col("weight") / (pl.col("height") ** 2)   # nothing has happened
```

Properties:
- Composable and reusable across queries.
- Independently optimizable and **embarrassingly parallel** — separate expressions in one
  context run on separate threads.
- Serializable (Polars: `Expr.meta.serialize` / `deserialize`).
- Introspectable (`meta.root_names`, `meta.output_name`, `meta.tree_format`,
  `meta.has_multiple_outputs`, `meta.is_column`, `meta.is_literal`).

### 4.2 Contexts and their length rules

| Context | Rule | Output |
| --- | --- | --- |
| **select** | All expressions must produce series of equal length, **or scalars which broadcast**. | Only the selected columns. |
| **with_columns** | Expressions must produce series matching the **original frame height**. | Original columns + new ones. |
| **filter** | Expressions must produce `Boolean`; multiple are AND-ed. | Subset of rows. |
| **group_by(...).agg(...)** | Expressions evaluate per group, over variable-length inputs. | One row per group: key columns + aggregate columns. |
| **sort_by / over** | Expression-defined ordering / partitioning. | Same height. |

Broadcasting also happens **inside** an expression: in `bmi - bmi.mean()`, `mean()` is a
scalar and is broadcast across rows. Literals broadcast the same way.

### 4.3 Expression expansion

A single expression can name **many** columns and fan out into independent sub-expressions
run in parallel:

```python
pl.col("weight", "height").mean().name.prefix("avg_")
# ≡ [ col("weight").mean().alias("avg_weight"),
#     col("height").mean().alias("avg_height") ]
```

Expansion sources:
1. **Multiple explicit names:** `col("a", "b", "c")`
2. **Regex** — a name wrapped in `^...$` is treated as a pattern: `col("^.*_high$")`
3. **By dtype:** `col(pl.Float64)`, `col(pl.Float32, pl.Float64)` — expansion count is
   determined at *runtime* from the schema
4. **`all()`** ≡ `col("*")`
5. **`exclude(...)`** — subtractive: `all().exclude("^day_.*$")`
6. **Selectors** (below)

Python forbids mixing names and dtypes in one `col()` call.

Renaming after expansion: `name.prefix`, `name.suffix`, `name.map(fn)`,
`name.keep`, `name.to_lowercase`, `name.to_uppercase`, `name.replace`,
`name.prefix_fields`, `name.suffix_fields`, `name.map_fields`.

### 4.4 Selectors (`polars.selectors`, `cs.*`)

Composable column matchers that support **set algebra**:

| Group | Selectors |
| --- | --- |
| By dtype | `numeric`, `integer`, `signed_integer`, `unsigned_integer`, `float`, `decimal`, `boolean`, `string`, `binary`, `temporal`, `date`, `datetime`, `time`, `duration`, `categorical`, `enum`, `list`, `array`, `struct`, `object`, `by_dtype` |
| By name | `by_name`, `starts_with`, `ends_with`, `contains`, `matches` (regex), `alpha`, `alphanumeric`, `digit` |
| By position | `all`, `first`, `last`, `by_index`, `exclude` |
| Utilities | `is_selector`, `expand_selector(df, sel)`, `as_expr` |

Set operations: `|` union · `&` intersection · `-` difference · `^` symmetric difference
· `~` complement. **Note the operator ambiguity:** `~sel` means "complement of the
selector"; to get Boolean negation you must first do `sel.as_expr()` then negate. *In Go
we can avoid this trap entirely by naming the methods.*

---

## 5. Operation catalogue

This is the surface area to eventually cover. Grouped as Polars groups them.

### 5.1 Aggregations (on `Expr`, `Series`, and group-by contexts) — P0/P1

`agg_groups`, `all`, `any`, `approx_n_unique`, `arg_max`, `arg_min`, `bitwise_and`,
`bitwise_or`, `bitwise_xor`, `count`, `first`, `has_nulls`, `implode`, `is_empty`,
`last`, `len`, `max`, `max_by`, `mean`, `median`, `min`, `min_by`, `n_unique`,
`nan_max`, `nan_min`, `null_count`, `product`, `quantile`, `std`, `sum`, `var`

### 5.2 Boolean / predicates — P0

`is_between`, `is_close`, `is_duplicated`, `is_finite`, `is_first_distinct`, `is_in`,
`is_infinite`, `is_last_distinct`, `is_nan`, `is_not_nan`, `is_not_null`, `is_null`,
`is_unique`, `not_`

### 5.3 Operators (must become named methods in Go) — P0

Comparison: `eq`, `eq_missing`, `ne`, `ne_missing`, `lt`, `le`, `gt`, `ge`
Logical: `and_`, `or_`, `xor`, `not_`
Arithmetic: `add`, `sub`, `mul`, `truediv`, `floordiv`, `mod`, `pow`, `neg`

### 5.4 Computation — P1

Math: `abs`, `cbrt`, `sqrt`, `exp`, `log`, `log10`, `log1p`, `sign`, `round`,
`round_sig_figs`, `floor`, `ceil`, `clip`
Trig: `sin`, `cos`, `tan`, `cot`, `arcsin`, `arccos`, `arctan`, `sinh`, `cosh`, `tanh`,
`arcsinh`, `arccosh`, `arctanh`, `degrees`, `radians`
Bit ops: `bitwise_count_ones`, `bitwise_count_zeros`, `bitwise_leading_ones`,
`bitwise_leading_zeros`, `bitwise_trailing_ones`, `bitwise_trailing_zeros`
Cumulative: `cum_sum`, `cum_prod`, `cum_min`, `cum_max`, `cum_count`, `cumulative_eval`
Statistics: `skew`, `kurtosis`, `entropy`, `hist`, `mode`, `rank`, `diff`, `pct_change`,
`dot`, `value_counts`, `unique_counts`, `search_sorted`, `peak_min`, `peak_max`,
`index_of`, `hash`
EWM: `ewm_mean`, `ewm_mean_by`, `ewm_std`, `ewm_var`, `ewm_sum`, `ewm_sum_by`

### 5.5 Rolling / windowed computation — P1

`rolling_min`, `rolling_max`, `rolling_mean`, `rolling_median`, `rolling_sum`,
`rolling_std`, `rolling_var`, `rolling_quantile`, `rolling_skew`, `rolling_kurtosis`,
`rolling_rank`, `rolling_map`
Plus a `_by` variant of each (`rolling_mean_by`, …) that uses a **temporal index column**
instead of a fixed row count — essential for irregular time series.
Two-column: `rolling_corr`, `rolling_cov`.

### 5.6 Manipulation / selection on `Expr` — P1

`alias`, `append`, `arg_sort`, `arg_true`, `arg_unique`, `backward_fill`, `forward_fill`,
`bottom_k`, `bottom_k_by`, `top_k`, `top_k_by`, `cast`, `cut`, `qcut`, `drop_nans`,
`drop_nulls`, `explode`, `extend_constant`, `fill_nan`, `fill_null`, `filter`, `flatten`,
`gather`, `gather_every`, `get`, `head`, `tail`, `limit`, `slice`, `interpolate`,
`interpolate_by`, `item`, `lower_bound`, `upper_bound`, `pipe`, `rechunk`, `reinterpret`,
`repeat_by`, `replace`, `replace_strict`, `reshape`, `reverse`, `rle`, `rle_id`, `sample`,
`shift`, `shrink_dtype`, `shuffle`, `sort`, `sort_by`, `to_physical`, `where`,
`set_sorted`, `truncate`

### 5.7 String namespace (`.str`) — P0

`contains`, `contains_any`, `starts_with`, `ends_with`, `find`, `find_many`,
`count_matches`, `extract`, `extract_all`, `extract_groups`, `extract_many`,
`replace`, `replace_all`, `replace_many`, `split`, `split_exact`, `splitn`, `join`,
`concat`, `explode`, `slice`, `head`, `tail`, `len_bytes`, `len_chars`,
`pad_start`, `pad_end`, `zfill`, `strip_chars`, `strip_chars_start`, `strip_chars_end`,
`strip_prefix`, `strip_suffix`, `to_lowercase`, `to_uppercase`, `to_titlecase`,
`reverse`, `normalize` (unicode), `escape_regex`, `encode`, `decode`,
`json_decode`, `json_path_match`,
`to_date`, `to_datetime`, `to_time`, `to_integer`, `to_decimal`, `strptime`

**Note:** `contains`/`replace` take a `literal` flag distinguishing regex from literal
matching. The `_many` variants (Aho-Corasick multi-pattern) are a meaningful performance
feature, not sugar.

### 5.8 Temporal namespace (`.dt`) — P0/P1

Component extraction: `year`, `iso_year`, `quarter`, `month`, `week`, `weekday`, `day`,
`ordinal_day`, `hour`, `minute`, `second`, `millisecond`, `microsecond`, `nanosecond`,
`century`, `millennium`, `date`, `time`, `datetime`
Calendar: `days_in_month`, `month_start`, `month_end`, `is_leap_year`,
`is_business_day`, `add_business_days`
Arithmetic / rounding: `offset_by`, `truncate`, `round`, `combine`, `replace`
Time zones: `convert_time_zone`, `replace_time_zone`, `base_utc_offset`, `dst_offset`
Units: `cast_time_unit`, `with_time_unit`, `epoch`, `timestamp`
Duration totals: `total_days`, `total_hours`, `total_minutes`, `total_seconds`,
`total_milliseconds`, `total_microseconds`, `total_nanoseconds`
Formatting: `strftime`, `to_string`
Series-only: `dt.min`, `dt.max`, `dt.mean`, `dt.median`

### 5.9 List namespace (`.list`) — P1

`get`, `first`, `last`, `head`, `tail`, `slice`, `gather`, `gather_every`, `len`,
`contains`, `count_matches`, `sum`, `mean`, `median`, `min`, `max`, `std`, `var`,
`n_unique`, `unique`, `arg_min`, `arg_max`, `sort`, `reverse`, `shift`, `diff`,
`drop_nulls`, `explode`, `join`, `concat`, `filter`, `eval`, `agg`, `item`, `sample`,
`to_array`, `to_struct`,
set ops: `set_union`, `set_intersection`, `set_difference`, `set_symmetric_difference`

`list.eval()` with `pl.element()` is the mechanism for running a full sub-expression
against each list — a powerful and non-obvious feature worth preserving.

### 5.10 Array namespace (`.arr`, fixed-size) — P2

Same shape as `.list` plus `dot`, `to_list`. Distinct because fixed width enables
much better kernels (no offsets buffer, direct SIMD strides).

### 5.11 Struct namespace (`.struct`) — P1

`field`, `unnest`, `rename_fields`, `with_fields`, `drop`, `json_encode`,
`fields`/`schema` (Series-only)

### 5.12 Binary namespace (`.bin`) — P2

`contains`, `starts_with`, `ends_with`, `get`, `head`, `tail`, `slice`, `size`,
`encode`, `decode`, `reinterpret`

### 5.13 Categorical namespace (`.cat`) — P1

`get_categories`, `starts_with`, `ends_with`, `len_bytes`, `len_chars`, `physical`, `to`
Series-only: `is_local`, `to_local`, `uses_lexical_ordering`

### 5.14 Top-level functions — P0/P1

Construction: `col`, `lit`, `all`, `exclude`, `nth`, `first`, `last`, `element`, `field`,
`struct`, `list`, `repeat`, `ones`, `zeros`
Conditionals: `when(...).then(...).otherwise(...)`, `coalesce`
Horizontal (row-wise across columns): `sum_horizontal`, `mean_horizontal`,
`min_horizontal`, `max_horizontal`, `all_horizontal`, `any_horizontal`,
`cum_sum_horizontal`, `concat_str`, `concat_list`, `concat_arr`
Folds: `fold`, `reduce`, `cum_fold`, `cum_reduce`
Ranges: `arange`, `int_range`, `int_ranges`, `linear_space`, `linear_spaces`,
`date_range(s)`, `datetime_range(s)`, `time_range(s)`
Temporal constructors: `date`, `datetime`, `time`, `duration`, `from_epoch`,
`business_day_count`
Statistics: `corr`, `cov`, `rolling_corr`, `rolling_cov`
Indices: `arg_sort_by`, `arg_where`, `row_index`, `groups`
Escape hatches: `map_batches`, `map_groups`, `format`, `sql_expr`

---

## 6. Frame-level operations

### 6.1 Shape / structure

`select`, `select_seq`, `with_columns`, `with_columns_seq`, `drop`, `rename`,
`insert_column`, `replace_column`, `drop_in_place`, `cast`, `head`, `tail`, `limit`,
`slice`, `sample`, `gather`, `gather_every`, `reverse`, `transpose`, `clear`, `clone`,
`with_row_index`, `to_dummies`, `unnest`, `explode`, `pipe`, `match_to_schema`

`*_seq` variants disable intra-context parallelism — needed when expressions have side
effects or when parallel overhead exceeds benefit.

### 6.2 Combining frames

| Operation | Semantics |
| --- | --- |
| `join` | Key-based; see §7 |
| `join_asof` | Nearest-key temporal join |
| `join_where` | Inequality / arbitrary-predicate join |
| `concat` (vertical) | Stack rows; schema must match (with relax modes: `diagonal`, `align`) |
| `hstack` / horizontal concat | Add columns side by side |
| `vstack` | Append rows, cheap (adds a chunk); `extend` copies into existing buffers |
| `merge_sorted` | Merge two frames already sorted on a key, preserving order |
| `update` | Left frame updated in place by matching rows from right |

### 6.3 Reshaping

- `pivot` — long → wide (`on`, `index`, `values`, `aggregate_function`)
- `unpivot` / `melt` — wide → long (`on`, `index`, `variable_name`, `value_name`)
- `explode` — one list-column row → many rows
- `implode` — inverse; many rows → one list
- `unstack` — reshape a single column into a grid
- `partition_by` — split a frame into a map of sub-frames by key
- `transpose`

### 6.4 Deduplication & sorting

- `unique(subset, keep=first|last|any|none, maintain_order)`
- `sort(by, descending, nulls_last, multithreaded, maintain_order)`
- `top_k` / `bottom_k` (`by` variants) — an O(n log k) heap, not a full sort
- `set_sorted` — assert sortedness so downstream ops can use fast paths (**important
  optimizer hint**; also `is_sorted` to check)

### 6.5 Missing data

`fill_null(strategy: forward|backward|min|max|mean|zero|one, limit)`, `fill_nan`,
`drop_nulls`, `drop_nans`, `interpolate`, `interpolate_by`, `null_count`

### 6.6 Descriptive

`describe`, `glimpse`, `estimated_size`, `n_unique`, `approx_n_unique`, `is_empty`,
`is_unique`, `is_duplicated`, `n_chunks`, `corr`, `equals`, `hash_rows`

---

## 7. Joins — full detail

### 7.1 Equi-join types

| `how` | Rows kept |
| --- | --- |
| `inner` | Only matching pairs (default) |
| `left` | All left rows; unmatched right columns null |
| `right` | All right rows; mirror of left |
| `full` | All rows from both; nulls where unmatched. **By default keeps both key columns**; `coalesce=True` merges them |
| `semi` | Left rows that have ≥1 match — a filter, right columns not added |
| `anti` | Left rows with no match — the inverse of semi |
| `cross` | Cartesian product, no keys |

### 7.2 Join parameters

- `on` / `left_on` / `right_on` — **arbitrary expressions**, not just column names.
  When joining on computed expressions, both original and computed columns appear.
- `suffix` — applied to right-side collisions (default `_right`)
- `coalesce` — merge same-named key columns into one
- `nulls_equal` — whether null keys match each other
- `validate` — `1:1`, `1:m`, `m:1`, `m:m`; raises when cardinality is violated.
  **Very high value: it catches the single most common analytics bug (accidental fan-out).**
- `maintain_order` — none / left / right / left_right / right_left
- `allow_parallel` / `force_parallel`

### 7.3 `join_asof` — nearest-key join

"Like a left outer join, but matches on the *nearest* key instead of on exact matches."

- `strategy`: `backward` (default — last right key ≤ left key), `forward` (first right
  key ≥ left key), `nearest`
- `by` / `by_left` / `by_right`: exact-match grouping columns applied *before* the asof
  match (e.g. match within the same stock symbol)
- `tolerance`: max distance for a match, numeric or a duration string (`"1m"`, `"5d"`)
- `allow_exact_matches`
- **Requires both sides sorted on the asof key.**

Canonical use: joining trades to the most recent quote. Irreplaceable in finance/IoT.

### 7.4 `join_where` — non-equi / inequality join

"Finds all possible pairings of rows from left and right that satisfy the given
predicate(s)." Multiple predicates are AND-ed. Implemented as a filtered cross product
with pushdown; `on` equality is optimizable into a hash join, the rest into a loop join.

---

## 8. Grouping — four distinct mechanisms

### 8.1 `group_by(keys).agg(exprs)` — standard

Keys may be arbitrary expressions. Output = one row per group: key columns then agg
columns. `maintain_order` controls whether group order is deterministic (costs
performance). `having(...)` filters groups post-aggregation.

Group-by shortcuts: `all`, `count`, `len`, `first`, `last`, `head`, `tail`, `min`,
`max`, `mean`, `median`, `sum`, `quantile`, `n_unique`, `map_groups`.

### 8.2 `.over(partition_by)` — window functions

Aggregate **within groups but keep the original row count**.

```python
pl.col("Speed").rank("dense", descending=True).over("Type 1")
```

`mapping_strategy`:
- **`group_to_rows`** (default) — result length equals group size; values map back to
  original row positions. Preserves order; slower.
- **`explode`** — groups rows by partition and **reorders** the output. Faster because
  positions need not be tracked; requires care with other columns.
- **`join`** — aggregates into a list, repeated across every row of the group. Produces
  a `List` column.

Scalars broadcast across the group (`mean().over(...)` gives every row the group mean).
Also accepts `order_by` for ordered windows.

**Key distinction:** `group_by` reduces row count; `over` preserves it.

### 8.3 `group_by_dynamic` — fixed temporal windows

Windows are predetermined intervals, **independent of where the data actually is**.

| Param | Meaning |
| --- | --- |
| `index_column` | Temporal (or integer) column, must be sorted |
| `every` | Interval at which windows *start* (`"1y"`, `"1mo"`, `"1d"`, `"5m"`) |
| `period` | Window *duration*; defaults to `every`. `period > every` ⇒ overlapping windows |
| `offset` | Shifts window boundaries |
| `closed` | `left` / `right` / `both` / `none` |
| `label` | Which boundary labels the window: `left` / `right` / `datapoint` |
| `start_by` | `window` / `datapoint` / weekday name |
| `include_boundaries` | Emit `_lower_boundary` / `_upper_boundary` columns |
| `group_by` | Additional categorical partitioning applied *within* the temporal grouping |

Produces a fixed number of groups matching the interval pattern, whether or not data
exists in each.

### 8.4 `rolling` (a.k.a. `group_by_rolling`) — data-driven windows

"The windows are not fixed at all! They are determined by the values in the
`index_column`." Every row anchors its own window of `period` looking backwards.
**Always produces exactly as many groups as there are input rows.**

Params: `index_column`, `period`, `offset`, `closed`, `group_by`.

Compare: `group_by_dynamic` → N groups where N = number of intervals.
`rolling` → N groups where N = number of rows.

### 8.5 Resampling

- `upsample(time_column, every, group_by, maintain_order)` — increase frequency, insert
  nulls, then `fill_null`/`interpolate`.
- Downsampling = `group_by_dynamic` + aggregation.

---

## 9. Lazy execution and optimization

### 9.1 The pipeline

```
User expression tree
      ↓  build
Logical plan (unoptimized)
      ↓  optimizer passes (below)
Logical plan (optimized)
      ↓  planning: pick physical operators, strategies, partition counts
Physical plan
      ↓  execute (in-memory | streaming | GPU)
DataFrame  /  stream of batches  /  a sink
```

### 9.2 Optimizer passes (verbatim from Polars' documented set)

| Pass | What it does | Runs |
| --- | --- | --- |
| **Predicate pushdown** | "Applies filters as early as possible / at scan level." Moves filters down to the source so less data is ever read. | once |
| **Projection pushdown** | "Select only the columns that are needed at the scan level." Minimizes columns read and memory used. | once |
| **Slice pushdown** | "Only load the required slice from the scan level. Don't materialize sliced outputs (e.g. `join.head(10)`)." | once |
| **Common subplan elimination** | "Cache subtrees / file scans that are used by multiple subtrees in the query plan." Avoids recomputation of shared branches. | once |
| **Simplify expressions** | "Various optimizations, such as constant folding and replacing expensive operations with faster alternatives." | until fixed point |
| **Type coercion** | Inserts casts so operations succeed, on the minimal required memory. | until fixed point |
| **Join ordering** | "Estimates the branches of joins that should be executed first in order to reduce memory pressure." | once |
| **Cardinality estimation** | "Estimates cardinality in order to determine optimal group by strategy." | 0..n times |

Additional passes worth having: **cluster `with_columns`** (merge adjacent projections),
**collapse joins** (fold a filter above a cross join into a real join condition),
**dead column elimination**, **limit pushdown into sorts** (top-k instead of sort+slice),
**sortedness propagation** (a `set_sorted` flag survives filters and enables merge joins).

### 9.3 Execution control

- `collect(engine=...)` — `auto` / `in-memory` / `streaming` / `gpu`
- `QueryOptFlags` — per-pass on/off switches for debugging and benchmarking
- `explain(optimized=True)` — textual plan
- `show_graph(plan_stage="logical"|"physical", engine=...)` — visual plan; the
  streaming physical graph includes a **memory-intensity legend** per operator
- `profile()` — per-node timings
- `collect_async` / `collect_batches` / `sink_batches` — non-blocking and streaming output
- `serialize` / `deserialize` — plans as bytes (enables caching, remote execution, RPC)
- `cache()` — force materialization of a subplan
- `inspect()` — debug hook in the middle of a chain

### 9.4 Streaming / larger-than-RAM

- Batch-by-batch execution through the pipeline; the working set is bounded rather than
  the dataset.
- Unsupported operators **silently fall back** to in-memory for those stages. *(Polars'
  behavior. We should make this explicit and optionally strict.)*
- Generally *faster* than in-memory for suitable workloads, not just more memory-frugal.
- Requirements for real out-of-core:
  - **Spilling** for hash aggregation and hash join (partition to disk on memory
    pressure, then process partition by partition)
  - **External merge sort** (sorted runs to disk, k-way merge)
  - **Backpressure** so producers don't outrun consumers
  - **Morsel-driven parallelism** — small work units off a shared queue, giving natural
    load balancing and NUMA locality (the Leis et al. design that Polars/DuckDB use)
  - **Sinks** that write results out without ever materializing the full frame

---

## 10. I/O

### 10.1 The read / scan / write / sink quadrant

| Verb | Meaning |
| --- | --- |
| `read_*` | Eager. Load fully into a `DataFrame`. |
| `scan_*` | **Lazy.** Returns a `LazyFrame`; the optimizer pushes predicates and projections *into the reader*, so only needed row groups / columns are ever touched. |
| `write_*` | Eager. `DataFrame` → file. |
| `sink_*` | **Streaming.** `LazyFrame` → file, without materializing the result in memory. |

The `scan_*` + `sink_*` pair is what makes larger-than-RAM ETL possible.

### 10.2 Formats and functions (Polars' full IO index)

| Format | Functions |
| --- | --- |
| **CSV** | `read_csv`, `read_csv_batched`, `scan_csv`, `write_csv`, `sink_csv` |
| **Parquet** | `read_parquet`, `scan_parquet`, `read_parquet_schema`, `write_parquet`, `sink_parquet` |
| **IPC / Feather / Arrow** | `read_ipc`, `read_ipc_stream`, `scan_ipc`, `read_ipc_schema`, `write_ipc`, `write_ipc_stream`, `sink_ipc` |
| **JSON / NDJSON** | `read_json`, `read_ndjson`, `scan_ndjson`, `write_json`, `write_ndjson`, `sink_ndjson` |
| **Avro** | `read_avro`, `write_avro` |
| **Excel / ODS** | `read_excel`, `read_ods`, `write_excel` |
| **Delta Lake** | `read_delta`, `scan_delta`, `write_delta` |
| **Iceberg** | `scan_iceberg` |
| **Database** | `read_database`, `read_database_uri`, `write_database` |
| **PyArrow dataset** | `scan_pyarrow_dataset` |
| **Clipboard** | `read_clipboard`, `write_clipboard` |

### 10.3 Cross-cutting I/O features

- **Globs and multiple files:** `scan_parquet("data/**/*.parquet")`
- **Hive partitioning:** derive columns from directory names
  (`year=2024/month=01/…`); `hive_partitioning`, `hive_schema`, `try_parse_hive_dates`
- **Cloud object stores:** S3, GCS, Azure, HTTP; `storage_options`, credential providers
  with refresh, retry counts
- **Schema handling:** `schema`, `schema_overrides`, `infer_schema_length`,
  `missing_columns`, `extra_columns`, `cast_options` for schema drift across files
- **`include_file_paths`** — add a column with the source file
- **Row limits and row indices:** `n_rows`, `row_index_name`, `row_index_offset`
- **Statistics pushdown:** `use_statistics` — skip Parquet row groups by min/max
- **Parallelism strategy:** by row group / by column / auto
- **`low_memory`, `rechunk`, `cache`** knobs

### 10.4 Interop / export

`to_arrow`, `from_arrow`, `to_pandas`, `to_numpy`, `to_dict`, `to_dicts`, `to_struct`,
`to_torch`, `to_jax`, `to_init_repr`, `iter_rows`, `iter_slices`, `iter_columns`, `rows`,
`rows_by_key`, plus the `__arrow_c_stream__` / `__dataframe__` protocol implementations.

**The Arrow C Stream interface is the important one** — it is the zero-copy FFI boundary
that lets any language consume our output.

---

## 11. SQL

Polars' approach: **no separate SQL engine.** "Polars translates SQL queries into
expressions, which are then executed using its own engine."

- `SQLContext` holds a name → frame registry
- `register`, `register_many`, `register_globals`, `unregister`
- `execute(query, eager=...)` — always plans lazily, optionally collects
- Frame-level `.sql()` shorthand; expression-level `sql_expr()`
- Can join across heterogeneous sources (CSV file + in-memory frame + cloud Parquet) in
  a single query, with lazy loading of only the needed rows/columns
- Supported: `SELECT` with `WHERE` / `ORDER BY` / `LIMIT` / `GROUP BY`, `UNION`, `JOIN`,
  `CREATE TABLE`, CTEs, `SHOW` / `DROP` / `TRUNCATE`
- **Not** supported: `INSERT`, `UPDATE`, `DELETE`, `ANALYZE`

**Lesson:** SQL should be a *frontend that lowers to the same logical plan*, never a
parallel engine. Build it after the expression API is stable.

---

## 12. Escape hatches (user-defined functions)

Every engine needs a way out. Ordered by cost:

1. **`map_batches`** — a function over whole `Series`/chunks. Vectorized, cheap. The one
   users should reach for.
2. **`map_elements`** — per-element. Catastrophically slow in Python; **in Go this is
   just a function call**, so our per-element UDFs will be orders of magnitude better
   than Polars'. *This is a genuine selling point.*
3. **`map_groups`** — a function over each group's sub-frame.
4. **`rolling_map`**, **`cumulative_eval`** — a function per window.
5. **`fold` / `reduce`** — accumulate across columns row-wise.
6. **`pipe`** — apply a function to the whole frame; pure composition sugar.

UDFs must declare their **output dtype** so the schema stays resolvable without running
them, and ideally an **elementwise/aggregating/filter** classification so the optimizer
knows whether they can be pushed down.

---

## 13. Cross-cutting non-features that decide adoption

| Concern | Requirement |
| --- | --- |
| **Error messages** | Column-not-found should list near-matches and the actual schema. Type errors should name the operation, both dtypes, and the column. This is 50% of perceived quality. |
| **Display** | `head`/`glimpse`/`describe` need aligned, dtype-annotated, width-limited terminal output. Configurable via a global config (`Config`). |
| **Determinism** | Same input ⇒ same output ordering, or explicit documentation of where it is not (parallel group-by without `maintain_order`). |
| **Reproducible sampling** | Seeded RNG on `sample`, `shuffle`. |
| **Cancellation** | Long queries must be interruptible. *(Polars weak point; Go's `context` is a differentiator.)* |
| **Memory limits** | A configurable budget that triggers spilling rather than OOM-kill. |
| **Observability** | Query profiling, per-operator timings and row counts, memory high-water mark. |
| **Testing utilities** | `assert_frame_equal`, `assert_series_equal` with tolerance and dtype/order flags. Ship these — every user needs them. |
| **Plan stability** | `explain()` output should be stable enough to snapshot-test. |

---

## 14. Scope recommendation for `ursus`

**v0.1 (prove the thesis):** Arrow-backed `Series`/`DataFrame`; `Expr` with arithmetic,
comparison, boolean, cast, alias; contexts `Select` / `WithColumns` / `Filter` /
`GroupBy.Agg` / `Sort`; expression expansion by name, regex, and dtype; inner/left/
semi/anti joins; `ScanParquet`/`ScanCSV` + `SinkParquet`/`SinkCSV`; logical plan with
predicate + projection pushdown; in-memory parallel execution; `Explain`.

**v0.2:** streaming engine with spilling hash-agg and hash-join, external sort, `.Over()`
window functions, `GroupByDynamic` + `Rolling`, string and temporal namespaces, `AsOf`
join, `Collect` into Go structs via generics.

**v0.3:** list/struct namespaces, `Pivot`/`Unpivot`, SQL frontend, cloud object stores,
Delta/Iceberg, plan serialization, SIMD kernel coverage across all primitive types.

**Deliberately out of scope:** GPU execution, distributed execution, plotting, Excel.
