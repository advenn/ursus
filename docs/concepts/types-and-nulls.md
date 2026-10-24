---
description: >-
  ursus's data types, from Int8 to Decimal, Datetime with a time zone, List and Struct,
  and how the Go dataframe library treats null, NaN and casts.
---

# Types and nulls

Every column has one type, and a frame's schema is its columns' names and types, known
before a query runs:

--8<-- "Example_typesSchema.md"

## The types

| Type | Go value | Notes |
|---|---|---|
| `Bool` | `bool` | |
| `Int8` … `Int64`, `Uint8` … `Uint64` | `int8` … `uint64` | |
| `Int128` | `ursus.Int128Value` | what an integer sum gives |
| `Float32`, `Float64` | `float32`, `float64` | |
| `Decimal(p, s)` | | 128-bit; exact `+`, `-`, `*` and `Sum`, refused past 38 digits |
| `String`, `Binary` | `string`, `[]byte` | |
| `Date` | `time.Time` | a calendar day |
| `Datetime(unit, zone)` | `time.Time` | a unit of s, ms, us or ns, and an IANA zone or none |
| `Duration(unit)` | `time.Duration` | |
| `TimeOf(unit)` | | a time of day |
| `Enum(...)` | `string` | fixed categories, sorted in their order |
| `Categorical` | `string` | text with a type of its own, sorted as its text (*new in 0.6.7*) |
| `List(T)` | a slice | a list of values a row |
| `Array(T, n)` | an array, `[n]T` | `n` values a row (*new in 0.6.7*) |
| `StructOf(fields...)` | a struct | named fields a row |

`ursus.Int64`, `ursus.Date` and the rest name the types, and `Datetime`, `Duration`,
`TimeOf`, `Decimal`, `List`, `Array`, `Enum` and `StructOf` make one. A type is a comparable
value, so it can key a map.

## Null is not NaN

**Null** is a missing value, and any type can hold one. **NaN** is a float that is not
a number, the answer to `0.0 / 0.0`. They are different, and ursus keeps them apart
everywhere:

--8<-- "Example_typesNaN.md"

A null's test is `IsNull`, a NaN's `IsNan`; `FillNull` and `FillNan` repair them, and
`DropNulls` and `DropNans` drop their rows.

## Nulls in aggregates

An aggregate skips nulls. `Count` counts the values that are there, `Len` the rows,
and a mean is the sum of the values over their count, not over the rows:

--8<-- "Example_typesNulls.md"

Two rules follow SQL rather than Polars, because a null can become a number but a
number cannot become a null again:

- the `Sum` of a group with no values is null, not 0;
- the `Mean` of a group with no values is null, not NaN.

## Categoricals

*New in 0.6.7:* a `Categorical` is Polars' Categorical: text with a type of its own, made
by `Cast(ursus.Categorical)` from a String or an Enum, and read from a CSV or NDJSON
file by a schema. It sorts, compares and groups as its text, and meets a String at
String. Its codes, `Cat().Physical()` or `ToPhysical()`, number its values in the order
they first appear over the frame, as Polars numbers them in a fresh process; a
group-by's aggregation and a window refuse them, where they would number a group's.
`Cat()` has its text functions, and a String's functions read it as its text. It is
stored as its text, so it costs what a String does; casting a number to it is refused,
where Polars reads one as a code.

## Casts

`Cast` converts a column to another type and refuses a value it cannot convert, naming
it. `CastLossy` turns such values into nulls instead:

--8<-- "Example_typesCast.md"

A cast never loses a value silently: an integer that does not fit a narrower type is
refused by `Cast`, and a float rounds only when the target is a float. A float with a
fraction is refused as an integer, where Polars truncates it; `Truncate(0)` first says
that is what you want.

*New in 0.6.7.* `ToPhysical` is a column as it is stored, as Polars' `to_physical`: a
`Date`'s days as an `Int32`, a `Datetime`'s, a `Duration`'s and a `Time`'s ticks as an
`Int64`, a `Decimal`'s unscaled units as an `Int128`, an `Enum`'s category as the
smallest unsigned integer that holds them, and a `Categorical`'s code, the order its
value first appears in over the frame, as a `UInt32`. `Reinterpret(signed)` reads an integer's
bits as the integer of its width and the other sign, so a `UInt64`'s maximum is an
`Int64`'s -1.

### A frame's casts

*New in 0.6.7.* `Cast` casts columns by name, `CastTypes` every column of a type, and
`CastAll` every column, the rest left as they are, in their places, as Polars' frame
`cast` does. `CastStrict(false)` turns what does not convert into nulls, as
`CastLossy` does.

--8<-- "Example_typesFrameCast.md"

A `Datetime` key matches its unit and zone exactly, so a frame of two units needs a key
for each, where Polars' `pl.Datetime` matches both.

### A frame made to a schema

*New in 0.6.7.* `MatchToSchema(schema, options)` makes a frame's columns a schema's: its
columns, in its order, of its types, as Polars' `match_to_schema` does. By default
every difference is refused, the first type that does not meet first; the options
say what to do instead:

--8<-- "Example_typesMatchToSchema.md"

| Option | `MatchRaise` / `MatchForbid`, the default | or |
|---|---|---|
| `MissingColumns` | refuse a schema column the frame lacks | `MatchInsert`, as nulls |
| `ExtraColumns` | refuse a frame column the schema lacks | `MatchIgnore`, dropped |
| `MissingStructFields`, `ExtraStructFields` | the same, for a struct's fields | `MatchInsert`, `MatchIgnore` |
| `IntegerCast`, `FloatCast` | refuse a narrower number | `MatchUpcast`, widened |

`MatchUpcast` widens only where nothing can be lost: an `Int32` to an `Int64`, a `UInt32`
to an `Int64`, a `Float32` to a `Float64`; never an integer to a float, an `Int64` to an
`Int32`, or a `UInt64` to an `Int64`. A struct's fields are matched by name, at any
depth, and a list's element widens as a column does.

`Columns` sets any option for one column, and `ColumnMatch.Fill` computes a missing
column from the others. A fill's answer meets the schema's type as a column of the
frame would; Polars keeps the expression's own type, so its answer can be of a type the
schema did not ask for.

## Values from Go

`ursus.Values` makes a column from a slice of a Go type, `[]time.Time` a
`Datetime(ns, "UTC")` one, and `ursus.ValuesNullable` one with nulls, from the values
and whether each is present.

Going the other way, a null never comes back as a zero mistaken for a value:

- `df.At[T](row, name)` and a `Series[T]`'s `Get` give the value and whether it is
  present;
- `df.Rows[T]()` decodes a null into a pointer field as `nil`; declare the field
  `*int64` where a column can be null.
