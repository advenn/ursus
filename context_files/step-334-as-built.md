# Step 334 — as built

**NDJSON read,** as Polars reads it (`polars-parity.md` §5): `ScanNDJSON` and its
several-file and in-memory forms, the types inferred as Polars infers them, and read
faster than Polars.

## 1. The API

| ursus | Polars | |
| --- | --- | --- |
| `ScanNDJSON(path, opts...)` | `scan_ndjson`, `read_ndjson` | a JSON object a line |
| `ScanNDJSONFiles(paths, opts...)` | `scan_ndjson([...])` | in order, the schema the first file's |
| `ScanNDJSONGlob(pattern, opts...)` | `scan_ndjson("*.ndjson")` | the matches sorted by name |
| `ScanNDJSONFrom(files, opts...)` | | files the caller opens, as `ScanCSVFrom` |
| `ScanNDJSONBytes(b, name, opts...)` | `read_ndjson(BytesIO)` | in memory |
| `WithNDJSONSchema(s)` | `schema=` | no line read to infer; any type, cast from what JSON gives |
| `WithInferSchemaLines(n)` | `infer_schema_length=` | 100 unless set; 0 every line |
| `WithNDJSONIgnoreErrors(b)` | `ignore_errors=` | a value its column cannot take a null |

**Inference, as Polars', probed case by case:**

- a key a column, in the order first seen; a key first seen past the inferred lines
  skipped, as Polars skips it; a key missing a null; a key twice its first value;
  a line of spaces, tabs and carriage returns skipped, a form feed refused;
- an integer Int64, past it Int128; a fraction or an exponent Float64; a number with a
  leading zero refused;
- **a key across lines** (`meet`): Int64 and Float64 Float64, Int64 and Int128 Int128,
  **text and anything String** (a List or a Struct too), two Lists their elements',
  two Structs their fields; a Boolean and a number, or a List or a Struct and a number
  or a Boolean, refused;
- **an array's elements** (`coerce`, by Polars' own rules, not `meet`'s): the distinct
  types in the order first seen; all Structs merged field by field, all Lists their
  elements coerced; two types: a value and a List a List, Float64 and Int64 Float64,
  **Int64 and Boolean Int64**, a null the other, anything else String; more than two a
  pair at a time in order, so `[1, true, 2.5]` is Float64 (and then refuses the
  `true`) and `[2.5, true, 1]` String; Int128 and Int64 String.

**A String column's text, as Polars writes it:** text as itself; a number as Rust
prints it, `1.5` for `1.50`, `1000` for `1e3`, `0.0000001` for `1E-7`, `0` for `-0`,
`-0` for `-0.0`, an integer past 64 bits as written; an array or an object as JSON with
`", "` and `": "`, its strings' `"`, newline and carriage return escaped and nothing
else, not even a backslash, its keys not escaped at all.

**Ignore errors, as Polars':** a value its column cannot take a null, a list's element
too; a Struct column's value that is not an object a row of nulls, not a null; a cast
from text non-strict. A line that is not JSON, or not an object, is still refused.

**A given schema** is read in each type's natural JSON type and cast: a narrower
integer as Int64, UInt64 as Int128, Float32 as Float64, a Date, a Datetime, a Time, a
Decimal or an Enum as its text. A refusal names the declared type, "cannot parse 'a' (a
string) as Int32", without inference's hint.

**Errors** name the line, blank ones counted, and the key: `at line 150001, key o.x`,
and the file when there are several, `at line 2 of b.ndjson, key a`. A cast's refusal
is found by a lossy cast's new nulls and placed at its line.

**Where ursus differs** (`feature-gaps.md` §9): a lone surrogate's escape is U+FFFD,
where Polars reads a high one as NUL and refuses a low one; `1e400` is ±Inf, which
Polars refuses; a given Int8, Int16, UInt8, UInt16, Time, Duration, Decimal or Binary
reads, where Polars panics or refuses; errors say where.

## 2. How

`internal/source/ndjson` (level 45):

- **`source.go`:** a `Source` over `File`s, each opened with the query's context;
  inference reads the first file's first lines, recovering a panic inside its
  `sync.Once`. The reader reads a **block** of whole lines (1 MiB first, doubling to
  4 MiB a thread, a line longer than a block growing it), cuts it at newlines into a
  **part a thread** of 64 KiB at least, and parses the parts at once (`wg.Go`, each
  recovering a panic), **the next block read meanwhile**; the batches go out in the
  file's order, sliced to the batch size, and stop at `MaxRows`. A part's error comes
  back with its line in the part, placed after the earlier parts' lines are counted.
- **`build.go`:** a builder a column, of the declared type's natural type, **decoding
  each value straight out of the line**, no tree: fixed slices sized to the part's
  lines, strings into offsets and characters taken by `NewStringOwned`, nulls kept as
  their rows and made a bitmap at the end. An object's k-th key is matched first
  against the last object's k-th, **byte for byte in the line** when it has no escape,
  then a map.
- **`value.go`:** the parser: a tree of values for inference and an error's text; key,
  string, number and skip scanners; `appendText` and `appendNested` for a String
  column; `refuse`, or `errNull` when ignoring errors.
- **`float.go`:** `scanFloat` reads a number's digits once, then Clinger's exact case
  or **Eisel-Lemire**, as strconv runs them, over a table of 128-bit powers of ten
  worked out at init with `math/big`; strconv itself for more than 19 digits that
  matter or a halfway case.
- **`infer.go`:** `typeOf`, `meet`, `coerce`, `inferSchema` with each line's number.

## 3. Tests

**`TestReadNDJSON`,** every answer Polars 1.44.1's: a column a key; the types met
across lines and within arrays (twelve cases); a String column's text of numbers and
nested values; escapes and surrogates; refusals with their lines; a given schema of
Float64, Date, Int32, UInt64, Float32 and an Enum, and its refusals; errors ignored
(Int64, Boolean, List, Struct, Date) and a malformed line still refused; several files
by list, glob and opener, and a later file's error by its name; a file in batches, its
columns pushed down, and `Head`.

**`TestReadNDJSONInParts`:** 3 MiB of random lines (nine keys of every family, escapes,
text not UTF-8, space anywhere, CRLF, blank lines, keys no schema has holding any
JSON) read as `encoding/json` reads each line, on one thread and on eight and in
batches of 777; an error at three quarters of the file, of four kinds, by its line; a
line of 9 MiB.

**Internal:** blocks under readers that hand back a byte at a time or half, cut at
newlines, a read's error; parts; a part's panic and inference's panic as errors;
**`TestScanFloatIsParseFloat`,** over 1.2 million numbers, bit for bit strconv's.

**Teeth (29 of 30 bite):**

- a key's later value read;
- a missing key no null;
- the expected key matched without its closing quote;
- a stale field for a key;
- nulls valid;
- ignored errors not ignored;
- a struct's non-object a null;
- the cast strict when ignoring;
- a number's text as written;
- a nested backslash escaped;
- text and a list no type in common;
- an integer and a Boolean as text;
- elements met last first;
- elements met as keys are;
- a part's lines numbered from its own start;
- blank lines not counted;
- a cast's row as its line;
- the schema from the last file;
- the read-ahead block dropped;
- a block's parts out of order;
- a block cut mid-line;
- a leading zero read;
- a float's leading zero read;
- a float's digits past 19 trusted;
- a halfway case trusted;
- Clinger's case past 2^53;
- the wider product skipped;
- the table's negative powers a bit short;
- the table read a power off.

**Silent:** the table's negative powers rounded up by one in their last bit.
Eisel-Lemire is built for a table rounded down, and its checks absorb an error of one
in the last of 128 bits: no input among the 1.2 million tells the two apart.

## 4. Performance

Against Polars 1.44.1, the median of seven, eight threads:

| | ursus | Polars |
| --- | --- | --- |
| a million lines of four keys (Int64, Float64, String, Boolean), 55 MB | 70.3 ms | 78.5 ms |
| the same, one column summed | 45.8 ms | 68.4 ms |
| the same, `Head(5)` | 1.7 ms | 14.1 ms |
| 300,000 nested lines (a List of text, a Struct, escapes), 46 MB | 59.9 ms | 137.4 ms |
| the same, a Struct's field summed | 35.0 ms | 91.3 ms |

The first draft built a tree of values a line, 534 ms; decoding straight into the
columns made it 101, and the float scan, the key match in the line and the read-ahead
70.

**Gate:** test-all 135 ok, race 27 ok (the root package 534 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
