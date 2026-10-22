# Step 312 — as built

**G4: an integer index for temporal groups** (`v0.6.7-scope.md` §4), Polars' `"3i"`:
`GroupByDynamic` and `Rolling` over an integer index, and with them `Upsample` and the
rolling windows and ewms by an integer column. It finishes the group-by items.

## 1. What Polars 1.44.1 has, probed

**The unit.** `"3i"` counts an integer index, three of it; `"2i3i"` is `5i`. It mixes
with no unit of time: "cannot mix month, week, day, and sub-daily units".

**Which integers.**

| | Int32, Int64 | UInt32, UInt64 | others |
| --- | --- | --- | --- |
| `group_by_dynamic` | yes | refused, "expected any of the following dtypes: { Date, Datetime, Int32, Int64 }" | refused |
| `rolling` | yes | yes | refused |
| `rolling_*_by`, `ewm_*_by` | yes | yes | refused |
| `upsample` | yes | yes | refused |

The index keeps its type, and so do a dynamic group's boundaries.

**Refusals.** An interval of time on an integer, "must be a parsed integer (i.e. use
'2i', not '2d')", and `"i"` on a date, "may not be a parsed integer".

**Where a date is moved, Polars takes `"i"` and does nothing sound with it:**

- `dt.truncate("3i")` and `dt.offset_by("3i")` return the values unchanged;
- `dt.round("3i")` panics, "attempt to calculate the remainder with a divisor of
  zero";
- `date_range(..., "1i")` refuses, its interval "must consist of full days";
  `datetime_range` refuses it as rounding to zero in microseconds;
- a `join_asof` tolerance of `"2i"` panics on an integer key, and on a Datetime reads
  as nanoseconds.

## 2. How

**`Every("3i")` is three nanoseconds marked an index.** `dtype.Interval` gains an
`index` flag and `Index()`. It prints `3i`, `Neg` keeps it, and a mix with any unit of
time is refused at the parse.

**An integer index is the ticks of a Datetime(ns).** The grid's arithmetic needs no
change:

- `temporalGroup` reads the index's type first;
- an Int32 or an Int64, and for a rolling group a UInt32 or a UInt64, is cast to Int64
  and then to `Datetime(ns)`, under its own name;
- `agg` casts the index back to its type, and `_lower_boundary` and `_upper_boundary`
  too.

Every way a group is answered goes through `agg`: the shorthands, `Having`, and a
key's buckets. A UInt64 past an Int64 fails the cast, where Polars' rolling answers a
null index.

**The by-windows read an integer by as ticks.**

- `rollingByOut` and `ewmByOut` take Int32, Int64, UInt32 and UInt64.
- The kernels read a UInt32 widened, and refuse a UInt64 past an Int64.
- `windowStarts` steps an integer by its own ticks, and the ewm's half-life is the
  count itself.

**`Upsample` steps an integer column** as a `DatetimeRanges` of its ticks, exploded and
cast back before the join.

**One check of the kind, `expr.IndexInterval`,** serves every place an index meets an
interval: an integer column takes `"i"` alone, and a date refuses it. The temporal
groups check every, period and offset; the by-windows their period or half-life; and
`Upsample` its every.

**`"i"` is refused where a date is moved,** by `timeOnly` at the API:

- `Truncate`, `Round` and `OffsetBy` by a constant;
- the ranges, before a date range's whole-days check, whose hint would point to
  `DatetimeRange`;
- a row's offset text, in `OffsetRefusal`, which the kernel asks;
- an as-of tolerance, in the plan, after the numeric-key refusal.

## 3. Tests

**`TestIntegerIndexGroups`, against Polars' answers:**

- every 3i; every 2i with period 3i; offset 1i;
- boundaries, closed both, labels right and datapoint, start by the first row;
- a negative index, floored;
- a rolling group, over a key, over a UInt32 and over a UInt64;
- `Having` and a shorthand;
- an Int32 kept;
- seven refusals, Polars' messages beside them.

**`TestIntegerBy`:**

- `rolling_sum_by` and `rolling_mean_by`, closed both;
- `ewm_mean_by`, Polars' values to the last digit;
- by an Int32, a UInt32 and a UInt64;
- refusals of time, of `"i"` on a date, of a float and an Int16, and of a UInt64
  past an Int64.

**`TestIntegerUpsample`:**

- every 2i, off-grid rows left out;
- an Int32, a UInt32, a negative index, and a group's;
- three refusals.

**`TestIntegerIntervalMovesNoDate`:** truncate, round, offset_by by a constant and by
a row's text, both ranges, and an as-of tolerance.

**`TestIntegerIndexAnswersAsADatetime`** checks the claim the design rests on:

- 50,000 rows over many batches and five keys;
- dynamic with boundaries, dynamic by a key, and rolling by a key;
- each equal to the same group over a Datetime(ns) index, by as many nanoseconds;
- the dynamic groups under a fifth of the input's bytes, too.

The peaks and refusals of the two indexes were equal to the byte at 20,000 and 50,000
rows. The test compares with `Equals` and the schema, as `AssertFrameEqual` took a
second over rows this many.

**dtype's `TestEveryIntegerUnit`:** the flag, `Neg`, `2i3i`, and `3ns` not `3i`. The
refusals and round trips gain `"1d1i"`, `"1i1d"`, `"1i1ns"`, `"0i"`, `"3i"` and
`"-1i"`.

## 4. Docs

- **Dates and times** has a section, an integer index, with an example of steps per
  five, which Polars answers alike.
- **Window functions** says a `By` form takes an integer by.
- **Coming from Polars** has a row for `"5i"` and `"3i"`.
- **The godocs** of `Every`, `GroupByDynamic`, `Rolling`, `RollingSumBy`, `EwmMeanBy`
  and `Upsample` cover it, and the parse hint lists `i`.
- The CHANGELOG lists it under features.

## 5. Found on the way

**`RollingOptions.Offset` is not Polars' `offset`** (§1 of `feature-gaps.md`, open):

- ursus's moves a window's end, `(t + offset − period, t + offset]`;
- Polars' places its start, `(t + offset, t + offset + period]`, by default `−period`.

Over `[1, 3, 5, 6]` at a period of `2i`, Polars' `offset="-3i"` sums `[0, 1, 2, 3]`,
and ursus's `Offset: Every("-3i")` `[null, null, 1, 2]`. ursus's `Every("-1i")` gives
Polars' windows. No doc says so. It is the maintainer's to decide, as step 310's null
condition was, and the tests leave a rolling offset out.

**Recorded in §9:**

- `"i"` refused where a date is moved;
- a weekday `StartBy` on an integer index refused, as on any grid not in weeks, where
  Polars ignores it.

The keyed dynamic group's empty windows and its column order were there already.

## 6. Teeth, all 26 biting

| Area | Teeth |
| --- | --- |
| The unit | `"i"` counting two; not marked an index; mixing allowed; printed as ns; `Neg` dropping the flag |
| The groups | no unsigned rolling; no interval check; no cast back; boundaries not cast back; the cast back off by one, which only the Datetime comparison catches |
| The kind check | either direction disabled |
| The kernels | ticks counted two a step; no UInt64 check; no UInt32 reader; the half-life doubled |
| Upsample | no cast back; no interval check |
| Moving a date | `timeOnly` never refusing, and each of the seven refusals removed: truncate, round, offset_by by a constant and by a row's text, both ranges, the tolerance |

The mixing tooth first failed the build, its variable left unused; it was rewritten.

## 7. The gate

**The first gate failed four tests.**

- **Three older tests held the refusals,** each now with the new message:
  - `TestEwmMeanAndSumBy`: an integer by with a half-life of `1h`, which is now refused
    as an interval of time, and a Time by, whose type list names the integers;
  - `TestRollingByRefuses`: the same for a period, and a String by;
  - `TestUpsample`: an integer index stepped by a day.
- **`TestIntegerIndexAnswersAsADatetime` under the race detector.** Its 5,000 rows,
  under a fifth of their own bytes, refused: the dynamic group's open windows and a
  batch hold about 34 KiB whatever the input. It now keeps the full run's limit at a
  tenth of the rows, as `TestTemporalGroupsStreamTheirInput` does.

**The second gate** passed: test-all 125 ok, race 25 ok, levels, vet ×3 and the bench
engine tests clean; the 22 PDS-H answers match DuckDB.
