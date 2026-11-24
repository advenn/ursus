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

## Against DuckDB, on CI (2026-11-23)

The PR's `qcheck-duckdb` job ran the wide cases against DuckDB for the first time,
and four rounds of CI fixed what it found:
- **Loading.** `duck.WithRID` handed DuckDB a Datetime, a Duration and a Time as
  DuckDB's own types: a Duration as an INTERVAL, and a zoned or millisecond Datetime
  as microseconds in UTC. It now hands each over as an Int64 of its ticks, as T-TICKS
  says. This took the DuckDB smoke from 53 findings to 2.
- **Text.** `u_todtext` and `u_tstext` multiplied the INTEGER 1000000000 by 3600,
  which overflowed INT32. They multiply in BIGINT now.
- **Decimal products.** DuckDB multiplies DECIMALs of 18 digits or fewer in an
  int64, and DECIMAL(12, 4) squared overflowed. Each operand is widened to
  DECIMAL(38) at its own scale first.
- **The rule tests.** `duck/rules_wide_test.go` gives each of the 23 T- rules a case
  at its edges, where ursus and DuckDB must agree. The test tables now take the wide
  types. The X- rules have no DuckDB test: they are not translated, and the
  maintainer said on 2026-11-23 that DuckDB's dialect is mirrored only for what
  ursus has.
- **The found seeds** in `self` and `duck` replay with the generator that found
  them. Step 523's two had silently become wide cases.
- **The smokes** run each generator. The DuckDB smoke compares nine in ten of the
  core generator's cases and three in four of the wide generator's: the X- rules
  exclude about one in six of the wide cases (336 of 400 compared, 0 findings).

The five-minute `qcheck self` run also found a real case, seed 874020098. A
Datetime(ms) cast to Time(us) was refused with the optimizer off alone. Step 542
fixed the cast and documented the rule behind the rest: a value the query discards
may not be computed, so its error may not be raised. The self check now allows such
a refusal only where the reference answered, the variant refused with ErrValue, and
the difference bisects to the optimizer alone, the variant's off
(`discarded`, `TestDiscardedIsNarrow`). Each allowance is counted in the report.

## Against DuckDB, the last rounds (2026-11-24)

The five-minute DuckDB run, on about 21,000 cases a round, found ursus's own bugs:
far instants' calendars, fixed in ursus by steps 543 and 544 and released in v0.8.1.
It also found what qcheck needed:
- **Decimal sums.** sqlgen widens the operands of a DECIMAL sum and difference to
  DECIMAL(38) too: DuckDB adds two of 18 digits or fewer in an int64, and
  overflowed, as it did on a product.
- **Both refused.** `compare.Run` counts ursus's ErrValue beside DuckDB's error as
  both refused (`Report.BothRefused`), not a finding: a Date or a year past int32
  is refused by each.
- **The discarded allowance in duck mode.** ursus with the optimizer off may refuse
  with ErrValue a value DuckDB's plan skips, the maintainer's rule (step 542). It is
  allowed (`FiredDiscarded`) only where ursus under the same configuration with the
  optimizer on answers DuckDB's answer.
- **Bounded receivers.** The generator draws Year and Date of a bounded column only
  (wild at most, years 0001 to 9999). Their refusal past int32 is designed, and
  DuckDB, which skips a column no answer reads, need not raise it. The other parts
  keep far instants, where DuckDB checks step 544's arithmetic.

## Checks

- `cd qcheck && go vet ./... && go test ./...`, under `CGO_ENABLED=0`: all seven
  packages pass, before and after master (arrow-go v18.9.0) was merged in.
- The changes of 2026-11-23's afternoon ran on CI only. The maintainer was away from
  home and asked that the laptop be kept quiet.
- Wide smoke: 400 cases, 4,065 runs, 1,200 comparisons, 0 findings.
- `TestRunAgainstUrsus`, wide: 1,000 cases, 842 compared, 0 findings.
- The translation against DuckDB itself, under the duckdb tags, is CI's
  `qcheck-duckdb` job: the laptop does not link DuckDB.
