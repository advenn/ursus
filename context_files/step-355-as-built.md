# Step 355 — as built

**CSV dates inferred,** Polars' `read_csv(try_parse_dates=True)`, from `feature-gaps.md`
§7: a column of dates, instants or times of day typed as one, and a Date, Datetime or
Time column a schema gives read by the same patterns. It found a bug in a date's Go
value, fixed.

## 1. How

**The patterns** (`dtype/temporaltext.go`, `scanTemporalText`), Polars' CSV reader's:
a date year first or day first, `2024-01-02` and `02/01/2024`, split by `-`, `/` or
`.`, the separator the same twice, a month and a day of one digit or two, a day that
exists in its month; a time of day after a `T` or a space, `03:04`, `03:04:05` or
`03:04:05.123456789`, an hour of one digit or two; after an instant an offset, `Z`,
`+09:00` or `+0900`, a space before it allowed; and a time of day alone. A month
first, `01/02/2024` as January, is not one: Polars reads it as the first of February.
The scan is a hand-written walk of the bytes, a number at most its width, no
allocation, the ticks by `time.Date` and `FromTime`, a fraction past the unit cut as
Polars cuts it, an offset taken off for the instant. Each value is read by its own
pattern, where Polars infers one from a column's first value and refuses another.

**Inference** (`csv.Options.TryParseDates`, `WithTryParseDates`): `classify` asks
`ClassifyTemporalText` after the numbers, so `20200102` stays an Int64, and a column
is a Date, a `Datetime(us)`, a `Datetime(us, UTC)` where its values carry offsets, or
a `Time(ns)`, Polars' types. Each kind meets only itself: a date beside an instant, an
instant in a zone beside one in none, or a time beside a date is text, as Polars infers
them. A value past the inferred rows that does not parse is refused, as for every
type.

**Parsing** (`newBuilder`): a Date, Time or Datetime column, inferred or given, is
read by `ParseTemporalText`, the scan first and `ParseTemporal` for what it does not
read, so every text read before still reads; a Duration by `ParseTemporal`.

**The bug** (`rows.go`, `instantOf`): `Rows`, `ToMaps` and `At` built an instant as
`time.Unix(0, ticks*nanosPerTick)`, whose nanosecond count overflows before 1677 and
after 2262, years a Date and a coarser Datetime hold; 0999-01-02 came out as
2168-02-10. The seconds and nanoseconds are taken apart now, by `ToTime`.

| Polars 1.44.1 `read_csv(try_parse_dates=True)` | ursus |
| --- | --- |
| `2020-01-02`, `2020/01/02`, `2020.01.02`, `02/01/2020`, `02-01-2020`, `02.01.2020`, `2020-1-2`, `1/2/2020` | Dates, as Polars |
| `01/02/2020` beside `12/31/2021`, `2020-02-30`, `2020-13-01`, `Jan 2 2020` | text, as Polars |
| `20200102` | Int64, as Polars |
| instants with `T` or a space, minutes alone, a fraction to nine digits, an hour of one digit, a day first | Datetime(us), as Polars |
| an offset `Z`, `+0900`, `+09:00`, after a space | Datetime(us, UTC), as Polars |
| a date beside an instant, a zone beside none | text, as Polars |
| `10:30`, `10:30:00.5` | Time, as Polars; `25:00:00` text |
| `2020-01-02` beside `02/01/2020`, `10:30` beside `10:30:00.123` | refused; both read here |
| ` 2020-01-02` | a Date; text here |
| a schema's Date of `02/01/2020`, Datetime of `02/01/2020 03:04:05`, Time of `10:30` | as Polars |

## 2. Tests

**`TestCSVTryParseDates`,** Polars 1.44.1's answers: 47 texts, dates, instants,
offsets, times and what is text; Polars' default; a null value, a schema override; a
schema's Date, Datetime and Time columns, and an RFC 3339 instant read as before; a
value past the inferred rows refused, and read as text when every row is inferred.
**`TestFarInstantsAsGoValues`:** a Date of 999, a `Datetime(ms)` of 3000 and a
`Datetime(s, Asia/Tokyo)` of 1500 through `Rows` and `ToMaps`.

**Teeth (14 of 14 bite):** a day first not read; a month of one digit refused; a dot
not a separator; a day past its month's taken; an offset not taken off; a fraction
not scaled; an offset's instant naive; a date inferred an instant; try_parse_dates
ignored; a schema's dates read as before; no space before an offset; an hour past 23
taken; a far instant's nanoseconds wrapped; a trailing space taken (silent at first:
the end's check guards a time alone, and `10:30 PM`, `10:30:00x` and an offset's
trailing text were added, text in Polars).

## 3. Performance

A million rows of a date and an instant, `2020-01-02,2020-01-02 03:04:05.123456`,
typed by a schema, the median of seven, against the reader before the step built
alike from `git archive`: 808 ms before, 105 now, where Polars takes 325; inferred by
`WithTryParseDates`, 110 ms, where Polars' `try_parse_dates` takes 316. The reader
tried `time.Parse` with each of six layouts a value; the scan reads one.

**Gate:** test-all 140 ok, race 28 ok (the root package 571 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
