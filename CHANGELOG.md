# Changelog

ursus follows Go's rule for v0: **nothing is promised**, and a minor version may
change behaviour. Each release below therefore says what a user of the previous one
can trip on, not only what is new.

**ursus will stay 0.x; there will be no 1.0.** So no API is frozen and none is
deprecated first: a minor version renames, removes or reshapes an API whenever the
result is better. From 0.6 on, each release lists its breaks, each with its
replacement.

Every step of construction has an as-built record in
[`context_files/`](./context_files/), with the measurements behind each change.
Step numbers below point at those records.

---

## Unreleased — toward v0.6.0

Everything on master since `v0.5.0`: steps 156–173, so far.
[`v0.6-scope.md`](./context_files/v0.6-scope.md) set the scope: **speed, the features
0.5 left, and robustness.** [`audit-0.6-midpoint.md`](./context_files/audit-0.6-midpoint.md)
took stock after step 165. Each step below was timed against the one before it; how
far ursus has moved against v0.5.0 is measured once, at the end.

### Faster

- **A probe row that misses does no work** when nothing spilled and nothing validates
  (158): q17 −19% CPU.
- **A key of one integer column is never encoded,** in a join's partitioned build and
  probe (159) and in a group-by, nulls included (160): q17 −29%, q7 and q16 −15%; q18
  −11%; h2o's integer group-bys about −20%.
- **Semi and Anti joins build in parallel,** keeping their keys alone until the build
  ends (161): q4 and q22 about −45% wall clock.
- **A join's sides are exchanged by what survives** (162). The planner still chooses
  from its estimates, but a join whose build runs in parallel, and whose probe side
  turns out the smaller by half, builds from that side instead: q12 −24% CPU.
- **The Parquet reader writes each value once** (163): a column takes the slices it
  was decoded into, and reading allocates 1.2× what it produces, from 1.7×. q19 −7%.
- **Kleene logic runs a word at a time, a filter's `And`s are applied one after
  another, and `IsIn` on an integer column compares its values directly** (164): q6
  and q15 −23 to −24%, q14 −14%, q7 −11%.
- **What a predicate above a join implies about one side goes below it** (165), with
  the predicate kept above: q19's part side keeps one part in a hundred, its CPU −12%.
- **Runtime filters** (167): an inner or semi join's built integer keys drop the rows
  below the joins between it and its probe key's source. q21 −56% CPU, q2 −35%, q11
  −30%. A filter that keeps three quarters of what it sees retires.
- **`First` and `Last` over many groups** (168): about 25 times faster over 182,000
  groups, from 0.8 and 1.2 s to 34 and 45 ms.
- **`Contains` with a regexp of literals joined by `.*`,** as `LIKE '%a%b%'` is
  written, searches for the literals rather than backtracking (169): q13 −15%.
- **A join shares its probe batch's columns** when the rows it takes from it are a
  run, rather than copying them (170).

### New

- **`Struct().WithFields(exprs...)`** replaces the fields its expressions name, in
  place, and adds the rest after (171). Polars' `struct.with_fields`, with fields read
  through `Field` rather than `pl.field()`.
- **`Interpolate(InterpolateNearest())`** fills a run of nulls with the nearer value,
  keeping the type, and **`RollingCenter()`** labels a rolling window at its middle
  row (172): Polars' `method="nearest"` and `center=True`.
- **The mean and median of instants:** a Datetime's or Time's, its own type; a
  Date's, a Datetime(us) keeping the fraction of a day; and a Duration's median
  (173). Each exact and truncated toward zero, as Polars answers.
- **Parquet UUID and JSON columns read,** as Binary and String (173).

### What a user of v0.5.0 can trip on

- **`Filter(a.And(b))` evaluates b only on the rows a keeps** (164), as `Filter(a, b)`
  always has. An error b would have raised only on rows a drops is no longer raised.
- **An inner join's row order may follow either input,** chosen at run time by the
  inputs' sizes (162). It is exchanged only where the planner already allowed it:
  nothing above the join depends on its order, `JoinMaintainOrder` is not set, and
  the left input was not sorted by the caller.
- **`Explain` shows more:** `swap_at_runtime` on a join that may be exchanged (162),
  the predicates a filter above a join implies, below it (165), and `RUNTIME FILTER`
  nodes with the `publish #n` of the join that fills each (167).
- **A deferred Semi or Anti join holds its build side's key columns until its build
  ends** (161), where it held only the distinct keys, as other joins hold their build
  rows. Past half the memory budget it streams, as before.

### Fixed

- **The group-by's partitioned fold** routed each worker's groups on a goroutine with
  no panic guard, so an internal panic there ended the process rather than failing
  the query (166).
- **The record:** v0.5.0's PDS-H SF=0.1 figure, below, read DuckDB-Go's column (166).

### Breaks

None yet. `v0.6-scope.md` lists the breaks 0.6 means to make, each with its
replacement.

---

## v0.5.0 — 2026-10-09

Everything since `v0.4.0` (2026-10-09): steps 140–155.

[`v0.5-scope.md`](./context_files/v0.5-scope.md) set the scope: **speed, and the
features users reach for first.** Object stores stayed out, by decision. Its
"Where it stands" list records each step; its Tier 2 and Tier 3 say what moved to
0.6 and why.

### Highlights

- **Faster.** The report, ursus and Polars re-run together on this release's code (154),
  puts ursus against Polars at:
  - **PDS-H SF=1:** 3.6×, from 4.4× at v0.4.0. The scope's target was 3×, with q15
    and q2 under 4×; q15 is at 4.3× and q2 at 4.9×. Not met: the Parquet reader and
    the join probe, where most of the rest is, were no 0.5 item's.
  - **About half of that move was Polars, not ursus.** Polars' SF=1 geomean was 70 ms
    in step 127's session and 78 ms in step 154's. Against 70 ms, ursus's 276 ms is
    3.9×, from its own 310 ms. On h2o over Parquet, ursus went from 914 to 947 ms,
    and only Polars' 475 to 547 ms moved that ratio. *This reading was added on master
    after the tag (step 156).*
  - **PDS-H SF=0.1:** 3.1×, from 4.0×. *This read 2.6× until step 166, which found
    it had been taken from the report's DuckDB-Go column: ursus was 29 ms.*
  - **h2o, 10 million rows:** 1.7× over Parquet, from 1.9×; 2.0× over CSV, from 3.3×.
  - **Peak memory at SF=1:** 1.10 GB against Polars' 0.84, from 1.02.

  Each step was also measured on its own queries, against the step before:
  - **A subtree a query uses twice runs once** (143): PDS-H q15 48% faster at SF=1,
    q11 42%, q2 39%, with the same peak memory.
  - **The CSV reader parses on every core** (144): h2o `gb1` 40% and `gb4` 38%
    faster over CSV at ten million rows.
  - **A filter copies its rows once** (142): q19, q12, q7 and q15 6 to 9% faster.
  - **A group-by of many groups folds on every core** (150): 33% faster over two
    million groups.
  - **A join's build side is built on every core** (151): q12 about 10 to 17%
    faster; 22 to 30% on a join of two million build keys.
- **The calendar** (145): `OffsetBy`, `Round`, `MonthStart`, `MonthEnd`,
  `IsLeapYear` and `ConvertTimeZone`, each reading the column in its own zone.
- **Windows** (148, 149, 153):
  - `RollingSum`, `RollingMean`, `RollingMin`, `RollingMax`, `RollingVar` and
    `RollingStd` over a fixed number of rows;
  - `EwmMean`, `EwmStd` and `EwmVar`, and `Interpolate`;
  - `Diff`, `PctChange` and `FillNull(Mean)` inside `.Over(g)`, and any window body
    built around an aggregate, such as `(x - x.Mean()).Over(g)`.
- **Everyday expressions** (146, 147): `ConcatStr`, `Struct`, `Str().Join`,
  `List().Join`, `Cut`, `QCut`, `ValueCounts`, `Describe`, and the six bit counts.

### Behaviour changes a v0.4 user can trip on

- **`Truncate` by a day or a month, and `GroupByDynamic`'s day windows, where a
  zone's midnight does not exist** (145). Santiago's 2024-09-08 began at 01:00. It
  used to begin, by ursus, at 23:00 the day before, a whole day early; it now begins
  at 01:00, as in Polars. A wall clock that falls in a gap now reads as the instant
  the gap ends, and one read twice in a fold at the input's own offset, at every
  interval size, as sub-day intervals always did.
- **A large unordered group-by comes out in another order** (150). Past 65,536 groups
  on several threads, its rows are partition-major. The order was already unspecified
  and already depended on the thread count; `MaintainOrder` keeps first appearance.
- **`SinkParquet` refuses a String that is not UTF-8** (152), naming the column. It
  used to write a file Polars and DuckDB refuse to read. Cast to Binary to write the
  bytes as they are.
- **A window refusal comes while the query is planned,** not at `Collect`:
  `Col("x").Over(g)`, and an ordered window over an aggregate written inside a
  larger body (148). Explain refuses them too.
- **A failing CSV read through `CollectBatches` may deliver fewer rows before its
  error** (144): the good rows of the block that holds the bad record. `Collect`
  discards them either way.
- **Explain shows `CACHE #n`** where a subtree is shared (143).

### API

Every addition is in the root package. **Nothing was removed or changed its
parameters** since v0.4.0.

- **Calendar:** `DtExpr.OffsetBy`, `Round`, `MonthStart`, `MonthEnd`, `IsLeapYear`
  and `ConvertTimeZone`.
- **Windows:** `Expr.RollingSum`, `RollingMean`, `RollingMin`, `RollingMax`,
  `RollingVar`, `RollingStd`, `EwmMean`, `EwmStd`, `EwmVar` and `Interpolate`;
  `WindowOption`, `MinSamples`, `Ddof`, `EwmAdjust`, `IgnoreNulls` and `Biased`;
  `EwmDecay`, built by `EwmAlpha`, `EwmSpan`, `EwmCom` and `EwmHalfLife`.
- **Expressions:** `ConcatStr` and `Struct`; `Expr.Cut`, `QCut` and `QCutN`, with
  `CutOption`, `CutLabels` and `CutLeftClosed`; `Expr.BitwiseCountOnes`,
  `BitwiseCountZeros`, `BitwiseLeadingOnes`, `BitwiseLeadingZeros`,
  `BitwiseTrailingOnes` and `BitwiseTrailingZeros`.
- **Namespaces:** `StrExpr.Join` and `ListExpr.Join`.
- **Frames:** `LazyFrame.ValueCounts` and `Describe`, and their `DataFrame` mirrors.

### Everything, by step

Newest first. The numbers are steps, each with an as-built record in
[`context_files/`](./context_files/).

- **v0.5.0 is tagged** (155). `gb10` and `j5` were run once more under 3 GB first,
  and pass.
- **The report** (154), ursus and Polars together on `fb7788b`, every query
  validated. `gb10` and `j5` pass under a 3 GB container over Parquet and CSV,
  peaking at 2.73 to 2.86 GB.
- **EWM and `Interpolate`** (153), as ordered window functions on pandas'
  recurrences, which Polars' follow. Checked against pandas' answers and against
  a closed form.
- **A String that is not UTF-8 is refused by the Parquet writer** (152), audit I20.
  Each Parquet footer read once was measured at 0.11 to 0.83 ms a file, under 0.5%
  of any PDS-H query, and declined.
- **A join's build side is inserted partitioned, in parallel** (151). The build
  waits for `freeze`, which inserts each partition's keys into its own table on its
  own goroutine, in row order, so a key's rows stay ascending. Past half the
  default budget it streams, the path that can spill.
- **A group-by of many groups folds its workers partitioned** (150), with a sparse
  remap; the serial fold was about 60% of a two-million-group query.
- **The fixed-size rolling functions** (149): a monotonic deque for min and max, an
  exact Int128 for an integer sum, and a compensated running state for floats,
  recomputed every N removals so rounding cannot build up.
- **The sugar inside `Over`, and a window body around an aggregate** (148), audit A8:
  an outer `Over`'s keys are handed down to the windows inside its body.
- **`Cut`, `QCut`, `ValueCounts` and `Describe`** (147). `Cut` and `QCut` share one
  kernel whose breaks are operands, so `QCut`'s can be quantile windows.
- **`ConcatStr`, `Struct`, `List().Join`, `Str().Join` and the bit counts** (146).
  ursus had no string concatenation. A family of calls whose every argument is an
  operand needed no new expression node.
- **The calendar, and a day whose midnight does not exist** (145). One rule now reads
  every wall clock back into its zone.
- **The CSV reader parses a stream on several goroutines** (144). A splitter cuts
  whole records by the scanner's own quote rule, and hands what it does not follow
  to the serial path.
- **A subtree a query uses at more than one place runs once** (143), as a `Cache`
  plan node, its result held once on the budget or spilled.
- **A filter copies its rows once, and a temporal literal folds** (142), so a date
  reaches the Parquet pruner.
- **The profile** (141): CPU profiles of the widest gaps re-ranked the speed items;
  reading Parquet was 29 to 57% of every PDS-H query profiled, ZSTD 13 to 29%.
- **The scope** (140).

### Known limitations

- **Object stores:** there is no built-in client, by decision for 0.5 as for 0.4.
  `ScanParquetFrom` and `ScanCSVFrom` are the seam.
- **Operators that do not spill** each fail under a budget with an error naming
  themselves: reverse, hstack, tail, `JoinAsOf` and `MergeSorted`; `Rolling` and
  `GroupByDynamic`; a window with no partition key, and one partition larger than
  the limit, the rolling and ewm functions included; a cross join; quantile-like
  aggregates over a few very large groups.
- **Not built:** SQL, `Pivot`, `MapGroups`, `Upsample`, rolling windows by time,
  `Corr`, `Cov`, `MinBy`, `MaxBy`, list set operations, `Sample`, `WithFields`,
  `List().Eval`, `ReplaceTimeZone`, and `Interpolate`'s nearest method; and
  `RollingMap`, Arrow IPC files and NDJSON, which only the README named until step
  166 found the two lists differing. `v0.5-scope.md`'s Tier 2 and Tier 3 give each
  one's reason.
- **Types:** as in v0.4.0. Nesting deeper than a List of a primitive or a Struct of
  primitives is refused by name in Parquet; Array has no column representation;
  CSV refuses nested columns; Enum is not written to Parquet or Arrow; Duration is
  not written to Parquet; temporal means, medians, quantiles and variances are
  refused. `Cut` and `QCut` answer a String, not Polars' Categorical.
- **Open audit rows:** I23, O13, J8's remainder, S21, S26, J11 and I26, as in
  v0.4.0, and O12, the optimizer turning a runtime error into a result. A8 and I20
  are fixed.
- **The default budget is Linux's only.** On macOS and Windows a query is
  unbudgeted unless `WithMemoryLimit` sets one, and no Go soft limit is set. *Added
  on master after the tag (step 156).*
- **Where ursus differs from Polars on purpose:** as in v0.4.0, and:
  - a NaN is a value to the ewm functions, where pandas skips it;
  - an ewm variance of one value is null, where pandas answers NaN.
- **Speed:** ursus is slower than Polars and DuckDB: 3.6× Polars on PDS-H at SF=1,
  by geomean, short of the 3× the scope set. The widest gaps are q7 at 7.2×, q19 at
  5.9× and q17 at 5.2×, mostly reading Parquet and probing joins.
- **Memory:** a shared subtree's result, a group-by's partitioned fold and a join's
  partitioned build each hold more at once than before, which the budget counts:
  PDS-H at SF=1 peaks at 1.10 GB, from 1.02. h2o `gb10` over CSV peaks at 7.28 GB
  under an 8 GB cap; `gb10` and `j5` pass under 3 GB.

---

## v0.4.0 — 2026-10-09

Everything since `v0.3.1` (2026-10-06): steps 92–138.

[`v0.4-scope.md`](./context_files/v0.4-scope.md) set the scope: **fast where it is
slow, and safe inside a service.** Its §7 records what was done, and what moved to
0.5 and why. Step 92 was also meant to be v0.3.2, which is not tagged; 0.4.0
contains it.

### Highlights

- **Faster where ursus was slowest.** The full report, re-run with every engine in
  one session on an otherwise idle machine (127), puts ursus against Polars at:
  - **PDS-H SF=1:** 4.4×, from 10.8× at v0.3.0, with a peak memory of 1.02 GB
    against Polars' 0.91 GB, where it was twice Polars';
  - **PDS-H SF=0.1:** 4.0×, from 7.5×;
  - **h2o, 10 million rows:** 1.9× over Parquet, from 3.3×; 3.3× over CSV, from 3.6×.
    `gb10` over CSV, killed under the 8 GB cap at v0.3.0, passes.

  Each step was also measured on its own queries, against the step before:
  - **Parquet row groups decode in parallel:** PDS-H q6 at SF=1 is 40% faster, and
    q7 31% (102).
  - **An inner join hashes its smaller input:** q8 72% faster with a quarter of the
    memory, q9 54%, q5 37% (104).
  - **h2o:** gb8 80% faster with `TopK` (106); gb6 30% with median by selection
    (105); j3 22% from a String concatenation that copies bytes, not rows (103).
  - **Comparisons** are about 3.5× faster (107).

  The report predates the memory work below and has not been re-run since.
- **Holds under a container's memory limit** (128–138). At ten million rows, h2o
  `j5` was killed under a 4 GB container; it and `gb10` now pass under 3 GB.
  - The Go soft limit is set with the default budget (128).
  - `Collect` refuses a result too large to hold, with an error naming what streams
    (130).
  - A join, a group-by and `Collect` no longer hold their input or answer twice
    while they assemble it (128, 131).
  - What they allocate fell, measured under 3 GB from step 131 to step 138: `gb10`
    38–47% less, `j5` 12–37% (133–138).
- **Safe inside a long-running service:**
  - compiled regexes and `is_in` sets are freed with their expression (94);
  - a query that fails while being planned closes what it opened (95);
  - `Rolling` and `GroupByDynamic` hold a bounded working set, charged to the
    budget (96);
  - `SetProcessMemoryLimit` sets Go's soft limit near the container's (97);
  - a null in a non-nullable column is caught in production (98);
  - a List column past 2^31-1 elements is refused, not wrapped (99).
- **New:**
  - `TopK` and `BottomK` aggregates (106);
  - `Str().Strptime` and `Dt().Strftime`, with strftime formats (113);
  - the trigonometric and hyperbolic functions (114);
  - Int128 `*`, `//` and `%`, so `Col("x").Sum().Mul(2)` works (100);
  - `JoinCoalesce(true)` on a full join (110);
  - `JoinAsOf` and `MergeSorted` on every integer, float and temporal key, and
    `MergeSorted` on String (109);
  - Parquet INT96 timestamps and unannotated fixed-length byte arrays (112);
  - a Null-typed column that concatenates, and writes to Parquet and CSV (108).
- **Errors name what you wrote** (111, 115). `audit.md`'s list of false refusals and
  misleading errors is closed, but for three rows (§ Known limitations).

### Behaviour changes a v0.3 user can trip on

- **The Go runtime's soft memory limit is set by default** (128). The first query
  that runs under the default budget sets it to nine tenths of the ceiling the
  budget is derived from, as `SetProcessMemoryLimit` does. The limit is the whole
  process's, so it changes how the program around ursus collects garbage too.
  `GOMEMLIMIT` in the environment, `off` included, a limit already set with
  `debug.SetMemoryLimit`, a limit given with `WithMemoryLimit`, and
  `LeaveProcessMemoryLimit` each keep ursus from setting it.
- **`Collect` refuses a result too large to hold, under the default budget** (130).
  Once the query holds more, its result and its operators' state together, than
  three quarters of the ceiling, `Collect` fails with a resource error naming
  `collect` and pointing to `CollectBatches`, `SinkParquet` and `SinkCSV`, which
  stream. It used to grow until the kernel killed the process. Under a limit given
  with `WithMemoryLimit`, or none, `Collect` holds whatever it is asked to, as
  before.

- **An inner join's row order is no longer promised** to follow the left frame:
  the smaller input is hashed, whichever side it is. `JoinMaintainOrder(true)`
  keeps the old order. Left, right, semi, anti and full joins are unchanged (104).
- **Int128 arithmetic is exact or refused.** `+` and `-` used to wrap, so Max + Max
  gave a plausible wrong number. A result past ±1.7e38 is now an error naming the
  row. Int64 arithmetic still wraps, as Polars' does (100).
- **A null in a column declared non-nullable is an error in production,** not only
  in the test suite (98).
- **A median or an implode over heavily overlapping rolling windows** is refused
  under the budget, naming the operator, where it used to grow past it (96).
- **A List column past 2^31-1 elements is refused,** naming `CollectBatches` and
  `SinkParquet` (99).
- **A group-by under a limit you set is serial again,** as in v0.3.0, so a spilling
  group-by gives the same order on every run. In v0.3.1 it was parallel until half
  the budget (92).
- **Parquet:** a file with an INT96 column opens, where it failed; a column of the
  NULL logical type reads as Null, not Int32 (108, 112).
- **Peak memory of a Parquet scan rises** by the few batches each worker decodes
  ahead. `WithThreads(1)` reads serially, as before (102).
- **Error kinds that changed,** for code that tests them with `errors.Is`:
  - a panic in a `MapName` function is `ErrValue`, and a panic in the
    `RecordReader` that `ScanArrow`'s factory returns is `ErrIO`; both were
    `ErrInternal` (115);
  - a dynamic group-by over a row at the last instant a `time.Time` holds is
    `ErrValue`, not `ErrInternal` (115);
  - a failed spill write is `ErrIO`; it had no kind (115).
- **Error text changed** for `FillNan`, `FillNull`, `CumSum`, a Duration scaled by a
  fraction, a failing udf (111), a window inside `Agg`, an ordering over a windowed
  aggregate, and `Any` (115).

### API

Every addition is in the root package unless named. **Nothing was removed or
changed its parameters** since v0.3.1.

- **Expressions:** `Expr.TopK` and `BottomK`; `Expr.Sin`, `Cos`, `Tan`, `ArcSin`,
  `ArcCos`, `ArcTan`, `Sinh`, `Cosh`, `Tanh`, `ArcSinh`, `ArcCosh`, `ArcTanh`,
  `Degrees` and `Radians`.
- **Namespaces:** `StrExpr.Strptime` and `DtExpr.Strftime`.
- **Joins:** `JoinMaintainOrder`.
- **Process:** `SetProcessMemoryLimit` and `LeaveProcessMemoryLimit`.
- **`i128`:** `Int128.AddChecked`, `SubChecked` and `DivMod`.

### Everything, by step

Newest first. The numbers are steps, each with an as-built record in
[`context_files/`](./context_files/).

- **The Parquet reader keeps its values slice too, and a join's per-row arrays
  double** (138), from an allocation profile of h2o `j5` (137).
  - The Parquet reader's fixed-width and List-element columns made step 136's
    mistake: the slice was dropped every batch, on the same comment. They allocated
    2.1 times what they produced, now 1.1.
  - A join's `rowKey` and `counts` grew by `append`; a build of a million keys
    allocated 2.3 times what it held, now 1.8.
  - Measured after, under a 3 GB container at ten million rows, `j5` allocates 6%
    less over Parquet and 2.5% less over CSV; CPU within the noise.
- **The CSV reader allocates about what it produces** (136): 4.1 times it, now 1.3,
  counting every batch.
  - Each fixed-width column dropped its values slice every batch. A comment claimed
    the column wrapped it, but the column has copied it since the first commit. So
    each batch's values were allocated twice, once by `append`'s quarter steps.
  - The slice is now kept, as the String builder's buffers always were. A test
    checks an earlier batch is intact after the later ones.
  - Measured after, under a 3 GB container at ten million rows over CSV, `gb10`
    allocates 15% less and `j5` 30% less, with 13% less CPU for `j5`.
- **The memory test's spilling join reads the heap exactly** (135), at each bucket's
  concatenation: 14.3 MB in every run under a 16 MB budget. Its sampled reading, which
  it was bounded on, once read 128 MB in a gate where every package competes for the
  CPU.
- **Accumulators grow by doubling** (134). Each allocated about five times what it
  held, growing a group at a time; now about twice. The memory budget now counts
  each per-group array's capacity, which doubling can leave up to half unused, not
  only its length. Measured after, under a 3 GB container, `gb10` allocates 1.0 GB
  less, 5 to 6%, with its CPU within the run-to-run noise.
- **The key table allocates about what it holds** (133). Its key arena is chunks
  that never move, and its per-key arrays double. A million distinct keys allocated
  4.1 times what the table held, and now 1.6.
  - `append` grows a large slice a quarter at a time. That made the table half of
    everything h2o `gb10` allocated, in an allocation profile at ten million rows
    (132).
  - It also held the old arena beside the new while it copied. A six-key group-by's
    live heap while aggregating fell from 1.31 of what it counts to about 1.1.
  - Measured after, under a 3 GB container at ten million rows, `gb10` allocates a
    third less and uses 19–28% less CPU. Over Parquet its peak fell from the whole
    3 GiB to 2.88 GB, under the soft limit. `j5` barely moved.
- **A spilled full or right join no longer panics on its last flush** (133). Where
  a join both kept build rows in memory and spilled others, it released its table
  and then emitted the last unmatched build rows from it. Reachable since v0.1 at
  limits that depend on the data; a sweep of limits now covers it.
- **A group-by with as many groups as rows holds its keys once while it assembles
  its answer** (131). It held them twice, uncounted, with its key table beside them:
  517 MB to assemble a 159 MB answer, measured on two million rows of `gb10`'s shape.
  It now holds 172 MB.
  - The cause was found in the budget's ledger. It is keyed by pointers into the
    buffers it counts, so an account keeps alive what it holds, and the group-by's
    released its key parts only after the answer was built.
  - The same kept step 130's `Collect` holding its result twice under the default
    budget, a regression from step 128's fix; that is fixed too.
- **`Collect` counts its result under the default budget** (130), and refuses one
  too large to hold (see the behaviour changes).
  - The result is counted apart from what the operators hold, so they spill as they
    would if it were streamed, and `MemoryStats.Peak` is unchanged.
  - The limit is three quarters of the ceiling, not the budget's half: at half, the
    `j5` runs that passed under 3 GB and 4 GB containers would have been refused.
- **A test fails when a query holds memory its budget does not count** (129). It
  measures the live heap of a join, a group-by, a spilling join and two `Collect`s
  on a few million rows, in seconds, and fails if a result or a build side is held
  twice, as step 128 found them.
- **The default memory budget holds** (128). Under a 4 GB container a join of two
  ten-million-row tables was killed: the heap ran to about twice the 2 GB budget.
  - Under the default budget, the Go soft memory limit is now set as well (see the
    behaviour changes).
  - `Collect` closes the query's operators before assembling the result, so a join's
    build side is no longer alive alongside it.
  - The result, and a join's build side, are concatenated column by column, each
    column's pieces let go once copied, instead of being held twice.
  - Measured after: under a 4 GB container `j5` and `gb10` pass over CSV and
    Parquet, peaking at nine tenths of the cap, as fast as under 8 GB. Under 3 GB
    both pass too, but `gb10` over Parquet reaches the cap. That is most likely its
    group-by holding its keys twice while it assembles its answer (130), not
    `Collect`.
- **The 0.4 report, re-run** after the performance round (127): PDS-H SF=1 at 4.4×
  Polars, SF=0.1 4.0×, h2o 1.9× over Parquet and 3.3× over CSV. Step 117's report,
  taken while this session's reviewers were grepping, had understated ursus.
- **The key table waits on memory once per chunk of keys, not once per key** (126).
  Each slot holds a tag of its key's hash, and joins and group-bys look up and insert
  64 keys at a time, reading their slots together. PDS-H q4 is 28% faster, q12, q9
  and q21 about 20%; h2o gb3 13%, gb10 6%. Peak memory rises about 5%.
- **`IsIn` on a String column compares strings** instead of building each row's
  grouping key (125): its CPU in q12 fell from 1.34 s to 0.51 s. q19 is 6% faster;
  q12's wall time is bound by its join build and did not move.
- **String columns are read with one copy, not two** (124): q1 −11%, h2o gb1 −14%,
  j4 about −10%.
- **The Parquet scan and the filter are cheaper** (123). PDS-H q6 at SF=1 is about
  45% faster, q1 36%, q3 44% and q14 41%; h2o gb4 38%, gb1 about 28%, j1 15%.
  - A column stored as Parquet stores it is read straight into its buffer.
  - Validity is built only once a null arrives, so a batch with none carries no
    bitmap, and the kernels downstream take their fast paths.
  - A filter's mask becomes a selection a word at a time; `Take` builds no validity
    for a column without nulls, and gathers strings by their bytes.
- **Strptime and Strftime, from the same audit** (122):
  - A strict parse into a zoned Datetime refuses a wall-clock time the clocks skip
    or pass twice, naming the zone; a lenient one gives null. It returned an instant
    an hour away, or either of two.
  - A format that names no whole date (`%a`, `%d/%m`, `%Y-%m`) is refused while the
    query is planned, as `%Z` is for parsing.
  - **Behaviour change:** `%y` reads 70–99 as 1970–1999, as chrono and Polars do; it
    read every year as 20xx.
  - `%z` reads "+05:30" as well as "+0530", and a year past 9999 is written with its
    sign, "+10000", and reads back.
- **Expressions, from the same audit** (121):
  - A conditional's branch that fails on a row's value — Int128 past its range, a
    strict cast — is evaluated again over only the rows it is taken for, so a guard
    such as `When(x.Lt(limit)).Then(x.Mul(2))` works. It failed on the rows the guard
    excluded.
  - Int128 `Neg` and `Abs` of the minimum are refused, as `0 - x` is; they wrapped.
  - `TopK` and `BottomK` rank +0.0 above -0.0, so the answer does not depend on
    arrival order.
- **Service safety, from the same audit** (120):
  - A CSV column a `WithSchema` declares non-nullable, with an empty cell in the
    file, is a value error naming the row. Since step 98 it failed as ursus's bug.
  - A finished group-by, rolling or dynamic group-by drops its aggregates' state,
    which stayed alive, uncounted, until the query ended.
  - A CSV scan whose stream fails on its header closes the stream.
  - A panic while a query is planned closes what was already opened, as an error
    did since step 95.
- **Parquet, from the same audit** (119):
  - A struct whose first field is Null reads again, and so does `List(Null)`, which
    PyArrow infers for a field or list that only ever holds None. Step 108 made both
    fail, the first as ursus's bug.
  - When a later file fails to open, the rows before it are delivered first, as the
    serial reader always did; `Head(n)` that the first file answers succeeds.
  - An INT96 timestamp on 1677-09-21 reads exactly; it came back in 2262.
  - An integer column under BYTE_STREAM_SPLIT reads; a FIXED_LEN_BYTE_ARRAY under a
    delta encoding is refused by name, where it reached a decoder panic.
- **Joins, from an audit of 0.4's own work** (118):
  - An as-of join whose keys meet at Int128 or a Decimal is refused while planned.
    It planned, then failed at `Collect` as ursus's bug.
  - A nearest as-of join on a float key no longer picks a NaN, and breaks a tie on
    an infinity backward, as on every other key.
  - An inner join keeps its left input's order when the caller sorted it, or when an
    as-of join or `MergeSorted` above needs it sorted. The build-side swap of step
    104 had made such a query fail, or `.Head(n)` return other rows, depending on
    the tables' sizes.
  - A join on a float key is not swapped, so a merged -0.0 key stays -0.0.
- **The last misleading refusals of `audit.md`'s list** (115):
  - A window inside `Agg` gets a hint that works: move the window, not the
    aggregate around it. `Col("x").Sum().Over(g).Max()` was refused as "not an
    aggregate".
  - An ordering over `First().Over(...)` no longer calls `first`
    order-independent.
  - `Any`'s hint names `.Any()`.
  - Temporal `Median`, `Quantile`, `Std`, `Var` and `Mean` are documented as
    refused, and their refusal names the cast to ticks.
  - A panic in a `MapName` function, or in the `RecordReader` that `ScanArrow`'s
    factory returns, is the caller's error, not "a bug in ursus".
  - So is a dynamic group-by over a row at the last instant a `time.Time` holds.
  - A spill write that fails says it was spilling, and to which file.

- **Trigonometric and hyperbolic functions:** `Sin`, `Cos`, `Tan`, their inverses
  `ArcSin`, `ArcCos`, `ArcTan`, the hyperbolic six, and `Degrees` and `Radians`.
  They widen to a float as `Sqrt` does (114).
- **`Str().Strptime(dtype, format, strict)` and `Dt().Strftime(format)`:** parse
  and format dates, datetimes and times with strftime directives (`%d/%m/%Y`,
  `%b %e, %Y`, `%H:%M:%S%.f %z`, …), as Polars does.
  - A format that cannot produce the type is refused while the query is planned.
  - A strict parse names the value that does not match; a lenient one gives null
    (113).
- **Parquet INT96 timestamps read,** as a naive `Datetime(ns)`, as PyArrow reads
  them. Spark writes INT96 by default, and such a file failed to open.
- **An unannotated fixed-length byte array reads as Binary.** Its schema promised
  Binary and its reader refused it (`audit.md` I16) (112).
- **Refusals name what you wrote.**
  - `FillNan`, `FillNullWith` and `FillNull` name themselves, not `is_not_nan()`,
    `when` or `mean()`.
  - `CumSum` names `cum_sum`, not `sum`.
  - A Duration scaled by a fraction no longer quotes a factor of 2.5 you did not
    write.
  - A failing udf names the input value it failed on (111).
- **A full join merges its keys with `JoinCoalesce(true)`:** the left key where the
  row has a left side, the right key where it does not. It was refused, with a hint
  that ursus had no coalesce expression (`audit.md` J9) (110).
- **`JoinAsOf` and `MergeSorted` take any integer, float or temporal key, and
  `MergeSorted` a String one.**
  - An as-of join on a Float, Int8 or UInt64 key used to plan and then fail at
    `Collect`.
  - `MergeSorted` refused Float64 with a hint that the key must be numeric.
  - What neither supports, Decimal for instance, is refused while the query is
    planned (`audit.md` J7) (109).
- **A Null-typed column concatenates, and writes to Parquet and CSV.**
  - It failed every `Collect` that concatenated batches (`audit.md` J12).
  - Parquet stores it as the NULL logical type, as PyArrow does, and reads that
    back as Null, not Int32 (I18) (108).
- **Comparisons are about 3.5× faster:** a literal is no longer copied to the
  column's length, and results are written 64 at a time (107).
- **`TopK(k)` and `BottomK(k)` aggregates:** a group's k largest or smallest
  values, as a List.
  - Each holds k values a group, so the group-by stays parallel and bounded.
  - h2o gb8 uses it now, as Polars does: 80% faster and a third of the memory
    (106).
- **`median` and `quantile` select instead of sorting each group.** h2o gb6 is 30%
  faster (105).
- **An inner join hashes its smaller input.** The inputs are swapped when the right
  is estimated at more than twice the left, from Parquet footers and in-memory
  frames.
  - PDS-H at SF=1: q8 is 72% faster with a quarter of the memory, q9 54% and q5
    37%.
  - **Behaviour change:** an inner join's row order is no longer promised to
    follow the left frame. `JoinMaintainOrder(true)` keeps it. Left, right, semi,
    anti and full joins are unchanged (104).
- **Collecting String and Binary columns is faster:** the final concatenation copies
  each batch's characters once, instead of building a string per row. h2o j3 is 22%
  faster (103).
- **Parquet row groups are decoded in parallel,** in file order.
  - PDS-H q6 at SF=1 is 40% faster and q7 31%; every SF=0.1 query measured is
    21–31% faster.
  - `WithThreads(1)` and a limit read serially, as before.
  - Peak memory rises by the few batches each worker holds ahead (102).
- **Int128 has `*`, `//` and `%`,** so `Col("x").Sum().Mul(2)` and Int64 × UInt64
  work. Every integer `Sum` is an Int128, and only `+` and `-` were implemented.
  - `//` and `%` floor, as Int64's do, and a zero divisor is null.
  - **Behaviour change:** Int128 arithmetic is exact or refused. `+` and `-` used
    to wrap: Max + Max gave a plausible wrong number. A result past ±1.7e38 is now
    an error naming the row. Int64 arithmetic still wraps, as Polars' does.
  - **Added:** `Int128.AddChecked`, `SubChecked` and `DivMod` (100).
- **A List column past 2^31-1 elements is refused,** naming `CollectBatches` and
  `SinkParquet`, as a String column past 2 GiB already was.
  - Its 32-bit offsets used to wrap silently. Two one-row lists of 2^30 elements
    concatenated to a second row spanning a negative range (99).
- **A null in a column declared non-nullable is an error in production too,** not
  only in the test suite (`audit.md` I24). Measured: 76 ns per such column per
  batch (98).
- **`SetProcessMemoryLimit`** sets the Go runtime's soft memory limit to nine
  tenths of the container's or the machine's memory.
  - It is opt-in, because the limit is the whole process's.
  - A limit already chosen, through `GOMEMLIMIT` or `debug.SetMemoryLimit`, is
    kept.
  - Under a container's limit, it keeps the Go heap's own slack inside the
    ceiling. The query budget bounds only what is live (97).
- **A long-running service no longer leaks through the engine:**
  - **Compiled regexes and `is_in` sets** are freed with the expression they came
    from. They were cached for the life of the process, so a service building
    queries per request grew without bound (94).
  - **A query that fails while being planned closes what it opened.** A CSV
    stream, a file or an HTTP body, was left open when a later part of the query
    failed to plan: the left side of a join whose right could not open, the inputs
    of a `Concat` before a failing one (95).
- **`Rolling` and `GroupByDynamic` hold a bounded working set,** and what they
  must keep is charged to the budget.
  - Overlapping windows were expanded whole and uncharged: a 4,000-row rolling
    sum held 244 MB.
  - **Behaviour change:** a median or an implode over heavily overlapping windows
    is now refused under the budget, naming the operator, where it used to grow
    past it.
  - The expansion is now built a batch at a time, which also made the rolling sum
    about 30% faster (96).
- **A group-by under a limit you set is serial again,** so a group-by that spills
  gives its rows in the same order on every run, as in v0.3.0.
  - In v0.3.1 it ran parallel until half of any budget, and where it switched
    depended on thread scheduling. The contents were always right, but the order
    of an unordered group-by could change from run to run.
  - CI caught it: `TestGroupBySpillIsDeterministic` is flaky in the v0.3.1 tag.
  - Parallel-then-serial now applies only under the default budget, where a switch
    needs half the machine or the container (92).

### Known limitations

- **Object stores:** there is no built-in client, by decision for 0.4
  ([`v0.4-scope.md`](./context_files/v0.4-scope.md) §4). `ScanParquetFrom` and
  `ScanCSVFrom` are the seam, through any `io.ReaderAt` or stream.
- **Operators that do not spill** each fail under a budget with an error naming
  themselves:
  - reverse, hstack, tail, `JoinAsOf` and `MergeSorted`;
  - `Rolling` and `GroupByDynamic`, which since step 96 are bounded and charged,
    so they are refused rather than killed;
  - a window with no partition key, and one partition larger than the limit;
  - a cross join;
  - quantile-like aggregates over a few very large groups.
- **Not built:** SQL, `Pivot`, `MapGroups`, common subexpression elimination,
  `Expr.Rolling*`, EWM, `Upsample` and `Interpolate`. `v0.4-scope.md` §7 lists what
  moved to 0.5: calendar offsets and the rest of `.dt`, `ValueCounts`, `Describe`,
  `Sample`, `Cut`, the string and list joins, Arrow IPC, NDJSON, hive partitioning,
  and the radix group-by, among others.
- **Types:**
  - Nesting deeper than a List of a primitive, or a Struct of primitives, is refused
    by name in Parquet, both ways.
  - Array has no column representation.
  - CSV refuses nested columns.
  - Enum is not written to Parquet or Arrow.
  - Duration is not written to Parquet; cast it to Int64.
  - The mean of a Date, Datetime or Time, and the median, quantile, variance and
    standard deviation of any temporal type, are refused; `Cast(Int64)` gives the
    ticks.
- **Open audit rows:**
  - **I23:** a third-party Parquet file written without null counts can be pruned
    wrongly by an `IsNull` filter.
  - **O13**, unmeasured: two Enum or Struct types can render alike.
  - **J8, a remainder:** an Int64 compared with a float literal past 2^53 meets at
    Float64.
  - **S26:** `MinInt // -1` wraps.
  - **J11:** a spilled join can refuse with "a single join key has more build rows
    than the limit" when no key has. Its cause has not been found.
  - **I26:** `DataFrame.Rows` does not decode a List column.
  - **A8:** `Diff`, `PctChange` and `FillNull(Mean)` inside `.Over()` are refused.
- **Where ursus differs from Polars on purpose:**
  - `Round` rounds half away from zero, which is Polars'
    `mode="half_away_from_zero"`, not its default, half to even.
  - UInt64 `Diff` is an exact Int128, where Polars gives Int64 and nulls.
  - The minimum Duration's `Abs` is refused, where Polars wraps.
- **Speed:** ursus is slower than Polars and DuckDB: 4.4× Polars on PDS-H at SF=1, by
  geomean, just short of the 4× the scope set as its target. The README's table gives
  the rest, from the report step 127 ran.
- **Memory:** h2o `gb10` over CSV peaked at 7.60 GB under an 8 GB cap, in step 127's
  report. The query budget was 4 GB; the rest is the Go heap's slack, which ursus
  bounds by default since step 128, after that report.
  - Re-measured after step 128, `gb10` and `j5` pass under 4 GB and 3 GB caps.
  - Under 3 GB, `gb10` over Parquet reaches the cap. A group-by with as many groups
    as rows held its keys twice, uncounted, while it assembled its answer: a six-key
    group-by of two million rows held 517 MB against the 375 MB it counted (130).
    Fixed in step 131, where the same assembly holds 172 MB.
  - Re-run under 3 GB after step 131, all four pass, and the peaks are unchanged.
    The heap fills to the cap whatever the live peak: `gb10` over Parquet allocated
    25.6 GB in 4.7 s, and its heap in use peaked at 3.19 GB against the 1.54 GB the
    budget counted.
  - **After steps 133–138, as tagged:**
    - `gb10` over Parquet peaks at 2.72 GB, under the soft limit, and allocates
      16.0 GB;
    - over CSV, it peaks at 2.75 GB and allocates 14.9 GB;
    - the heap in use still sits near the soft limit while allocation outpaces the
      collector.

    *This item was corrected on master after the tag: as tagged, it ended at the
    figures above it.*

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
