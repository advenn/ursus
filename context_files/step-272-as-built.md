# Step 272 — as built

**D of `v0.6.6-scope.md`: the temporal namespace's missing functions.** These are
Polars' `iso_year`, `century`, `millennium`, `is_business_day`, `add_business_days`,
`replace`, `base_utc_offset`, `dst_offset`, `with_time_unit`, the totals in
milliseconds, microseconds and nanoseconds, `fractional` on every total and on
`second`, and `epoch` and `timestamp` in a unit.

On the way, the evaluator contract was found to stop short of two blocks of
functions. The fix widened its sweep and two tests' fixtures, and closed the one gap
it found.

## 1. What changed

**The calendar** (`internal/kernel/dtmore.go`):

- **`IsoYear`:** the year of a day's ISO week, so 2024-12-30 is 2025.
- **`Century` and `Millennium`:** `floor((y + 99) / 100)` and
  `floor((y + 999) / 1000)`, which agree with Polars' at 24 years around year 0. Year
  0 is in neither, and the centuries before it count down from −1.
- **Inputs and output:** each takes a Date or a Datetime, read on its zone's wall
  clock, and gives an Int32. A Time is refused.

**The zone's offsets:**

- **`BaseUtcOffset` and `DstOffset`:** a zoned Datetime's offsets, each a
  Duration(ms) whatever the value's unit, as Polars gives them.
- **Go's limit:** Go's zones say only whether a period is daylight-saving time, not
  its base offset.
- **The rule:** a daylight-saving period takes its base from the nearest earlier
  period that is not daylight-saving time, else the next.
- **Checked:** this agrees with Polars for:
  - New York, Lord Howe's half hour and Kolkata;
  - Dublin, whose winter is −1h of daylight-saving time in the zone data;
  - Casablanca's Ramadan, also −1h;
  - London;
  - São Paulo in 2018 and Moscow in 2013, base +4h.
- **Speed:** the offsets are found once per period of the zone.
- **Refused:** a naive Datetime.

**Units, totals and epochs:**

- **`WithTimeUnit`:** reads the same integers in another unit, keeping a Datetime's
  zone, as Polars reinterprets them. Cast converts instead.
- **`TotalMilliseconds`, `TotalMicroseconds` and `TotalNanoseconds`:** truncate toward
  zero, as the coarser totals do. A total that needs a multiply, nanoseconds of
  milliseconds, refuses past an Int64.
- **The seven `…Fractional` totals and `SecondFractional`:** Float64.
- **Polars' float formula:** it divides by a constant by multiplying by the
  reciprocal. So a fractional total is `float64(ticks) * (1 / ticksPerUnit)`, and the
  fractional second is `second + nanosecond * (1/1e9)`.
- **Checked to the bit:** 50 of 50 values agree with Polars across five units, where
  plain division misses 2 to 3 of every 10.
- **FMA:** the second's product is converted before the add, so GOAMD64=v3 cannot fuse
  it into an FMA.
- **`EpochIn(unit)` and `EpochDays()`:** Polars' `epoch(time_unit)`, floored, so a
  microsecond before 1970 is −1 in every coarser unit. Days are an Int32.
- **`Timestamp(unit)`:** `EpochIn` in milliseconds, microseconds or nanoseconds; a
  second is refused, as Polars refuses it.
- **`Epoch()`:** whole seconds, as before.

**Business days** (`internal/kernel/dtreplace.go`), on step 265's `BusinessCalendar`:

- **`IsBusinessDay(cal...)`:** whether each value's local date is a business day.
- **`AddBusinessDays(n, roll, cal...)`:**
  - **`n`:** a constant or an expression of any integer type. Polars takes only
    32- and 64-bit integers.
  - **A day that is not a business day** is refused with `RollRaise`, or moved to the
    next or the last business day first, as Polars rolls it.
  - **A Datetime** keeps its time of day. A zoned one moves on its wall clock, and a
    wall clock its zone reads twice or never is refused, as Polars refuses it.
- **`BusinessDays.Add`:** jumps whole weeks, less the holidays in them, so a thousand
  business days take a few steps; then single days.
- **Horizontal calls:** both use the calendar operands of `business_day_count`.

**`Replace(DateTimeParts, opts...)`:**

- **Parts:** each is nil to keep the value's own, an integer, or an expression whose
  null keeps it too, as Polars' null does.
- **Reading and rebuilding:** each value is read on its wall clock as a day, a second
  in it, and the ticks left over. Nothing else is touched, so a Datetime(ns) keeps its
  nanoseconds unless a microsecond is given; a microsecond sets the whole fraction, as
  Polars sets it.
- **A Date's time parts** are not read.
- **Refused, as Polars refuses them:** parts that are no date or time, such as the
  30th of February, a 13th month, a 60th second or an hour of −1.
- **A zoned value:**
  - it is placed again by `replace_time_zone`'s `zoneClock`;
  - a fold is refused, or answered by `AmbiguousEarliest`, `Latest` or `Null`;
  - a gap is refused, or made null by `NonExistentNull`. Polars has no such option
    and always refuses a gap.
- **Year 0** is a leap year, with ticks equal to Polars'.

**CallFns:**

- **A block of its own at 900** holds the twelve one-operand functions, as the string
  family's at 800 does. Each new temporal function in the first block would have
  shifted the list family onto the struct family's base, the collision step 270 met.
  `IsTemporal` covers both blocks.
- **The horizontal block** gains `FnDtIsBusinessDay`, `FnDtAddBusinessDays` and
  `FnDtReplace`.
- **The names tests:** the range of 900, sweeps to 1000, and 186 functions.

**The contract's reach:**

- **The gap:** `allCallFns` swept CallFns below 700. The list block at 700 (step 253's
  and later) and the string block at 800 (steps 270 and 271) were never driven, so
  step 271's account of its contract was wrong for its four functions at 800, as a
  note there now says.
- **The sweep** runs to 1000 now.
- **Arguments:** the 700 and 800 blocks' functions have argument sets as their public
  methods pass them, and `callLit` builds nil and List literals.
- **One gap found:** `list.item` was typed without its `allow_empty` argument, which
  its kernel cannot run without. Its typing now requires it. No public method builds
  it without one.
- **The sliced-list test** sweeps the same range, and its fixture gains float, Boolean
  and nested lists with the same offsets, for `drop_nans`, `any`, `all`, `arg_true`
  and `explode`. All 32 list functions but `item` answer a sliced list as they answer
  the whole, and the floor is raised from 15 to 32. `item` refuses these lists, of
  other than one element, on their values.

**Public:**

- **New methods:**
  - `IsoYear`, `Century`, `Millennium`;
  - `BaseUtcOffset`, `DstOffset`, `WithTimeUnit`;
  - `TotalMilliseconds`, `TotalMicroseconds`, `TotalNanoseconds`, and
    `Total…Fractional` for days to nanoseconds;
  - `SecondFractional`, `EpochIn`, `EpochDays`, `Timestamp`;
  - `IsBusinessDay` and `AddBusinessDays` with `Roll`, `RollRaise`, `RollForward`
    and `RollBackward`;
  - `Replace` with `DateTimeParts`.
- **Naming:** a Polars keyword flag becomes a suffix, as `SplitInclusive` did at step
  270.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestIsoYearCenturyMillennium`:**
  - seven dates, a null among them;
  - the century and millennium of 24 years around year 0;
  - a Datetime's ISO year;
  - a Time refused.
- **`TestBusinessDays`:**
  - Polars' Friday, Saturday, Sunday, null and Christmas, under three calendars;
  - one forward, backward and back, and none, each rolled both ways;
  - a count a row, and a null count;
  - past a holiday, a thousand on and back;
  - ten over two holidays and back, and a mask with Thursdays off;
  - a Datetime keeping its time, and a zoned one moving on its local date;
  - five refusals, a fold among them.
- **`TestReplace`:**
  - hour and minute, the microsecond, a day a row with a null, and year 0 to its
    ticks;
  - nanoseconds kept and set, and milliseconds;
  - a Date;
  - a fold answered three ways, and a gap made null;
  - nine refusals.
- **`TestZoneOffsets`:** five zones in January and July, Casablanca in Ramadan, the
  Duration(ms) type, and a naive Datetime refused.
- **`TestTimeUnitsTotalsAndEpochs`:**
  - `with_time_unit`, its type, its zone kept, and a Duration's;
  - the three new totals;
  - seven fractional totals over ten durations, to the bit;
  - a Duration(ms)'s;
  - fractional seconds of microseconds, of seven nanosecond values and of a Time;
  - epochs in five units of Datetimes and Dates, and of a zoned value;
  - five refusals.

| tooth | result |
| --- | --- |
| the century of year 100 is the 2nd | **bites** |
| the millennium of 1000 is the 2nd | **bites** |
| the ISO year is the year | **bites** |
| epoch truncates | **bites** |
| a total floors | **bites** |
| a fraction is divided | **bites** |
| a second's fraction is divided | **bites** |
| no daylight-saving time | **bites** |
| offsets in seconds | **bites** |
| a week's holidays forgotten | **bites** |
| a week's holidays forgotten, going back | **bites** |
| rolled the other way | **bites** |
| raise rolls | **bites** |
| a microsecond adds | **bites** |
| every month has 31 days | **bites** |
| a 60th second | **bites** |
| latest is earliest | **bites** |
| a null part is no part | **bites** |
| epoch days are Int64 | **bites** |
| with_time_unit drops the zone | **bites** |
| list.item typed without allow_empty (the contract) | **bites** |
| the sweep stops at 700 (the sliced-list floor) | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
