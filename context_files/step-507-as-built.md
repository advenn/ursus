# Step 507 — as built

**The 25 differences from Polars 2.1 closed: 22 documented, 2 docs corrected, 1 bug
fixed.** The first row of [`step-506-as-built.md`](./step-506-as-built.md) §3. Waves
496 to 503 recorded twenty-five places where ursus answers otherwise than Polars 2.1.0
and nothing said it meant to, thirty-nine cases skipped by `oracleFinding` or a
`t.Skip` and listed in [`feature-gaps.md`](./feature-gaps.md) §12 by step 504. This
step read each under the maintainer's rule of 2026-11-14: "ursus doesn't need to be
clone of polars. polars has features and we need those features, thats it." Polars is
the list of capabilities, not ursus's specification; where ursus answers otherwise for
a reason of its own, its answer stands and is documented; a difference is fixed only
where ursus is wrong by its own rules or docs; and the public API and documented
behaviour are stable (step 494), so no behaviour changed but the one bug.

## 1. What

- **One bug fixed** (§3): an NDJSON value that is not an object, read into a Struct
  column under `WithNDJSONIgnoreErrors`, is a null Struct, where it was a Struct of
  nulls. In `internal/source/ndjson/build.go`; a CHANGELOG "Fixed" line.
- **Two docs corrected** (§4), wording only: `StrExpr.ToDecimal`'s and
  `RollingOptions.Offset`'s, each of which said Polars answers as ursus does, which
  Polars 2.1 does not. Their rules stand and are documented in §9.
- **Twenty-four differences documented** in `feature-gaps.md` §9 (§5), each in its
  area: what ursus does, what Polars 2.1 does, why ursus's answer stands, and the
  step. The twenty-two rules of the triage and the two corrected docs' rules.
- **Public docs** where a rule was stated nowhere public, one plain sentence each,
  no behaviour changed: `Otherwise` (the branches' meet), `Unpivot` (two units at the
  finer), `FillNullWith` (a fill past the type, text outside an Enum), `Mod` (a zero's
  sign), `Cast` (a Decimal and a Boolean), `Any` and `AllTrue` (a Decimal refused),
  `Var` and `Quantile` (the float's ends), `Skew` (shifted by the first), `Dt().Round`
  (Polars' month), `GroupByDynamic` (the grid is `Truncate`'s), `ScanCSV` (a column of
  only NaN), `WithDatetimeFormat` (an offset with seconds), `WriteCSV` (no columns) and
  `WithHiveDates` (each value by its own layout). `Combine`, `BusinessDayCount`,
  `TopK`, `Entropy`, `FillMean` and `AsOfNearest` stated theirs already.
- **The thirty-nine cases asserted again** (§2): twenty-eight recorded cases asked of
  Polars spelled so that it computes ursus's documented rule, two whose want is
  Python's own semantics, one as recorded (the bug's), six refusals in root tests, two
  held to math/big. None is asserted by typed overrides. Eight cases left the
  recordings: 2,590 cases in 106 groups, from 2,598.
- **No finding, no skip left:** `oracleFinding` is gone from `oracle_test.go`, whose
  header says how a difference is asserted now, and no `oracle_*_test.go` skips a
  case. §12 says no finding is open and where each went; `oracle-v2-scope.md` §4 says
  the rule from here.
- **Stale comments** citing a finding "left to the maintainer" now say where it went:
  `dtype_test.go`, `hive_test.go`, `castbool_test.go`, `decimalagg_test.go`,
  `dtcombine_test.go`, `enum_byhand_test.go`, `fill_test.go`, and `io_byhand_test.go`,
  whose Go case of a frame of no columns written as nothing went, the recording
  asserting it now.

## 2. The twenty-five

Class: **bug** (fixed), **doc corrected** (the doc said Polars answers so; the rule
kept and documented), **documented** (ursus's own rule, in §9). How asserted:
**spelled** (Polars asked so that it computes ursus's rule, said in the case's note),
**Python** (the want computed in the generator by Python's own semantics), **Go
oracle**, **refusal** (a root test, the kind by `errors.Is`, a phrase of the
message), **recorded** (Polars' answer as it is).

| # | finding | cases | class | the change | asserted |
| --- | --- | --- | --- | --- | --- |
| 1 | text, a Boolean, a Date or an instant beside a number | `dtype/when_string_int64`, `when_bool_int64`, `when_date_dt_us`, `when_dt_ns_int64` | documented | §9; `Otherwise`'s doc names the meet | refusal: `TestCondRefusals`, `ErrType`, "no common type: String and Int64" and its kin; the four cases left `dtype.py` |
| 2 | two time units meet at the finer | `dtype/when_dt_us_utc_dt_ns_utc`, `when_dt_ms_dt_ns`, `when_dur_ms_dur_ns`, `unpivot_dt_ms_dt_ns` | documented | §9; `Otherwise`'s and `Unpivot`'s docs | spelled: both sides cast to the finer before the `when` or the unpivot |
| 3 | two Enums meet at String | `dtype/when_enum_a_enum_b` | documented | §9 | spelled: both cast to String before the `when` |
| 4 | a weak fill past the type widens to Int64 | `fill/fill_past_the_type` | documented | §9; `FillNullWith`'s doc names Polars' Int16 | spelled: the column cast to Int64 before `fill_null(5000)` |
| 5 | a zero float remainder takes the divisor's sign | `scalar_byhand/float_mod_zero_sign` | documented | §9; `Mod`'s doc | Python: `x % y` per row, two rows added (−4 % 2, 0 % 2, +0 both) |
| 6 | `ToDecimal`'s tie | `castmore/to_decimal_ties` | doc corrected | §4, §9 | Python: `decimal`, `quantize(ROUND_HALF_UP)` at scales 2 and 0 |
| 7 | a Decimal cast to a Boolean refused | `castbool/decimal_to_bool` | documented | §9; `Cast`'s doc | refusal: `TestDecimalGuards`, `ErrType`, "cannot cast Decimal(10, 2) to Bool"; the case left `castbool.py` |
| 8 | rounding to the nearer month | `calendar/round_1mo`, `round_1mo_dates` | documented | §9; `Round`'s doc names Polars' month | spelled: `round_month`, the nearer of `truncate("1mo")` and a month after it, halfway up |
| 9 | a dynamic group's day where its midnight is missing | `calendar/midnight_gap_windows` | documented | §9; `GroupByDynamic`'s doc | spelled: a group-by of `truncate("1d")`, the count cast to Int64 on both sides |
| 10 | `Combine` reads the zone's wall clock | `dtcombine/combine_zoned_ny` | documented | §9 (its doc stated it) | spelled: `replace_time_zone(None)`, combine, the zone back |
| 11 | business days counted backwards over [end, start) | `components/business_day_count_reversed` | documented | §9 (its doc stated it) | spelled: `-business_day_count(e, s)` where the end is first |
| 12 | a rolling window's end under an offset | `rollingoffset/1mo_offset_m28d`, `1mo_offset_m4w` | doc corrected | §4, §9 | spelled: period 28d with offset −28d, and 4w with −4w, which end at the row in Polars 2.1 |
| 13 | the float range's ends held exact | `aggwindow_byhand/median_extremes`, `midpoint_huge`, `var_alike_huge` | documented | §9; `Quantile`'s and `Var`'s docs | Go oracle: `TestAggregatesAtTheFloatsEnds` (math/big) for the median and the midpoint, the two cases left the recording; spelled: the variance of `v - v.first()` |
| 14 | the skew of large close values held exact | `aggstats/skew_near_1e9`, `moments_far_2000` | documented | §9; `Skew`'s doc | spelled: the moments of `x - x.first()`, rel:1e-12; `TestSkewAndKurtosis` holds them to big.Float besides |
| 15 | a one-value group's raw entropy | `aggstats/entropy_raw_by_g` | documented | §9 (its doc stated it) | spelled: `-(x * x.log()).sum()` by group |
| 16 | `TopK` skips nulls | `topk_agg/top_k_ints`, `bottom_k_ints`, `top_k_decimals` | documented | §9 (its doc stated it) | spelled: `drop_nulls()` before `top_k`, an empty list made null, TopK's "null when none" |
| 17 | `Any` and `AllTrue` of a Decimal refused | `decimalagg/any_all` | documented | §9; `Any`'s doc | refusal: `TestDecimalGuards`, `ErrType`, "requires a Boolean operand, got Decimal(10, 2)", each; the case left `decimalagg.py` |
| 18 | `FillNullWith` outside an Enum's categories gives a String | `enum_byhand/fill_null_zzz` | documented | §9; `FillNullWith`'s doc | spelled: the Enum cast to String before `fill_null("zzz")` |
| 19 | the mean strategy widens to Float64 | `fill/fill_strategy_mean`, `frame_fill_mean` | documented | §9 (`FillMean`'s doc stated it) | spelled: the column cast to Float64 before the mean fill |
| 20 | `AsOfNearest` takes the backward row on a tie | `asofoptions/nearest_tie_gen` | documented | §9 (its doc stated it) | spelled: Polars' own backward and forward joins, the nearer taken and the backward on a tie, then the tolerance |
| 21 | a CSV column of only NaN stays String | `io_byhand/control_only_nan_inferred` | documented | §9; `ScanCSV`'s doc | spelled: Polars reads under a String override, which the Go side does not pass |
| 22 | an offset with seconds round-trips exactly through CSV | `io_byhand/i8_monrovia_1970_round_trips` | documented | §9; `WithDatetimeFormat`'s doc | spelled: Polars writes the instant converted to UTC, read back under the Monrovia type |
| 23 | an empty frame writes nothing to CSV | `io_byhand/i22_no_rows_no_columns_written` | documented | §9; `WriteCSV`'s doc | spelled: `write_csv(include_header=False)`, no header naming nothing |
| 24 | an NDJSON non-object in a Struct under `ignore_errors` | `ndjsonread/ignored_in_a_struct` | **bug** | §3: a null Struct | recorded: Polars 2.1's answer, unchanged |
| 25 | a hive key of mixed date layouts typed Date | `hive_infer/date_layouts_mixed` | documented | §9; `WithHiveDates`' doc | spelled: the key read as text, then each value by the first of the six layouts it parses under |

Twenty-eight spelled, two Python, two Go oracle, six refusals, one recorded: thirty-
nine. No case is asserted by an `oracleDifferences` override.

## 3. The bug

`internal/source/ndjson/build.go`, `structBuilder.decode`. A value that is not an
object, read into a Struct column under ignore_errors, was skipped and every field
given a null with the struct itself valid, "as one with no keys: a row of nulls, not a
null", Polars 1.44.1's reading. Every other builder refuses such a value through
`parser.refuse`, which under ignore_errors skips it and returns `errNull`, and
`decodeValue` gives the column a null. The struct's builder does the same now, a
null Struct:

```go
if p.s[p.pos] != '{' {
	// Under ignore_errors a value that is not an object is a null struct, as
	// every value its column cannot take is a null (step 507); a struct of
	// nulls, which Polars 1.44.1 read, would claim an object of no keys.
	return p.refuse(b.want)
}
```

**The docs read first.** `WithNDJSONIgnoreErrors`: "reads a value its column cannot
take as a null, as Polars' ignore_errors: text in an Int64 column, an element of a list
that does not fit its type, a Date column's "not a date"." The guide
(`docs/guide/io.md`) says the same, "reads a value its column cannot take as a null",
and `StructExpr`'s doc keeps a null struct and a struct of nulls apart ("A null struct
is not a struct of nulls"). No doc promised a struct of nulls; only the code's comment
said so. `ScanJSON`, which reads each object as an NDJSON line, follows. The recorded
case passes as Polars records it, `[{x: 1, y: "q"}, null, null, {x: null, y: "r"}]`.

## 4. The two docs corrected

`StrExpr.ToDecimal` (`expr_str.go`), before:

```
// ToDecimal parses a number into a Decimal(38, scale), as Polars' str.to_decimal
// (step 325): rounded half away from zero to the scale, an exponent read, 1e3 a
// thousand; text that is no number, or past 38 digits, is null.
```

after:

```
// ToDecimal parses a number into a Decimal(38, scale), as Polars' str.to_decimal
// (step 325): an exponent read, 1e3 a thousand; text that is no number, or past 38
// digits, is null. A tie is rounded half away from zero to the scale, 0.005 to 0.01,
// as Round rounds by default, where Polars 2.1 rounds it half to even, to 0.00.
```

`RollingOptions.Offset` (`groupby.go`), before:

```
	// A window whose Offset is as long as Period, behind its row, ends at the row
	// itself, as Polars' does, measuring a month as 28 days: so a period of "1mo"
	// with an offset of "-1mo" ends at March 31 for March 31, where February 29 plus
	// a month is March 29. Before step 313, Offset moved the window's end.
```

after:

```
	// A window whose Offset is as long as Period, behind its row, ends at the row
	// itself, a month measured as 28 days: so a period of "1mo" with an offset of
	// "-1mo" ends at March 31 for March 31, where February 29 plus a month is March
	// 29, and one with an offset of "-28d" or "-4w" ends at its row too. Polars 1.44.1
	// measured so; Polars 2.1 ends a window at its start plus the period unless the
	// offset is the period's own length, so "-28d" before "1mo" ends February 28's
	// window at February 29 there. Before step 313, Offset moved the window's end.
```

Polars 2.1 was asked before the wording was chosen: `rolling(period="1mo",
offset="-1mo")`, `("28d", "-28d")`, `("4w", "-28d")` and `("24h", "-1d")` end at the row
there too; `("1mo", "-28d")` and `("1mo", "-4w")` do not.

## 5. The §9 bullets

Twenty-four, each placed beside its kin: a conditional's meet by `Promote`; two units
at the finer; two Enums at String; a fill past the type; `FillNullWith` outside an
Enum; `ToDecimal`'s tie; a Decimal and a Boolean; `Any` and `AllTrue` of a Decimal; the
float's ends; `Skew` and `Kurtosis` shifted; the mean strategy; a zero remainder's
sign; `TopK`'s nulls; `Round` by a month; a dynamic group's missing midnight;
`Combine`'s wall clock; `BusinessDayCount` backwards; `Entropy` without normalize;
`AsOfNearest`'s tie; a hive key's date layouts; a CSV column of only NaN; an offset
with seconds through CSV; a frame of no columns written; a rolling window's end under
an offset. §1 gains the NDJSON row, fixed.

## 6. Teeth

| mutation | |
| --- | --- |
| the NDJSON fix reverted, `structBuilder.decode` as it was | **bites:** `ndjsonread/ignored_in_a_struct`: `row 1 differs: got: {"x": null, "y": null} want: null` |
| ursus's nearest tie sent forward, `preferBack`'s `<=` made `<` (`internal/physical/asof.go`) | **bites:** `asofoptions/nearest_tie_gen`, a spelled case: `row 63 differs: got: 41 want: 40` |

Both undone, each file put back from a copy saved before the mutation; `git diff`
shows `asof.go` unchanged and `build.go` as fixed.

## 7. Checks

All under `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`, after the last
edit to code:

- `make oracle` twice, every group: 106 of 106 files byte-identical (`md5sum -c`);
  only the seventeen groups this step changed differ from master, and within them only
  the thirty-nine cases (eight removed, thirty-one re-recorded or unchanged).
- `go test -run TestOracle .`: ok, 2,590 cases, none skipped, the thirty-one former
  findings among the passes.
- `go test ./internal/... ./dtype/...`: ok.
- `go test .` in full: ok, 294 s.
- `GOEXPERIMENT=simd go vet ./...`: clean.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and 0 gone
  since v0.7.2; nothing exported, only docs.
- `go run ./internal/gen/testinventory -check`: ok, 1,225 lines, 1,225 in the
  baseline, without `-write`.
- `gofmt -l .` (Go's own, `$(go env GOROOT)/bin/gofmt`): nothing.
