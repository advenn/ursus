# Step 317 — as built

**V1, `FromRows`:** a frame from Go structs, as Polars builds one from dataclasses,
and `Rows`' inverse (`v0.6.7-scope.md` §6).

## 1. The API

```go
func FromRows[T any](rows []T) *LazyFrame
```

**Columns.** A column a field, in the fields' order. Each is named by its `ursus` tag,
as `Rows` reads it, or by its own name. Unexported fields and `ursus:"-"` are left
out.

**Types.**

- Scalars take `Values`' types: the integers, floats, bool, string, `[]byte` a Binary,
  `time.Time` a `Datetime(ns, UTC)` and `time.Duration` a `Duration(ns)`.
- A pointer field is nullable, nil a null.
- A slice field, other than `[]byte`, is a List of its elements, nil a null list and
  an empty slice an empty list.
- A struct field is a Struct of its own fields, named as these are.
- Any other type is refused, naming the field: a map, a channel, an array.

## 2. How, and how fast

**Each field is read once a row by its offset.** A `loc` locates n values:

- a walk from the first row's field by the struct's stride;
- for a list's elements and a pointer's targets, an address each;
- with `deref`, through a pointer to a scalar, nil a null, in the same walk.

**Text** is written straight into a String column's offsets and bytes, with no
`[]string` between. Past 2 GiB of text in one column it is refused, the String
column's limit.

A million structs of five fields, an int64, a float64, a string, an int32 and a
`*string` nil in two rows of three:

| | first version | final |
| --- | --- | --- |
| `FromRows` | 63–78 ms | 56–66 ms |
| the same columns by hand with `Values` | 49–66 ms | |

The profile showed what the first version paid. It was not the closures that located
each field, which the walk replaced at no gain. It was the collector, scanning the
intermediate `[]string` and the slice of pointers built for the nullable field. Both
are gone. What scanning is left is the caller's own structs. The machine was shared,
so each figure is the median of nine runs.

## 3. Found on the way, and fixed

**`Rows` could not decode a Struct column into a struct field.** `makeSetter` knew a
`time.Time` among structs and nothing else. `structSetter`, new, decodes each field
from the struct's field of its name, case-insensitively as a frame's columns are
matched. A null struct is the zero value. So `FromRows` and `Rows` round-trip.

**A null instant decoded as the epoch.** The test's zero `time.Time` came back
1970-01-01:

- `Values` makes an instant `Datetime(ns)` cannot hold null, on purpose;
- `Rows`' `timeSetter` read a null's ticks, 0, as an instant, the epoch: a plausible
  instant no row held;
- a null now decodes as time.Time's zero, as every other field type's null does as its
  zero, and `IsZero` tells it. The CHANGELOG lists it under fixes.

## 4. Tests

**`TestFromRows`:**

- a struct of every kind: a tag, a pointer, a list, a time, a duration, a nested
  struct, int8, uint64, `[]byte`, a skipped field and an unexported one;
- the columns, their names and types;
- the round trip through `Rows`;
- a nil list a null and an empty one a list;
- a nil number and a nil time nulls;
- a null instant the zero time;
- no rows: ten columns of none;
- refused: a map field, and ints.

## 5. Docs

- **Go values:** `FromRows`, with an example of the chapter's own `order` struct,
  whose output is the hand-built frame's.
- **Coming from Polars:** a row.
- **The CHANGELOG:** the feature and the fix.

## 6. Teeth, all 8 biting

- the stride walked short;
- a nil pointer read as a value;
- text offsets one off;
- a nil slice an empty list;
- the tag unread;
- an unexported field read;
- a struct field undecoded;
- a null instant the epoch.

One was silent at first, the nil pointer: the test's only nullable field was text,
which takes another path. The nil number and time test was added for it.

**The first gate failed `TestOnlyNodeReadsTheField`,** a check that no root-package
function reads an `Expr`'s node field, `.n`, directly. The locator's count was a field
`n`, which the check, by name, took for one. It is `rows` now.

**The second gate failed under the race detector,** in `TestFromRows` and the guide's
example: "checkptr: pointer arithmetic result points to invalid allocation". The race
detector turns on `checkptr`, which refuses a pointer moved past its allocation even
when it is never read. The walk stepped its pointer by the stride after each row, the
last too, one past the rows. It steps between rows now. Unsafe code is tested under
`-race` before a gate from here on.

**Gate, the third:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine
tests clean; the 22 PDS-H answers match DuckDB.
