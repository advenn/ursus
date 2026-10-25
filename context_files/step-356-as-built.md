# Step 356 — as built

**Text parsed by Polars' patterns:** `Str().ToDate()`, `ToDatetime(unit, tz)` and
`ToTime("", strict)`, and `Strptime(dt, "", strict)`, read what Polars' `str.to_date`,
`str.to_datetime` and `str.to_time` of no format read, the patterns step 355 scans. One
break, `ToDatetime` of a zone.

## 1. How

**An empty format** is Polars' `format=None`. The planner types it without compiling a
format (`internal/expr` `inferredOut`, a Datetime's zone checked), and the strptime
kernel reads each value by `dtype.ScanTemporalText`, the scan exported as
`TemporalText`, into the fields a strftime parse gives (`inferredFields`): a Date's a
date alone, a Time's a time of day alone, a Datetime's a date with a time or its
midnight, and an offset. The rest is `strptimeCall`'s as it was: an offset makes the
instant, and a value without one is read on the target zone's wall clock, or kept as
it is when naive; a wall clock the zone skips or reads twice is an error when strict
and a null when not (`oneInstant`); digits past the unit are dropped. A refusal says
the value does not parse "by Polars' patterns".

**The API:** `ToDate()` and `ToDatetime(unit, tz)` were lossy casts, which read
ISO 8601 and `ParseTemporal`'s layouts; they are lenient strptimes of an empty format
now, and `ToTime("", strict)` one where it was a cast. **The break:**
`ToDatetime(unit, tz)` of a text without an offset reads it on `tz`'s wall clock, as
Polars does, where the cast took it as UTC's; `Cast(Datetime(unit, tz))` still does,
as Polars' CSV reader reads it.

| Polars 1.44.1 | ursus |
| --- | --- |
| `str.to_date()` of `02/01/2020`, `31/12/2021`; `2020.01.02` after a null; `01/02/2020` | Dates, as Polars; the last the first of February |
| `str.to_date(strict=False)` of a value no pattern reads | null, as `ToDate` |
| `str.to_datetime()` of instants, `T` or a space, to ms and ns, a date's midnight | as Polars |
| `str.to_datetime(time_zone="Asia/Tokyo")` of `03:04:05`, of `03:04:05+0900` | 03:04:05 in Tokyo, both, as Polars |
| `str.to_datetime(time_zone="UTC")` of `03:04:05+0900` | 18:04:05 UTC, as Polars |
| `str.to_time()` of `3:04:05` | as Polars |
| `str.strptime(pl.Date)` of no format | as Polars |
| `str.to_date()` of an instant's text, of no pattern | refused, as Polars, by `Strptime(..., true)` |
| `to_time` of `10:30` and `11:00:01`; `to_date` of two patterns | refused; both read here |
| a wall clock New York skips or reads twice, lenient | refused; null here |
| `str.to_datetime()` of an offset's text | `Datetime(us, UTC)` from the data; the type asked for here |

## 2. Tests

**`TestStrToTemporalByPatterns`,** Polars 1.44.1's answers: 17 parses, dates day
first, lenient, after a null, never month first, `Strptime` of no format; instants, to
milliseconds and nanoseconds, a date's midnight, a zone's wall clock, an offset in a
zone and in UTC; a time of a one-digit hour; two patterns in a column; a skipped and a
twice-read wall clock; and four strict refusals.

**Teeth (8 of 8 bite):** an empty format compiled; a Date of an instant's text; a
Time of a date's text; `ToDatetime` a cast; an offset not passed on; `ToDate` a cast;
`ToTime` of no format a cast; the refusal not naming the patterns.

## 3. Performance

A million strings, the median of seven, against the step before, built alike from
`git archive`:

| | before | now | Polars |
| --- | --- | --- | --- |
| `ToDate` of `2020-01-02` | 26.9 ms | 19.6 | 97.0 |
| `ToDatetime(Micro, "")` of `2020-01-02 03:04:05.123456` | 402.7 | 30.6 | 201.7 |
| `ToDatetime(Micro, "Asia/Tokyo")` | 576.4 | 64.6 | 252.9 |

The cast tried `time.Parse` with each of `ParseTemporal`'s layouts a value, a
naive instant failing the two RFC 3339 layouts first; the scan reads one.

**Gate:** test-all 140 ok, race 28 ok (the root package 560 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
