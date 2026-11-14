# What ursus lacks, against established dataframe libraries

**Taken on 2026-10-14, at `028f0c6`** (after v0.6.4's step 246), at the maintainer's
request: everything a user of Polars, pandas or DuckDB would reach for that ursus
does not have today. **Measured again against Polars 2.1 on 2026-11-03** (step 487):
§11.

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
- SQL was out by an earlier decision; since step 487, SQL execution in DuckDB's
  dialect is wanted (§8).

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
| **A generator whose argument is an aggregate of the frame refuses under a memory limit beside a string column.** `WithColumns(IntRange(0, Len(), 1))` stacks the frame beside the generator's branch, a keyless aggregate that reads all its input before it answers, so the frame's side is held meanwhile; at the limit the aggregate, with nothing to spill, refuses, rather than the stack spilling what it holds. A literal end streams, and so does `WithRowIndex` (found at step 287). Fixed at step 425: the side held is one batch, which was charged as all the column's text, a String column's slice sharing its characters; it is charged as the text its rows cover, so an int-only frame's batch and a text frame's are both a batch | internal/data/nbytes.go | fixed |
| **A float aggregate's last bits depend on the thread count.** Its partial states merge in an order the threads set: of eight probed by key, `Sum`, `Mean`, `Var`, `Std`, `Skew`, `Kurtosis` and `Entropy` differ between one thread and eight, and `Corr` does not. A spilled run equals the one-thread run to the bit. Row order is deterministic, as §10 says (found at step 287). | the accumulators' Merge | **not bit-exact** |
| **A streamed rolling var or std differs from the answer in memory in its last bits.** Over the whole frame under a memory limit, a window's state is rebuilt from each batch's history rows, so its periodic recompute falls at other rows: 35–52% of rows over `sin(i)·1000`. Step 284's skew, kurtosis, cov and corr do too. The streaming test's floats are quarters, whose sums are exact, so it never showed. The fix is to carry the kernel's state across batches, as an ewm's is (found at step 284). Fixed at step 429 for a window ending at its row, over the whole frame, a hot key's partition and an order of its own: `kernel.RollingNext` carries the window's rows and its state, and every answer is the one in memory to the bit; and at step 430 for a centred one, walked its lead rows ahead of each batch and nulls past the last | internal/physical/winstream.go, internal/kernel/rollingnext.go | fixed |
| **`Rank` ranked a null, as the smallest value;** Polars leaves a null's rank null and ranks the rest among themselves. `[5, null, 4]` ranked `[3, 1, 2]` here and `[2, None, 1]` there, in a window and in `List().Eval` alike (found at step 268). Fixed at step 335: `winRank` and `RankNext` pass a run of nulls over, and a streamed max or average counts the values, not the rows | internal/kernel/window.go `winRank`, and the streamed rank (`RankNext`) | fixed |
| **A printed frame padded its cells by bytes,** so a row holding a letter outside ASCII came out a column short: "Zoë" is three columns and four bytes. Found writing the user guide's strings chapter; fixed at step 296, cells padded by their width on a terminal | frame.go | fixed |
| **A selection among a variadic function's operands expanded the whole function once per column.** `MaxHorizontal(All())` gave each column itself, `SumHorizontal(All()).Alias("s")` was refused as two columns named "s", `ConcatStr`, `Struct`, `ConcatList` and `Coalesce` of a selection answered a column each, where Polars writes a selection out as the function's operands and answers one column. Found writing `HashRows` over every column; fixed at step 302 by `expr.Variadic`, built when the plan is resolved, as a fold is | horizontal.go, cond.go, internal/expr/variadic.go | fixed |
| **A fold in a list's body was never built.** `List().Eval(Fold(...))` and a fold in `CumulativeEval`'s body failed, "fold reached typing unexpanded", since step 266: a body is lowered when it is written, and the fold waited for an expansion that never reaches it. Found when step 302's variadic functions failed the same way in a body, which its gate caught; fixed at step 302, a body building both over its expressions as written | internal/expr/listeval.go | fixed |
| **`Format` names its column when it is written,** after its first argument, which a selection cannot name yet. `Format("<{}>", All())` is refused, its columns all named "literal", where Polars formats each column, named after it; `Format("<{}>", SumHorizontal(All()))` is named "literal" where Polars' is "a". A name taken at expansion would fix both. Found at step 302 | fold.go | open |
| **An aggregate in an ordered function's input inside a window reached the evaluator.** `(x - x.Mean()).CumSum(false).Over(g)` failed, "physical: no evaluator for *expr.Agg", an internal error, where it was neither answered nor refused. Found at step 335; fixed at step 336, the aggregate computed beneath the window in its partitions | internal/plan/resolve_window.go | fixed |
| **A float cast to String was Go's shortest text,** `2`, `-0`, `1e-07` and `+Inf`, where Polars writes `2.0`, `-0.0`, `1e-7` and `inf`. Found at step 303; closed at step 325, `kernel.FloatText` writing Polars' text: plain from 1e-5 to below 1e16, a whole number with `.0`, else a signed exponent of no padding | internal/kernel/unary.go | closed |
| **A join under a limit too small for the whole query blames one key.** Three frames aligned under 64 KiB, every key but a few held once, were refused "a single join key has more build rows than the limit": `oneKeyOver` reads the query's total through the account's `Check`, and the memory was the other join's. Its message should say what holds the memory. Found at step 307; at 256 KiB the query spills and answers. Fixed at step 426: past the deepest split, a key that fits the limit alone is refused naming the query's other operators and their bytes, 64.0 KiB of the 65.4 here beside the join's 1.4 | internal/physical/extjoin.go | fixed |
| **A column-free expression was named "literal" past one wrapper.** `Len().Cast(Int64)`, `Len().Add(1)` and their kind fell to `OutputName`'s fallback, which named an expression with no root column "literal"; Polars names it after its first input, "len". Found by `Having`'s tests; fixed at step 309, the fallback recursing into the first input | internal/expr/node.go | fixed |
| **A conditional of a List or a Struct was refused,** "a conditional is not implemented for List(String)", so `When` with List branches and `Coalesce` of Lists failed, where Polars' when/then takes either. Found writing step 310's test of `FirstNonNull` over lists; fixed at step 310, the kernel taking each row from its branch | internal/kernel/select.go | fixed |
| **A null condition yielded null, where Polars and SQL take Otherwise.** `When(c).Then(a).Otherwise(b)` of a null c was null, by design (internal/expr/cond.go: "a null condition takes neither branch"); Polars' `when` gives b, and SQL's `CASE WHEN NULL` its ELSE. Found at step 310, in none of the docs; the maintainer chose Polars', and step 311 made a null mask take the Else branch, a break. `Clip` of a null bound follows Polars now too; `FillNan`, which leaned on the old rule, was rewritten to keep its nulls | internal/kernel/select.go, internal/expr/cond.go | fixed |
| **A date before 1677 or after 2262 was a wrong Go value.** `Rows`, `ToMaps` and `At` built each instant as `time.Unix(0, ticks*nanosPerTick)`, whose nanosecond count overflows outside those years, which a Date and a coarser Datetime hold: 0999-01-02 came out as 2168-02-10. Found by step 355's tests; fixed there, the seconds and nanoseconds taken apart (`instantOf`, by `ToTime`) | rows.go | fixed |
| **A typed view of a `Date` or a `Datetime` is its integer.** `df.Column[time.Time]` and `df.At[time.Time]` refuse one, "no typed view for time.Time"; `Column[int32]` gives a Date's days and `Column[int64]` a Datetime's ticks. `Rows[T]` decodes either into a `time.Time`. Polars' `to_list` gives dates and datetimes. For 0.6.7's V3, the eager Series (step 297) | series.go, internal/data | open |
| **A naive `Datetime` is written with a `Z`,** as though it were UTC: `Datetime(s)` prints `2026-03-08T06:30:00Z`, and a cast to String gives it so (step 298). Polars writes a naive instant with no offset. The CSV writer, which shared `FormatTemporal`, writes Polars' text since step 354, a naive instant with none and a zoned one with `+0900`; the print and the cast are left (step 296); since step 422 (0.7.2) none writes an offset for a naive one | dtype/temporal.go:212 | fixed, step 422 |
| **`RollingOptions.Offset` was not Polars' `offset`.** ursus's moved a window's end from its row's instant, `(t + offset − period, t + offset]`; Polars' places its start, `(t + offset, t + offset + period]`, by default `−period`. Found at step 312; the maintainer's goal of 2026-10-22, Polars' features matched, decided it, and step 313 placed the window as Polars does, a break, with `NoOffset()` for Polars' `"0d"` and Polars' rule for a window that ends at its row | groupby.go `RollingOptions`, internal/physical/tempstream.go `rollRow` | fixed |
| **An NDJSON value that is not an object, in a Struct column under `WithNDJSONIgnoreErrors`, was a struct of nulls,** where every other value a column cannot take is a null, and a null struct and a struct of nulls are kept apart on purpose (step 499). Read as Polars 1.44.1 read it; Polars 2.1 reads a null. Found by the recorded oracle at step 502 (`ndjsonread/ignored_in_a_struct`, §12); fixed at step 507, the struct's builder refusing the value as every builder does, which ignore_errors reads as a null | internal/source/ndjson/build.go | fixed, step 507 |

## 2. The expression model

The structural gap. Each item needs the planner to allow expressions whose length
differs from their input's, or scalars outside a group-by.

| missing | Polars | prio |
| --- | --- | --- |
| **Length-changing expressions:** `Expr.Sort`, `SortBy`, `ArgSort`, `Filter`, `Head`, `Tail`, `Slice`, `Limit`, `Gather`, `GatherEvery`, `Get`, `Unique`, `Reverse`, `DropNulls`, `DropNans`, `Explode`, `Append`, `ExtendConstant`, `Sample`, `Shuffle`, `Rle`, `RleID`, `RepeatBy`, `ArgTrue`, `ArgUnique`. **Built at steps 251–260** for `Sort`, `SortBy`, `Filter`, `Head`, `Tail`, `Slice`, `Gather`, `GatherEvery`, `Get`, `Unique`, `Reverse`, `DropNulls`, `DropNans` and `Explode`, in an aggregation, `Select` and `WithColumns`, with `Unnest` (step 260). Built since: `RepeatBy` (step 263), `ArgSort`, `ArgTrue` and `ArgUnique` (267), `Rle` and `RleID` (281), and `Append`, `ExtendConstant`, `Sample` and `Shuffle` (282). Still missing: `Gather` in `Select`, any of them in `Filter`, and any of them in `.Over`, mapped back onto the partition's rows | all of `Expr` | P1 |
| **An elementwise expression over a window:** Polars takes any expression in `.over()`, `(col("a") * 2).over("g")` and `col("s").name.prefix_fields("p_").over("g")` each the expression computed in each partition, which for an elementwise one is the expression itself. ursus refuses one, "is not a window function", taking only an aggregate or a window function (found at step 323) | `Expr.over` | P2 |
| **Aggregates in `Select` and `WithColumns`:** `Select(Col("x").Sum())` for a frame-wide reduction, and `x - x.mean()` broadcast without `.Over()`. **Built at step 250.** | the default context rules | P0/P1 |
| **Expression-valued arguments:** `IsIn(expr or list column)`; `List().Get(expr)`, `List().Contains(expr)`; `Str().Contains/Replace/Split` with an expression pattern; `Dt().OffsetBy(expr)`. **Built at steps 254–256,** but `IsIn(Col("b").Implode())`, 0.7's scalar subqueries | most namespace methods | P1 |
| **List and Series literals:** `Lit([]int64{...})`. **List literals built at step 257;** Series literals missing | `pl.lit([..])`, `pl.Series` | P1 |
| built: **selectors**, package `selector`, in step 322: the type families, the name patterns, `by_name`, `by_index`, `first`, `last`, the set algebra, named, and `expand_selector` as `Expand`. Not built: `categorical` and `array`, whose types ursus lacks, and `object` | `polars.selectors` | — |
| built: **`name.*`**, `Expr.Name()`, in step 323: `keep`, `map`, `prefix`, `suffix`, `to_lowercase`, `to_uppercase`, `replace`, `prefix_fields`, `suffix_fields`, `map_fields`; and `struct.drop` | `Expr.name` | — |
| **Serialization:** expressions and plans to and from bytes. `meta.*` built in step 326, `Expr.Meta()`: `root_names`, `output_name`, `is_column`, `is_column_selection`, `is_literal`, `is_regex_projection`, `has_multiple_outputs`, `undo_aliases`, `eq`, `pop`, `tree_format` (ursus's drawing) | `Expr.meta.serialize`, `LazyFrame.serialize` | P2 |
| **`Expr.Pipe`, `Expr.Where`, `Expr.Inspect`** | | P2 |

## 3. Types

| missing | note | prio |
| --- | --- | --- |
| **Categorical:** built in step 351, stored as its text: casts, order, keys, codes over the frame (`WinCatCodes`), `Cat()`, CSV and NDJSON by a schema, spilling. Arrow's dictionary and Parquet's Categorical in and out, and `qcut` and `hist` answering one, in step 352 | — |
| **Array** (fixed-size list) | built in step 341: held as a List each of whose rows has its size, a null row none; casts to and from a List, `List().ToArray`, `Arr().ToList`, a Go `[n]T`, Arrow `FixedSizeList` in and out, Parquet by the footer's Arrow schema, NDJSON by a schema, JSON; its `arr` namespace in step 342, a List's functions of the same elements, `Reverse`, `Sort`, `Shift` and an elementwise `Eval` keeping the Array, `ToStruct` and `Dot` of its own; `Reshape` and `ConcatArr` in step 343, and a key in step 344 | — |
| **Large strings and binary:** built in step 368: a String or Binary column of 64-bit offsets past 2 GiB, made by the combiners every Collect, join, sort, window and aggregation ends in, read through the same accessor; Arrow's LargeString and LargeBinary out and in, a spill kind of its own. A batch a source reads keeps 32-bit offsets and refuses past them | — |
| **String view layout** | Arrow's view types are converted on import | P2 |
| **UInt128, Float16, Object, Unknown** | Float16 is widened to Float32 on import | P2 |
| **Dictionary, run-end, union, ListView** in memory | decoded or refused on import | P2 |
| built: **a Decimal's `round`, `floor`, `ceil` and `sign`,** Polars 2.0's, exact at its scale, Decimal(p+1, s) where digits are dropped, in step 490; `truncate` and `round_sig_figs` of one still refused | — |
| `Expr.ToPhysical`, `Reinterpret`, `ShrinkDtype` | `Cast(dt.Physical())` works for temporal types only | P2 |
| built: **a List, an Array or a Struct column as a key,** in step 344: the group-key encoder writes a list's length and each element tagged, and a struct's fields tagged, so `Unique`, `UniqueKeep`, `GroupBy`, `Over`, `Join`, `PartitionBy`, `RleID`, `Rle`, `Mode` and `HashRows` key them as Polars does, and a group-by of them spills; a mode of them without `maintainOrder` comes as its values first appear, having no order (found at step 289) | — |

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
- **`.name`:** `strip_prefix` and `strip_suffix` built in 0.8 (step 491), as
  `Name().StripPrefix` and `StripSuffix`.

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
- **Built in 0.8:** `erf` and `erfc`, Polars 2.x's, as `Erf` and `Erfc` (step 488).

## 5. Computation and aggregation

| missing | prio |
| --- | --- |
| **Statistics:** built: `hist` in step 286; `skew`, `kurtosis` and `approx_n_unique` in step 276; `entropy`, `mode` and Spearman `corr` in step 277 | P1 |
| **Search and hashing:** built: `dot` in step 276; `search_sorted`, `index_of`, `hash`, `peak_min` and `peak_max` in step 279, a search's value a Go value only; `unique_counts` and `value_counts` on an expression in step 280 | — |
| built: **a window inside a window,** in step 336: an ordered function, an aggregate or a window in a window's input, computed beneath it in its partitions and order, a Window node a layer, as Polars computes them; a window the user wrote inside another partitioned within the outer one's partitions. An aggregate whose answer depends on the order, under an order, stays refused (found at step 281) | — |
| built: **holistic aggregates that spill:** `CorrSpearman`'s pairs in step 339, and `Mode`'s counts in step 340, an aggregate of its own, beside `Median`'s, `Quantile`'s and `NUnique`'s; `Implode`, whose answer is every value, is refused past the limit (found at step 287) | — |
| built: **a non-aggregate in an aggregation,** in step 331: `Agg(Col("x"))`, `Agg(Col("x").Mul(2))` and a window in `Agg`, each group's values as a list, as Polars answers them; and a window inside an aggregate, `CumSum(false).Max()`, a group at a time. A mix of an aggregate and a row's value too, since step 336, the row below (found at step 329) | — |
| built: **an aggregate inside an aggregate's input,** in step 336: `Col("a").Sub(Col("a").Mean()).Abs().Sum()` in a `Select`, an `Agg` and a window, the inner aggregate computed over the group and broadcast beneath the outer; in `Agg` a mix, `a - a.Mean()`, a list a group, and `a.Sum().Mean()` the sum; `SortBy` and `Gather` take an aggregate in their keys (found at step 279) | — |
| built: **`truncdiv`,** Polars 2.0's division toward zero, as `TruncDiv`, in step 489 | — |
| **Integers as bits:** built: `& \| ^ ~` of integers in step 278, the `bitwise_and/or/xor` aggregates in step 276. Polars has no shifts | — |
| **Aggregate odds and ends:** `agg_groups`. `has_nulls`, `is_empty`, `nan_min` and `nan_max` built in step 276 | P1 |
| **Rolling:** weights; Polars' `random` rank; a rank by time over one partition past a memory limit, refused, as its walk keeps a window's values as floats. `map` and `rolling_rank_by` built at step 338, `RollingMap` and `RollingRankBy`. `skew`, `kurtosis`, `rank`, `rolling_cov` and `rolling_corr` built at step 284; an integer by, `"2i"` periods, at step 312; the generic `Expr.rolling(index_column)` at step 337, `Expr.Rolling`, in an aggregation not yet | P1 |
| **EWM:** built: `ewm_sum`, `ewm_mean_by` and `ewm_sum_by` at step 285; an integer by, a `"2i"` half-life, at step 312 | — |
| **Other:** `include_breaks`; `cut` answers an Enum and `qcut` a Categorical, as Polars', since steps 350 and 352. `cumulative_eval` built at step 286. `round_sig_figs`, `cot`, numeric `truncate`, the half-to-even mode and a one-sided `clip` were built at step 283 | P1/P2 |

## 6. Frame operations

`v0.6.7-scope.md` takes this section (step 288).

| missing or partial | prio |
| --- | --- |
| built: `unique(keep=)` with `maintain_order=True`, as `UniqueKeep`, in step 289 | — |
| built: a frame `fill_null` strategy's `limit`, as `FillForward.Limit(n)`, in step 301 (the strategies at step 249). A frame's `shift`, `first` and `last` built in step 293; its `null_count`, `sum`, `mean`, `median`, `min`, `max`, `std`, `var`, `quantile` and `product` in step 300. `approx_n_unique` is deprecated on a frame in Polars 1.44.1, and is `Select(All().ApproxNUnique())` | — |
| built: `slice` with a negative offset, in step 291 | — |
| built: a frame's `cast`, by name, by type and for every column, in step 298; `match_to_schema` in step 299 | — |
| built: a frame's `sample` and `shuffle` (fraction or n, seed), in step 292 | — |
| built: `partition_by` into frames, in step 305 | — |
| built: `upsample`, in step 306; a frame's `interpolate` and `drop_nans` in step 301 | — |
| built: `is_duplicated`, `is_unique` and `n_unique` over several columns, in step 290 | — |
| built: `pivot` of several `On` columns, and a lazy pivot with its columns given, in step 303 | — |
| built: `concat`'s relaxed, horizontal and align modes, and `ConcatFrames` for `DataFrame`s, in step 307 | — |
| join: emit a computed key column. `maintain_order` of every order and kind built in step 314; an as-of `tolerance` on numeric keys, as-of `coalesce` and `JoinWhere`'s suffix in 315 | P1 |
| group-by: `.over(order_by)` of an aggregate; mapping `explode` and `join`. `head` and `tail` built in step 308, `having` in 309, `all` in 310, an integer index for `GroupByDynamic`, `Rolling` and `upsample` in 312, `map_groups` and iterating over groups in 321 | P1/P2 |
| built: `gather_every`, `clear`, `is_empty` and `equals` in step 293; `insert_column` and `hash_rows` in step 302; `update` in 316; `to_dummies` and `unstack` in 320; `extend`, `glimpse`, `estimated_size`, `with_columns_seq` and `select_seq` in 321; `corr`, `gather`, `row`, `item`, `get_column_index`, `replace_column`, `drop_in_place`, `to_series`, `to_struct`, the horizontal reductions, `fold`, and a lazy frame's `columns`, `dtypes` and `width` in 327; `map_rows`, `map_columns`, `show` and a lazy frame's `cache` in 328; `interpolate_by` in 329 | — |
| built: `unpivot` without `On`, in step 304; `transpose` of mixed text and numbers, and named from a column, in 320 | — |
| built: an eager `Series` with operations, in step 319: sort, unique, reverse, fills, casts, slices and the aggregates; Polars' Series mirrors every Expr method, which here a frame's expressions answer | — |
| built: `FromRows`, a frame from Go structs, in step 317; `RowsByKey`, `ToMaps` and `IterRows` in 318 | — |

## 7. I/O

| missing or partial | prio |
| --- | --- |
| built: **lines,** Polars 2.x's `scan_lines` and `read_lines`, as `ScanLines` and NDJSON's spellings of it, in step 492 | — |
| built: **JSON read,** in step 345: `ScanJSON` and `ScanJSONBytes`, Polars' `read_json`, each object made a line of NDJSON in place and read as step 334's NDJSON is, faster than Polars; schema overrides for both. NDJSON read and scanned in step 334; JSON and NDJSON written and sunk in step 333, each value in Polars' text | — |
| built: **Arrow IPC,** in step 332: a file or a stream scanned, written and sunk, uncompressed, LZ4 or Zstandard, and a file's schema read; and a Parquet file's schema. Over arrow-go's IPC readers and writers | — |
| **Hive partitioning:** built on read for Parquet in step 347, a directory read whole, its `key=value` directories typed columns, a filter on them skipping files, `include_file_paths`; `include_file_paths` for CSV and NDJSON in step 359; IPC's in step 360, a directory, a glob and a list, `internal/source/files`. **Partitioned writes** in step 367: `WithPartitionBy`, `WithPartitionIncludeKey` and `WithMaxRowsPerFile` for the four sinks, Polars' paths name for name | built |
| **Multi-file schema evolution:** built for Parquet in step 348, `schema`, `missing_columns`, `extra_columns` and `cast_options`' integer and float upcasts; `include_file_paths` for Parquet in step 347. `**` globs, and CSV and NDJSON directories, in step 349. Left: `cast_options`' datetime casts and struct fields, `include_file_paths` for CSV, NDJSON and IPC | P1 |
| **Schema overrides on Parquet and Arrow scans.** The rest of `read_csv`'s options built in step 357: `ignore_errors`, `infer_schema=False`, `raise_if_empty=False`, `decimal_comma` and `encoding="utf8-lossy"`, text checked as UTF-8; `skip_rows_after_header`, `skip_lines` and `quote_char=None` in step 358; `eol_char`, a line end other than `\n`, left: the scanner, the splitter and the UTF-8 span find `\n` by itself. CSV date inference built in step 355, `WithTryParseDates`, and `str.to_date()`, `to_datetime()` and `to_time()` without a format by the same patterns in step 356. The CSV writer's options built in step 354: quote style and character, date, datetime and time formats, float precision, scientific notation and the decimal comma, a BOM, gzip and Zstandard output and the path's extension check. Compressed input built in step 346: a CSV, NDJSON or JSON file of gzip, zlib or Zstandard, by its first bytes | P1 |
| **Cloud object stores** by URI (`s3://`, `gs://`, `https://`), with listing, credentials and retries. Today you bring a `ReaderAt` | P2 |
| **Parquet, complete (wanted since step 506):** read and write nested columns at any depth, where today a flat column, a List of a primitive and a Struct of primitive fields are read and written and a list of lists, a list of structs, a struct holding either and a map column are refused by name (`internal/source/parquet/types.go`); a map read as `List(Struct(key, value))`, ursus having no Map type; Parquet field IDs, which Iceberg needs; page indexes and bloom filters, read to skip pages and written; compression level and per-column options on write; key-value metadata read and written; a footer cache; and the Zstandard decode, a pure-Go decoder's (§8) | **wanted** |
| **Apache Iceberg (wanted since step 506):** read a table: its metadata, the snapshot asked for (the current one, or one by id or time), its manifests and their data files, Parquet as Iceberg writes it; columns matched by field ID, so a renamed or reordered column reads; files pruned by partition and column statistics; v2 position and equality deletes applied. Then write: append and overwrite, committed as a new snapshot. Catalogs: a table's path first, the REST catalog next. Storage: through ursus's openers and `fs.FS`, as object stores are still the caller's. Manifests are Avro, which ursus does not read: an Avro reader of its own, or a dependency, is a decision for the scope. Polars: `scan_iceberg`, `write_iceberg`, `sink_iceberg` | **wanted** |
| Avro write; Excel; Delta Lake. **Databases** read in step 353, `ReadDatabase` and `ReadDatabaseRows` through `database/sql`, Polars' `read_database`; written in step 366, `WriteDatabase`, Polars' `write_database`; left: `read_database`'s `iter_batches`, and ADBC's Arrow results | P2/P3 |
| Predicate pushdown into CSV and Arrow scans | P2 |

## 8. Engine and tooling

These belong to a performance release more than to features.

| missing or partial | prio |
| --- | --- |
| built: **`profile()`,** in step 369: `Profile`, each node's span, rows, batches, own time and peak memory; `ExplainOptFlags`. Not built: a physical-plan explain | — |
| built: **slice pushdown,** in step 371: through projections, `WithColumns`, a row index, a union's inputs and an ordered left join's left input, and of an offset slice | — |
| built: **a count reads no column,** in step 370: a scan of no columns, Parquet's rows from its footers; `Select(Len())`'s aggregation is slower than Polars' still, P4's | — |
| built: **predicate pushdown through group-by keys and window partition keys,** in step 382: a conjunct of the keys alone, rewritten through their definitions, not of a float key; a window's where every window of the node partitions by its columns | — |
| **Expression-level CSE;** clustering of `with_columns`; dropping a computed column nothing reads. Measured at step 388: a row-wise subexpression in two outputs of a million rows is a sixth of the query, windows and aggregates already shared by identity; a `WithColumns` reads its earlier outputs, so a shared subexpression must not cross one that redefines what it reads | P1 |
| **Multi-join reordering** on statistics; sortedness propagation and a merge join | P2 |
| **A work-stealing morsel scheduler.** A sort runs on the query's threads since step 361 | P1 |
| built: **the CSV writer on the query's threads,** in step 390: each batch's text made on a goroutine of its own and the texts written in order, and a Date, a naive Datetime and a Time written digit by digit; a million rows of an integer, a float, text and an instant 108–116 ms where they took 367–501 and Polars takes 162–189. A partitioned sink writes a batch at a time still. Step 354 measured 397 ms against 168 | — |
| `collect_all` and several sinks from one plan; plan serialization. `cache()` built in step 328 | P2 |
| Display settings (rows, width, `glimpse`) | P1 |
| Spill-file compression; approximate aggregates | P2 |
| **A heap bound that trips:** `TestTemporalMediansSpillTheirValues` holds the live heap's growth under 10 MB, a measure of when the collector runs; under test-all's load it read 11 MB once (step 292) and passed fifteen reruns. With other work holding the machine at a load of 18 to 24, `GODEBUG=simd=0` read 12 and 14 MB in two test-alls (step 304); alone there it passed at 17. A bound on the heap after each collection would hold | P2 |
| **A memory limit at its edge:** `TestV066UnderAMemoryLimit`'s moments case ran a group-by of 2,000 keys under 64 KiB, its states at the edge by design; CI's race job read 64.3 KiB once, the query's whole, 1.3 KiB of it the group-by's, and refused (step 309). Step 319's gate met it again, and it refused two runs in a hundred under the race detector here. It runs under 96 KiB since, none of a hundred refused and each spilling at least sixteen times (step 319) | closed |
| **`Upsample` is a join:** 355 ms for a million rows of 100 groups, where Polars, walking a frame it requires sorted, takes 24 ms (step 306); without its sort, the join keeping the grid's order, 224–229 against 21–25 (step 383). Two thirds of what was left was the join's keys, a group's Int64 and a Datetime with nulls equal, encoded as bytes, packed into words since step 391: 210–217 ms. What is left is the join itself, building the frame's million keys and looking up the grid's, where Polars joins a group at a time on its sorted instants; an operator of its own would close it | P1 |
| **A frame-wide aggregate pays a group-by's machinery, and reads on after its answer is known:** of a million Int64s, `Len` 0.95 ms, `Min` 1.6, `First` 2.6 and `IsSorted` 2.2 to 2.9, where Polars answers `is_sorted` in 0.03 ms from the first pair out of order, or 0.8 from a sorted column. Each batch builds its group ids, all zero, and the scan runs to the end (step 324) | P2 |
| **A window keeps its whole input and joins it, every column, before it computes:** `InterpolateBy` of a million rows, by sorted, 15.5 ms where Polars takes 7.1, and `Interpolate` 12.3 where Polars takes 5.8, after step 329 took a third off each; `Over`'s partition keys are hashed a row at a time, a quarter of a window's time over 100 groups (step 329). One integer key is numbered by the group-by's integer table since step 336, which halved a window over a million rows of 1000 groups: `Sum().Over(g)` 15.5 ms where it took 38 and Polars takes 8.1, `CumSum` 23 (46, 14.9), `Shift(1)` 24 (48, 16.2). A window inside a window is a window beneath: `Shift(1).CumSum(false).Over(g)` 45 ms against Polars' 23, `(x - x.Mean()).Abs().Sum()` by g 32 against 17 and over the frame 21 against 4.2, the mean broadcast as a column where Polars keeps it one value | P1 |
| **An expression's rolling is a rolling window by time, or the frame's rolling group:** over a million rows and a window of 100, `Col("x").Sum().Rolling(t)` 49 ms where Polars takes 10, by `RollingSumBy`'s sliding state, its sums compensated (42 ms, Polars 18.2 for its own); `Max().Sub(Min())` 65 ms against 14.6; under `Over(g)` of 1000 groups 60 against 48. An aggregate with no window of its own goes by the rolling group, each row given to each of its windows: a window of 100 rows, a hundred times a row (step 337)  Since step 387 its windows run at once: `Sum().Rolling(t)` of 100 rows over a million 22–25 ms against Polars' 10.2–10.5, a rolling mean, max and std together 56–83 against 23–26 | P1 |
| **A window a group at a time in `Agg` is a window beneath a group-by:** `Agg(Col("x").CumSum(false))` over a million rows of 1000 groups takes 113 ms where Polars, computing it in the group-by's own groups, takes 13.5; `Over`'s keys are hashed by the generic key table, where the group-by uses its integer one, and an Int64's `CumSum` is computed in Int128 (step 331). Over Float64 values it took 76 ms and takes 51 since step 336's integer key table, Polars 10.6. A list a group, `Agg(Col("x"))`, is `Implode`'s 26.7 ms against Polars' 10.1  Since step 386 a window of one column is the column's list a group with the window evaluated over it: `Agg(Col("x").CumSum())` 34–36 ms over a million Int64s of 1,000 groups, Polars 10.6–12.0 | P1 |
| **An Arrow IPC file is read a batch at a time and copied:** a million rows of three columns in 20.4 ms, and Polars' own file, its strings views, in 29.7, where Polars takes 16.2. Its batches can be read in any order, so a reader of its own could convert them in parallel (step 332). Writing is faster than Polars', 14.3 ms against 20.0. A dataset of ten files of 200,000 rows, read ahead a file a thread since step 360: whole in 63 ms against Polars' 18, summed by its key in 21 against 14, one key's file in 9 against 3; one file of its two million rows in 77 against 41 | P2 |
| **An eager frame's `Head` and `Tail` plan and run a query,** about 20 µs a call, where a slice of its batch would cost nothing: `MapGroups` calling `Head` on each of 1,000 groups spent 20 ms of 48 there (step 321) | P2 |
| **A mean of a million integers is twice Polars':** 4.8 ms, where Polars' Series takes 0.47, and since step 372, a frame's aggregate of no keys taking its columns whole, 0.94 to 1.1, against Polars' 0.41 to 0.57; a float's sum, in one chain to keep its bits, 0.9 against 0.13. A Series' operations measured it (step 319). The unique, 457 ms then, takes 54 to 105 since step 362, faster than Polars' unique that keeps the rows' order, 119; the sort, 154 ms then, takes 30 on eight threads since step 361 and 69 on one, where Polars' takes 10 to 19 | P1 |
| **A unique of a few keys is twice Polars' time:** 1,000 keys among a million Int64s take 16 to 25 ms on any thread count, where Polars takes 8. The tables are in cache, so one goroutine inserts them; what is left is reading and hashing each row, naming its table, and the frame's batches (step 362) | P2 |
| **A sort of text is one and a half times Polars':** a million distinct strings of eight bytes take 150 ms on one thread and 75 on eight, where Polars takes 52, and a million URLs of one prefix 240 and 126, where it takes 104, since step 363's radix of seven bytes a round; they took 853 and 1,128 on one thread before. What is left is the output's gather of the strings and the key's read of each row's bytes in an order no longer the rows' (step 363) | P2 |
| **A group's `Mode` runs on one worker:** it depends on its rows' order, the first appearance of each value and a float's first bits, so its group-by is serial, as `Implode`'s is: over a million rows by 1000 groups 38 ms, where Polars, a group to a thread, takes 19, and over the frame 18 against 5. Without `maintainOrder`, a mode of anything but a float could run on every worker and merge (step 340) | P2 |
| **A List column pays to pass through a query:** selecting a list of three integers a row, a million rows, held in memory, takes 11.6 ms where Polars takes 0.6: the frame is cut into batches and its lists concatenated again at the end. `List().ToArray(3)` adds 0.4 ms to it, a check of each row's width and a relabel; a cast to `Array(Float64, 3)` takes 20 ms against Polars' 3.8, its elements gathered and cast (step 341) | P2 |
| **A reshape copies its column three times:** `Reshape` in a `Select` implodes the column, reshapes the one list and explodes it again: a million values by four take 28 ms, and an Array of three a row flattened 98, where Polars' reshape is a view, 0.03 ms. `ConcatArr` of two Arrays of three takes 50 ms against Polars' 24 (step 343) | P2 |
| **A join's right order hashes the left frame:** a million left rows against 100,000 right, in memory, takes 57 ms inner and 75 ms left, where Polars' takes 33 and 37; ursus's left order takes 16, Polars' 12. The hash of the larger side is most of it, as a plain join that hashes a million rows takes 47 ms. A probe of the left frame and a counting sort by the right row would hash the smaller side. An ordered right or full join numbers its table's rows always, about a tenth of its time, where only a spilled one needs them (step 314) | P2 |
| **A CSV file of text is read in about 1.4 times Polars' time:** a million rows of an integer, two text columns and a float, 43 MB, in 165–250 ms on one thread where Polars takes 111–146, and 78–142 on four against 55–68, at a load of 4 to 5.7 (step 373; three and a half times at step 357, 76 ms against 22). Left: the splitter, a fifth of the read since its mask; the float's parse, a sixth, most of it its divide; a block's records appended a field at a time, a builder's interface call each, a seventh, which a column at a time would save. Step 389: a float of 17 digits is parsed in one pass too, by Eisel-Lemire, and `Collect`'s copy runs on the query's threads; §4's probe of a million rows of an integer, a float, text and an instant on four threads, 59.7–73.0 ms median where Polars takes 28.2–45.4. Left: the scanner, a quarter; the float, a sixth; the builders' calls made direct measured nothing | P1 |
| **A file-path column is the answer's text:** `WithCSVIncludeFilePaths` over a million rows of a 110-byte path adds 25 ms to a read of 79, where Polars, keeping a scalar column, adds none; the repeat is built once a file and sliced, and what is left is the collected frame's concatenation of 110 MB of text, `data.ConcatStrings`, on the query's threads since step 389 and not measured again here. A constant column the frame holds as one value would cost nothing (step 359) | P2 |
| **A compressed CSV is read in about twice Polars' time:** a million rows of four columns, 34 MB, by gzip in 191–207 ms where Polars takes 83–93, by Zstandard 129–152 against 77–86. The inflate is the read's serial part, Go's (klauspost's, 315 MB/s, the fastest pure-Go one) slower than the one Polars links, and the CSV parse's own gap stands beside it: the plain file in 84–97 ms against 29–44 (step 346) | P2 |
| **A Parquet file's Zstandard is a pure-Go decoder's:** klauspost's, the one arrow-go uses, decodes lineitem's two widest columns at 508–571 MB/s on one thread, where libzstd, through pyarrow, decodes 849; its faster copy is off in its source (`seqdec_asm.go`, `const useSafe = true`) (step 375). The four columns q14 reads at SF=1, summed on one thread, take 248–277 ms where Polars takes 141–156, and zstd is 62% of ursus's, about 170 ms, more than Polars' whole read; `l_extendedprice` alone 100–104 against 56–58; on four threads the four 118–127 against 55–70 and the one 48–54 against 22–29; at a load of 3.3 to 3.5, a first round there reading 342–374 against 167–175 (step 380). The rest of ursus's, about 105 ms, is the sums' 40 and the read's own decode, collections and copies 65, where it was about 115 at step 375. A decoder of ursus's own is out of 0.7's scope | P2 |
| **A Parquet dataset is read in about twice Polars' time:** two million rows in forty Zstandard files, 42–61 ms against 19–28, its column chunks decompressed and decoded; the keys of a hive dataset cost nothing over it (step 347) | P2 |
| **A Categorical is a String's cost:** stored as its text, it groups in 7.6 ms over a million rows of 1000 values where Polars, grouping its codes, takes 2.6; its codes are computed, 41 ms, where Polars' are stored; and it sorts as a String does, 616 ms against Polars' 40, the String sort's own gap (step 351) | P2 |
| **SQL, DuckDB-compatible** (Polars' `SQLContext`, `.sql()`, `sql_expr`): DuckDB's dialect parsed into ursus's plan, so a SQL query shares the optimizer, the spilling and the memory budget, and its answers are checked against DuckDB, the bench's oracle; PDS-H's 22 queries in their SQL the first suite. Out by decision at step 156; wanted since step 487, a theme of its own | **wanted** |
| GPU, plugins, Python bindings, distributed execution | P3 |

## 9. Where ursus differs from Polars on purpose

These are differences, not gaps; each is documented. A completeness release should
decide whether to keep or switch each one.

- The sum or product of an all-null group is null, not 0 or 1.
- `Max` propagates NaN. Polars' `max` ignores it, and `nan_max` propagates it.
- `n_unique` does not count null; Polars counts it as a value.
- `Round` rounds half away from zero by default; Polars rounds half to even, which
  `RoundHalfToEven` selects (step 283).
- `ToDecimal` rounds a tie half away from zero, 0.005 to 0.01 at scale 2, as `Round`
  does by default; Polars 2.1's `str.to_decimal` rounds it half to even, 0.00, 0.015
  to 0.02 and 2.5 to 2 at scale 0. Its doc said Polars rounds half away from zero,
  which 2.1 does not, and was corrected at step 507; ursus's rule stands, the default
  every rounding here shares (`castmore/to_decimal_ties`'s want is Python's decimal,
  ROUND_HALF_UP).
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
- A conditional's branches meet by `Promote`, which refuses text, a Boolean or an
  instant beside a number and a Date beside a Datetime, with a hint to cast; Polars
  2.1's `when` meets a String and an Int64 at String, a Boolean and an Int64 at
  Int64, a Date and a Datetime at the Datetime, and a Datetime(ns) and an Int64 at
  Int64, its ticks. ursus's answer stands: each of those meets guesses what was
  meant, a number made text, a truth value a number, an instant its ticks, and a Date
  is no instant until a zone is chosen (`Promote`'s doc); one cast says which.
  `dtype.Supertype`, a relaxed concat's meet, answers the first three as Polars does
  (step 507; `TestCondRefusals`).
- Two units of one temporal kind meet at the finer, in a conditional, an unpivot, a
  comparison and arithmetic: a Datetime(us, UTC) beside a Datetime(ns, UTC) is a
  Datetime(ns, UTC). Polars 2.1 meets them at the coarser, its 2 ns read as 0 µs.
  ursus's stands: the finer unit holds every value of both, and the coarser drops
  ticks silently (`Promote`, `TimeUnit.Finer`, `Otherwise`'s and `Unpivot`'s docs;
  step 507).
- Two Enums of different categories meet at String, the text both hold exactly
  (step 79); Polars 2.1 refuses them, "failed to determine supertype of enum and
  enum". ursus's stands: an Enum's values are text, and an Enum beside a String meets
  at String in both (step 507).
- `Remove` drops a row whose predicate is null, as `Filter` does; Polars' `remove`
  keeps it (found writing the user guide, step 295).
- A right join keeps the left frame's row order, its unmatched right rows after them,
  and the key where the left frame has it; Polars gives the right frame's order, the
  left's other columns first and the key among the right's (step 295).
- A join on keys of different names keeps both key columns, `JoinLeftOn(a)` and
  `JoinRightOn(b)` giving `a` and `b`; Polars drops the right one.
  `JoinCoalesce(true)` drops it here (step 295).
- `Having` refuses a predicate that is not Boolean, where Polars casts one, so that a
  sum keeps every group whose sum is not 0 (step 309).
- A group's `Head(0)` and `Tail(0)` are no rows; Polars' are a row of nulls for each
  group, of its building them as an aggregation exploded. Their groups always come in
  the order each first appears, Polars' `maintain_order=True`, which is not its
  default (step 308).
- `Concat`'s strict mode meets an Int32 and an Int64 at Int64, where Polars'
  vertical refuses them; a relaxed mode meets an Int64 and a UInt64 at Int128, as
  Polars 2.1 does too (1.44.1's supertype was a Float64; step 503), and a Decimal and
  an integer at the Decimal wide enough, where Polars' is a Decimal(38).
  `ConcatHorizontal` refuses frames of two heights, which Polars 1.44.1 pads,
  deprecated since 1.42.1 for `horizontal_extend`.
  `ConcatAlign` refuses a column in some frames but not every one, where Polars 1.44.1
  keeps the first frame's and drops the others' (its docs say it raises); its ties,
  rows of one key, are in each frame's order, frame by frame, where Polars' are its
  joins' "right_left" (step 307).
- An as-of join refuses a negative tolerance, which Polars takes and searches as
  though it were positive; and a number on a temporal key, which Polars reads as its
  ticks (step 315).
- `AsOfNearest` takes the backward row where it and the forward one are equally far;
  Polars 2.1 takes the forward. ursus's stands: backward is the as-of join's own
  direction, a value known at the key rather than after it (`AsOfNearest`'s doc; step
  507).
- `Upsample` takes a frame unsorted, which Polars refuses; leaves out a row of a null
  instant and answers a frame of no rows with none, where Polars refuses both; and
  keeps its groups in the order each first appears, Polars' `maintain_order=True`. It
  refuses a Date stepped by less than a day, which Polars steps into each date twice
  (step 306). An integer index, Polars' every "1i", it steps since step 312.
- `PartitionBy` gives each part's key as a one-row frame beside the part, where
  Polars' `as_dict` keys a dict by tuples; Go has no map by a tuple. Its parts keep
  the order the keys first appear, as Polars' `maintain_order=True`, which is its
  default; there is no other order to ask for (step 305).
- `Unpivot` with `Index` left out keeps every column it does not melt; Polars' keeps
  none. Its doc chose it, so a column added to the source is kept (step 304).
- `Unpivot` meets the melted columns' types exactly, as `ConcatStrict` does: an Int64
  beside a Float64, a Boolean beside a number and text beside a number are refused,
  where Polars' supertype makes a Float64, a number and text. Cast first (step 304).
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
  (step 296). A count of none is 0, as Polars' is, since step 303.
- A pivot's columns are named by ursus's text of their On values, which differs from
  Polars' for a float (§1), an instant (its `T`, below) and a duration, whose String
  cast Polars refuses. A combination holding a null is written `{1,null}`; Polars
  names that whole column "null", and refuses a second. Several On columns' instants
  and durations are written as their casts are, where Polars' display writes
  `2024-01-02 03:04:05` and `1m 30s` there (step 303).
- A pivot's `Agg` reads only the value it is given, and one reading another column
  is refused; Polars' aggregate is an expression of `pl.element()`, which can read
  nothing else. A lazy pivot keeps the order each index first appears, as Polars'
  `maintain_order=True`, Polars' lazy default leaving it open; and it holds a row for
  each index and On values that meet, refusing past a memory limit (step 303).
- A fill of a whole-number float keeps an integer column's type: `FillNullWith(2.0)`
  and a frame's `FillNull(0.0)` of an Int64 column give Int64, and only a fraction,
  `0.5`, widens it to Float64. Polars widens it for any float (step 297).
- A fill that does not fit the column falls back to the literal's own type:
  `FillNullWith(5000)` of an Int8 column is an Int64, as a Go int's literal is. Polars
  2.1's `fill_null(5000)` is an Int16, the narrowest type that holds it. ursus's
  stands: falling back is ordinary promotion of the literal's own type, as every
  literal here has its Go value's type, not a type picked by its magnitude
  (`FillNullWith`'s doc; step 507).
- `FillNullWith` of text outside an Enum's categories gives a String column holding
  it, the Enum meeting the text at String; Polars 2.1 keeps the null and the Enum.
  ursus's stands: Polars' answer loses the value silently, ursus's changes the type,
  which the schema shows (`FillNullWith`'s doc; step 507).
- A cast of a float with a fraction to an integer is refused, "not representable",
  and is null when lossy; Polars truncates it toward zero, strict or not: 1.5 is 1,
  −1.7 is −1. ursus's casts are exact or refused; `Truncate(0)` first for Polars'
  answer. Listed by step 298, where a frame's cast inherits it.
- A Decimal does not cast to a Boolean, nor a Boolean to a Decimal: a truth value is
  not an amount (step 69). Polars 2.1 casts a Decimal to true where it is not zero,
  and refuses the other way. ursus's stands: `Gt(0)` says in one comparison what zero
  means (`Cast`'s doc; step 507; `TestDecimalGuards`).
- `Any` and `AllTrue` refuse a Decimal, "requires a Boolean operand", for the cast's
  reason; Polars 2.1's `any` and `all` answer one, true where a value is not zero.
  ursus's stands as the cast's does: `Gt(0).Any()` (`Any`'s doc; step 507;
  `TestDecimalGuards`).
- A cast of an instant to text is ISO 8601's, `2026-01-01T00:00:00.000000`, where
  Polars writes a space for the `T`. A naive one is written with no offset, as Polars
  writes it, since step 422; before, it gained a `Z` (step 298).
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
- The median and the midpoint of two finite floats do not overflow, nor the variance
  of two alike: the median of [−1.7e308, 1.7e308] is 0, the midpoint of [1e308,
  1.5e308] 1.25e308, and the variance of [1e308, 1e308] 0; Polars 2.1 answers +Inf,
  +Inf and NaN. ursus's stands: each answer is representable, and exact to a few ulps
  (`Quantile`'s and `Var`'s docs; step 507; `TestAggregatesAtTheFloatsEnds` holds them
  to math/big).
- `Skew` and `Kurtosis` shift each group's values by its first, so the skew of [1e9+1,
  1e9+2, 1e9+4] is that of [1, 2, 4], 0.38180177416060623, as 1.44.1's was; Polars 2.1
  takes a float mean at 1e9 and answers 0.38180167858053743, seven digits lost, and
  two thousand values near 1e9 7e-9 off in both moments. ursus's stands: a central
  moment does not move with a shift, and the shifted sums keep the digits (`Skew`'s
  doc; step 507; `TestSkewAndKurtosis` holds them to big.Float).
- A frame's `Median` of a Decimal answers, as `Expr.Median` does; Polars 1.44.1's
  `DataFrame.median` gives null for one, though its `quantile` answers (step 300).
- `ForwardFill(0)` and `BackwardFill(0)` mean no limit, where Polars'
  `forward_fill(limit=0)` and `backward_fill(limit=0)` fill nothing. So a strategy's
  limit is at least 1: `FillForward.Limit(0)`, which Polars' `fill_null(limit=0)`
  answers by filling nothing, is refused rather than meaning either (step 301).
- The mean strategy of a fill makes a Float64 of an Int32 or Int64 column and fills
  the mean, 4.5; Polars 2.1's `fill_null(strategy="mean")` keeps the integer type and
  fills the mean truncated, 4. The six other strategies agree. ursus's stands: a mean
  is a float, and truncated it fills a value the data does not hold (`FillMean`'s doc;
  step 507).
- `Interpolate` of a Decimal answers a Float64, where Polars' frame `interpolate`
  keeps the Decimal (step 301).
- A fold in `CumulativeEval`'s body answers, built ahead into a value a prefix, where
  Polars refuses one, "not allowed with non-scalar output", its function opaque to it
  (step 302).
- `HashRows` takes one seed, where Polars' takes four, and its numbers are ursus's own,
  as `Hash`'s are (step 302); a List or a Struct column is hashed as a key since step
  344.
- `Div` and `Pow` always return a float.
- A float remainder of zero takes the divisor's sign, −0 for 4 % −2, as Python's %
  does; Polars 2.1's `a − b·floor(a/b)` is +0. ursus's stands: `Mod` takes the
  divisor's sign, a zero's too (its doc), Python's rule, which its want is computed by
  (`scalar_byhand/float_mod_zero_sign`; step 507).
- `Cut` answers an Enum and `QCut` a Categorical, as Polars' do, since steps 350 and
  352.
- An expression's `TopK`, `BottomK`, `TopKBy` and `BottomKBy` return a List. `TopKBy`
  and `BottomKBy` order rows that tie on by by their values, the greater first for
  `TopKBy`, where Polars' sort orders ties as it finds them (step 330).
- `TopK` and `BottomK` skip nulls: a group with fewer than k values gives a shorter
  list, and one with none null, as Polars 1.44.1 did; Polars 2.1 keeps nulls as the
  least values, [7, null] of [7, null] and [null, null] of three nulls. ursus's
  stands: a null is no value, and every aggregate here skips it (`TopK`'s doc; step
  507).
- `Time` takes a unit; Polars' is nanoseconds.
- `GroupByDynamic` with keys puts the index column first and the keys after it;
  Polars puts the keys first (found at step 251).
- `Round` by a month rounds to the nearer month's start, a value exactly halfway up,
  nearer in elapsed time, so noon on 2024-02-15 is March and 2024-01-17 February;
  Polars 2.1 adds fourteen days and truncates, so both round down. ursus's stands: it
  is the rule of every other grid `Round` takes (its doc; step 507).
- A dynamic group's windows lie on `Truncate`'s grid, so a day whose midnight the
  zone skips starts where it begins, 01:00 for Santiago's 2024-09-08, as Polars' own
  `truncate("1d")` puts it; Polars 2.1's `group_by_dynamic` starts that window at
  23:00 on the 7th and puts 23:30 on the 7th in it. ursus's stands: a day's window is
  the day (`GroupByDynamic`'s doc; steps 145 and 507).
- `Combine` of a zoned Datetime reads its day on the zone's wall clock, so 22:00 on 11
  March in New York with 13:15 is the 11th; Polars 2.1 reads the day in UTC, the
  12th. ursus's stands: a zoned value's day is its zone's, as `MonthStart`,
  `DaysInMonth` and every component read it (`Combine`'s doc; step 507).
- `BusinessDayCount` with the end before the start is minus the business days in
  [end, start), 0 from a Monday back to the Saturday before; Polars 2.1 counts those
  in (end, start], numpy's `busday_count` rule, −1 there. ursus's stands: the same
  days are counted whichever end is first, the sign telling which (its doc; step
  507).
- `"3i"`, an integer interval, is refused where a date is moved: by `Truncate`,
  `Round`, `OffsetBy`, a range and an as-of tolerance. Polars' `truncate` and
  `offset_by` take it and move nothing, its `round` panics, dividing by zero, and its
  as-of tolerance reads it as nanoseconds on an instant and panics on an integer key.
  A weekday `StartBy` on an integer index is refused, as on any grid not in weeks,
  where Polars ignores it (step 312).
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
- `Hist`'s category is a Categorical, as Polars' is, since step 352 (text from step 286).
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
  and a List's or a Struct's, which have no order, come as they first appear (step
  344). `CorrSpearman` takes numbers;
  Polars ranks text too (step 277). Its sums are exact since step 339, its answer
  within an ulp of the true one, where Polars' float sums are a few ulps off.
- `Entropy` without normalize is −Σ x·ln x of every group, −5 ln 5 for [5]; Polars 2.1
  answers 0.0 for a group of one value. ursus's stands: one value is a sum of one
  term, as two are of two (`Entropy`'s doc; step 507).
- `Arr()`'s functions answer as `List()`'s (step 342): an integer `Sum` is an Int128
  where Polars' `arr.sum` is an Int64, `NUnique` passes a null over where Polars
  counts it, and `ToStruct` of a null array is a null struct, as Polars 2.1's is
  (1.44.1 gave a struct of nulls; step 499). `Head`, `Tail` and `Slice` give a List,
  Polars' default; its `as_array`, which panics on a null row in Polars 1.44.1, is
  `List().ToArray(n)`.
- NDJSON: a lone surrogate's escape reads as U+FFFD, where Polars reads a high one as
  NUL and refuses a low one; a number past a float's range, `1e400`, is ±Inf, which
  Polars refuses; a given schema of an Int8, an Int16, a UInt8, a UInt16, a Time, a
  Duration, a Decimal or a Binary reads, where Polars panics or refuses; an error
  names its line and its key, where Polars' names neither (step 334).
- JSON: a document of one object of no key, `{}` or `[{}]`, is a row of no column,
  where Polars' `read_json` reads no row (and reads `[{}, {}]` as two). A later value
  its column cannot take is refused, `true` or `1.5` in an Int64 column, as both
  refuse it in NDJSON, where Polars' `read_json` reads it as 1. Schema overrides
  set a given schema's types in NDJSON too, where Polars' `read_ndjson` keeps the
  schema's (step 345).
- Compressed input: a stream a zlib header begins, `78 5e` ("x^") among them, is
  read as zlib only where it is zlib's beyond doubt, decompressed to its end or its
  first 64 KiB to a quarter more than they are, so a CSV whose header begins "x^" is
  read as text, where Polars reads it as zlib and finds no rows; a stream past a zlib
  header that does not decompress is text, where Polars refuses it as "corrupt
  deflate stream"; bytes after a zlib stream are refused (step 346).
- Hive datasets: a directory's names beginning '.' or '_' are passed over, files and
  directories, where Polars reads a hidden directory and refuses a `_metadata` or
  `.crc` file beside the data, of another extension; `WithHiveSchema` may name some
  keys, the rest inferred, where Polars refuses a key its `hive_schema` does not
  name; a date of a two-digit year, `24-01-02`, is text, where Polars reads the year
  2; every file's footer is read when the query is planned, where Polars reads the
  first's (step 347).
- A hive key of dates in several layouts is a Date, each value read by its own
  layout as CSV dates are, below; Polars 2.1 reads every value by the first's layout
  and refuses the third, "could not parse '2024/01/02' with pattern 'DateDMY'".
  ursus's stands as CSV's rule does (`WithHiveDates`' doc; step 507).
- Globs: an element of `**` last matches every file below it, `logs/**`, where
  Polars' matches none (step 349).
- Categorical: its codes number its values in the order they first appear over the
  frame, where Polars' are its process's string cache's, the same in a fresh process;
  a group-by's aggregation and a window refuse them. A String's functions read it as
  its text, as an Enum's, and a join or a comparison with a String meets at the
  text, where Polars refuses the functions and the join; a cast of a number to it is
  refused, where Polars reads the number as a code (step 351).
- Text parsed without a format (`ToDate`, `ToDatetime`, `ToTime`, `Strptime` of ""): as
  CSV dates below, each value by its own pattern; a wall clock a zone skips or reads
  twice is null when lenient, where Polars raises unless told; and an offset's text is
  the naive Datetime of its UTC wall clock, or the zone's that is asked for, where
  Polars' `to_datetime()` of no zone types the column `Datetime(us, UTC)` from the data
  (step 356).
- CSV reading (step 357): a file of blank lines and no header is no rows, where
  Polars reads a column of nulls, a row a line. `WithSkipRows(2)` of a banner whose
  field holds a quote mid-way, `junk "q`, skips it as the record it is, where Polars'
  `skip_rows` opens a quoted field there and refuses the file as empty (step 358).
- A CSV column of only `NaN`, `inf` or `infinity` infers String: such a word counts as
  a float only beside a number, a column of nothing else likelier words; Polars 2.1
  infers Float64. ursus's stands, step 77's rule, and a float column with a NaN,
  written by either, still reads back as a float (`ScanCSV`'s doc; step 507).
- CSV dates (`WithTryParseDates`, and a schema's temporal columns): each value is read
  by its own pattern, so 2024-01-02 beside 02/01/2024, and 10:30 beside 10:30:00.123,
  read, where Polars reads a column by its first value's pattern and refuses the
  other; a value with a space before it, ` 2024-01-02`, is text, which Polars reads as
  a date; and `2024-01-02 03:04:05UTC` is text, which Polars infers an instant and then
  refuses (step 355).
- The CSV writer: a field beginning with a space is quoted, which Polars writes bare,
  and so is a temporal value whose format writes the separator, `%Y-%m-%d,%a`, which
  Polars writes bare, a record of one field too many; a Duration is written as the
  frame prints it, which Polars refuses; under a decimal comma, scientific notation is
  each value's, where Polars 1.44.1 runs a column's values together, `2e01e-7`; a
  Zstandard level past 22 is refused, which Polars takes, and a gzip level past 9,
  where Polars panics (step 354).
- An instant whose offset has seconds, Monrovia's −00:44:30 in 1970, is written to
  CSV in UTC and reads back exactly; Polars 2.1 writes the wall clock with the offset
  rounded to −0045 and reads back an instant 30 s away. ursus's stands: +hhmm cannot
  spell the offset, and UTC's text is the instant (I8, step 77;
  `WithDatetimeFormat`'s doc; step 507).
- A frame of no rows and no columns writes nothing to CSV, having no header to write;
  Polars 2.1 writes a bare newline, its empty header. ursus's stands: the empty text
  reads back under `WithRaiseIfEmpty(false)` as the frame it was, where a newline is a
  line (step 77; `WriteCSV`'s doc; step 507).
- Databases: every row's value types a column, where Polars reads 100 and reads a
  later float in an integer column as its integer part, or refuses a later kind; an
  instant beside text meets at text, by Polars' supertype, where Polars refuses it; a
  schema override of a column the result has not is refused, as NDJSON's is, where
  Polars ignores it, and one of text parses it, a Date or a Decimal, where Polars'
  builder refuses or nulls it. A Go driver's own types are taken where its values fit
  them, so SQLite's DATE, TIMESTAMP and BOOLEAN columns, which go-sqlite3 and modernc
  give as instants and Booleans, are a Date, a Datetime and a Bool, where Python's
  sqlite3 gives Polars their text and integers (step 353).
- A database written: a table's columns are of ursus's own types for its dialect,
  where Polars' are SQLAlchemy's or ADBC's; the dialect is the driver's package's, or
  named, since `database/sql` has no name for it. A Duration is refused, which
  SQLAlchemy writes as an INTERVAL in PostgreSQL. SQLite has no decimal, and its
  numeric affinity keeps a Decimal as a REAL, read back as a Float64; nor an instant
  with a zone, so one is SQLite's text with its offset, read back as its instant in
  UTC. MySQL keeps one at its UTC wall clock, read back with no zone (step 366).
- A partitioned write: a time of day's key keeps its fraction, `03:04:05.123456`,
  where Polars' path drops it, `03:04:05`, so a dataset of such keys reads back as it
  was; a key's name is escaped as its value is, `a%20b%2Fc=1`, and read back so,
  where Polars writes it as it is, and a slash in it makes a directory, read back as
  another key. Past 64 keys' files open at once, the least recently written closes, and a
  later row of its key begins its next file, numbered on, where Polars holds a file a
  key open (step 367).
- Text exports to Arrow as a String, or a Binary, where its rows' characters fit 32-bit
  offsets, and as a LargeString or a LargeBinary past 2 GiB, where Polars' export is
  always the large type, or a view. A batch a source reads, a Parquet column chunk's,
  a CSV block's or one Arrow record's rows, holds at most 2 GiB of text a column and
  refuses past it; a whole frame past it is held (step 368).
- An aggregate whose answer depends on the order, `First` or `Last`, inside a window
  with an `OrderBy`, `(x - x.First()).CumSum(false)` over g ordered by t, is refused as
  an ordering over an aggregate is; Polars takes the first in the order (step 336).
- `Expr.Rolling` of aggregates with windows of their own finds each window by value,
  so an index out of order is answered, which Polars refuses. Rolled by the frame's
  rolling group instead, under `Over`, it holds every window until the input ends, as a
  partition's rows may come after another's later ones, and it refuses an index not
  sorted within a partition, where Polars answers from the rows as they come. A frame's
  keyed `Rolling` and `GroupByDynamic` want the index sorted across the frame, where
  Polars' want it sorted within each group (step 337).
- A rolling window whose `Offset` is as long as its `Period`, behind its row, ends at
  the row, a month measured as 28 days: with period 1mo and offset −28d, Feb 28's
  window is (Jan 31, Feb 28], [2]. Polars 2.1 ends it at its start plus the period,
  (Jan 31, Feb 29], [2, 3], unless the offset is the period's own length; 1.44.1
  measured as ursus does. `RollingOptions.Offset`'s doc said "as Polars' does", which
  2.1 does not, and was corrected at step 507; ursus's rule stands, step 313's: a
  window that far behind its row looks no further than the row, whatever the month's
  length.
- `BaseUtcOffset` and `DstOffset` read the system's zone database, so where it is in
  the rearguard form, as Ubuntu's is, Dublin's and Casablanca's daylight-saving time
  is positive. Polars bundles the main form, where it is negative. Other zones agree
  (step 272).

## 10. What ursus has that most others do not

A completeness release should keep these intact:

- spilling for every blocking operator, with bit-exact answers, a streamed rolling
  var, std, skew, kurtosis, cov and corr among them since steps 429 and 430;
- a memory budget on by default;
- order-deterministic output at any thread count, but a float aggregate's last bits
  (§1);
- `ursustest` with golden plans;
- validation of every benchmark answer against DuckDB.

## 11. Against Polars 2.1

Polars 2.0 shipped after the parity work, which was against 1.44.1.
[`step-487-as-built.md`](./step-487-as-built.md) matched 2.1.0's 853 public names
outside `Series` against ursus's API and ran 2.0's semantic changes in both. The
expression oracle's 46 cases answer under 2.1 as under 1.44.1.

| missing | Polars | prio |
| --- | --- | --- |
| `Erf`, `Erfc` | `erf`, `erfc` | P1, step 488 on |
| `TruncDiv`, division toward zero | `truncdiv` | P1, step 488 on |
| `Round`, `Ceil`, `Floor` and `Sign` of a Decimal, refused today | `Decimal(p+1, s)` since 2.0 | P1, step 488 on |
| `Name().StripPrefix`, `StripSuffix` | `name.strip_prefix`, `strip_suffix` | P2, step 488 on |
| `ScanLines`, `ReadLines`: a file's lines as one String column | `scan_lines`, `read_lines` | P1, step 488 on |
| `CollectAll`, `ExplainAll`: queries run together, a shared `Cache` once | `collect_all`, `explain_all` | P1 |
| the Map type, `List().ToMap()`, Parquet and Arrow maps read as Maps | `Map`, `list.to_map` | P2 |
| `ApproxQuantile` in bounded memory | `approx_quantile` | P2 |
| `Struct().Eval` | `struct.eval` | P2 |
| `BinIntervals`, `BinQuantiles`, `BinRanks`; `Cut` and `QCut` answer the first two | 2.0's spellings, `cut` and `qcut` deprecated | P2 |
| `LazyFrame.Gather`, `DataFrame.IsSorted(by)`, Parquet key-value metadata | `gather`, `is_sorted`, `read_parquet_metadata` | P2 |
| `Meta()`'s `IsScalar` and kin, `SetSorted`, `json_normalize`, `align_frames` | | P3 |

**Where ursus answers as 1.44.1 and 2.0 does not,** each a break for the maintainer
to decide:

- `Explode` of an empty list is one null row; 2.0's is none.
- A headerless CSV's columns are `column_1`, …; 2.0's start at `column_0`.
- `Repeat` and `MakeDatetime` are named `repeat` and `datetime`; 2.0 names them after
  their first input.
- `Int64.IsIn(1.5)` answers false, and `Cast(String → Date)` parses; 2.0 refuses
  both.
- Parquet and Arrow map columns read as `List(Struct)`; 2.0's are its `Map` type.

## 12. Found by the recorded oracle, for the maintainer

**No finding is open.** Waves 496 to 503 of [`oracle-v2-scope.md`](./oracle-v2-scope.md)
asked Polars 2.1.0 the suite's questions, and where it answered otherwise than ursus
and no line of §9 or §11 said ursus meant to, the case was recorded and skipped:
twenty-five findings in thirty-nine cases, collected by step 504. Step 507 read them
under the maintainer's rule of 2026-11-14, "ursus doesn't need to be clone of polars.
polars has features and we need those features, thats it": a difference is fixed only
where ursus is wrong by its own rules or docs, and otherwise documented in §9. One was
a bug, fixed; two docs claimed a Polars behaviour Polars 2.1 does not have and were
corrected, their rules kept and documented in §9; twenty-two were ursus's own rules,
documented in §9. Every case is asserted again; none is skipped
([`step-507-as-built.md`](./step-507-as-built.md)).

| finding | where it went |
| --- | --- |
| text, a Boolean, a Date or an instant beside a number | §9, a conditional's branches meet by `Promote`; `TestCondRefusals` |
| two time units | §9, the finer unit |
| two Enums of different categories | §9, at String |
| a fill past the column's type | §9, the literal's own type |
| a zero float remainder's sign | §9, the divisor's sign |
| `ToDecimal`'s tie | §9, half away from zero; its doc corrected |
| a Decimal cast to a Boolean | §9, refused; `TestDecimalGuards` |
| rounding to a month | §9, the nearer month |
| a dynamic group's day where midnight does not exist | §9, `Truncate`'s grid |
| `Combine` of a zoned Datetime | §9, the zone's wall clock |
| business days counted backwards | §9, [end, start) |
| a rolling window's end under an offset | §9, at the row; `RollingOptions.Offset`'s doc corrected |
| the ends of a float's range | §9, held exact; `TestAggregatesAtTheFloatsEnds` |
| a skew of large values close together | §9, shifted by the first |
| a one-value group's raw entropy | §9, −Σ x·ln x |
| nulls in a `TopK` | §9, skipped |
| `Any` and `AllTrue` of a Decimal | §9, refused; `TestDecimalGuards` |
| `FillNullWith` outside an Enum's categories | §9, a String |
| the mean strategy of a fill | §9, a Float64 |
| `AsOfNearest`'s tie | §9, backward |
| a CSV column of only `NaN` | §9, String |
| an offset with seconds through a CSV | §9, written in UTC |
| a frame of no rows and no columns written to CSV | §9, nothing |
| a non-object in an NDJSON Struct column under ignore_errors | §1, fixed: a null struct |
| a hive key of dates in several layouts | §9, each by its own layout |
