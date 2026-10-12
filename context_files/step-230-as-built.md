# Step 230 — as built

**F6 of `v0.6.3-scope.md`, the temporal half: `Dt().DaysInMonth()` and
`Dt().Combine(t)`.**

## 1. Why

Polars' `dt.days_in_month` and `dt.combine` had no ursus spelling. The second
takes a Time column, so like the list set operations it is a call of the
horizontal family.

## 2. What changed

- **`dt.days_in_month`** (`FnDtDaysInMonth`, the dt family):
  - an Int8, as Polars';
  - the month read in the column's own zone, beside `is_leap_year` in
    `dtfn.go`.
- **`dt.combine`** (`FnDtCombine`, the horizontal family). Its operands are a
  Date or Datetime and a Time, a column or a literal. It answers a Datetime: a
  Date's in microseconds, a Datetime's in its own unit and zone. `dtcombine.go`:
  - a zoned column is read as its wall clocks (`replaceTimeZone` to naive);
  - the day is floored on the wall clock, and the time of day added, truncated
    to the unit;
  - the result is put back in the zone with both policies "raise", so a wall clock
    the zone skips or repeats fails the query, as Polars' does;
  - a day or a sum past the unit's range is refused;
  - it is fallible (`plan/fallible.go`).
- **`expr_dt.go`:** the two methods.

**One difference from Polars:** a Datetime keeps its unit, where Polars answers
microseconds unless given `time_unit`. A Cast gives another unit.

## 3. Tests and teeth

- **`TestDaysInMonthAndCombineAnswerAsPolars`:**
  - Polars 1.44.1's answers for February in a leap year and not, a null, a Date
    and a Datetime;
  - Combine of each with a Time, a null on either side, a millisecond Datetime;
  - in New York:
    - 22:00 on 2024-03-11, the 12th in UTC, combined with 13:15 is 13:15 on the
      11th;
    - February 29 at 21:00, March 1 in UTC, has 29 days in its month;
    - a skipped 02:30 is refused;
  - 23:59 on 2262-04-11 in nanoseconds is refused as out of range;
  - a Time receiver and an Int64 time are refused.
- **`TestACalendarRefusalIsNotPushedPastAJoin`** adds Combine.

| tooth | result |
| --- | --- |
| the previous month's length | **bites** |
| days_in_month read in UTC | **bites:** the zoned case, added for it |
| a Datetime's time of day kept | **bites** |
| a zoned day read in UTC | **bites:** the 22:00 case, moved off a morning so the two days differ |
| the time of day not converted to the unit | **bites** |
| a skipped wall clock made null | **bites** |
| combine not fallible | **bites:** the join test |
| the overflow check gone | **bites:** the 2262 case, added for it when the tooth was first silent |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. The test file was left
unformatted by one indentation when the gate began; gofmt fixed it after, and vet
and its tests were run again.
