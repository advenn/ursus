# Step 501 — as built

**The frames, joins and reshaping wave converted to recorded oracles.** Wave 501 of
[`oracle-v2-scope.md`](./oracle-v2-scope.md) §3, in step 496's framework
([`step-496-as-built.md`](./step-496-as-built.md)): twenty-two files' hand-typed
expected values are Polars 2.1.0's answers now, recorded in twenty-two groups of
`testdata/oracle` by `bench/scripts/oracle/<group>.py` and spelled in
`oracle_<group>_test.go`. What a file keeps asserts a refusal, a type, a plan, a count
or a Go-API shape, or is a differential: the same query spilled, eager, or at another
batch size.

## 1. What

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `join_byhand_test.go` | 250 | 194 | 25 | 0 | 11 | 2 |
| `joinorder_test.go` | 235 | 118 | 17 | 0 | 47 | 10 |
| `asofoptions_test.go` | 183 | 109 | 15 | 0 | 17 | 3 |
| `concat_test.go` | 473 | 353 | 50 | 0 | 25 | 4 |
| `pivot_test.go` | 551 | 276 | 110 | 1 | 39 | 4 |
| `dummies_test.go` | 209 | 74 | 60 | 0 | 36 | 2 |
| `unstack_test.go` | 139 | 72 | 30 | 0 | 17 | 2 |
| `unpivot_test.go` | 344 | 246 | 25 | 0 | 6 | 1 |
| `upsample_test.go` | 230 | 176 | 40 | 0 | 17 | 2 |
| `update_test.go` | 147 | 94 | 35 | 0 | 19 | 3 |
| `matchschema_test.go` | 273 | 186 | 40 | 0 | 17 | 1 |
| `frameagg_test.go` | 184 | 93 | 25 | 0 | 23 | 7 |
| `framecolumns_test.go` | 215 | 195 | 8 | 0 | 8 | 1 |
| `framerows_test.go` | 136 | 46 | 28 | 0 | 31 | 3 |
| `frameconv_test.go` | 325 | 191 | 25 | 0 | 19 | 4 |
| `framenulls_test.go` | 226 | 147 | 20 | 0 | 15 | 4 |
| `fill_test.go` | 712 | 424 | 55 | 4 | 25 | 2 |
| `intindex_test.go` | 333 | 216 | 55 | 0 | 32 | 5 |
| `dynamicopts_test.go` | 135 | 32 | 25 | 0 | 16 | 2 |
| `having_test.go` | 132 | 88 | 30 | 0 | 16 | 2 |
| `grouphead_test.go` | 129 | 118 | 18 | 0 | 9 | 2 |
| `generate_test.go` | 143 | 43 | 45 | 0 | 37 | 4 |
| | 5,704 | 3,491 | about 780 | 5 | 482 | 70 |

482 cases in twenty-two groups, 70 generated (`Gen`, seeds 5011 to 5032, 50 to 200
rows, nulls, the floats' edges where the function takes them), no refusals recorded;
the JSON files are 23 KB to 236 KB, 1.4 MB together. `make oracle` recorded twice to
the same bytes. The five hand-typed lines that stay each pin a documented rule where
Polars 2.1 has no answer or another one: `pivot_test.go`'s combination of On values
holding a null, written `{null,"c"}`, which Polars names "null" and refuses as a
second column (feature-gaps.md §9); and `fill_test.go`'s fill of 5000 into an Int8
column, an Int64 here (fill.go's fallback: "falling back is right, truncating is not")
and an Int16 in Polars (Found, below), with the group's two maxima of 7 in the
temporary-collision guard.

**Several inputs, by name.** The joins take `left` and `right`, join_where `a` and
`b`, update `df` and `other`, concat `f0`, `f1`, `f2` (or `a`, `b`), and
`insert_column` the column to insert as a frame of its own, `n`, which
`oracleColumnOf` takes the column of. The generate group's generators of literals
alone take no frame: their arguments are the case's `params`, read by
`oracleParamInt`, `oracleParamFloat` and `oracleParamString`, and `gather`'s indices
by `oracleParamInts`; the Go side selects the generator from a frame of one row, its
length the generator's own.

**Polars 2's streaming order.** A join's `maintain_order` promises one side's order and
appends the other side's unmatched rows after it in no promised order, another each
run, where ursus appends them in their own frame's order; a full `update` appends the
keys only one side has likewise. Each such case sorts that tail by its own value in the
Python query, which is its frame's order on these inputs, so the recording is Polars'
matched rows in Polars' order and the tail in the order ursus promises, and is the
same bytes every run. A group-by, an upsample of groups and a group's head take
`maintain_order=True`, the order here; a pivot's rows are in the order each index
first appears on both sides.

## 2. Differences named

In `oracleDifferences`, each documented in `feature-gaps.md` §9 or §11:

- `pivot/none_len` and `pivot/none_n_unique`: a count is a Uint64 here, Polars' a
  UInt32. `pivot/none_sum`: an integer sum is an Int128 here, Polars' an Int64, and a
  pivot's cell of none is null under Sum here, 0 in Polars (step 296);
  `pivot/none_sum_fill_0`: the Int128 alone.
- `frameagg/sum`: an integer or Boolean sum is an Int128; `frameagg/product`: a product
  is a Float64 for every number; `frameagg/null_count`, `empty_null_count` and
  `gen_null_count`: a count is a Uint64; `frameagg/empty_sum` and `empty_product`: a
  sum or product of no value is null here, 0 or 1 in Polars (step 300).
- `generate/repeat_1_3`, `repeat_none`, `repeat_a_string` and `repeat_a_total`:
  `Repeat` is named `repeat` here, where Polars 2 names it after its first input
  (`literal`, or `a` for a total of `a`; §11), and a literal integer in it is an Int64
  here, an Int32 in Polars (§9, step 263).

Spelled around in the query, as the earlier waves did: an integer `Sum` cast to Int64
and a `Len` to Int64 on both sides where the windows, not the types, are the question
(intindex, dynamicopts, having, framecolumns' broadcast aggregate, generate's `summed`
and `repeat_a_total`); a float var, std, mean, corr, a horizontal sum of three floats,
a pivot's mean of a few floats and an interpolated line held to rel:1e-15 or rel:1e-12
with the reason in the note, ursus's sums exact and Polars' a few ulps off (§1, §9),
NumPy's corrcoef an ulp or two from exact as the old test allowed; a join of an Int32
key against a Float32 one, of a Datetime(ms) against a Datetime(ns) and an as-of join
by Int32 and Int64 keys, which Polars refuses, asked with the narrow key cast to the
type the two meet at here (join_byhand's rule); a filter on a coalesced key of two
widths asked with the key cast to Int64 first, Polars' coalesced key keeping the
left's Int32 and 50000·50000 wrapping there; `ConcatStrict` of an Int32 and an Int64
asked as `vertical_relaxed`, Polars' vertical refusing them (§9); `Unpivot`'s row-major
order asked of Polars through a row index and a stable sort (§9); `Upsample` of a
frame unsorted or holding a null instant asked of the frame sorted or without the
row (§9); a frame's `FillZero` and `FillOne`, which leave a String or Bool column
alone, asked of the numeric columns, Polars filling the text with "" and refusing
`one` at frame level; a rolling group by a key selected index first (§9); a dynamic
group's windows of no rows filtered out on the Go side (§9); a pivot's `n_unique`,
which Polars' `aggregate_function` has no name for, asked as `pl.element().n_unique()`;
a Decimal's corr asked of the floats it holds, Polars' `to_numpy` refusing a Decimal.

## 3. What stays, and why

- **Refusals**, as they were: every file's `refused` subtest, join_byhand's J2, J5, J8
  and J10, the pivot's schema errors and `TestPivotRefusals`, `TestTransposeAnswersAsPolars`'s
  namings, `TestUnpivotRefusals`, `TestGeneratorsRefuse`, `TestDynamicOptionsRefuseWhatPolarsRefuses`,
  `TestExprDropNullsIsRefused`, `TestConcatRejectsMismatchedSchemas`, `TestHStack`.
- **Types, plans and counts:** concat's nullability and column order over twenty runs,
  its pushdown and golden plan; the unpivot's value nullability, implicit index and
  pushdown arm; `TestPivotRefusals`' lazy schema and plan; `TestUnpivotWithoutOn`'s plan
  and empty schemas; `fill_test.go`'s weak-literal types, the temporary-collision
  guard, the limit in the rendering, a frame fill's columns in the plan, `Diff`'s one
  window, `TestClipPreservesType`, `TestFillStrategies`' types (six kept, the mean a
  Float64); `TestAsOfCoalesceAsPolars`' column names; `TestLenKeepsItsNameThroughACast`;
  `TestRowShortcuts`' `IsEmpty`; `TestHashRows`, whose numbers are ursus's own; the
  frame's Go accessors (row, item, columns, series, struct, a lazy frame's schema).
- **Pins of a documented rule Polars has no answer for:** a group's `Head(0)`, no rows
  here and a row of nulls a group in Polars (§9); an upsample of no rows, which Polars
  refuses; an unstack of no rows, which Polars refuses; the pivot's null combination
  (§1 above).
- **Differentials:** join_byhand's J3, the spilled right join against the same join in
  memory (its 4,097-row build stays in Go); `TestJoinOrderUnderAMemoryLimit`,
  `TestOrderedJoinLeavesTheLimitToTheJoin`, `TestConcatAlignUnderAMemoryLimit`,
  `TestPivotUnderAMemoryLimit`, `TestUpsampleUnderAMemoryLimit`,
  `TestUpsampleKeepsTheGridsOrder`, `TestUpdateUnderAMemoryLimit`,
  `TestMatchToSchemaUnderAMemoryLimit`, `TestFrameAggregatesUnderAMemoryLimit`,
  `TestGroupHeadTailUnderAMemoryLimit`, `TestInterpolateTemporal`'s spill,
  `TestIntegerIndexAnswersAsADatetime`, `TestConcatPreservesOrder` (every batch size
  against the default), `TestUnpivotIsBatchSizeInvariant`, `TestFillIsBatchSizeInvariant`,
  `TestKeylessWindowMatchesGlobalAggregate`, the long parallel corr and gather of
  `TestFrameConveniences`, every `eager` subtest, having's and grouphead's keyless
  frame against the frame's own `Head` and `Tail`, and `TestUnpivotKeepsItsColumnsAliveThroughPushdown`'s
  value column against the whole melt's.
- **Dropped as covered by the recordings' dtypes and names:** `TestConcatPromotesTypes`,
  `TestUnpivotPromotesTheValueType`, the Int32 index's type, the sorted pivot's and the
  left-out Values' column names, `TestRowShortcuts`' types, `TestDummyNames` (the
  recordings carry the names), `TestFrameEquals` (each a recorded Boolean).
- **Helpers gone:** `u8s`, `pairs`, `matchTypes`, `dynamicRows`, pivot's `assertPivot`,
  `TestConcatModes`' local `at`; kept and shared: `i64s`, `f64s`, `strs`, `dfOf`, `at`,
  `timed`, `ticks`, `nullable`, `pivotFrame`, `unpivotFrame`, `generateFixture`.
- **Renamed:** `TestPivotSeveralOn` is `TestPivotSeveralOnNamesANull`, its one pin.

## 4. Found

Each recorded, skipped in the registry with `oracleFinding`, and the maintainer's to
decide; none is documented in `feature-gaps.md` §9 or §11, so none is named a
difference and no recording was changed.

- **A fill past the column's type** (`fill/fill_past_the_type`): `fill_null(5000)` of
  an Int8 column is an Int16 in Polars 2.1, the narrowest type that holds the
  literal, and an Int64 here, the weak literal's fallback (fill.go; `TestWeakLiteralThatDoesNotFit`
  pins it). `fill_null(-1)` of a UInt32 is an Int64 in both.
- **The mean strategy keeps an integer column's type in Polars** (`fill/fill_strategy_mean`,
  `fill/frame_fill_mean`): `fill_null(strategy="mean")` of an Int32 or Int64 column
  fills the mean truncated, 4.5 as 4, and keeps the type; ursus widens the column to
  Float64 and fills 4.5 (`TestFillStrategies`: only the mean widens). The six other
  strategies agree.
- **`AsOfNearest`'s tie** (`asofoptions/nearest_tie_gen`): where the backward and
  forward rows are equally far, Polars 2.1 takes the forward one and ursus the
  backward (asof.go: "ties going backward"; `TestAsOfNearestTiesBackward`). The
  generated tolerance case asserts backward and forward alone; the literal `nearest_2`
  has no tie.

Found and known: every other pasted value of the twenty-two files is Polars 2.1.0's
answer too, the right join's coalesced null key, the as-of by-key null matching
nothing, the align modes' null keys sorted first and a float -0.0 key kept included;
Polars 2.1 has `pivot(on_columns=)`, so the lazy pivot's cases ask it directly, and
`update(maintain_order=)`.

## 5. Checks

`make oracle` recorded twice to the same bytes (`md5sum -c`, 22 of 22). `go test -run
TestOracle .`: 482 cases pass or skip as named. `go test .` in full, `go vet ./...`,
`go run ./internal/gen/api -check` (0 gone; nothing exported), `gofmt -l .` empty, all
under `GOEXPERIMENT=simd GOAMD64=v3`. `TestEveryCitedTestExists` passes: `fill.go`'s
comment cited `TestFillNanLeavesNullsAlone`, which is the fill group's
`fill_nan_leaves_nulls` now, and says so.
