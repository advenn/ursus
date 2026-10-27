# Step 390 — as built

**The CSV writer on the query's threads, and its dates and instants written digit by
digit.** C1 of `v0.7-scope.md`.

## 1. What C1's probe spends

§4's C1 probe (step 382), the million rows of C2's file, an integer, a float, text
and a microsecond instant, written to `io.Discard` on four threads, profiled on step
389's build: one goroutine made and wrote every record, 99% of one core. The instant
was 58% of it, the time package's `AppendFormat` interpreting the layout
`2006-01-02T15:04:05.000000` a field at a time for each value; the float 16%, the
text's quoting 12%.

## 2. How

- **`dtype.AppendISO`** (`dtype/isotext.go`): a Date, a naive Datetime and a Time
  in the writer's default layouts, written from the ticks digit by digit, the date by
  Howard Hinnant's days-to-civil, where the year is 0 to 9999 and a Time within a day;
  anything else, a zone or a format given, is written as before.
- **Batches formatted at once** (`csv.go`, `WriteCSV`; `Writer.Format` and
  `Writer.WriteText`): each batch's text is made on a goroutine of its own, as many
  at once as the query's threads, and the texts written in the batches' order, so
  the bytes are the same however many run, as the JSON writer writes. A batch still
  being formatted when the write stops is waited for before the tree is closed; a
  panic in one is an error. `WithThreads(1)` writes as before, a batch at a time, its
  text now made whole and written once; the header has a buffer of its own, as a
  first `WriteBatch`'s text is in the writer's. A partitioned sink writes a batch at a
  time still.

## 3. Measured

C1's probe, four threads, minimum and median of seven, three rounds alternating at a
load of 3.3 to 6, the laptop busy. The instant digit by digit alone, to `io.Discard`:

| | minimum | median |
| --- | --- | --- |
| step 389 | 332.4–409.7 ms | 337.6–429.3 |
| the instant digit by digit | 239.9–297.2 | 276.3–414.6 |

And all of it, with Polars 1.44.1 between:

| | minimum | median |
| --- | --- | --- |
| step 389, to `io.Discard` | 366.8–501.2 ms | 401.7–605.1 |
| here, to `io.Discard` | 108.0–115.9 | 124.7–155.5 |
| here, `SinkCSV` to a file | 131.4–181.4 | 151.7–198.2 |
| Polars' `write_csv` to a file | 161.5–188.7 | 176.3–225.1 |

The write takes about a third of its time, and about three quarters of Polars' where it
took twice it; step 382 measured 413–436 ms against 209–237. Step 389's build and this
one write the probe's 54 MB file byte for byte the same.

## 4. Tests

- **`TestAppendISOIsTheTimeLayouts`** (`dtype`): `AppendISO` against the time
  package's layouts, for every unit, either side of the epoch, about the leap days of
  1900, 2000 and 2024 and at the ends of the years 0 and 9999, 200,000 random dates
  and instants a unit and times of day; declining a year past them, a Time outside a
  day and a type with no layout, and leaving dst as it was.
- **`TestCSVWritesTheSameBytesOnFourThreadsAsOnOne`** (root): 30,000 rows in batches
  of 997, of integers, floats with NaN, infinities and nulls, text to be quoted with
  nulls, dates and instants of years either side of 0 to 9999, naive at three units
  and zoned, times of day and booleans, written on one thread and four under four
  sets of options, the defaults, every field quoted with a null's text, another
  separator with a datetime format and a byte-order mark, and gzip without a header:
  the same bytes each time; and a Date's, a naive Datetime's and a Time's text the
  time package's, row for row.
- **`TestCSVWriteStopsAtItsFirstError`** (root): a destination failing partway, and
  a batch failing late, each end the write with their error.
- **`TestCSVFormatterPanicIsAnError`** (root, internal): a panic formatting a batch,
  a row of text past its column's characters, comes back as an internal error, and
  the caller's writer's as an error; `goroutineSites` lists the new goroutine.

**Teeth: 7 of 7 bite:** a month a month on; a fraction a digit short; a zoned
instant written as naive; the texts written newest first; the header written over a
batch's text, which a partitioned sink's first `WriteBatch` shows; the last texts left
unwritten; a formatter's panic not recovered, which crashed the test binary.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
