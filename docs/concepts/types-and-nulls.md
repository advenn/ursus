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
| `List(T)` | a slice | a list of values a row |
| `StructOf(fields...)` | a struct | named fields a row |

`ursus.Int64`, `ursus.Date` and the rest name the types, and `Datetime`, `Duration`,
`TimeOf`, `Decimal`, `List`, `Enum` and `StructOf` make one. A type is a comparable
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

## Casts

`Cast` converts a column to another type and refuses a value it cannot convert, naming
it. `CastLossy` turns such values into nulls instead:

--8<-- "Example_typesCast.md"

A cast never loses a value silently: an integer that does not fit a narrower type is
refused by `Cast`, and a float rounds only when the target is a float. A float with a
fraction is refused as an integer, where Polars truncates it; `Truncate(0)` first says
that is what you want.

## Values from Go

`ursus.Values` makes a column from a slice of a Go type, `[]time.Time` a
`Datetime(ns, "UTC")` one, and `ursus.ValuesNullable` one with nulls, from the values
and whether each is present.

Going the other way, a null never comes back as a zero mistaken for a value:

- `df.At[T](row, name)` and a `Series[T]`'s `Get` give the value and whether it is
  present;
- `df.Rows[T]()` decodes a null into a pointer field as `nil`; declare the field
  `*int64` where a column can be null.
