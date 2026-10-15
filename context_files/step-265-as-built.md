# Step 265 — as built

**G3 of `v0.6.6-scope.md`: values from their components.** These are Polars'
`pl.date`, `pl.datetime`, `pl.time`, `pl.duration`, `pl.from_epoch` and
`pl.business_day_count`. `Date`, `Datetime` and `Duration` already name the types,
so the first four are `MakeDate`, `MakeDatetime`, `MakeTime` and `MakeDuration`.

## 1. What changed

**Six call functions in the horizontal block** (`internal/kernel/components.go`).
Each operand is a column or a literal, and a null one gives null.

**`date`, `datetime` and `time`** (`makeTemporal`):

- integer components only;
- they give a Date, a naive Datetime(us) and a Time(ns), as Polars types them;
- `civil` refuses components that name no date or time, which `time.Date` would
  normalise: February 30th, a month 13, an hour 25. The refusal names the components
  and the row, "date's components (2023, 2, 29) in row 0 name no date", as Polars
  refuses them.

**`duration_ticks`,** a duration's ticks as Int64:

- the root package casts them to the Duration, as an integer casts to a temporal type
  tick for tick;
- integer parts sum exactly, a unit's coarser parts divided before the sum and its
  finer ones after, and a total past int64 is refused, where Polars wraps;
- with a float part the total is summed in float64 and truncated toward zero, as
  Polars sums it: 0.6µs and 0.0004ms make one microsecond.

**`epoch_ticks`:** a count times a factor. An integer count multiplies exactly, and
one past int64 is refused. A float count is truncated toward zero, as Polars
truncates it.

**`business_day_count`:**

- Date ends, the week mask an Int64 literal, and the holidays a List(Int64) literal
  of days: step 257's List literals carry them;
- `BusinessDays` counts [start, end) by whole weeks and a remainder, less the
  holidays on a business day, by binary search;
- an end before the start counts minus [end, start), as Polars counts;
- it is exported, for `.dt`'s business-day functions in D.

**Public (`components.go`):**

- `MakeDate`, `MakeDatetime` and `MakeTime` take every component as an Operand, a
  constant or an expression. Polars' defaults for the omitted ones are spelled out,
  and the columns are named `date`, `datetime` and `time`;
- **`MakeDatetime`'s zone** is `Dt().ReplaceTimeZone`, whose options are Polars'
  `ambiguous`, and its unit is a cast;
- **`MakeDuration(DurationParts{...}, unit...)`:** each part is nil, a Go number or an
  Expr. The unit is microseconds, or nanoseconds when the Nanoseconds part is set, as
  Polars chooses it. The column is named `duration`, where Polars names a duration of
  columns `literal`;
- **`FromEpoch(e, unit)`:** seconds, milliseconds and microseconds give Datetime(us),
  nanoseconds Datetime(ns). Days since the epoch are `e.Cast(Date)`;
- **`BusinessDayCount(start, end, cal...)`:** a `BusinessCalendar` gives the week
  mask, Monday first, and the holidays. An all-false mask is Polars' default, Monday
  to Friday.

**The typing checks each call's operand count.** The gate's first run failed
`internal/physical`'s `TestEvaluatorContract`, which calls every function with an
operand of each type. Typing that took any number of integers promised a Date that
evaluation of one operand refused. The ranges of steps 263 and 264 checked theirs
already.

**Differences from Polars,** in `feature-gaps.md` §9:

- a total or a count past int64 is refused, where Polars wraps;
- a duration of columns is named `duration`.

## 2. Tests and teeth

**`TestValuesFromComponents`:** 19 expressions over a frame with nulls, and a zone,
every answer Polars 1.44.1's:

- dates, one from an Int8 month;
- datetimes and times;
- durations of every part, in nanoseconds and milliseconds, of float hours, of
  columns, negative, with fractions summed and then truncated, truncated toward zero,
  and of no part;
- epochs of seconds, milliseconds, microseconds and nanoseconds, and of float seconds
  truncated toward zero;
- a datetime in New York, and one the clock reads twice taken the earlier.

**`TestBusinessDayCount`:** Polars' answers for Monday to Friday, Monday to Saturday,
and with holidays, backwards counts and a null.

**`TestValuesFromComponentsRefuse`:**

- February 29th 2023, a month 13, an hour 25 in a datetime and in a time;
- a float component and a string part;
- instants where business days take Dates;
- an unknown epoch unit.

| tooth | result |
| --- | --- |
| a normalised date is taken | **bites** |
| float parts truncate one by one | **bites** |
| a float epoch is floored | **bites** |
| a weekday is off by one | **bites** |
| a backward count is positive | **bites** |
| holidays are not taken off | **bites** |
| milliseconds are microseconds | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
