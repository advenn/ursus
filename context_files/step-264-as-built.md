# Step 264 — as built

**G2 of `v0.6.6-scope.md`: temporal ranges,** Polars' `date_range`,
`datetime_range` and `time_range`, and their per-row forms `DateRanges`,
`DatetimeRanges` and `TimeRanges`.

## 1. What changed

**Three call functions in the horizontal block,** one kernel (`temporalRanges`,
`internal/kernel/temporalrange.go`). The operands are start, end, the interval's
months, days and nanoseconds, and closed, the last four literals.

**Their typing (`rangeElem`):**

- **`date_range`:** a Date, from Dates or Datetimes. A Datetime end is cast, so read
  as its UTC date, as a cast reads it;
- **`datetime_range`:** the Datetime of the ends, a Date end taken as Datetime(us),
  two Dates Datetime(us), as Polars types them;
- **`time_range`:** a Time.

Ends of other types are refused with a hint to cast.

**The kernel:**

- the k-th value is start plus k intervals, not each from the one before, as Polars
  counts them: a month from January 31st is February 29th, then March 31st;
- calendar parts move the wall clock in the ends' zone, through `Interval.AddTo`, and
  the rest elapsed time, as `OffsetBy` moves them;
- a Date steps by whole days on its ticks, and so does every elapsed interval;
- closed says whether the ends belong;
- a negative interval gives an empty list, as Polars' does;
- a value the type cannot hold is past the end, which it holds, so the range stops
  there rather than refusing;
- an interval finer than the type holds is refused by `expr.OffsetRefusal`, the check
  `OffsetBy` uses.

**Public (`generate.go`):** the six functions, generic over the ends and over `Span`
for the interval, an `Interval` or a `time.Duration`, with an optional Closed. The
scalar forms explode as step 263's generators do. Refused while the query is planned:

- an interval of zero;
- a date range's interval that is not whole days, as Polars refuses it;
- a time range's calendar interval.

**Unlike Polars' signatures:**

- `datetime_range`'s `time_unit` and `time_zone` are spelled by casting the ends;
- `time_range`'s defaults, midnight to 23:59:59.999999 by the hour, are spelled out.

`expr.Generators` is exported, so the root package names a generator in its
messages.

## 2. Tests and teeth

**`TestTemporalRanges`:** 17 ranges at batch sizes 1 and 8192, every answer Polars
1.44.1's:

- **dates:** by days, months from the 31st, and weeks;
- **closed:** left and none;
- **empty:** a reversed range and a negative interval;
- **ends of other types:** dates from an instant;
- **instants:** by hours and by ninety minutes, from dates, and months at six;
- **in a zone:** America/New_York by a day across the change to daylight time (a day
  of the wall clock) and by an hour (elapsed);
- **a month and a day;**
- **times,** closed and open;
- and a range in `WithColumns`.

**`TestTemporalRangesAsLists`:** `DateRanges` a row, with a null start.

**`TestTemporalRangesRefuse`:** hours of dates, months of times, an interval of zero,
and ends of two types.

| tooth | result |
| --- | --- |
| each value from the one before | **bites** |
| the wall clock is read in UTC | **bites** |
| an open start is kept | **bites** |
| the end is always open | **bites** |
| a date steps a day whatever the interval | **bites** |
| dates take a finer interval | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
