# Step 145 — as built

**Item 7 of `v0.5-scope.md`: the calendar.** `OffsetBy`, `Round`, `MonthStart`,
`MonthEnd`, `IsLeapYear` and `ConvertTimeZone`, all Polars' names.

**And a defect found first.** Where a zone's midnight does not exist, `Truncate` and
`GroupByDynamic` started that day at 23:00 the day before.

## 1. Evidence first

The new calls were to be built on `dtype.Interval.AddTo` and `TruncateTo`. Both read
a wall clock back into the column's zone with `time.Date`. Where that wall clock
does not exist, `time.Date` may pick either side of the gap; it picks the side before.

A probe truncated an instant on three days that begin at 01:00, by `Every("1d")`:

| zone | instant | floored to | right answer |
| --- | --- | --- | --- |
| America/Santiago | 2024-09-08T10:00-03:00 | **2024-09-07T23:00-04:00** | 2024-09-08T01:00-03:00 |
| America/Havana | 2024-03-10T10:00-04:00 | **2024-03-09T23:00-05:00** | 2024-03-10T01:00-04:00 |
| Asia/Beirut | 2024-03-31T10:00+03:00 | 2024-03-31T01:00+03:00 | the same |

Two of the three land a whole day early. `GroupByDynamic`'s day windows did the same,
because it builds them from `TruncateTo` and `AddTo`. A row at 23:30 on 2024-09-07
went into the window labelled with the 8th.

The sub-day path had already been fixed, at step 76. It reads
the wall clock back by its own rule: at the input's offset when that holds, else at
the offset in force; in a gap, at the instant the gap ends. Days and months did not
use it.

## 2. What changed

### One reading of a wall clock (`dtype/interval.go`)

**`wallTime(ws, ns, loc, prefer)`** is that rule, taken out of `TruncateTo`'s sub-day
branch and used everywhere a wall clock is read back:

- **in a fold,** the reading at offset `prefer`, the input's own, when that offset
  holds there, and otherwise the one at the offset in force;
- **in a gap,** the instant the gap ends.

Both are Polars' answers.

**`TruncateTo`** finds the floored wall clock (`floorWall`), then reads it back with
`wallTime`, at every size.

**`AddTo`** advances the date in UTC, where every day is a day: months with their
clamp, then days. It reads the result back once, with `wallTime`. Nanoseconds are
added to the instant afterwards, as before.

**`addMonths`** works on a date and returns one. The clamp is unchanged.

### The new calls

| call | what it does | type | refused at plan time |
| --- | --- | --- | --- |
| `OffsetBy(Interval or Duration)` | `AddTo`: months and days on the wall clock, nanoseconds in elapsed time | the input's | a Time; an offset finer than the unit (an hour on a Date, a nanosecond on `Datetime(us)`) |
| `Round(Interval or Duration)` | the nearer end of `Truncate`'s window, halfway up | the input's | a Time; a zero or negative interval |
| `MonthStart()`, `MonthEnd()` | the first or last day of the month, the time of day kept | the input's | a Time |
| `IsLeapYear()` | the year has 366 days | Bool | a Time |
| `ConvertTimeZone(tz)` | the same ticks, labelled with another zone | `Datetime(unit, tz)` | a naive Datetime; an unknown zone; no zone |

**Each reads its column in the column's own zone.** 2024-01-31T20:00Z is February in
Tokyo, so its `MonthEnd` there is the 29th.

**`Round`'s "nearer" is elapsed time:**

- **A sub-day grid** rounds `t` plus half the interval down. Across a fall-back fold,
  01:30 EDT rounds by the hour to 01:00 EST, the half hour the clock reaches next.
  A first version took the next boundary from the wall clock instead, which put
  02:00 EST two hours after the window's start.
- **A day or a month** has no fixed half, so both ends of its window are found on the
  calendar and compared:
  - February 2024's middle is the 15th at noon;
  - a 23-hour day's middle is 11:30 after it starts.
- **A Time is refused.** It can round up to 24:00, which it cannot hold, and Polars
  refuses it too.

**Refusals are asked once.** `truncateOut`'s interval checks moved into
`expr.GridRefusal`, which the kernel also asks; until now each held its own copy.
`expr.OffsetRefusal` is the same for `OffsetBy`.

**`TestTruncateRefusalsAgree` still runs.** It now drives `Round` as well.

**Range.** An answer the unit cannot hold is the data's error, as `Truncate`'s is.
Examples: `OffsetBy` past 2262 at `Datetime(ns)`, or the first of 1677-09.

**A Date past an int32 of days was a silent wrap waiting to happen.** `FromTime`
returns an int64, and `packTicks` stores a Date as int32. The new
`fromTime`/`fitsTicks` refuse it; `Truncate` now uses them too.

**Fallible.** `OffsetBy`, `Round`, `MonthStart` and `MonthEnd` joined
`plan/fallible.go`'s list, so predicate pushdown does not move them past a guard.

`IsLeapYear` cannot fail. `ConvertTimeZone` changes no value.

**`date + 1mo` as arithmetic stays out,** as the scope decided: an Interval is not an
operand.

## 3. Tests

**`calendar_test.go`.**

- **The by-hand tables:**
  - `TestOffsetByByHand`:
    - clamping, forward and back, and across years;
    - a Date, and a naive Datetime;
    - a day across spring forward keeps 09:00, where an hour is elapsed;
    - a day into a fold, from each side;
    - a day and a month into a gap;
    - four refusals.
  - `TestMonthStartAndEndByHand`: leap years, 1900 and 2000, the zone's month, back
    across spring forward, and into Asunción's missing 00:30.
  - `TestRoundByHand`: halfway up, before the epoch, February's and January's
    middles, a 23-hour day, Kolkata's local hour against the epoch's, a fold, and
    three refusals.
  - `TestIsLeapYear` and `TestConvertTimeZone`, which also shows that the instants do
    not move and that `Hour` follows the new zone.
- **`TestADayStartsWhereItsMidnightDoesNotExist`** is the defect:
  - Santiago's and Havana's days;
  - Asunción's October 2023 by the month;
  - `GroupByDynamic`'s windows, with the 23:30 row counted in the 7th.
- **`TestCalendarAgainstTheOracle`** checks fifteen operations at the instants within
  a day of every 2024 transition of seven zones, and around every month's end:
  16,380 answers.
  - **The zones:** UTC, New York, Santiago, Havana, Asunción, Lord Howe's 30-minute
    shift and Kolkata.
  - **The oracle, `readWallOracle`,** tries every offset a zone uses within a day and
    a half of a wall clock. It keeps the offsets under which that wall clock is a real
    reading: with none it finds the gap's end, and with two it picks by the preferred
    offset.
  - **Sub-day rounding** is checked against `gridFloorOracle` from the wall-clock
    truncate test.
- **`TestOffsetByPastTheRangeIsRefused`** covers 2262 at `Datetime(ns)` and a Date
  past 2^31 days.
- **`TestACalendarRefusalIsNotPushedPastAJoin`:** an inner join removes the rows
  each call would refuse, and the filter after it must not be pushed into the left
  side.
- **The evaluator contract** gains argument sets for `OffsetBy`, `Round` and
  `ConvertTimeZone`, the refusing ones included.

## 4. Teeth

| tooth | result |
| --- | --- |
| a gap reads as the side before it | **bites:** the midnight test, the oracle, both by-hand tables |
| a fold ignores the input's own offset | **bites:** the oracle, the fold cases, `TestTruncateFloorsTheWallClock` |
| `AddTo` resolves its wall clock with `time.Date` | **bites:** the midnight test, the oracle, the gap and fold cases |
| months do not clamp | **bites:** the oracle, three by-hand cases |
| a calendar round's halfway goes down | **bites:** the oracle, the month and 23-hour-day cases |
| a sub-day calendar round is a floor | **bites:** the oracle, Kolkata, the fold |
| a tick round's halfway goes down | **bites:** fifteen minutes, before the epoch |
| `MonthEnd` moves to the start | **bites:** the oracle, the by-hand table, the range test |
| `IsLeapYear` reads the year in UTC | **bites:** the Tokyo case |
| a Date past an int32 of days wraps | **bites:** the range test |
| an offset finer than the unit is accepted | **bites:** two refusal cases |
| `ConvertTimeZone` accepts a naive Datetime | **bites:** its refusal case, the contract |
| `ConvertTimeZone` keeps the old label | **bites:** its test, the contract |
| `OffsetBy` is not fallible | **bites:** the join test |
| `Round` accepts a Time | **bites:** its refusal case |

**The last one bit only the by-hand refusal, and that is the shared rule working.**
Plan time and the kernel ask the same `GridRefusal`, so they accept together, and the
agreement test sees nothing to disagree about.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. No query PDS-H runs uses a calendar call, so its answers were not rerun.
