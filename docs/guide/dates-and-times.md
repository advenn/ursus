---
description: >-
  Dates, times and time zones in ursus, the Go dataframe library: parsing, the parts
  of a date, calendar arithmetic, truncation, formatting, time zones, date ranges,
  dynamic groups by week or day, and rolling groups by time.
---

# Dates and times

| Type | Holds |
|---|---|
| `Date` | a calendar day |
| `Datetime(unit, zone)` | an instant, to the second, ms, us or ns, in an IANA zone or none |
| `Duration(unit)` | a length of time |
| `TimeOf(unit)` | a time of day |

A Go `time.Time` is a `Datetime(ns, "UTC")` in `Values`, and a literal anywhere an
expression goes. Methods for these types sit behind `Dt()`.

## Parsing

`Str().Strptime(type, format, strict)` parses text with a strftime format; not strict,
what does not parse is null:

--8<-- "Example_timeParse.md"

A CSV column is read as a date when the scan is told so:
`WithSchemaOverrides(map[string]ursus.DataType{"date": ursus.Date})`, as the guide's
`sales` does.

## The parts of a date

The examples here read three days:

--8<-- "func.days.md"

--8<-- "Example_timeParts.md"

`Weekday` is ISO's, Monday 1 to Sunday 7, and `Week` the ISO week: 2026's last day is
in its week 53. Datetimes add `Hour`, `Minute`, `Second` and the fractions, `Date` and
`Time`.

## Arithmetic

A date minus a date is a `Duration`; `TotalDays` and its kin turn it into a number.
`OffsetBy` moves by a calendar interval, so a month after January 31 is February 28,
not March 3:

--8<-- "Example_timeArithmetic.md"

`MonthStart` and `MonthEnd` move to a month's ends. `AddBusinessDays(n, roll)` counts
business days, Monday to Friday unless a `BusinessCalendar` gives other weekdays and
its holidays; `roll` says what a start on a day off does: `RollForward`,
`RollBackward`, or `RollRaise`, the default, to refuse it. `IsBusinessDay` tests one.

## Truncating and formatting

`Truncate(every)` moves each value to the start of its interval: a week starts on
Monday, as in Polars. `Round` moves it to the nearest. `Strftime(format)` formats it:

--8<-- "Example_timeTruncate.md"

## Time zones

A `Datetime` with a zone is an instant shown on that zone's clock.
`ConvertTimeZone` shows the same instant on another clock; `ReplaceTimeZone` keeps
the clock and changes the instant. New York's clocks moved forward an hour at 07:00
UTC on March 8, 2026, and the second row is on the other side of it:

--8<-- "Example_timeZones.md"

ursus reads zones from the system's zone database, as Go's `time` package does,
where Polars bundles its own. They agree but for a zone whose daylight time the
system's database writes in the rearguard form, as Ubuntu's does for Dublin.

## Ranges

`DateRange(start, end, every)` makes a date for each step, ends included, and
`DatetimeRange` and `TimeRange` the same for instants and times of day. `DateRanges`
makes a list a row from columns:

--8<-- "Example_timeRanges.md"

## Dynamic groups

`GroupByDynamic(index, options)` groups rows into windows of time cut from a sorted
index: per day, per week, per hour. `Every` is how far apart windows start, and
`Period` how long each is, `Every` when not given, so the windows tile the time.

--8<-- "Example_timeDynamic.md"

!!! note "Empty windows are rows, which Polars leaves out"

    ursus makes every window of the grid from the first instant to the last, so a day
    with no rows is a row with a count of 0: the weekend above. Polars'
    `group_by_dynamic` leaves those windows out. Filter `rows > 0` for Polars' answer.

`GroupBy` cuts windows within each key; the index column comes first, then the keys,
where Polars puts the keys first:

--8<-- "Example_timeDynamicWeeks.md"

`Offset` shifts the grid, `Closed` says which end of a window holds its instant,
`Label` what the index column shows, `StartBy` where the grid begins, and
`IncludeBoundaries` adds each window's bounds.

## Rolling groups

`Rolling(index, options)` gives every row its own window, reaching back `Period` from
its instant, and aggregates it: the rows and the high of the three days up to each
close.

--8<-- "Example_timeRolling.md"

A rolling group is a group-by with one row for each input row. The rolling windows by
time in [Window functions](windows.md#rolling-windows-by-time) answer the same
question as a column beside the rows.

## Under a memory limit

Dynamic and rolling groups stream their input and hand out each window when it
closes, keyed ones too, so a long series runs under a memory budget.
