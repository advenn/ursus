# Step 500 — as built

**The aggregates and windows converted to recorded oracles.** Wave 500 of
[`oracle-v2-scope.md`](./oracle-v2-scope.md) §3, in step 496's framework
([`step-496-as-built.md`](./step-496-as-built.md)): nineteen files' hand-typed expected
values are Polars 2.1.0's answers now, recorded in nineteen groups of `testdata/oracle`
by `bench/scripts/oracle/<group>.py` and spelled in `oracle_<group>_test.go`. What a
file keeps asserts a refusal, a type, a plan, a name, a stream past a limit or a
differential, or holds to math/big what Polars 2.1 answers wrongly.

## 1. What

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `aggwindow_byhand_test.go` | 256 | 200 | 53 | 0 | 27 | 4 |
| `aggstats_test.go` | 320 | 158 | 30 | 0 | 60 | 5 |
| `aggpair_test.go` | 393 | 204 | 25 | 0 | 26 | 3 |
| `agglists_test.go` | 102 | 29 | 16 | 0 | 20 | 2 |
| `argsort_test.go` | 106 | 45 | 19 | 0 | 25 | 5 |
| `hist_test.go` | 138 | 35 | 26 | 0 | 26 | 4 |
| `search_test.go` | 227 | 79 | 34 | 0 | 44 | 5 |
| `seq_test.go` | 198 | 118 | 22 | 0 | 30 | 7 |
| `modeagg_test.go` | 166 | 119 | 10 | 0 | 10 | 3 |
| `ewmmore_test.go` | 267 | 112 | 44 | 0 | 31 | 2 |
| `window172_test.go` | 164 | 34 | 11 | 0 | 12 | 2 |
| `windownest_test.go` | 231 | 74 | 33 | 0 | 42 | 3 |
| `rollingby_test.go` | 292 | 167 | 18 | 0 | 24 | 3 |
| `rollingoffset_test.go` | 103 | 37 | 25 | 0 | 22 | 6 |
| `rollingquantile_test.go` | 99 | 23 | 17 | 0 | 5 | 2 |
| `rollmap_test.go` | 167 | 101 | 14 | 0 | 14 | 2 |
| `exprroll_test.go` | 255 | 163 | 31 | 0 | 26 | 2 |
| `cumeval_test.go` | 99 | 62 | 12 | 0 | 13 | 2 |
| `topk_agg_test.go` | 136 | 102 | 7 | 0 | 7 | 1 |
| | 3,719 | 1,862 | 447 | 0 | 464 | 63 |

464 cases in nineteen groups, 63 generated (`Gen`, seeds 5001 to 5019, 120 to 200
rows, nulls, NaN, ±Inf, −0 and the type's edges where the function takes them), no
refusals recorded (every refusal stays in Go); the JSON files are 7 KB to 172 KB. The
hand-typed value lines are counted from the diff: a removed line that carries an
expected value, inputs and error messages left out (no inventory classifier exists yet;
step 504 builds one).

**The loader** (`oracle_test.go`) grew one thing: a case's `float` may name an
absolute tolerance beside the relative, `"rel:1e-12 abs:1e-15"`, for an answer that is
exactly 0 in Polars' two passes and a rounding's 1e-16 in ursus's one (the skew of
symmetric values), or a running sum of deviations from the mean that ends near 0, its
residual the whole value. `ursustest.WithTolerance` had both all along.

## 2. Differences named

In `oracleDifferences`, each in `feature-gaps.md` §9:

- **A sum of only nulls is null here, 0 in Polars** (nine cases): `agglists/over_y`;
  `windownest/sum_over_g_within_h` and `sum_over_g_of_sum_over_g`;
  `rollingby/sum_2h_closed_left` and `sum_2h_closed_none` (an empty window by time);
  `exprroll/no_offset_3i`, `ahead_sum_no_offset_over_g`, `sum_1d_offset_m2d`,
  `filter_sum_2d` and `in_aggregation` (a window of a rolling group holding no value).
- **`aggstats/entropy_none`:** the entropy of no value is null here, as Sum's is;
  Polars' is −0.0.
- **`aggpair/min_by_max_by_nan`:** MaxBy orders by as Max does, NaN the greatest (§9:
  Max propagates NaN; MaxBy's doc says so for itself), so it chooses the NaN's row where
  Polars' `max_by` skips it. §9 names Max alone; a line for MaxBy would make it exact.

**Spelled around in the query,** as the earlier waves did: an integer Sum is an Int128
here (`Sum`'s doc, `internal/expr.ResolveAggBinding`) and a count a Uint64 (§9), so a
Boolean's sum, a chained window's sums, an aggregation's cumulative sums, an integer
dot, `approx_n_unique`, `Count` and `Len` are cast to Int64 on both sides where their
type is not the question; `Product` is a Float64 here (§9), so Polars' Int64 is cast
and the question is +0's sign. A mode's order over the frame is Polars' hash table's,
so those cases are sets; by group, ursus's `Mode(false)` is sorted, asked of Polars as
`mode().sort()`, and a Struct's modes come as they first appear in both (§9), asked as
`maintain_order=True`. A `top_k`'s order is open in Polars, so each list is sorted on
its side as ursus orders it. `arg_sort_by` is asked with `maintain_order=True`, ursus's
being stable (two rows tied on both keys came the other way round). A `search_sorted`
in an aggregation is a list of one in Polars 2.1, so its `first()`. A hist's, a rle's
and a value_counts' struct is unnested on both sides, so the Categorical labels compare
as text. A `WithColumns` whose later windows read its earlier columns (audit W1) is
Polars' chained `with_columns`. The zoned ewm case is London's, Dublin having two forms
in the zone database (step 498).

**Tolerances, each with its reason in the note:** rel:1e-12 for the moments, the
entropies, `corr` and `cov` (Polars' two passes against ursus's merged one), the ewm
family (an FMA fused or not, step 496), a rolling variance, and in the generated cases
a sliding sum's or mean's residual, a linear interpolation's formula order, a window's
or a prefix's sum order; rel:1e-6 for a Float32's ewm and entropy (its own ulp 6e-8);
abs:1e-15 beside it for the skew of symmetric values, abs:1e-12 for the running sums of
deviations.

## 3. What stays, and why

- **Refusals**, as they were: the audit's `Closed(99)` (three), the bitwise reductions
  of a float and an Int128, mode beside a column, entropy of Booleans, Spearman and skew
  of text, `Nth(9)`, `ArgSortBy` of two keys in an aggregation and `ArgTrue` of integers,
  hist's falling edges, both kinds of bin, text and a Date, a search side that is none,
  a peak of text, value_counts beside a column of another length, an append without
  upcast or of text and a number, the ewm half-lives and bys (six), the ordering over an
  aggregate (three), rolling by time's ten, a quantile outside [0, 1], an ordinal
  rolling rank, two rolling_maps of one name, a panic and an error in one, Expr.Rolling's
  four, cumulative_eval's two, TopK's k below 1 and a Boolean.
- **math/big as the oracle** where Polars 2.1 answers wrongly:
  - `TestIntegerMeanIsExact` (audit A4): an integer mean divides an exact Int128 sum,
    so [2^53+1, 2^53+2]'s is 2^53+2, where Polars sums in float and answers 2^53; the
    exact rational mean, rounded once, over the pair and 200 large integers as a
    group's, a window's and a list's mean, to the bit;
  - `TestAggregatesAtTheFloatsEnds` (A2, A5): the median of [-1.7e308, 1.7e308] is 0 and
    the midpoint of [1e308, 1.5e308] 1.25e308, +Inf in Polars 2.1; the variance of
    [1e308, 1e308] is 0, NaN there; 65 pairs across the float's range against the exact
    midpoint and variance, within four ulps where finite (lo + (hi − lo)/2 rounds twice),
    to the bit at 0 and +Inf;
  - `TestSkewAndKurtosis`: the skew and kurtosis of [1e9+1, 1e9+2, 1e9+4] and of two
    thousand values near 1e9 against their exact rationals through a 256-bit float
    (`exactMoments`), to rel:1e-12; Polars 2.1 is seven digits off (Found, below).
- **Differentials:** the four-thread merge of the moments, `TestRollingOfAnExpression`'s
  index out of order (Polars refuses it, ursus answers by value, §9), held to the sorted
  frame's answer; its window's own rolling against the rolling group's over 400 rows;
  `TestPairedAggregatesAgreeWithABruteForce`, `TestRollingByAgreesWithABruteForce`,
  `TestTopKIsTheRankWindow`, `TestCumulativeEvalPastAChunk`, and the streams past a
  memory limit (mode, ewm, nested windows, rolling_map, Expr.Rolling).
- **Structure:** a global aggregate of nothing is one row and no columns; an aggregate
  (Int128), a list and a literal beside each other; an appended column's nullability
  under a verified run; a mode's plan and its first-seen order on four threads; the ewm,
  the paired aggregates' and the rolling-by names and plans; a rolling offset's plan;
  TopK's own rule that a group of only nulls is a null list (its doc; Polars 2.1 keeps
  the nulls, Found); `TestHash`, `TestSampleAndShuffle`, whose numbers are ursus's own.
- **Dropped as covered by the recordings' dtypes:** the type checks of the bitwise
  reductions' narrow types, rle's struct and rle_id's Uint32, unique_counts' Uint32,
  value_counts' share struct, hist's name and type, search_sorted's and index_of's
  Uint32, an aggregation's `List(Uint32)`s, Append's and ExtendConstant's types,
  Interpolate's kept Int64, min_by's kept Int64, a Float32's entropy.
- **Renamed:** `TestRollingOffsetAsPolars` is `TestRollingOffsetRenders`,
  `TestRollingMedianAndQuantileAnswerAsPolars` is `TestRollingQuantileRefusesOutsideUnit`.
  **Gone:** `TestCorrCovAnswerAsPolars`, `TestMinByMaxByAnswerAsPolars`, `TestRle`,
  `TestEwmSum`, `TestEwmFingerprints` (its 300 rows recorded whole, in two cases),
  `TestInterpolateNearestAnswersAsPolars`, `TestRollingCenterAnswersAsPolars`,
  `TestRollingCenterIsPerPartition`, `TestLinearAndNearestAreTwoAnswers`,
  `TestRollingByAnswersAsPolars`; the helpers `nanNulls`, `nullableInts`,
  `windowMembers`, `knownAggDefects`. `rendered172` stays in `window172_test.go`, six
  other files rendering with it; `floats175`, `near175` and `nearFloats` stay, used
  elsewhere.

## 4. Found

Each recorded, skipped in the registry with `oracleFinding`, and the maintainer's to
decide; none is documented in `feature-gaps.md` §9 or §11, so none is named a
difference and no recording was changed.

- **Polars 2.1 overflows at the ends of a float's range** (`aggwindow_byhand/
  median_extremes`, `midpoint_huge`, `var_alike_huge`): the median of [-1.7e308,
  1.7e308] is +Inf there (lo + (hi − lo)/2, the span past the range) and 0 here; the
  midpoint of [1e308, 1.5e308] +Inf ((lo + hi)/2) and 1.25e308 here; the variance of
  [1e308, 1e308] NaN (its sum of squares) and 0 here. The audit's A2 and A5 chose these,
  held to math/big now.
- **Polars 2.1's skew of large values close together loses seven digits**
  (`aggstats/skew_near_1e9`, `moments_far_2000`): [1e9+1, 1e9+2, 1e9+4]'s skew is
  [1, 2, 4]'s, 0.38180177416060623, which 1.44.1 answered and ursus answers; 2.1 answers
  0.38180167858053743, a float mean at 1e9 having an ulp of 1.2e-7. Two thousand values
  near 1e9 are 7e-9 off in both moments. ursus shifts by the first value (step 276).
- **A one-value group's raw entropy is 0.0 in Polars 2.1** (`aggstats/entropy_raw_by_g`):
  −Σ x·ln x of [5] is −5 ln 5, −8.05, which ursus answers; Polars 2.1 answers 0.0 for
  one value, over the frame too (`entropy_raw_single` in the probe). Over several values
  the two agree (`entropy_raw_all`).
- **Polars 2.1 ends a rolling window at its offset plus the period**
  (`rollingoffset/1mo_offset_m28d`, `1mo_offset_m4w`): with period 1mo and offset −28d,
  Feb 28's window is (Jan 31, Jan 31 + 1mo] = (Jan 31, Feb 29], so [2, 3]; 1.44.1 ended
  it at the row, [2], "a month measured as 28 days" (step 313's note), and ursus does.
  Every other offset case agrees, the default and −1mo included.
- **Polars 2.1 keeps nulls in a `top_k`** (`topk_agg/top_k_ints`, `bottom_k_ints`,
  `top_k_decimals`): of [5, 9, 1, null] it answers [5, 9] in an aggregation (the order
  open), of [7, null] [7, null], and of three nulls [null, null]; 1.44.1 skipped them,
  as ursus does (TopK's doc: a shorter list, null when none; `TestTopKValues` keeps the
  null-list rule). The float, string and date columns, which have no nulls, are
  recorded and agree once each list is sorted.

Found and known, not findings: an integer mean past 2^53 is Polars' float sum's (above,
A4, now a Go oracle: audit.md §7's rule, which §9 does not list); `ApproxNUnique` is
exact where Polars estimates past about 64 distinct values (its doc), so the generated
case keeps under 64; `MaxBy` over a NaN (§2); and the W1 chained windows, which Polars
cannot spell in one `with_columns`.

## 5. Checks

`make oracle` recorded twice to the same bytes (`md5sum -c`, 19 of 19). `go test -run
TestOracle .`: 464 cases pass, are a named difference or skip as a finding (11 skipped).
`go test .` in full, `go vet ./...`, `go run ./internal/gen/api -check` (0 gone; nothing
exported), `gofmt -l .` empty, all under `GOEXPERIMENT=simd GOAMD64=v3`.
`TestEveryCitedTestExists` passes: no Go comment cites a removed name (the removed and
renamed tests are cited in step records alone).
