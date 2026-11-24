# Step 543 — the calendar of a far instant is refused, where it was wrong

**Two silent wrong answers fixed by refusals, in v0.8.1 before its tag.** The
maintainer decided both on 2026-11-24: a refusal, not a null, and in v0.8.1. qcheck's
DuckDB run in #96 (step 539) found them. DuckDB's translation computes the calendar
in 128-bit integers, and 10 of its 21,000 random cases disagreed with ursus at
instants millions or billions of years from 1970.

## What was wrong

- **`Dt().Date()` wrapped its day.** `localDateTime` stored the day count as
  `int32(... / 86400)` unchecked. A Datetime(s) of -281474976710655, the year
  -8,917,617, is day -3,257,812,231, and it came back as another date. So did
  Datetime(ms) 288230376151711743, day 3,335,999,723. A Date holds int32 days, about
  5.9 million years each way.
- **Go's calendar ends before a Datetime(s) does.** The kernel reads every calendar
  part through `time.Time`, by design: this file's header calls Go's time package the
  source of truth. Go computes a date only from about the year -292,277,022,399. A
  Datetime(s) near int64's minimum, the year -292,277,022,657, is past it, and its
  Month answered another instant's. qcheck's case had Month 1 (27 January), and
  `Month() >= 3` kept the row.

## The fix

`calendarTime` stands before Go's calendar at every calendar part: the components
(Year to Nanosecond, Weekday, OrdinalDay, Quarter, Week…), DaysInMonth, IsLeapYear,
and `Date` and `Time`. It refuses with ErrValue, naming the row:
- an instant more than 10^12 days from the epoch, about 2.7 billion years, where
  Go's calendar is still exact;
- then one whose year is past Int32, the type Year answers in.

`Date` also refuses a day past int32.

Only a Datetime(s) reaches the year bound, and a Datetime(s) or a Datetime(ms) the
Date bound. The header's design stands: Go's time stays the source of truth, read
only where it is right.

**Not changed.** Other paths read Go's calendar too: calendar Truncate, OffsetBy,
MonthStart and MonthEnd, and Strftime. qcheck draws their inputs from bounded
columns, and they are left as they are.

## Tests

`internal/kernel/dtcalendar_test.go`. The oracle is Hinnant's civil_from_days over
math/big, which shares no code with `time.Time`.

- **`TestCalendarPartsOrRefused`** checks a Datetime(s) at int64's ends, both sides
  of the 10^12-day bound, both sides of int32's day bound, the year-1 and year-9999
  edges, the epoch's neighbours and the instants qcheck found. For each it runs Year,
  Month, Day, Weekday, OrdinalDay and Date. Each answers the oracle's value or is
  refused with ErrValue exactly where the oracle puts it outside.
- **`TestDateOfMillisecondsPastInt32`** holds a Datetime(ms) to the Date bound.

## Docs

- `DtExpr`'s godoc states the calendar's range, and `Date`'s states its bound.
- v0.8.1's CHANGELOG lists both in "What a v0.8.0 user can trip on" and under Fixed.

## Release

v0.8.1 is tagged on this pull request's merge commit, on the maintainer's word.

## Checks

Run on CI only: the maintainer was away and asked that the laptop be kept quiet.
