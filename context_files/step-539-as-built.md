# Step 539 — as built

**qcheck generates strings, temporal values and more types.** Random queries now
cover the types and functions v0.9's Parquet work will touch, both in the self
check and against DuckDB. qcheck only: no change to ursus. The maintainer approved it
on 2026-11-20, in the six-item plan after the hunt ("widen qcheck"). It was built on
2026-11-20, saved as WIP on 2026-11-23, and finished the same day.

## What a case can hold now

The **wide generator** (`gen/wide.go`, `gen/widequery.go`) is the default. Each
column's type is drawn as follows:
- half the time, a base type of step 512;
- otherwise one of:
  - Int8 or Int16;
  - an unsigned integer;
  - Float32;
  - a Datetime of each unit, naive or in a zone;
  - a Duration or a Time of each unit;
  - a Decimal, from `Decimal(5, 2)` to `Decimal(38, 10)`.

Each type comes in step 453's tame, wild and extreme profiles.

The queries gain **function nodes** (`ast/fn.go`):
- **strings:** `Contains`, `StartsWith`, `EndsWith`, `Replace`, `ReplaceAll`,
  `LenBytes`, `LenChars`, `Slice`, `ToUpper`, `ToLower`, `StripPrefix`,
  `StripSuffix`, `Split(...).List().Len()`, and the cast to an integer;
- **temporal:** the components (year to nanosecond, weekday, ordinal day),
  `Truncate`, `Round`, `OffsetBy`, `Date`, `Time`, `ConvertTimeZone`, and the
  `Total*` family.

The **core generator**, step 512's types and operations alone, remains:
`gen.NewCoreCase`, and `Core` in `self.Options` and `compare.Options`. Each seed
there draws the case it drew before this step, so the corpus and the found seeds
replay as they were.

## Against DuckDB

`sqlgen/wide.go` translates the wide types and functions. Each translation is a
T- rule naming the ursus code it follows. A Datetime, a Duration and a Time are
their ticks in SQL, a BIGINT, just as a Date is its days (T-TICKS). DuckDB's own
TIMESTAMP reads int64's ends as infinities, and its TIMESTAMPTZ is microseconds.

What sqlgen does not translate is an X- rule. A query holding one is excluded
against DuckDB, and the self check alone runs it:

| rule | why |
| --- | --- |
| X-CASE-MAP | ursus's `ToUpper` and `ToLower` use Unicode's full case mapping (ß to SS), which DuckDB documents no equivalent of |
| X-ZONE-WALL | a zoned Datetime's components, `Date`, `Time` and calendar `OffsetBy` read the wall clock from the system's zone database; DuckDB's zones are ICU's (feature-gaps §9) |
| X-TEMPORAL-TEXT | a zoned Datetime's text has its offset; a Duration's is the frame's display |
| X-PARSE-TEMPORAL | text to a Datetime or a Time reads each value by its own pattern (§9) |
| X-PARSE-DEC | text to a Decimal rounds a tie half away from zero, by ursus's grammar (§9) |
| X-F32-TEXT, X-F32-AGG, X-F32-FLOORMOD, X-F64-F32 | Float32's text, sums, floor division and narrowing, as ursus's kernels round them |
| X-FLOAT-DEC, X-DEC-DIV, X-DEC-MEAN, X-DUR-MEAN | a float to a Decimal (its shortest digits, exact or null), a Decimal's quotient and mean (the Float64 nearest the exact one; DuckDB divides in DOUBLE), and a Duration's mean |

Of 1,000 wide cases from the teeth seed, 158 were excluded. The largest share was
X-ZONE-WALL, 68 of the 158.

## Answers compared

- `compare.Answer` reads the new types into canonical cells:
  - a Datetime, a Duration and a Time as their ticks;
  - a Decimal as its exact value at the query's scale, whatever scale the engine
    gave, so a cell no other scale can hold is its own text;
  - a Float32 as its bits.
- The self check reads them by their storage width.
- The shrinker shrinks wide cases and emits them as Go and as SQL
  (`shrink/wide.go`).

## The tests, finished on 2026-11-23

The WIP commit (22b189f) failed three packages. Each failure was in a test or in
qcheck's reporting, not in ursus:
- **compare.** Its stand-in for DuckDB, `ursusEngine`, did not know the wide types.
  It now exports them as DuckDB does: an unsigned integer and a Float32 as
  themselves, a Decimal as a Decimal128 of its precision and scale, and a temporal
  value as its ticks.
- **self.** The teeth append "!" to a cell, and a null's "!" is a cell of no bytes.
  `show` now prints a fixed-width cell not of its type's width as it is, where it
  read 16 bytes past the end of a Decimal's.
- **shrink.** The DuckDB-mode planted cases now skip a case sqlgen excludes, as the
  DuckDB run does.

Three checks need a float sum on several threads, and the wide generator spreads
its cases over many more types and functions:
- the smoke's float-threads rule: none in 400 cases;
- the noisy allowance in `TestRunAgainstUrsus`: none in 1,000;
- the noisy tooth: none in 150.

These three checks now run on the core generator's cases. Every generator must still
find nothing and cover every operation.

## Checks

- `cd qcheck && go vet ./... && go test ./...`, under `CGO_ENABLED=0`: all seven
  packages pass, before and after master (arrow-go v18.9.0) was merged in.
- Wide smoke: 400 cases, 4,065 runs, 1,200 comparisons, 0 findings.
- `TestRunAgainstUrsus`, wide: 1,000 cases, 842 compared, 0 findings.
- The translation against DuckDB itself, under the duckdb tags, is CI's
  `qcheck-duckdb` job: the laptop does not link DuckDB.
