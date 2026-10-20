---
description: >-
  Moving data between Go and ursus, the Go dataframe library: building frames from Go
  slices, reading typed columns as Series, decoding rows into Go structs with tags and
  pointer fields, and streaming an answer into Go a batch at a time.
---

# Go values in and out

## Into a frame

`Values(name, slice)` makes a column from a Go slice, its type the slice's, and
`ValuesNullable(name, slice, valid)` one with nulls where `valid` is false. A slice of
structs becomes a frame a field at a time:

--8<-- "Example_goIn.md"

| Go slice | Column |
|---|---|
| `[]bool` | `Bool` |
| `[]int8` … `[]int64`, `[]int` | `Int8` … `Int64`, `Int64` |
| `[]uint8` … `[]uint64`, `[]uint` | `Uint8` … `Uint64`, `Uint64` |
| `[]float32`, `[]float64` | `Float32`, `Float64` |
| `[]string`, `[][]byte` | `String`, `Binary` |
| `[]time.Time` | `Datetime(ns, "UTC")`; cast to `Date` for days |
| `[]time.Duration` | `Duration(ns)` |

Arrow records come in through `ScanArrowRecords`; see
[CSV, Parquet and Arrow](io.md#arrow).

## A column out: Series

`df.Column[T](name)` is a column as a `Series[T]`, typed by a generic method, so no
assertion is needed at the call site. A `Series` answers in Go:

--8<-- "Example_goColumn.md"

- `Values()` is the values that are not null, as a slice;
- `Map[U](fn)` maps each value to a Go slice of another type, and `MapErr` with a
  function that can fail;
- `Fold(init, fn)` reduces the values left to right;
- `Name`, `DType`, `Len` and `NullCount`.

A null is never a zero mistaken for a value. `All()` iterates each value with whether
it is there, and `Get(i)` and `df.At[T](row, name)` answer one:

--8<-- "Example_goNulls.md"

`T` must match the column: `int64` for `Int64`, `float64` for `Float64`, `string` for
`String`, `ursus.Int128Value` for an `Int128` sum. A mismatch is an error naming both.
A `Date` is viewed as its `int32` days and a `Datetime` as its `int64` ticks; `Rows`,
below, decodes either into a `time.Time`.

## Rows out: structs

`df.Rows[T]()` decodes every row into a struct. A field takes the column its `ursus`
tag names, else its own name, else its name ignoring case; `ursus:"-"` skips it:

--8<-- "Example_goRows.md"

A field with no column is an error, not a zero left behind: a typo would otherwise be
a wrong number in a report. A pointer field is how a struct sees a null, `nil`:

--8<-- "Example_goRowsNulls.md"

The mapping of fields to columns is worked out once, so decoding costs a setter call a
field a row, not a reflection lookup.

## A large answer, a batch at a time

`CollectBatches` streams an answer as frames of `WithBatchSize` rows, a Go iterator, so
an answer larger than memory reaches Go without being held at once:

--8<-- "Example_goBatches.md"

Breaking out of the loop stops the query and removes its spill files.
