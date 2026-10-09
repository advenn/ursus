# Audit — 2026-09-27, at `8dbc618` (after step 69)

**Seventy-eight distinct defects, about forty of them silent wrong answers.** Five
auditors each took one subsystem — scalar expressions; aggregation, sorting and
windows; joins; I/O; the optimizer and planner — and measured every finding with a
throwaway probe against an oracle that shares no code with ursus.

Authoritative over the "still open" lists in the as-builts. Line numbers are as of
`8dbc618` and will drift.

---

## 1. What this refutes

Step 68's as-built closed with: *"With this closed I know of no remaining silent
wrong answer reachable from ordinary user code with ordinary data."* It was true of
what I knew and false of the code. The first finding below is a `Filter` after a
`WithColumns`. The second is `ScanParquetGlob` over two files whose columns are in a
different order. Neither needs an edge value.

The difference was method, not effort. Steps 51–69 hunted one defect at a time and
built an instrument around each. This audit compared whole subsystems against
something else:

- **the reference engines** — Polars 1.44, DuckDB 1.5, PyArrow 25, pandas 3, from
  `bench/.venv`;
- **ursus against itself** — optimizer on vs off (`WithOptFlags(plan.NoFlags())`),
  pruning on vs off (`WithPruning(false)`), spilled vs in memory, one thread vs four,
  batch size 1 vs 8192;
- **exact arithmetic** — math/big, and working the answer out by hand.

Wherever ursus deliberately differs from Polars and says so in a doc comment, it is
not reported here. Section 9 lists those.

## 2. How much of this is checked

- Every finding was produced by running code, and the observed output was quoted.
- **✔** marks the 26 most severe, which I re-ran in the main repo against a
  hand-computed answer. All 26 reproduced.
- None was introduced by step 69: its commits touched none of these paths, and the
  five scalar findings I also ran at `ac54f44`, the commit before it (S1, S2 and
  S4–S6), fail identically there.
- Two defects were reported from two directions: O4 and J4, and O7 and the I/O
  pruning findings. They are counted once.

Legend: **SW** silent wrong answer · **CR** crash (panic, hang, or `ErrInternal` on
ordinary input) · **FR** false refusal · **ME** misleading error or hint.

## 3. Optimizer and planner

| | | finding | cause |
| --- | --- | --- | --- |
| O1 ✔ | ~~**SW**~~ **fixed, step 70** | A `Filter` after a chained `WithColumns` returns wrong rows. `WithColumns(x+1 as w, w*2 as v).Filter(v>7)` gives x = 1, 3, 5, and the answer is 3, 4, 5. The in-place form returns 0 rows instead of 3; another form refuses with `unknown column`. | `rule_predicate.go:276-305`, `:343-351`: the definition is substituted verbatim, not recursively. `WithColumns` is documented as sequential. |
| O2 ✔ | ~~**FR**~~ **fixed, step 70** | `Concat` then a projection is refused: `concat: frame 1 has 2 columns, frame 0 has 1`. Also refused after `GroupBy`, `Sort`, `WithColumns` and `Unique`. `WithVerify` catches it: *"projection_pushdown produced a plan whose schema does not resolve"*. | `rule_projection.go:264-281`, the Union arm, which lacks `pushdownMergeSorted`'s symmetry check. |
| O3 ✔ | ~~**SW / CR**~~ **fixed, step 72** | Literals of different types merge into one computation: `Lit(int8(100))` and `Lit(int64(100))` both render `lit(100)`, as do `1` and `1.0`. A sum came back −111 instead of 401; `x*1` beside `x*1.0` raises `ErrInternal`. Affects `Agg`, `GroupByDynamic`, window temporaries and partition keys. | `Lit.String()` (`expr/nodes.go:98-116`) omits the type. The dedup maps are `physical/agg.go:753`, `plan/resolve_window.go:82` and `physical/window.go:382`. This is step 65's UDF bug, for literals. |
| O4 | ~~**SW**~~ **fixed, step 78** | Cross-join collapse turns IEEE `==` into hash equality, so NaN matches NaN: `JoinWhere`, cross join + `Filter`, `WhereExists` and `WhereNotExists` all answer differently with the rule off. | `rule_collapse.go:88-104`, `:214-216`. |
| O5 | ~~**SW**~~ **fixed, step 78** | Cross-join collapse copies `NullsEqual`, so a `JoinNullsEqual(true)` cross join followed by `Filter(k == k_right)` matches null keys. `JoinNullsEqual` on a cross join is accepted silently. | `rule_collapse.go:103` (`c := *j`), whose comment at `:113` says it "stays false". |
| O6 ✔ | ~~**SW**~~ **fixed, step 74** | A filter on a coalesced join key is pushed into the side with the narrower key type and runs at that width: Int32 `k*m > 1e9` returns 0 rows, and the answer is 1. | `rule_predicate.go:597-605`, `rewriteForSide`'s Col arm. |
| O7 | ~~**SW**~~ **fixed, step 71** | Parquet row-group pruning — see I2–I5. | Closed at step 78: it is I2–I5, which step 71 fixed. |
| O8 ✔ | ~~**FR**~~ **fixed, step 70** | Merging stacked filters reverses their order, so a guard no longer protects the filter after it. `Filter(ok).Filter(s.Cast(Int64) > 1)` fails with a cast error when optimized. | `rule_predicate.go:86`. |
| O9 | ~~SW (edge)~~ **fixed, step 78** | A filter pushed below `Unique` can tell −0.0 from +0.0, which `Unique` merges. | `rule_predicate.go:420-431`. |
| O10 | ~~CR~~ **fixed, step 78** | `GroupByDynamic(Col("t").Shift(1), …)` raises `ErrInternal`. | `resolveTemporalGroup` never calls `rejectWindow` or `rejectAggregate`. |
| O11 | ~~ME~~ **fixed, step 78** | `Explain` and `CollectSchema` accept `Sum().OverWith(OrderBy…)`, which `Collect` refuses. | The refusal exists only at `physical/window.go:405`. |
| O12 | info | The optimizer can turn a runtime error into a result, e.g. `cast OR true`. This is probably acceptable, but it contradicts the rule's own contract for `Validate`. | |
| O8b | ~~**FR**~~ **fixed, step 78** | Found when O8 was fixed: pushdown still moves a fallible conjunct *below* one that stays. `Unique("s").Filter(ok).Filter(s.Cast(Int64) > 1)` casts beneath the Distinct, ahead of the guard. Needs a rule about which conjuncts can fail, not an ordering. | `rule_predicate.go`, the Distinct arm. |
| O8-join | ~~FR~~ **fixed, step 78** | The same across a join: a fallible filter is pushed into a join side, onto rows the join would remove. Polars makes the same trade. | the join arm of predicate pushdown. |
| **P1** | ~~**SW**~~ **fixed, step 78** | **Found by step 70's differential.** Projection pushdown stops reading a column that a `WithColumns` redefines without reading it, and the redefinition then lands at the END instead of in place: `WithColumns(x+1 as w)` on `{x, w, y}` returns `{x, y, w}`, while `CollectSchema` promises `{x, w, y}`. Silent without `WithVerify`. 132 shapes in the differential. | the `WithColumns` arm of projection pushdown, against `WalkWithColumns`' replace-in-place. |
| **W1** | ~~**SW / FR**~~ **fixed, step 75** | **Found measuring step 70.** A window over a column defined earlier in the same `WithColumns` reads the input's column — a per-group sum of 200 where the answer is 30 — or, for a new name, is refused with `unknown column`. Wrong with the optimizer on AND off, so no on/off differential can see it. | windows are lifted below the whole node, `resolve.go:329`, `resolve_window.go:131`. |
| O13 | SW, unmeasured — **step 86 measured its plainest shape, two typed nulls whose types render alike, and each kept its own type** | **Recorded at step 72, not fixed.** An Enum's categories and a struct's field names render unquoted in `DataType.String()`, so two different types can render alike. `expr.Identity` carries a literal's type through that rendering, so two typed nulls of such types could still share one computation. | `dtype` rendering. It reaches the dedup maps only through a typed null. |
| O14 | ~~SW~~ **fixed, step 85** | **Recorded at step 72, not fixed.** A worker goroutine that ends in `runtime.Goexit` — which no recover sees — closes its lane as though the stream had ended, and the rows after it are silently dropped: a udf calling `Goexit` on the third of five rows, at 4 threads and a batch size of 1, gave `Count` = 2 and no error. | `parallel.go`, `parjoin.go`: a closed lane is read as end of stream. Nothing in ursus calls `Goexit`; a udf could. |

## 4. Joins

| | | finding | cause |
| --- | --- | --- | --- |
| J1 ✔ | ~~**SW**~~ **fixed, step 74** | `AsOfBy` keys of different integer widths never match: every row is null. The schema shows the key promoted. | `asof.go:154`, `:281` call `evalKeys(…, nil)`, so the by-keys are never cast. |
| J2 ✔ | ~~**SW / CR**~~ **fixed, step 74** | Temporal keys of different units: a value outside the finer unit's range is nulled. A left join shows the key as null; under `NullsEqual` it matches a null key; with a non-nullable key it is `ErrInternal`. Affects Datetime and Duration. | A non-strict cast in `join.go:1417` (`evalKeys`) and `:1362` (`gatherOut`). The as-of path already switched to strict for this reason. |
| J3 | ~~**SW**~~ **fixed, step 74** | A spilled right join on two key columns nulls the non-null half of a partly-null right key. | `extjoin.go:506`: `nullPadOp` calls `gatherOut(…, false)`. |
| J5 ✔ | ~~**SW**~~ **fixed, step 74** | `Validate` 1:m / 1:1 misses duplicate left keys that have no match, and duplicate null left keys under `NullsEqual`. | `join.go:1002-1019` returns before the `seen` check. |
| J6 | ~~**SW**~~ **fixed, step 74** | `AsOfBy` matches a null by-key to a null by-key — undocumented, and against the `NullsEqual` default. | `asof.go` `bucket()` and `match()`. |
| J7 ✔ | ~~**FR**~~ **fixed, step 109** | An as-of join on a Float, Int8/16, Uint or Int128 key plans fine, then fails at `Collect` with *"cannot be read as Int64"*. `MergeSorted` refuses Float, Int16, Uint64 and String with a hint that contradicts itself. | `tempgroup.go:574` `temporalTicks`; `mergesorted.go:147`. |
| J8 | ~~SW (lossy)~~ **fixed, step 74**, for joins, Concat and Unpivot | Int64 vs Float64 promotion rounds above 2^53, both in joins and in strict `Concat`. This contradicts `resolve_union.go:68`: *"Promote only ever chose a type that holds both exactly"*. | `promote.go:97-107`. |
| J9 | ~~FR / ME~~ **fixed, step 110** | A full join with `JoinCoalesce(true)` is refused, and the hint says "no coalesce expression" — `ursus.Coalesce` exists. | `resolve.go:545-555`. |
| J10 | ~~ME~~ **fixed, steps 74 and 111**: the key-cast hint in 74; by 111 the `WhereExists` advice no longer appeared | The key-cast hint suggests `.Cast(ursus.Int64)` for a UTC vs naive Datetime join, and `WhereExists` suggests `JoinSuffix`, which it does not accept. | `join_layout.go:213`, `:276`; `join.go:203`. |
| J11 | ME | The spilled join refuses with *"a single join key has more build rows than the limit"* when no key has more than two. | `extjoin.go:422`; the real cause was not found. |
| J12 | ~~FR~~ **fixed, step 108** | `Concat` of a frame with a Null-typed column: *"concat is not implemented for Null"*. The same happens with plain `Collect` at batch size 1. | |

## 5. I/O

| | | finding | cause |
| --- | --- | --- | --- |
| I1 ✔ | ~~**SW**~~ **fixed, step 71** | `ScanParquetFiles` / `ScanParquetGlob` apply the first file's column layout to every file. `{b, a}` after `{a, b}` swaps the values; a file with other column names is read silently; a file with fewer columns panics inside arrow-go. | `parquet.go:255`, `:422`; the panic comes from `checkEncodings`, `:529`. |
| I2 ✔ | ~~**SW**~~ **fixed, step 71** | Pruning reads another column's statistics when a struct comes before the filtered column: `c == 2` returns 0 rows, and the answer is 1. | `prune.go:74/81/162` use the top-level index; `:180` passes it to `rg.ColumnChunk`, which counts leaves. `s.leaves` has the right mapping. |
| I3 ✔ | ~~**SW**~~ **fixed, step 71** | `!=` pruning drops NaN rows. Statistics exclude NaN, so min == max == v does not prove every row is v. | `prune.go:262-264`. |
| I4 | ~~**SW**~~ **fixed, step 71** | A row group holding `""` and a string over 4096 bytes: the max is dropped and reads back as `""`, so the `lo > hi` guard cannot fire and `>`, `==` and `>=` prune the group. Affects files written by ursus and by pyarrow. | `prune.go:250`; `bound.go:85`. |
| I5 | ~~**SW**~~ **fixed, step 71** | `IsNotNull` pruning on Struct and List columns uses one leaf's null count — the struct's first field, or the list element, where an empty list counts as null. | `prune.go:63`, `:214-221`. |
| I6 | ~~**SW**~~ **fixed, step 71** | A List whose element is declared non-null reads `[]` as `[null]`. Spark writes this shape (`containsNull=false`). | `column.go:581`. |
| I7 ✔ | ~~**SW**~~ **fixed, step 71** | Multi-file CSV skips later headers without checking them: columns are matched by position. | `csv.go:292`. |
| I8 | ~~**SW**~~ **fixed, step 77** | The CSV writer drops the seconds of a UTC offset, so the written text names an instant 30 s away (Africa/Monrovia 1970, Europe/Amsterdam 1930). Parquet and Arrow are unaffected. | `temporal.go:204`, layout `"Z07:00"`. |
| I9 | ~~**SW**~~ **fixed, step 77** | CSV inference accepts Go-only syntax: `1_000` becomes 1000, `0x1p3` becomes 8. Polars, pandas and DuckDB all read these as String. | `infer.go:72`. |
| I10 | ~~SW (low)~~ **fixed, step 77** | In a one-column CSV, an empty line is dropped instead of read as null. | |
| I11 ✔ | ~~**CR**~~ **fixed, step 72** | A Parquet List of Int8 or Int16 panics. | `parquet.go:710` stores the elements in an `[]int32` buffer. |
| I12 | ~~**CR**~~ **fixed, step 72** | Corrupt Parquet: in a byte-flip sweep of 7458 runs, 118 panicked inside arrow-go, unrecovered and in a worker goroutine. One file with an inflated `num_rows` **hangs forever and ignores the context**. | `parquet.go:482-484` (`rows == 0 → continue`). |
| I13 | ~~CR~~ **fixed, step 71** | A required List (level 0 means empty) and a required struct field both raise `ErrInternal`. | `column.go:577`. |
| I14 | ~~CR~~ **fixed, step 72** | `WithCompression(Lz4)` and `WithCompression(Lzo)` panic inside arrow-go. `Lz4Raw` works. | `writer.go:124`. |
| I15 | ~~FR~~ **fixed, step 71** | A UTF-8 BOM is not stripped from a CSV header, so `Col("a")` fails with *did you mean "﻿a"*. | |
| I16 | ~~FR~~ **fixed, step 112** | An unannotated FIXED_LEN_BYTE_ARRAY is typed Binary by the schema and then refused by the reader. | `parquet.go:617`, `types.go:192`. |
| I17 | ~~FR / ME~~ **fixed, step 81** | List of Uint8, Uint32, Time(ms), Bool or Decimal is refused, with a hint claiming the unsigned and Time types are read. | `parquet.go:732`. |
| I18 | ~~FR~~ **fixed, step 108** | Null-typed columns are refused by both writers; Polars writes them. | |
| I19 | ~~FR~~ **fixed, step 77** | The CSV reader refuses the Decimal and Int128 schemas the CSV writer produces, and inference reads a 38-digit decimal or `u64::MAX` as lossy Float64. | |
| I20 ✔ | ~~—~~ **fixed, step 152** | Invalid UTF-8 in a String column is never validated, so `SinkParquet` writes an out-of-spec STRING column that Polars and DuckDB refuse. | |
| I21 | **fixed, step 77** | A CSV round trip changes float types: integral floats come back Int64, and NaN/Inf come back String. It is exact with `WithSchema`. | |
| I22 | **fixed, step 77** | A zero-column frame loses its row count in Parquet, and in CSV becomes a column named `""`. | |
| I23 | SW, unmeasured | **Recorded at step 71, not fixed.** arrow-go reports `HasNullCount()` true for every statistic read from a file, so a file written WITHOUT `null_count` reads as having no nulls, and `IsNull` pruning would drop rows; and a one-sided integer or float min/max reads its absent side as 0. Only a third-party writer reaches either — arrow-go and pyarrow always write both — and arrow-go's read API cannot detect them. | arrow-go `statistics_types.gen.go`; recorded in `prune.go`'s header. |
| I24 | ~~class~~ **fixed, step 98** | **Recorded at step 71.** `data.CheckNonNullable` is on only in test binaries. A null in a column declared non-nullable is therefore `ErrInternal` in the test suite and **silent in production**: I1's nullability case and I13 both reached it, and were loud only because tests ran them. | `internal/data/batch.go`; no production setter. |
| I25 | ~~ME~~ **fixed, step 115** | **Recorded at step 72.** A panic in `ScanArrow`'s factory is KindIO, but a panic in the `RecordReader` it returns — the caller's code too — is `ErrInternal`, "a bug in ursus". | `arrowsrc` recovers only inside its schema `Once`; `reader.Next` is recovered by the engine's `Pull`. |
| I26 | low | **Recorded at step 72.** `DataFrame.Rows` decodes no List column: `no decoder for Go type []int64 from column List(Int64)`. | `rows.go` `makeSetter`. Step 72's list cases explode the list instead. |
| I27 | ~~**SW**~~ **fixed, step 77** | **Found at step 77.** A null String written to CSV is an empty field, and the reader read every empty String field as `""`, so null strings came back as empty strings, silently. The writer's doc called it a property of CSV; Polars writes the empty string as a quoted `""` and reads the two apart. DuckDB reads both as null. | `csv/writer.go` `NullValue`; `csv.go`, the empty-field rule. |

## 6. Scalar expressions

| | | finding | cause |
| --- | --- | --- | --- |
| S1 ✔ | ~~**SW / CR**~~ **fixed, step 73** | String → integer casts parse through float64: `"9007199254740993"` becomes …992, and MaxInt64 becomes 2^63 as Uint64. A strict cast returns the wrong value, or `ErrInternal`. `Str().ToInteger()` is the same. This is step 69's bug, one path over. | `unary.go:1172` → `narrowInt` → `fromFloat64(…, false)` at `:1277`. |
| S2 ✔ | ~~**SW / CR**~~ **fixed, step 73** | Float32 maths nulls every inexact result: `Sqrt`, `Cbrt`, `Exp`, `Ln`, `Log10`, `Log1p`, `Round`. On a non-nullable column it is `ErrInternal`. Also affected: `CastLossy(Float32)` of 0.1 is null, string → Float32, and `IsIn` over Float32. | `fromFloat64(strict=false)` then narrow's round-trip check (`unary.go:347`, `:775`; `mathfn.go:75`). The comment at `unary.go:343` says the opposite. |
| S3 ✔ | ~~**CR**~~ **fixed, step 72** | `Str().Slice(1, MaxInt64)` panics in a worker goroutine and **kills the process**. | `strfn.go:277`, `start+int(length)` overflows. `listSlice` clamps correctly. |
| S4 ✔ | ~~**CR**~~ **fixed, step 73** | A strict String → narrow integer or Float32 cast raises `ErrInternal` (`"256"` → Uint8), or with a null present returns a silent null. | `narrowInt` / `narrowFloat` pass `strict=false` (`unary.go:1277`, `:1281`). |
| S5 ✔ | ~~**SW**~~ **fixed, step 76** | `Dt().Epoch()` truncates before 1970: it returns 0, and the answer is −1. | `dtfn.go:99`. |
| S6 ✔ | ~~**SW**~~ **fixed, step 76** | `IsIn` casts the set strictly to the column's type. An Int64 column matches `IsIn("1")`, which `Eq` refuses; a Datetime(s) matches a sub-second value; a Date matches a Datetime at 13:00 on that day. Its error message names a cast the user never wrote. **Step 73** made the cast's float rule round, so `IsIn(0.1)` on a Float32 column now matches the float32 nearest 0.1 — as DuckDB's and PyArrow's do — where `Eq(0.1)` compares at Float64 and does not; the rule is still S6's. | `physical/eval.go:320-321`, and `buildListNeedle`. |
| S7 | ~~**SW**~~ **fixed, step 76** | Regex `Replace` (first match) does not expand `$1`; `ReplaceAll` does. | `strfn.go:173-179`. |
| S8 | ~~**SW**~~ **fixed, step 76** | Integer `FloorDiv` and `Mod` truncate while their float versions floor: `−7 // 2` is −3 as Int64 and −4 as Float64. Polars floors both, and the method is called FloorDiv. | `scalar.go:133-153`. |
| S9 | ~~**SW**~~ **fixed, step 76** | `SplitN(sep, 0)` returns `[]`; the doc says n ≤ 0 means unlimited. | `strfn.go:348`. |
| S10 | ~~**SW**~~ **fixed, step 76** | `Truncate(Every("1h"))` in a zone with a +05:30 offset floors on the UTC grid, although the doc says an Interval floors on the calendar. | `interval.go:448`. |
| S11 | ~~CR / SW~~ **fixed, step 76** | `Truncate` near the Datetime(ns) minimum: `1mo` raises `ErrInternal`, and a Duration step wraps int64. | `dtfn.go:173`, `:300`. |
| S12 | ~~SW~~ **fixed, step 76** | `CountMatches("", literal=true)` is null; the answer is len+1. | `strfn.go:112`. |
| S13 | ~~SW~~ **fixed, step 76** | `StripCharsStart("")` and `StripCharsEnd("")` strip only ASCII whitespace; `StripChars("")` uses `TrimSpace`. | `strfn.go:211`. |
| S14 | ~~FR / ME~~ **fixed, step 100, pinned in step 115** | Uint64 with an int literal in `//`, `%` or `*` is refused, and the hint suggests an Int64 cast that loses data. | `expr/resolve.go` ~160. |
| S15 | ~~ME~~ **fixed, step 111** | The Duration × float refusal hard-codes "2.5", and its own recipe fails. | `resolve.go:380-394`. |
| S16 | ~~ME~~ **fixed, step 111** | `FillNan` and `FillNull` errors name their desugaring (`is_not_nan`, `when`) rather than the method called. | |
| S17 | ~~doc~~ **fixed, step 115** | The Neg/Abs doc says unsigned types are supported; they are refused. | `expr.go:223`. |
| S18–S20 | ~~low~~ **fixed, step 86** | A strict Int64 → Time cast wraps around the day; a Duration unit cast floors while `TotalSeconds` truncates; Duration `Abs`/`Neg` at MinInt64 wrap silently. | |
| S21 | low, **the cast half fixed, step 77; the `Round` doc, step 86** | ~~`Cast(String → Int128)` is refused;~~ `ToUpper("ß")` is `"ß"`. ~~The `Round` doc says half-away "matching Polars", but Polars 1.44 defaults to half-to-even.~~ | |
| S22 | ~~CR, not run~~ **fixed, step 85** | **Recorded at step 72, not fixed.** `PadStart`, `PadEnd` and `ZFill` with a huge width allocate it: a fatal out-of-memory, which no recover can catch. Read from the code; running it takes the memory it exhausts. | `strfn.go`, no bound on the width. |
| S23 | ~~ME~~ **fixed, step 115** | **Recorded at step 72.** A panic in a `MapName` function is `ErrInternal`, where a panicking udf is the caller's KindValue. | `expr.Rename.Fn` is called by the planner with no attribution. |
| S24 | ~~SW, not run~~ **fixed, step 87** — the pads at step 85; every String column and the key table at 87 | **Recorded at step 72, not fixed.** `data.NewString` keeps 32-bit offsets, and past 2 GiB of string data in one column they wrap without an error. Read from the code, for the same reason as S22. | `internal/data`, string construction. |
| S25 | ~~low~~ **fixed, step 77** | **Recorded at step 73.** A String → float cast accepts Go's literal syntax, because it is `strconv.ParseFloat`: `"1_000"` is 1000, measured, and hex floats such as `"0x1p3"` parse too. Polars rejects both, and the String → integer parse accepts neither. The CSV reader shares the float grammar. | `castparse.go` `parseFloat`; the parse sweep skips underscore strings for floats rather than pinning either answer. |
| S26 | low | **Recorded at step 76, not fixed.** Integer `FloorDiv` of MinInt by −1 wraps to MinInt, silently; its quotient is MaxInt+1. Go and Polars both wrap. Int128 `//` and `%` are not implemented at all. | `kernel/scalar.go` `divIntScalar`; `arithI128` has no arm. |

## 7. Aggregation, sorting and windows

| | | finding | cause |
| --- | --- | --- | --- |
| A1 ✔ | ~~**CR**~~ **fixed, step 75** | `ShiftFill` with a fill of any other type raises `ErrInternal`: *"select branches must share a type"*. Only an exactly matching Go type works, which is all the test covers. | `expr/window.go:472` resolves without checking the fill; `kernel/select.go:46`. |
| A2 ✔ | ~~**SW**~~ **fixed, step 75** | At an exact rank, `Quantile` and `Median` return NaN over ±Inf and +Inf for the midpoint of huge values: `Quantile(0)` of `[−inf, 1, 2]` is NaN, while `Max` agrees with Polars. | `aggstat.go:493-496` has no `lo == hi` short-circuit. |
| A3 ✔ | ~~**SW**~~ **fixed, step 75** | `PctChange` on u8/i8 subtracts at the narrow width, wrapping before it divides: `[3, 1, 255, 0]` gives 84.67 where the answer is −0.67. | `window.go:202`, `:210-213`. |
| A4 | ~~**SW**~~ **fixed, step 75** | Integer `Mean` accumulates in Float64, so past 2^53 it disagrees with the exact `Sum` in the same query. Polars does the same; DuckDB is exact. Decimal and Duration means already divide an exact sum. | `agg.go:312-317`. |
| A5 ✔ | ~~**SW**~~ **fixed, step 75** | `Var(0)` of `[1e308, −1e308]` is −Inf. A variance cannot be negative. | Welford update, `aggstat.go:67-71`. |
| A6 ✔ | ~~**CR**~~ **fixed, step 72** | `Rank(RankMethod(99))` panics in a worker goroutine and **kills the process**. `Interpolation(99)` is silently treated as linear. | `window.go:112` does not validate; `kernel/window.go:147-149`. |
| A7 | ~~CR~~ **fixed, step 72** | A zero `Expr{}` used as a method receiver panics with a nil dereference. | `lazy.go:73-86` checks only the top level. |
| A8 ✔ | ~~FR / ME~~ **fixed, step 148** | `Diff`, `PctChange` and `FillNull(Mean)` inside `.Over(g)` are refused as "a window inside a window": the sugar embeds a window the user never wrote. Polars supports all three. | `resolve_window.go:79`. |
| A9 | ~~ME~~ **fixed, step 115** | The hint for a window inside `Agg` recommends two things that are both refused. | `resolve_window.go:159-163`. |
| A10 ✔ | ~~FR / ME~~ **fixed, step 75** | `Sum` of a Bool column is refused, and the hint's `Count()` counts rows, not trues. Polars and DuckDB both answer 2. | `agg.go:271-274`. |
| A11–A12 | ~~ME~~ **fixed, steps 111 (A11) and 115 (A12)** | The ordered-aggregate refusal contradicts itself; `CumSum` of a String reports "sum()"; the Any/AllTrue hints use lower-case names; a group-by error says "one output row per input row". | `physical/window.go:405-412`. |
| A13 | ~~low~~ **fixed, step 75**, and the last sentence in step 115 | Integer `Product` of `[3, 0, −5]` is −0. `GroupBy().Agg()` with no aggregates returns shape (0, 0) against the doc's one row. Temporal `Median`/`Quantile`/`Std` are refused without that being documented. | |
| A14 | ~~SW~~ **fixed, step 75** | **Recorded at step 72.** `Closed(99)` is accepted and behaves as `ClosedLeft` — measured, the same windows — which is A6's shape, an undeclared value of a public integer enum, without the panic. | No `Valid()` check on `Closed`. |
| A15 | ~~ME~~ **fixed, step 111** | **Recorded at step 72.** A udf's error, returned or panicked, names its row within the BATCH, not the frame: under `CollectBatches` with a batch size of 2, the third row is "row 0". | `udf.go`, `i` is the batch index. |
| A16 | ~~SW~~ **fixed, step 85** | **Recorded at step 75, not fixed.** `Diff` on an unsigned column subtracts at its width and wraps: UInt8 `[3, 1, 255, 0]` diffs to `[null, 254, 254, 1]`. Polars widens to Int16 and answers `[null, −2, 254, −255]`. `PctChange` no longer goes through it (A3). | root `window.go`, `Diff` is `Sub` at the column's type. |

## 8. The patterns, which are the point

Most of the seventy-eight fall into seven mistakes. Each fix below closes a class
rather than a finding.

1. **A rendering used as an identity** (O3). This is the UDF-name defect from step
   65, now for literals. Anything that deduplicates on `String()` needs a structural
   key. **Step 72: the four dedup maps key on `expr.Identity`**, the rendering plus
   every literal's type and every udf's id.
2. **float64 as the common currency** (S1, S2, S4). Step 69 removed it only from
   integer → integer casts. String parsing and Float32 maths still pass through it.
   Narrow's round-trip check is right for a *cast*, and wrong for the *result of a
   computation*, which is supposed to round. **Step 73: narrow is gone.** A string
   parses at the target's width, a computation rounds once, and a cast to a float
   rounds once from the source's own type and refuses only an overflow.
3. **Position instead of identity** (I1, I2, I7). Multi-file scans match columns by
   position, and pruning uses a top-level index as a leaf index.
4. **Rewrites nothing checks** (O1, O2). `WithVerify` catches O2, and it is off in
   every test and in `Explain`. A permanent optimizer-on-vs-off differential would
   have caught O1 as well.
5. **Hash equality standing in for IEEE `==`** (O4). A rewrite must not change
   which equality applies.
6. **Panics escape worker goroutines** (S3, A6, I12). `parallel.go:131` has no
   recover, so any kernel panic kills the host process. For a library embedded in a
   service, that is the worst failure mode there is. **Step 72: every call a driver
   or worker makes is recovered, and every entry point**; `TestEveryGoroutineIsCovered`
   fails on a new `go` statement until it names the test that covers it. A fatal
   runtime error — an out-of-memory, S22 — is still beyond any recover.
7. **`strict` not threaded through** (S2, S4, J2). Internal casts pass
   `strict=false`, and the result is either silent nulls or an `ErrInternal` from the
   non-null check, depending on the column's nullability. **Step 73 threaded it through
   the kernel's own paths (S2, S4)**, and the evaluator contract now fails a null in a
   column its Field declared non-nullable. **Step 74** threaded it through the join's two key casts (J2).

## 9. Documented differences, not reported

These are deliberate and written down, so none is reported as a defect:

- NaN compares by IEEE rules (Polars treats NaN == NaN).
- `When` with a null predicate yields null.
- `Every("1w")` truncates to Thursday, from the epoch.
- An unknown time zone falls back to UTC.
- A naive string parsed into a zoned type is read as UTC.
- `Str().Slice` clamps an over-negative offset — though `strfn.go`'s claim that
  this is "the convention every dataframe library uses" is false.
- As-of nearest breaks ties backward; a null as-of key is refused; as-of input must
  be globally sorted even with `by`.
- `AsOfLeftOn`/`AsOfRightOn` drop the right key.
- Semi and anti joins accept `Validate`.
- Ordinary Int64 arithmetic wraps.

## 10. Checked and correct

The negative results matter as much: they say where not to look. Each auditor
recorded 30–60 behaviours that held. In summary:

- **Sort** is right: NaN order, −0, nulls first or last, stability, the radix path,
  multi-key sorts, top-k with ties, and `Sort` + `Head` across batch sizes 1–1024,
  one to four threads and 4–16 KB memory limits.
- **Group-by keys** are right with NaN, −0, nulls, `""` and boundary collisions.
  Zero-row global aggregates are right. `Var` with n ≤ ddof, and every quantile
  interpolation at eight q values, match Polars.
- **Rank**, in every method, matches Polars. The cumulative functions are right in
  both directions with nulls.
- **Joins**:
  - null keys in all six kinds match Polars under both `NullsEqual` settings, and so
    do NaN and ±0 keys;
  - duplicate keys and empty sides are right, and output nullability is right;
  - spilled and in-memory joins agree at 4–6 KB limits for every kind but the one in
    J3;
  - the optimizer agrees with itself over 97 join queries, apart from O4.
- **Optimizer**: about 230 query shapes were compared with it on and off. Predicate
  pushdown through every outer join with null-sensitive filters (`IsNull`,
  `EqMissing`, `Coalesce`) is right, and so are limit/top-k pushdown and constant
  folding (division by zero, overflow, `MinInt64 // −1`, NaN).
- **Parquet**:
  - every scalar type round-trips exactly, and pyarrow reads the file identically;
  - files written by pyarrow with every codec, both page versions and every encoding
    match pyarrow, and so do files from Polars and DuckDB;
  - about 40 other pruning predicates match `WithPruning(false)`.
- **CSV and Arrow**: a 3000-row randomized quoted CSV matches Python's `csv` module
  byte for byte. Arrow export passes pyarrow's `validate(full=True)` for every type
  and at non-byte-aligned offsets. Arrow import of sliced arrays is right.
- **Scalar**:
  - Kleene logic over all nine pairs;
  - IEEE float comparisons;
  - Float64 ↔ String round trips;
  - temporal field extraction before 1970;
  - DST truncation in New York;
  - integer division and modulo by zero;
  - mixed-width promotion.

## 11. Order of work

Ordered by harm per unit of fix, not by count.

1. ~~**O1 and O2, with the instrument that would have caught them.**~~ **Done — step
   70**, with O8, a stronger `Verify` that `Explain` now runs, and a generated
   optimizer on/off differential over 1629 query shapes. That differential found P1,
   and measuring the step found W1; both are above, and P1, O4, O5, O6, O8b and O9
   are what it still counts.
2. ~~**I1–I7: read multiple files by name, and prune only what the statistics prove.**~~
   **Done — step 71**, with I13 and I15 beside them, a generated pruning differential
   and a several-files-against-one differential.
   This is silent data corruption from ordinary file reads, and I1 alone makes
   `ScanParquetGlob` unsafe over any directory written by more than one tool.
3. ~~**O3: literals carry their type in their identity.**~~ **Done — step 72**, with
   `expr.Identity` and a sweep over every literal type the public API lifts.
4. ~~**Recover in worker goroutines.**~~ **Done — step 72**, and at every entry point,
   with S3, A6, A7, I11, I12 and I14 fixed at their causes too. A panicking udf is the
   caller's KindValue error; a corrupt Parquet file is KindIO.
5. ~~**Finish step 69's job**: no float64 go-between for S1, S2 and S4, and `strict`
   threaded through.~~ **Done — step 73**, with the same go-between found and removed in
   Int128 and Decimal to a float and in temporal ↔ integer casts.
6. ~~**The join promotion paths**: J1, J2, O6 and J8.~~ **Done — step 74**, with J3,
   J5, J6 and J10's hint: a key, a Concat column and an Unpivot value meet at a type
   that holds both exactly (`dtype.PromoteExact`), keys are cast there strictly, and a
   pair with no such type — a 64-bit integer with a float — is refused.
7. ~~**Aggregations and windows**: A1–A5, A10, A13, A14 and W1.~~ **Done — step 75**:
   an integer mean divides an exact Int128 sum, a quantile at an exact rank is that
   value and nothing interpolates through an overflow, a variance is never negative,
   PctChange computes in float, a fill meets its column, a Bool sums its trues,
   `Closed` is validated, and a window reads the columns defined before it. A16 is new.
8. ~~**Scalar functions**: S5–S13.~~ **Done — step 76**: `Epoch` floors; `IsIn` and
   `list.contains` compare as `Eq` does; a first-match regex `Replace` expands its
   groups; `FloorDiv` floors and `Mod` is its remainder, for integers, floats and
   Durations; `SplitN(0)` is unlimited; an Interval floors the wall clock, sub-day
   included, in `Truncate` and `GroupByDynamic`; a truncated instant outside the
   type is refused; an empty literal is counted; and the one-sided strips trim
   Unicode whitespace. S26 is new.
9. ~~**CSV and I/O**: I8–I10, I19, I21, I22, with S25.~~ **Done — step 77**: an offset
   with seconds is written in UTC; one float grammar, without underscores or hex; a
   blank line in a one-column CSV is a null; Int128 and Decimal read from CSV and parse
   from String, exactly; a float is written so it reads back as one; a frame with rows
   and no columns is refused by the writers; and a null String and an empty one are
   written and read apart (I27, found by the step's round-trip sweep).
10. ~~**The optimizer's leftovers**: O4, O5, O8b, O8-join, O9, P1, with O10 and
    O11.~~ **Done — step 78**: step 70's on/off differential has no mismatch left over
    1629 queries. A float key extracted from `==` is checked again after the hash
    match; a collapsed join never equates nulls; a fallible conjunct does not move
    ahead of a guard or into a join side that loses rows; a filter reading a float
    does not pass a Distinct; a redefined column keeps its place; and the planner
    refuses what Collect refuses. O7 is closed as step 71's.
11. Then the rest, by table.

Known and excluded, because they were already on the open lists:

- Decimal arithmetic precision, and Decimal against a literal;
- ~~strict Int64 → Float32 at 2^53~~ — closed by step 73's rule: a cast to a float
  rounds, so 2^53+1 → 2^53 is the right answer;
- `Optimizer.Verify` off (O2 is its first concrete consequence);
- ~~`rolling` unaccounted~~ (charged since step 96), and ~~`unique`/`over` not spilling~~ (spilling since step 82);
- nested write, and object stores;
- ~~the planner leak~~ (closed in step 95), and ~~`spill.Writer.Write`'s bare error~~ (wrapped in step 115);
- the README debt.
