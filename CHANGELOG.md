# Changelog

ursus follows Go's rule for v0: **nothing is promised**, and a minor version may
change behaviour. Each release below therefore says what a user of the previous one
can trip on, not only what is new.

Every step of construction has an as-built record in
[`context_files/`](./context_files/), with the measurements behind each change.
Step numbers below point at those records.

---

## v0.3.0 — unreleased

Everything since `v0.2.0` (2026-09-04): steps 22–83, about 250 commits.

The scope was set by [`v0.3-scope.md`](./context_files/v0.3-scope.md): **the types
and the I/O are honest**. Most of the release is correctness work — a measured audit
([`audit.md`](./context_files/audit.md)) found silent wrong answers across the engine,
and steps 51–81 fixed them with the instruments that would have caught them.

### Highlights

- **Nested data, end to end.**
  - List and Struct columns read from Parquet (steps 28–34) and Arrow (60), and
    **write to Parquet** (81). A Parquet file with a nested column it cannot read is
    no longer unopenable as a whole (28).
  - `Explode` (30) and `Unnest` (35).
  - A `.list` namespace of sixteen functions (32, 33), and `.struct.field()` (34).
  - `Str().Split`, `SplitN` and `ExtractAll`, which build lists (45).
  - `Implode`, which collects a group into a list (46).
- **Decimal you can compute with.**
  - `sum` is exact (69).
  - `+ - *` are exact and refused past 38 digits; `/` gives the nearest Float64
    (80).
  - Every cast between Decimal and a number is exact or refused (69).
- **Enum you can build:** from String, ordered by its categories, and compared with
  String (79).
- **Joins:**
  - `JoinWhere` for non-equi joins (43);
  - `WhereExists` and `WhereNotExists` (44);
  - a parallel join probe (36);
  - and join keys of different types meet at a type that holds both exactly (74).
- **Reshaping:** `Unpivot` (47). `Pivot` was decided against for now, with the
  reason recorded: its schema would depend on the data.
- **UDFs:** `MapElements` per value and `MapBatches` per column, as plan-visible Go
  functions (50).
- **Arrow both ways:** `df.Record()`, `df.ArrowSchema()` and `lf.CollectRecords()`
  export without copying (58); `ScanArrow` and `ScanArrowRecords` import (60).
- **Spilling that covers what people hit.**
  - `Unique` and windows with a partition key spill, and keep their order exactly.
  - Sort, group-by and join spill frames that carry nested columns.
  - Every operator that cannot spill fails with an error naming itself (82).
- **Reading from anywhere:**
  - `ScanParquetFrom` reads Parquet through any `io.ReaderAt`, and `ScanCSVFrom`
    CSV through any stream (83). A ranged GET reaches an object store, and ursus
    fetches only the footer and the column chunks a query needs.
  - Several files are read by column name (71).
- **Performance:**
  - The CSV reader makes 126x fewer allocations, and its scan is about twice as
    fast (22).
  - Groups are found through an open-addressed key table instead of a Go map (23).
  - The radix sort carries its keys with its rows: 2.4x faster at a million rows
    (24).
  - In-memory frames are split into batches, so `WithThreads` applies to them (37).
  - CSV columns convert in parallel (42).
  - `n_unique` halves its peak memory on q21 (39).

### Behaviour changes a v0.2 user can trip on

**Types and casts**

- A literal keeps its type: `i8 + int8(100)` wraps in Int8, and `i8 + int64(100)`
  widens. Before, the two collided in the plan cache and gave one answer (72).
- A string casts to an integer exactly, not through a float64:
  `"9007199254740993"` stays itself, and a strict `"256"` → Uint8 is refused, not
  nulled (73).
- A cast to a float rounds to the nearest value, as a float cast should; a strict
  cast no longer refuses 0.1 → Float32 (73).
- An Enum compares equal to a String as text, orders strictly by its own
  categories, and casts through its text (79).

**Arithmetic and functions**

- **Decimal results have new types (80):**
  - `+ -` give Decimal(max integer digits + max scale + 1, max scale);
  - `*` gives Decimal(p1+p2, s1+s2);
  - each is refused past 38 digits;
  - `/` returns Float64;
  - Decimal with a float is Float64, and Decimal with an Int128 is refused.
- `FloorDiv` floors and `Mod` is its remainder, for integers, floats and Durations.
  A float `%` follows Python, including ±Inf and the signed zero (76).
- `Dt().Epoch()` floors (76).
- `Truncate` and `GroupByDynamic` floor the wall clock, sub-day intervals included
  (76).
- `IsIn` and `list.contains` compare as `Eq` does (76).
- `SplitN(0)` is unlimited (76).
- A first-match regex `Replace` expands its groups (76).
- The one-sided strips trim Unicode whitespace (76).
- **Temporal arithmetic is checked, not wrapped:**
  - Date arithmetic works (52), and a Time is a time of day (57).
  - Arithmetic that leaves a type's range is refused rather than wrapping (61).
  - Duration aggregates are exact (62).
  - The as-of tolerance compares in the key's own unit (63).
  - An interval that cannot be represented is not built (66).
  - A dynamic group-by's window grid covers every row, or refuses at a named
    ceiling, where it used to stop early and answer short (67).
  - An interval finer than its index's resolution is refused (68).

**Aggregations and windows (75)**

- An integer `mean` divides an exact sum.
- A `quantile` at an exact rank is that value.
- A variance is never negative.
- `PctChange` computes in float.
- A Bool `sum` counts its trues.
- `Closed` is validated.
- A window reads the columns defined before it.

**Joins (74)**

- Keys of different types are cast to a type that holds both, strictly.
- A 64-bit integer key against a float key is refused.
- `AsOfBy` no longer matches a null by-key to a null by-key.

**I/O**

- Several Parquet or CSV files are matched by column name. A file that differs is
  refused, naming both files, where before its values could be swapped silently
  (71).
- Row groups are pruned only on what their statistics prove (71).
- **CSV (77):**
  - an offset with seconds is written in UTC;
  - floats follow one grammar, with no underscores or hex;
  - a blank line in a one-column file is a null;
  - Int128 and Decimal read exactly;
  - a null String and an empty one are written and read apart;
  - a column of only NaN stays String;
  - the writers refuse a frame with rows and no columns.
- Parquet reads lists of Bool, the unsigned types, Decimal, Int128 and Time(ms)
  (81).
- A file that fails to open or read is named in the error, for CSV and Parquet
  alike (83).

**The optimizer (70, 78)**

- No rule changes an answer any more; a generated on/off differential over 1629
  query shapes agrees.
- The planner refuses what `Collect` would refuse, so some errors now arrive at
  plan time.

**Memory limits (82)**

- A window and an as-of join are now charged for the second copy they make. A
  window spills, and an as-of join or a window with no partition key refuses, at
  about half the input size it used to.
- A group-by is charged for its key table, so it may freeze sooner under a limit.
  The row order of an unordered group-by under a limit has always depended on the
  limit, and it moves.

**Errors**

- A panicking UDF is the caller's `ErrValue`, and a corrupt Parquet file is
  `ErrIO`. Neither kills the process any more: worker goroutines recover (72).
- Two UDFs sharing a name are refused, where before they merged into one
  computation (65).

### API

Every addition is in the root package unless named.

- **Frames:** `DataFrame` and `LazyFrame` gain `JoinWhere`, `WhereExists`,
  `WhereNotExists` and `Unpivot` (with `UnpivotOptions`). `LazyFrame` also gains
  `Explode`, `Unnest` and `CollectRecords`. `DataFrame` also gains `Record` and
  `ArrowSchema`.
- **Expressions:** `Expr.Explode`, `Expr.Implode`, `Expr.Unnest`, `Expr.MapElements`
  and `Expr.MapBatches`.
- **Namespaces:**
  - `Expr.List` returns a `ListExpr`: `Len`, `Get`, `First`, `Last`, `Contains`,
    `Min`, `Max`, `Sum`, `Mean`, `Reverse`, `Head`, `Tail`, `Slice`, `Sort`,
    `SortDesc`, `Unique` and `DropNulls`.
  - `Expr.Struct` returns a `StructExpr`, with `Field`.
  - `StrExpr` gains `Split`, `SplitN`, `ExtractAll`, `PadStart`, `PadEnd`, `ZFill`,
    `StripCharsStart`, `StripCharsEnd` and `EscapeRegex`.
- **Sources:** `ScanArrow`, `ScanArrowRecords`, `ScanParquetFrom` with
  `ParquetFile`, and `ScanCSVFrom` with `CSVFile`.
- **`dtype`:**
  - `PromoteExact`, `ExactMismatch`, `MeetHint`, `DecimalDigits`, `DecimalMeet`,
    `ValidDecimal`, `ParseFloat`, `TicksPerDay` and `DataType.FromTimeBound` are
    new.
  - **`DaysInterval` and `MonthsInterval` are removed.** They built intervals
    nothing validated (66). Use `IntervalOf` or `Every`.
- **`i128`:** `ParseDecimal`, `Int128.MulChecked`, `Int128.DivUint64` and
  `Int128.Float32`.
- **Unchanged:** no other exported identifier was removed or changed its parameters.
  Some methods gained named results, which callers cannot see.

### Known limitations

- **Object stores** have no built-in client. S3, GCS and Azure each want a vendor
  SDK, and ursus is pure Go with one dependency; `ScanParquetFrom` and
  `ScanCSVFrom` are the seam, and the stores are planned for 0.4
  ([`v0.3-scope.md`](./context_files/v0.3-scope.md) §4).
- **Operators that do not spill:** reverse, hstack, tail, `JoinAsOf`,
  `MergeSorted`, `Rolling`, `GroupByDynamic`, a window with no partition key, a
  cross join, and quantile-like aggregates over a few very large groups. Each fails
  under `WithMemoryLimit` with an error naming itself.
- **Not built:** SQL, `Pivot`, `MapGroups`, common subexpression elimination,
  `Expr.Rolling*`, `Upsample`, `Interpolate`, and the trigonometric functions.
- **Nested types:** a List of Lists, a List of Structs and a Struct holding either
  are refused by name, in Parquet both ways. Array has no column representation.
  CSV has no nested representation and refuses nested columns.
- **Still-open audit rows** are listed with their severity in
  [`audit.md`](./context_files/audit.md). Among them is O14: a UDF that calls
  `runtime.Goexit` ends its worker as if the stream had ended, and the rows after
  it are dropped. Nothing in ursus calls `Goexit`.
- **Speed:** ursus is slower than Polars and DuckDB, and the README's table says
  by how much.

---

## v0.2.0 — 2026-09-04

The module renamed to `github.com/advenn/ursus`, and everything before step 22: the
engine, Parquet and CSV, the optimizer, spilling sort, group-by and join, and
parallel execution.
