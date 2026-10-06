# Step 113 — as built

**`Str().Strptime` and `Dt().Strftime`: dates parsed and formatted with strftime
formats.** This is `v0.4-scope.md` item 19's first entry.

## 1. What was missing

`Str().ToDate()` and `ToDatetime()` are lenient casts, with no format.

- **"09/02/2024" could not be read as a date,** except by splitting the string and
  rebuilding it.
- **Nothing formatted a date as anything but ISO 8601**, which is what `ToString` is.

Polars has `str.strptime(dtype, format, strict)` and `dt.strftime(format)`, with
chrono's directives.

## 2. Why not Go's layouts

`time.Parse` reads a layout by example: "2006-01-02". A strftime format translated
into one hands its literal text to Go's matcher too. A literal "1" in `"Q1 %Y"` reads
as a month, and "Mon" as a weekday. So **`internal/strftime`**, a new level-0
package with the standard library only, compiles a format into directives and
literals, and only the directives are fields.

**Directives:**

| group | directives |
| --- | --- |
| date | `%Y %y %m %d %e %j` |
| time | `%H %I %M %S %p` |
| fractions | `%f %.f %.3f %.6f %.9f %3f %6f %9f` |
| names (English) | `%b %h %B %a %A` |
| zones | `%z %:z`, and `%Z` for formatting only |
| shorthands | `%T %D %F %R %%` |

**Parsing is strict about shape:**

- a number takes at most its width (`%Y` is four digits, `%m` up to two), so
  `%Y%m%d` reads "20240229";
- a date must exist: no 30 February, and no day 366 in 2023;
- the whole input must be consumed: a trailing space fails;
- a weekday name is read and not checked.

## 3. The API

**`Str().Strptime(dt, format, strict)`** parses into a Date, Datetime or Time, one
call function each, because a type is not a literal.

- **What a Datetime's instant comes from:**
  - with `%z`, the parsed offset, and a naive target keeps that instant's UTC wall
    clock;
  - without `%z`, the wall clock in the target's zone (New York in January is
    −05:00, in July −04:00);
  - for a naive target, the wall clock as written.
- **Digits finer than the unit are dropped,** toward the past.
- **Strict,** a value that does not parse is an error naming it, with its batch row
  and a hint to pass `strict=false`. **Lenient,** it becomes null.
- **Refused while the query is planned:**
  - an unknown directive;
  - a Date format with no date, or with an offset;
  - a Time format with a date or an offset;
  - an unknown zone.

**`Dt().Strftime(format)`** formats a Date, Datetime or Time. A zoned Datetime is
written in its own wall clock (Tokyo's `%Z` is JST); a Date's time is midnight. A
date from a Time, or `%z`/`%Z` from a naive Datetime or a Date, is refused while
planning.

## 4. Inventories

Two refused the new functions until they were given their answers:

- `TestCallFnFamiliesDoNotOverlap` counts call functions, 64 before and 68 now.
- The evaluator contract drives each call with legal arguments. Calls missing from
  its table get none, so I added the four: lenient formats for the parsers, so the
  fixture's non-date strings give nulls; and a date format and a time format for
  `strftime`, because a Time refuses the first.

## 5. Tests and teeth

**`internal/strftime`:**

- compile refusals;
- 18 parses that succeed and 13 that must not, among them 2023-02-29, a two-digit
  `%Y`, 24:00, 13 PM, `%Y%m%d` eating a month of 22, and a half offset;
- formatting every directive;
- 5,000 random instants with random quarter-hour offsets across five centuries,
  formatted and parsed back exactly.

**`TestStrptime`:**

- nine format and type shapes;
- strict and lenient failures;
- five plan-time refusals.

**`TestStrftime`:**

- six shapes, including a zoned Datetime in its own zone and a UTC Datetime's
  `+0000`;
- three plan-time refusals. The first version of the naive-offset case used a
  `time.Time` column, which `Values` makes UTC, so `%z` was rightly allowed; it now
  casts a naive column.

**`TestStrftimeStrptimeRoundTrip`:** 2,000 Datetime(us) values across five centuries,
formatted and parsed back, all equal.

| tooth | result |
| --- | --- |
| days not checked against the month | **bites:** 2023-02-29 accepted |
| PM adds nothing | **bites** |
| the parsed offset ignored | **bites:** both offset cases |
| a zoned Datetime formatted in UTC | **bites:** Tokyo |
| a Date format with no date admitted | **bites** |
| finer digits rounded instead of dropped | **bites** |

**Gate:** test-all 110 ok (the new package adds five, one per width) and race 22; levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
