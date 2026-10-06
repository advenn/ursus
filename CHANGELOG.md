# Changelog

ursus follows Go's rule for v0: **nothing is promised**, and a minor version may
change behaviour. Each release below therefore says what a user of the previous one
can trip on, not only what is new.

Every step of construction has an as-built record in
[`context_files/`](./context_files/), with the measurements behind each change.
Step numbers below point at those records.

---

## Unreleased

- **A group-by under a limit you set is serial again,** so a group-by that spills
  gives its rows in the same order on every run, as in v0.3.0.
  - In v0.3.1 it ran parallel until half of any budget, and where it switched
    depended on thread scheduling. The contents were always right, but the order
    of an unordered group-by could change from run to run.
  - CI caught it: `TestGroupBySpillIsDeterministic` is flaky in the v0.3.1 tag.
  - Parallel-then-serial now applies only under the default budget, where a switch
    needs half the machine or the container (92).

---

## v0.3.1 — 2026-10-06

Steps 89–91: what benchmarking v0.3.0 found, fixed.

- **A query is budgeted by default, so it spills under a container's limit.**
  - With no `WithMemoryLimit`, the budget is half the smaller of the cgroup's memory
    limit (the least over the process's cgroup and its ancestors) and the
    machine's RAM, on Linux.
  - v0.3.0 spilled only under an explicit limit, and was killed by the kernel
    under a cgroup's instead: h2o gb10 over CSV, at the benchmark's 8 GB.
  - `WithMemoryLimit(0)` is unlimited, the old default. Off Linux nothing changes.
  - **Behaviour change:** an operator that cannot spill now fails at that point,
    with an error naming itself, rather than running into swap (90).
- **A group-by stays parallel under a budget.**
  - It runs on several workers until the query holds half its budget, then folds
    them into one and finishes serially, spilling as before.
  - In v0.3.0 any limit made it serial outright, which with a default budget
    would have made every group-by serial: about 1.5× slower on h2o's small ones,
    measured (91).
- **PDS-H q7 is fixed:** 1.55 s and 0.59 GB, against 3.9 s and 1.68 GB in v0.3.0.
  A temporal cast that cannot fail, such as a nanosecond Datetime narrowed to a
  Date, no longer keeps a filter above the joins it should be pushed into (89).

---

## v0.3.0 — 2026-10-06

Everything since `v0.2.0` (2026-09-04): steps 22–88, about 270 commits.

[`v0.3-scope.md`](./context_files/v0.3-scope.md) set the scope: **the types and the
I/O are honest**. Most of the release is correctness work. A measured audit,
[`audit.md`](./context_files/audit.md), found 78 defects, about 40 of them silent
wrong answers. Steps 70–85 fixed nearly all of them, each alongside the instrument
that would have caught it.

### Highlights

- **Nested data, end to end.**
  - List and Struct columns read from Parquet (steps 28–34) and Arrow (60), and
    **write to Parquet** (81). pyarrow and Polars read ursus's nested files value for
    value.
  - A file holding a nested column ursus cannot read no longer refuses as a whole
    (28).
  - `Explode` (30), `Unnest` (35), `Implode` (46) and `Unpivot` (47).
  - A `.list` namespace of seventeen methods (32, 33), and `.Struct().Field()` (34).
  - `Str().Split`, `SplitN` and `ExtractAll`, which build lists (45).
- **Decimal you can compute with.**
  - `sum` is an exact Decimal(38, s); `mean`, `var`, `std`, `median`, `quantile` and
    `product` are Float64 (69).
  - `+ - *` are exact and refused past 38 digits; `/` is the nearest Float64 (80).
  - Every cast between Decimal and a number is exact or refused (69).
  - Decimals of different scales compare, join and concatenate exactly (80).
- **Enum you can build:** cast from String, strict or lossy. Sort, group-by, join,
  unique and windows run in category order, and CSV reads and writes it (79).
- **Joins:**
  - `JoinWhere` for non-equi joins (43);
  - `WhereExists` and `WhereNotExists` (44), which made PDS-H q21 1.4× faster with
    1.75× less memory;
  - a parallel join probe (36);
  - keys of different types meeting at a type that holds both (74).
- **UDFs:** `MapElements` per value and `MapBatches` per column, Go functions that
  take part in the plan (50).
- **Arrow both ways:** `df.Record()`, `df.ArrowSchema()` and `lf.CollectRecords()`
  export without copying (58); `ScanArrow` and `ScanArrowRecords` import (60).
- **Spilling that covers what people hit.**
  - `Unique` and windows with a partition key spill, and keep their order exactly.
  - Sort, group-by and join spill frames that carry nested columns.
  - Every operator that cannot spill fails with an error naming itself (82).
- **Reading from anywhere:**
  - `ScanParquetFrom` reads Parquet through any `io.ReaderAt`, and `ScanCSVFrom`
    CSV through any stream, each Open given the query's context (83).
  - A ranged GET reaches an object store, and ursus fetches only the footer and the
    column chunks a query needs.
- **Performance:**
  - The CSV reader makes 126× fewer allocations, and is about 2× faster (22).
  - Groups are found through an open-addressed key table (23).
  - The radix sort is 2.4–4.1× faster (24).
  - In-memory frames honour the batch size, so the join is 3.7× faster at eight
    threads (36, 37).
  - CSV columns convert in parallel (42).
  - PDS-H SF=1's peak memory fell from 3.98 to 2.26 GB (38–41).

### Behaviour changes a v0.2 user can trip on

Most of these were wrong answers. A few were answers that are now refused, because
the right one could not be given.

**Types and casts**

- A literal keeps its type: `i8 + int8(100)` wraps in Int8, and `i8 + int64(100)`
  widens. The two used to collide and give one answer (72).
- **String → integer** is exact at every width, not through a float64:
  `"9007199254740993"` stays itself, and an out-of-range value is refused by `Cast`
  (73).
- **To a float:** a cast rounds to the nearest. 0.1 → Float32 used to be refused or
  null. Float32 maths returns the nearest float32 instead of null (73).
- **Between integers:** Int64↔Uint64 and Int128→Int64 casts are exact.
  `Sum().Cast(Int64)` is exact above 2^53. A strict float → Int128 refuses a
  fraction (69).
- **Strings that are not numbers:** `1_000` and `0x1p3` are no longer parsed as
  numbers, in `Cast`, in CSV inference, or in float schemas (77).
- **Datetime → Time** takes the time of day. It used to reinterpret the epoch ticks
  (57).
- **Int64 → Time** refuses a value outside the day when strict, and is null when
  lossy, as Polars does; it used to fold 90000 seconds to 01:00 (86).
- **A Duration cast to a coarser unit** truncates toward zero, as Polars and
  `TotalSeconds` do: −1.5 s is −1 s, where it was −2 s. An instant still floors (86).
- **Instants outside 1678–2262:** they are null in `Values([]time.Time)`, and refused
  by `Lit` and by casts from String. They used to wrap: 2300 became 1715 (61).
- **Datetime → Date** errors when strict and is null when lossy (49).
- **Refused at plan time:** Bool ↔ temporal casts (52), and `==` on a List or a
  Struct (64).
- **Enum:**
  - Enum → number goes through its text;
  - number → Enum is refused;
  - an Enum and a String meet at String in Concat, joins and conditionals (79).

**Arithmetic and functions**

- **Decimal results have new types (80):**
  - `+ -` give Decimal(max integer digits + max scale + 1, max scale), so
    Decimal(10,2) + Decimal(10,2) is Decimal(11,2);
  - `*` gives Decimal(p1+p2, s1+s2);
  - each is refused past 38 digits;
  - `/` returns Float64;
  - Decimal with a float is Float64, and Decimal with an Int128 is refused.
- **Temporal arithmetic is checked, not wrapped.**
  - An overflowing Datetime ± Duration, Datetime − Datetime or Duration arithmetic
    is a KindValue refusal naming the row (61).
  - Plain integer arithmetic still wraps, as it does in Polars.
  - Date − Date is a Duration, and Date ± Duration a Datetime. Naive minus UTC is
    refused (52).
  - Time ± Duration wraps around midnight (57).
  - `Abs` and `Neg` of the minimum Duration are refused; they wrapped (86).
- **`FloorDiv` floors and `Mod` takes the divisor's sign**, for signed integers,
  floats and Durations (54, 76). A float `%` follows Python, with ±Inf and the signed
  zero.
- **`Diff` on an unsigned column widens:** UInt8→Int16, UInt16→Int32, UInt32→Int64
  and UInt64→Int128. It used to wrap: UInt8 3 then 1 was 254 (85).
- **`Dt().Epoch()`** floors, and it and `Total*` are exact past 2262 (61, 76).
- **`Truncate` and `GroupByDynamic`** floor the wall clock, not the UTC grid, which
  moves answers in zones like +05:30. A truncated instant outside the type is
  refused (76).
- **`IsIn` and `list.contains`** answer what `Eq` answers. A type `==` would refuse
  is refused at plan time (76).
- **String functions (76):**
  - a first-match regex `Replace` expands `$1` and `${name}`;
  - `SplitN(0)` is unlimited;
  - `CountMatches("")` counts;
  - the one-sided strips trim Unicode whitespace.
- **Intervals:**
  - an `Every` that overflows sets `Err()`;
  - `IntervalOf` refuses mixed signs (66);
  - an interval finer than its index's resolution is refused (68).
- **`PadStart`, `PadEnd` and `ZFill`** refuse a width whose output a String column
  cannot hold. It used to be a fatal out-of-memory (85).
- **A String column past 2 GiB of characters is refused**, with `ErrResource`.
  Its 32-bit offsets used to wrap silently, and `Collect` reaches that size at
  around 30 million rows of 100-byte strings. `CollectBatches` and the sinks never
  build one column of all of it (87).
- **A group-by or join over more than 2 GiB of distinct keys** groups correctly. Its
  key table's offsets wrapped too (87).

**Aggregations and windows**

- **Integer `mean`** divides an exact sum (75).
- **Int128 `sum`** is refused when the total does not fit (69).
- **Duration `sum`, `mean` and `cum_sum`** are exact (62).
- **`quantile` at an exact rank** is that value (75).
- **A variance** is never negative (75).
- **`PctChange`** computes in float (75).
- **A Bool `sum`** counts its trues (75).
- **A global `GroupBy().Agg()` with nothing to aggregate** is one row with no
  columns, where it was shape (0, 0) (75).
- **Undeclared enum values** — `Rank(99)`, `Interpolation(99)`, `Closed(99)` — are
  KindValue errors. They used to crash, or quietly act as a default (72, 75).
- **A window reads the columns defined before it** in the same `WithColumns` (75).
- **A dynamic group-by's grid** covers every row, or refuses at a named ceiling. It
  used to answer short (67).
- **Two UDFs sharing a name** are refused. They used to merge into one computation
  (65).

**Joins (74)**

- **Keys of different types** are cast to a type that holds both, strictly. A 64-bit
  integer key against a float key is refused, and so is Concat or Unpivot of such
  columns.
- **`AsOfBy` keys of different integer widths** match now; a null by-key matches
  nothing.
- **`Validate` 1:m and 1:1** refuse a duplicate key even when it has no match.
- **The as-of tolerance** compares in the key's own unit (63).

**I/O**

- **Several Parquet or CSV files are matched by column name.** A file that differs is
  refused, naming both. Before, a different column order swapped values silently
  (71).
- **Row groups are pruned only on what their statistics prove**, so some files may
  read more row groups (71).
- **CSV (77):**
  - an unquoted empty field in a String column is null, and a quoted `""` is the
    empty string; the writer quotes every empty string;
  - floats are written so they read back as floats (`1.0`, `inf`, `NaN`);
  - an integer past Int64 infers as Int128;
  - Int128 and Decimal read exactly;
  - an offset with seconds is written in UTC;
  - a blank line in a one-column file is a null;
  - the writers refuse a frame with rows and no columns.
- **Parquet:**
  - lists of every element type ursus writes are read (72, 81);
  - `WithCompression(Lz4)` is refused (use `Lz4Raw`);
  - a corrupt file is `ErrIO` rather than a crash or a hang (72).
- **A file that fails to open or read is named in the error** (83).

**The optimizer (70, 78)**

- No rule changes an answer any more; an on/off differential over 1629 generated
  queries agrees.
- **A filter that can fail** — a strict cast, a UDF, temporal arithmetic — is no
  longer moved ahead of a guard, so such a query may read more rows.
- **The planner refuses what `Collect` would refuse**, so some errors arrive at plan
  time.

**Memory limits (82)**

- **A window and an as-of join are charged for the second copy they make.**
  - A window now spills at about half the input it used to.
  - An as-of join, or a window with no partition key, now refuses at about half
    its old size.
- **A group-by is charged for its key table**, so it may freeze sooner. Under a
  limit, the row order of an unordered group-by moves, as documented.

**Errors and testing**

- **A panic never kills the host (72).**
  - A panicking UDF is the caller's `ErrValue`.
  - A corrupt Parquet file, or a failing `ScanArrow` factory, is `ErrIO`.
- **A UDF that calls `runtime.Goexit`** — as `t.FailNow` does — is an error. It used
  to end the stream early, silently (85).
- **`ursustest.AssertFrameEqual`** now compares List and Struct values, and Float32
  under a tolerance. **Tests that passed may now fail**: before, any two nested
  frames compared equal (60).

### API

Every addition is in the root package unless named.

- **Frames:**
  - `DataFrame` and `LazyFrame` gain `JoinWhere`, `WhereExists`, `WhereNotExists`
    and `Unpivot` (with `UnpivotOptions`).
  - `LazyFrame` also gains `Explode`, `Unnest` and `CollectRecords`.
  - `DataFrame` also gains `Record` and `ArrowSchema`.
- **Expressions:** `Expr.Explode`, `Expr.Implode`, `Expr.Unnest`, `Expr.MapElements`
  and `Expr.MapBatches`.
- **Namespaces:**
  - `Expr.List` returns a `ListExpr`, with `Len`, `Get`, `First`, `Last`,
    `Contains`, `Min`, `Max`, `Sum`, `Mean`, `Reverse`, `Head`, `Tail`, `Slice`,
    `Sort`, `SortDesc`, `Unique` and `DropNulls`.
  - `Expr.Struct` returns a `StructExpr`, with `Field`.
  - `StrExpr` gains `Split`, `SplitN`, `ExtractAll`, `PadStart`, `PadEnd`, `ZFill`,
    `StripCharsStart`, `StripCharsEnd` and `EscapeRegex`.
- **Sources:** `ScanArrow`, `ScanArrowRecords`, `ScanParquetFrom` with `ParquetFile`,
  and `ScanCSVFrom` with `CSVFile`.
- **`dtype`:**
  - **Added:** `MaxDecimalPrecision`, `ValidDecimal`, `DecimalDigits`, `DecimalMeet`,
    `PromoteExact`, `ExactMismatch`, `MeetHint`, `ParseFloat`, `TicksPerDay` and
    `DataType.FromTimeBound`.
  - **Removed:** `DaysInterval` and `MonthsInterval`, which built intervals nothing
    validated (66). Use `IntervalOf`.
  - **Changed behaviour:** `Every`, `IntervalOf` and `FromDuration` validate.
- **`i128`:**
  - **Added:** `ParseDecimal`, `Int128.MulChecked`, `Int128.DivUint64` and
    `Int128.Float32`.
  - **Changed behaviour:** `Parse` checks its range, and `Float64` rounds correctly.
- **Nothing else** was removed or changed its parameters. Some methods gained named
  results, which callers cannot see.

### Known limitations

- **Object stores:** there is no built-in client. S3, GCS and Azure each want a
  vendor SDK, and ursus is pure Go with one dependency. `ScanParquetFrom` and
  `ScanCSVFrom` are the seam. The stores were planned for 0.4, and have since
  been deferred past it ([`v0.4-scope.md`](./context_files/v0.4-scope.md) §4).
- **Operators that do not spill** each fail under `WithMemoryLimit` with an error
  naming themselves:
  - reverse, hstack, tail, `JoinAsOf`, `MergeSorted`, `Rolling` and
    `GroupByDynamic`;
  - a window with no partition key, and one partition larger than the limit;
  - a cross join;
  - quantile-like aggregates over a few very large groups.
- **Not built:** SQL, `Pivot`, `MapGroups`, common subexpression elimination,
  `Expr.Rolling*`, `Upsample`, `Interpolate`, and the trigonometric functions.
- **Types:**
  - Nesting deeper than a List of a primitive, or a Struct of primitives, is refused
    by name in Parquet, both ways.
  - Array has no column representation.
  - CSV refuses nested columns.
  - Enum is not written to Parquet or Arrow.
  - Duration is not written to Parquet; cast it to Int64.
- **Still-open audit rows that can answer wrongly**, each recorded in
  [`audit.md`](./context_files/audit.md):
  - **I23:** a third-party Parquet file written without null counts can be pruned
    wrongly by an `IsNull` filter.
  - **O13**, unmeasured: two Enum or Struct types can render alike.
  - **J8, a remainder:** an Int64 compared with a float literal past 2^53 meets at
    Float64.
  - **S26:** `MinInt // -1` wraps.
  - **I24:** a null in a non-null column is checked only in test builds.
- **Where ursus differs from Polars on purpose:**
  - `Round` rounds half away from zero, which is Polars'
    `mode="half_away_from_zero"`, not its default, half to even.
  - UInt64 `Diff` is an exact Int128, where Polars gives Int64 and nulls.
  - The minimum Duration's `Abs` is refused, where Polars wraps.
- **Speed:** ursus is slower than Polars and DuckDB, and the README's table, measured
  at v0.3.0, says by how much.
- **PDS-H q7 regressed** to 3.9 s from 2.4 s, because its date filter is no longer
  pushed below its joins. *Fixed in v0.3.1.*
- **ursus spills only under an explicit `WithMemoryLimit`.** Under a cgroup's limit it
  can be killed instead: h2o gb10 over CSV was, at 8 GB. *Fixed in v0.3.1,* which
  budgets every query by default.

---

## v0.2.0 — 2026-09-04

The module renamed to `github.com/advenn/ursus`, and everything before step 22: the
engine, Parquet and CSV, the optimizer, spilling sort, group-by and join, and
parallel execution.
