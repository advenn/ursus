# Step 544 — a far naive instant's calendar computed, as approved

**Step 543 made right, in v0.8.1 before its tag.** On 2026-11-24 the maintainer
approved a fix described as "calendar parts computed with ursus's own overflow-free
arithmetic instead of Go's time near int64's ends". Step 543 refused every calendar
part past Go's calendar instead.

#96's qcheck run then found the difference. ursus refused `Dt().Minute()` of
Datetime(s) 9223372036854775807, whose minute is plain arithmetic, while DuckDB
answered.

## Now

- **Within 10^12 days of the epoch**, Go's time is the source of truth, as the
  kernel's header says, unchanged.
- **Past 10^12 days**, which only a Datetime(s) reaches, `calendarAt` computes a
  naive instant's date. It uses Hinnant's civil_from_days in int64 (`civilOf`): exact
  for every day an int64 of seconds reaches. Its time of day is the tick remainder.
  From these come Year, Month, Day, the time of day, Weekday, OrdinalDay, Quarter,
  DaysInMonth, IsLeapYear and `Time`.
- **Refused with ErrValue**, naming the row:
  - `Year` past Int32, its type;
  - `Date` past int32 days (step 543);
  - past 10^12 days, an instant in a zone other than UTC (its zone's rules there are
    no one's wall clock) and the ISO week. UTC's wall clock is the instant's own, so
    a UTC instant is read as a naive one.

## Tests

`internal/kernel/dtcalendar_test.go`, against Hinnant over math/big:
- **`TestCalendarPartsOrRefused`** checks a Datetime(s) at int64's ends and both
  sides of each bound. For each it runs Year, Month, Day, Weekday, OrdinalDay, Hour,
  Minute, Second, Date and Time. Each part answers the oracle's value, or is refused
  exactly where the value does not fit its type.
- **`TestFarZonedInstantRefused`** holds a far instant in Asia/Kolkata to its refusal,
  and a UTC one to the naive one's answer. Its first version expected UTC to refuse
  too, and CI failed it: `locationOf` reads UTC as no zone, rightly.

## Docs

`DtExpr`'s godoc, and v0.8.1's CHANGELOG, "What a v0.8.0 user can trip on" and
Fixed.

## Release

v0.8.1 is tagged on this pull request's merge commit, on the maintainer's word.

## Checks

On CI only: the maintainer is away and asked that the laptop be kept quiet.
