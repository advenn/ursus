# Step 412 — as built

**A JSON value nested past 1,000 levels is an error.** H1 of `v0.7.2-scope.md`, the
plan's 418.

## 1. What was wrong

NDJSON's parser recursed once a level of a line's arrays and objects, with no limit:
`value` through `object` and `array`, `skip` for a key no column reads, and
`appendNested` for a value read into a String column. A line nested two million deep,
4 MB, overflowed a 64 MB goroutine stack, `fatal error: stack overflow`: no recover
catches it, and the process ends. At Go's default 1 GB a line about sixteen times
deeper does the same. `ScanNDJSON` and its kin read through it, and `ScanJSON`'s
documents too.

## 2. The change

- **A count of the arrays and objects open,** `parser.depth`, the line's own object
  among them: `enter` at each `{` and `[` refuses one past `maxNesting`, with an
  `ErrValue`, "JSON nested deeper than 1000 levels at byte N"; `leave` as each closes.
  `object`, `array`, `skip`, `appendNested`, the list builder and the struct builder
  count. An error ends the line's parse, so a count left open by one is never read;
  the count starts at 0 each line.
- **Why 1,000 and not encoding/json's 10,000:** a column's type nests as deep as its
  values, and building one costs the square of its depth, `dtype.List`'s interning
  and `natural` each writing the inner type's text, and the column's buffers walked
  again a level: a read of one line takes 21 ms at 250 levels, 466 ms at 1,000, 3 s at
  2,000, once a type, however many lines. At 10,000 a line of 20 KB would take
  minutes. Python's json stops near 1,000. Polars 1.44.1 reads 200 levels, raises a
  RecursionError at 1,000 and segfaults at 5,000, so it is no oracle here.
- **Left:** the quadratic cost of a deep type, for a step of its own; `JsonDecode` and
  `JsonPathMatch` (`kernel/strjson.go`), the Arrow importer's and Parquet's schema
  walks, and `FromRows` on a recursive type, each the plan's own step.

## 3. Tests

- **`TestJSONNestedPastTheLimitIsAnError`** (root): two million levels of arrays, and
  of objects, each read inferred, with a key no column reads, into a String column,
  into a List column given, ignoring errors, and as a JSON document: an `ErrValue`
  naming the limit each time, under a 64 MB stack. A limit's worth of arrays inside
  the line's object is one too many; one fewer reads. Before the change the test
  binary ended with `fatal error: stack overflow`.

**Teeth: 5 of 7 bite:** each of `object`'s, `array`'s, `skip`'s two and
`appendNested`'s counts taken out ends the test binary by the overflow; `object`'s and
`skip`'s object branch bit once the test nested objects too. Silent: the list
builder's and the struct builder's counts, whose recursion a schema bounds, there for
the limit's meaning, not for the stack.

**Gate:** the NDJSON package's tests and the root package's NDJSON, JSON and panic
tests, vet and levels; CI's every width, the scalar build and the race detector on the
pull request.
