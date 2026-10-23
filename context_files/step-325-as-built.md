# Step 325 — as built

**Casts as Polars':** `str.to_time`, `str.to_decimal` and `dt.cast_time_unit`, and a
float cast to text in Polars' text, which closes `feature-gaps.md` row 64
(`polars-parity.md` §2).

## 1. The API

| Method | Polars | What it does |
| --- | --- | --- |
| `Str().ToTime(format, strict)` | `str.to_time(format, strict=)` | a Time of nanoseconds: by a format, as `Strptime` reads one, or with none each value as a cast reads it |
| `Str().ToDecimal(scale)` | `str.to_decimal(scale=)` | a `Decimal(38, scale)`, rounded half away from zero, an exponent read, text that is no number null |
| `Dt().CastTimeUnit(unit)` | `dt.cast_time_unit` | a Datetime's or a Duration's values in another unit: an instant floored, a length truncated toward zero, a zone kept; any other type refused |

**A float cast to text is Polars' text now,** `kernel.FloatText`: the shortest digits
that read back as the value at its width, plain from 1e-5 to below 1e16, a whole
number with `.0`, otherwise scientific with a signed exponent of no padding; `NaN`,
`inf` and `-inf`. So `2.0` is `"2.0"`, `-0.0` `"-0.0"`, `1e16` `"1e+16"`, `1.5e-7`
`"1.5e-7"`, where Go's were `"2"`, `"-0"`, `"1e+16"` and `"1.5e-07"`. The CHANGELOG
lists it as a break: a pivot on a float names its columns so too, `"0.0"` where it
was `"0"`, and `ConcatStr` writes a float so.

**Where ursus differs:** with no format, `ToTime` reads each value as it comes,
"10:30" and "10:30:15" alike, where Polars infers one format from the first value and
nulls the rest; and `ToDecimal` of "-0.005" at scale 2 is -0.01, half away from zero
as its "-1.255" is -1.26, where Polars answers 0.00.

## 2. How

- `ToTime` and `ToDecimal` are `Strptime` and the casts, which already floored,
  truncated and rounded as Polars does: probed, a Datetime's cast to a coarser unit
  floors, a Duration's truncates, and text to a Decimal rounds half away from zero.
- `CastTimeUnit` is a temporal call, `FnDtCastTimeUnit`, typed as `WithTimeUnit` and
  evaluated as a cast; the evaluator contract sweeps it.
- `FloatText` appends into one buffer the cast reuses, a string's allocation a value.

## 3. Speed

A million floats cast to text, medians of seven: 26 to 29 ms, where Polars takes 33.
`FloatText` itself costs 12 to 20% more a value than Go's `'g'`, 73 to 79 ms a million
against 64 to 66, its fixed notation's shortest digits the most of it.

## 4. Tests

**`TestCastsAsPolars`,** every answer Polars':

- `ToTime` by a format, strict and not, by another format, and with none;
- `ToDecimal`: seven values, nulls among them, rounded both ways, an exponent, and its
  type `Decimal(38, 2)`;
- `CastTimeUnit` of four instants and four lengths down, up, a zone kept, a Date
  refused;
- 30 Float64s and 9 Float32s as text, each Polars' own.

**Thirteen tests held Go's text** and hold Polars' now, each a reformatting of the same
numbers: `rendered172`'s rolling and window answers, a frame's aggregates, a mode's
values, a median day, an interpolation, `ConcatStr`'s mixed row, a pivot's zero
columns, a JSON decode's Float32 and a top-k's floats.

## 5. Docs

- **Strings:** `ToTime` and `ToDecimal`.
- **Coming from Polars:** a row.
- **The CHANGELOG:** the break and the features; **`feature-gaps.md`** row 64 closed;
  **`polars-parity.md`** two rows struck.

## 6. Teeth, all 7 biting

- a whole float without its `.0`;
- scientific from 1e15;
- the exponent padded;
- an infinity as Go writes it;
- `CastTimeUnit` relabelling, not casting;
- `ToDecimal`'s scale ignored;
- `ToTime` never strict.

Four were rewritten when `FloatText` came to append into a buffer, their anchors gone.

**Gate:** test-all 130 ok, race 26 ok (the root package 509 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
