# Step 122 — as built

**What the audit of step 113's strptime and strftime found, fixed.** Each defect was
reproduced by a test first.

## 1. The defects

| | what happened |
| --- | --- |
| a time the clocks skip or repeat | A strict `Strptime` into `Datetime(…, "America/New_York")` read "2024-03-10 02:30", inside the spring-forward gap, as 03:30 EDT, an instant an hour from what was written, without an error. "2024-11-03 01:30", which happens twice, took one of the two. `time.Date` resolves both silently, and step 113 trusted it. Polars raises on both. |
| a format that names no date | `%a`, `%d/%m`, `%Y-%m` and `%A %H:%M` passed the plan-time check, which asked for any date directive. A weekday is not a date, and a day and a month are not one without a year. `%a` alone gave the year 0 on every row; the others failed on every row, though the refusal's own message says what a date needs. `%Z`, which cannot be read back, also passed. |
| `%y` | "99" read as 2099. chrono, and so Polars, read 70–99 as 1970–1999 (`parsed.rs:627` in chrono 0.4.44). |
| `%z` | It required "+0530" and refused "+05:30", which chrono and Python accept. |
| years past 9999 | Formatted as "10000-01-01", which Parse, reading four digits, could not read back. Date and Datetime(ms) and (us) hold such years. |

## 2. The fixes

- **`oneInstant`:** after `time.Date`, the wall clock must come back as written, or
  the time is skipped. Then, where the zone's offset changes within three hours, the
  instant moved by the change must not show the same wall clock, or the time is
  ambiguous.
  - Strict, either is a value error naming the zone; lenient, a null.
  - Away from a transition this costs two offset lookups a row.
- **`Format.ParsesDate`:** a year, with a month and a day or with `%j`.
  `HasDate` keeps its meaning, any part of a date, which is what a Time refuses to
  format. `HasZoneName` marks `%Z`, refused for any parse.
- **`%y` pivots at 70.** `%z` takes the colon or not; `%:z` still requires it.
- **`%Y` writes a sign outside 0000–9999,** as chrono does, and reads a sign with up
  to nine digits.

## 3. Tests and teeth

`strftimeaudit_test.go`:

- **`TestStrptimeInAZoneWithAGapOrAnOverlap`:** both times strict, and lenient with
  the minute before and after each transition.
- **`TestStrptimeFormatsThatNameNoDate`:** five refusals while planning.
- **`TestStrftimeDirectivesAsChronoReadsThem`:**
  - `%y` at 99, 70, 69 and 00;
  - `%z` both ways;
  - a year of 10000 and one of 123456, round-tripped.

| tooth | result |
| --- | --- |
| a skipped wall clock accepted | **bites** |
| a repeated wall clock accepted | **bites** |
| any date directive counts as a date | **bites:** three formats |
| `%Z` passes planning | **bites** |
| `%y` is 2000+ | **bites** |
| `%z` refuses the colon | **bites** |
| a year past 9999 unsigned | **bites** |

## 4. The gate for steps 118–122

The five steps were each tested in their own packages, then gated together, and
pushed together after the gate.

**Gate:** test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB. The gate's PDS-H run now writes under `PATH_RESULTS` outside `bench/results`, so a later `make report` cannot publish a one-iteration gate run as the latest.
