# Step 498 — as built

**The temporal wave converted to recorded oracles.** Wave 498 of
[`oracle-v2-scope.md`](./oracle-v2-scope.md) §3, in step 496's framework
([`step-496-as-built.md`](./step-496-as-built.md)): thirteen files' hand-typed expected
values are Polars 2.1.0's answers now, recorded in thirteen groups of `testdata/oracle`
by `bench/scripts/oracle/<group>.py` and spelled in `oracle_<group>_test.go`. What a
file keeps asserts a refusal, a type, a plan or a Go-API shape, or holds to Go's time
package what Polars 2.1 has no answer for.

## 1. What

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `calendar_test.go` (the by-hand half) | 632 | 420 | 26 | 0 | 30 | 7 |
| `tzreplace_test.go` | 163 | 63 | 20 | 0 | 19 | 3 |
| `components_test.go` | 139 | 39 | 26 | 0 | 28 | 4 |
| `daterange_test.go` | 128 | 73 | 22 | 0 | 22 | 3 |
| `csvdates_test.go` | 171 | 113 | 60 | 0 | 62 | 5 |
| `strtemporal_test.go` | 84 | 68 | 17 | 0 | 21 | 6 |
| `temporalagg_test.go` | 232 | 101 | 19 | 0 | 25 | 7 |
| `weeks_test.go` | 71 | 22 | 12 | 0 | 11 | 3 |
| `dtcombine_test.go` | 111 | 89 | 7 | 0 | 7 | 1 |
| `dtexact_test.go` | 132 | 131 | 5 | 4 | 9 | 3 |
| `timearith_test.go` | 187 | 161 | 6 | 0 | 2 | 1 |
| `businessdays_range_test.go` | 47 | 39 | 1 | 0 | 4 | 1 |
| `interpby_test.go` | 205 | 78 | 27 | 0 | 20 | 4 |
| | 2,302 | 1,397 | 248 | 4 | 260 | 48 |

260 cases in thirteen groups, 48 generated (`Gen`, seeds 4981 to 4993, 60 to 200 rows,
nulls, the type's edges and each zone's 2024 transitions), no refusals recorded; the
JSON files are 19 KB to 210 KB. `dtexact_test.go`'s four lines that stay are the
quotients of a three-century span in seconds by a day's, an hour's and a minute's,
`Duration(s)` being a type Polars has not; its other unit, and the finer units of
every question, are recorded.

**Zones.** Every recorded zone has one form in the zone database, so Ubuntu's rearguard
CI and Debian read it alike: UTC, New York, Tokyo, Kolkata, Lord Howe, Paris, London,
Apia, Santiago, Havana and Asunción; Dublin and Casablanca are nowhere in a recording.

**The loader** (`oracle_test.go`) grew two unexported things: a case's `params`, an
input that is not a frame (`in.text("csv")`, the CSV text the csvdates group scans),
and an `oracleDifference`'s `names`, the recorded column name to ursus's, for the two
documented naming differences; and `oracleFinding(what)`, a registry entry that skips
a case recorded and not asserted, naming the finding.

## 2. Differences named

In `oracleDifferences`, each documented in `feature-gaps.md` §9 or §11 (step 487 §3):

- `calendar/convert_tokyo` and `calendar/convert_gen`: `dt.hour` is an Int32, Polars' an
  Int8 (every component is an Int32 here; the same entry as `expressions/convert_tokyo_hour`).
- `daterange/range_beside_columns`: `dt.day` is an Int32, Polars' an Int8.
- `components/datetime`: `MakeDatetime` of columns is named `datetime`; Polars 2 names
  it after its first input, `y` (§11, step 487 §3).
- `components/duration_float_hours` and `components/duration_of_columns`: a duration of
  columns is named `duration`; Polars names it `literal` (§9, step 265).

Spelled around in the query, as the numodds group did: an integer's `Sum` is an
Int128 here, widened not to wrap (`Sum`'s doc, `internal/expr.ResolveAggBinding`), so
the weeks group's `GroupByDynamic` sums are cast to Int64, the question being the
windows. A query of literals alone is one row in Polars and a row for each of the
frame's rows here (§9), so the components group's literal-only cases take a frame of
one row. A range of literal ends is named `literal` by Polars and after its generator
here, so each is aliased `r` on both sides. ursus's lenient `ToDate`, `ToDatetime` and
`ToTime` (§9) are asked of Polars with `strict=False`, and a wall clock read twice
with `ambiguous="null"` besides.

## 3. What stays, and why

- **Refusals**, as they were, gathered where a table's values left: `TestCalendarRefusals`
  (13), `TestReplaceTimeZoneRefuses` (4), `TestValuesFromComponentsRefuse`,
  `TestTemporalRangesRefuse`, `TestCombineRefuses`, `TestInterpolateBy`'s three,
  `TestAddBusinessDaysPastTheRangeIsRefused`, `TestOffsetByPastTheRangeIsRefused`,
  `TestReplaceTimeZoneRaisesByDefault`, `TestTemporalVarAndStdStillRefuse`, a late CSV
  value that does not parse, and the strict `Strptime` refusals.
- **Go's time package as the oracle** where Polars 2.1 has no answer:
  - a Time plus or minus a Duration, which Polars refuses ("failed to determine
    supertype of time and duration"): `TestTimeArithmeticIsTheClocks` holds the wrap at
    midnight, at every unit, 106,751 days included, to the time of day Go's calendar
    lands on; `TestTimePrintsSortsAndComparesConsistently` compares the rolled-over
    clock with the genuine one; `TestTimeDifferenceStaysASignedDuration` subtracts two
    clocks in Go. A Time minus a Time Polars answers, recorded at nanoseconds, its
    only Time unit;
  - `Round` by a `time.Duration` on a zoned column, the epoch's grid, which Polars'
    `round` has no spelling for: `TestRoundByADurationIsTheEpochsGrid`, three zones,
    three steps, an instant before 1970;
  - two values of two patterns in one CSV or String column, which Polars refuses,
    reading a column by its first value's pattern, where ursus reads each by its own
    (§9): `goParsedTicks`, Go's `time.Parse` with each value's layout, in
    `TestCSVTryParseDates` and `TestStrToTemporalByPatterns`;
  - the day a zoned Datetime combines, read on its own wall clock (Combine's doc):
    `TestCombineReadsTheDayOnTheWallClock`, four wall clocks, against `time.Date` in
    New York; Polars 2.1 reads the day in UTC (Found, below);
  - `Datetime(s)` and `Duration(s)`, types Polars has not: `dtexact_test.go`'s epochs
    and totals in seconds, unchanged, the ninety-minute floor against `time.Duration`.
- **Differentials:** `TestTemporalRangesAgreeAcrossBatchSizes` (a range under a batch
  of one row and of 8192), `TestInterpolateByStreamsPastItsLimit`,
  `TestCalendarAgainstTheOracle` (unchanged, a Go oracle already).
- **Go-API and structure:** `TestFarInstantsAsGoValues` (`Rows`, `ToMaps` and `At` of a
  year a nanosecond count does not reach, against `time.Date`), `TestParquetUUIDAndJSONRead`
  (a file written by Go), `TestWeeksAndDaysRenderApart` (`Every`'s rendering),
  `TestACalendarRefusalIsNotPushedPastAJoin` (a plan).
- **Dropped as covered by the recordings' dtypes:** `ConvertTimeZone`'s type check,
  `ReplaceTimeZone`'s unit check, the quantiles' schema check, `Combine`'s New York
  type check (held in the Go-oracle test instead).
- **Helpers gone:** `calRender`, `inUTC`, `agg1`, interpby's `run` and `same`, the
  package-level `ny`; `calCase` keeps `refuse` alone.

## 4. Found

Each recorded, skipped in the registry with `oracleFinding`, and the maintainer's to
decide; none is documented in `feature-gaps.md` §9 or §11, so none is named a
difference and no recording was changed.

- **`dt.round("1mo")`: Polars 2.1 rounds a month by adding fourteen days and
  truncating** (`calendar/round_1mo`, `calendar/round_1mo_dates`). A sweep over
  January and February: the 18th of January rounds up and the 17th down, the 16th of
  a 29-day February up and the 15th down, the 15th of a 28-day one up; every threshold
  is fourteen days before the next month. ursus rounds to the nearer month boundary
  in elapsed time, a value halfway up (Round's doc, `dtype/interval.go`), so noon on
  2024-02-15 is March here and February there, and 2024-01-17 February here and
  January there. Polars' own docstring says half-way points map to the start of the
  bucket, which neither its month rule nor its "15m" (10:07:30 rounds up, as here)
  follows. Not a bug here; a choice of rule.
- **`group_by_dynamic` across a midnight that does not exist** (`calendar/midnight_gap_windows`):
  over Santiago's 2024-09-08, which begins at 01:00, Polars 2.1 starts the day's window
  at 23:00 on the 7th and puts 23:30 on the 7th in it, where its own `truncate("1d")`
  of the same rows answers 01:00 on the 8th, as ursus's `Truncate` and `GroupByDynamic`
  both do since step 145 (the Truncate cases for Santiago, Havana and Asunción are
  recorded and agree).
- **`dt.combine` of a zoned Datetime** (`dtcombine/combine_zoned_ny`): 22:00 on the 11th
  of March in New York, 02:00 on the 12th in UTC, with 13:15 is the 12th in Polars
  2.1, the day read in UTC, and the 11th here, the day on the zone's wall clock
  (Combine's doc). `days_in_month` of the same column reads the wall clock in both.
- **`business_day_count` with the end before the start** (`components/business_day_count_reversed`):
  Polars counts minus the business days in (end, start], numpy's `busday_count` rule,
  so a Monday back to the Saturday before is −1; ursus counts those in [end, start),
  0 there (BusinessDayCount's doc: "minus the business days from end up to start").
  The two agree where both ends are business days or neither is, which the old test's
  one reversed pair was (−7); the generated case was found disagreeing by one on the
  rows whose ends straddle a weekend, and asks the agreed half, end on or after start,
  so that it asserts; the reversed pairs are the finding's case.

Found and known: `Sum` of an Int64 is an Int128 (§2, cast in the spelling).

## 5. Checks

`make oracle` recorded twice to the same bytes (`md5sum -c`, 13 of 13). `go test -run
TestOracle .`: 260 cases pass or skip as named. `go test .` in full, `go vet ./...`,
`go run ./internal/gen/api -check` (0 gone; nothing exported), `gofmt -l .` empty, all
under `GOEXPERIMENT=simd GOAMD64=v3`. `TestEveryCitedTestExists` passes: no Go comment
cites a removed name (the renamed and gathered tests were cited in step records alone).
