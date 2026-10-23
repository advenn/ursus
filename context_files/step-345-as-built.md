# Step 345 — as built

**JSON read,** `polars-parity.md` §5's first row: `ScanJSON` and `ScanJSONBytes`,
Polars' `read_json`, a file of an array of objects, a row an object, or of one object,
one row. And `WithNDJSONSchemaOverrides`, Polars' `schema_overrides`, for it and for
NDJSON.

## 1. How

**Each object is made a line of NDJSON where it is read** (`ndjson.objectLines`), and
step 334's reader reads the lines: its blocks, cut at their newlines into a part a
thread and parsed at once, its inference, its casts, its projection. The bytes are
rewritten in place, in the buffer the reader reads into, and none is moved: what lies
between the objects, the array's brackets, its commas and its space, becomes space,
but for a newline after each object, and an object's newlines, which only its space
can hold, become spaces. Each line is then one object between spaces, which a line's
reading passes over, so the n-th object is the n-th line, and an error names it: "at
object 151". The document is never held whole, nor parsed into a tree.

**The scan is the read's one serial part,** so an object is read sixty-four bytes at a
time on a CPU with AVX2 (`objectVector`, `simd/archsimd`, behind the experiment's build
tag): five masks of a bit a byte, its quotes, opening and closing brackets, newlines
and backslashes; with no backslash every quote opens or closes a string, so the bits a
string spans are the quotes' running parity (`prefixXor`), flipped where the block
starts in one, and the brackets and newlines outside them are the object's; its
brackets are followed one by one only where the block holds as many closing ones as
are open, and so may end the object. A block with a backslash, and the last bytes of a
read, are scanned a byte at a time (`objectBytes`), a backslash escaping the byte after
it, in this read or the next. At `GODEBUG=simd=128` and `0`, and with the experiment
off, every object is: the tests' width matrix runs both.

**What Polars refuses is refused:** an element that is not an object, a null among
them, a comma before the array's end, text after it, an empty document; and a key the
inferred schema has not, at any depth, in a list too, projected or not
(`structBuilder.strict`, the top level's known keys the whole schema's), which a given
schema's reading skips, as Polars' does. NDJSON's reading skips it still, as Polars'
`read_ndjson` does. A UTF-8 byte-order mark is passed over. Errors are named
`read_json`; one the scan finds says at which byte.

**Schema overrides** set the named columns' types over the inferred schema or the
given one, as Polars' `read_json` sets them (its `read_ndjson` keeps a given schema's
type); a name the schema has not is refused, as Polars refuses it. Each is read as a
given schema's type is, in its natural type and cast.

| Polars 1.44.1 | ursus |
| --- | --- |
| `read_json` of `[{"a":1,"b":"x"},{"a":2,"c":true}]` | `ScanJSONBytes`: three columns, a key missing a null |
| one object, `{"a":[1,2],"b":{"x":1}}` | one row, a List and a Struct |
| `[]` | no row, no column |
| `[{}, {}]` | two rows of no column |
| `[{}]`, `{}` | no row in Polars; one in ursus (feature-gaps §9) |
| `[{"a":1}, null]`, `[[1]]`, `[{"a":1},]`, `[{"a":1}] x`, `""` | refused |
| a key past the first 100 objects, at the top or in a struct | refused: `key "b" is not in the schema, which was inferred from the first objects`, at object 151 |
| the same with `schema=` | the key skipped |
| a later `"x"` in an Int64 column | refused |
| a later `true` or `1.5` in an Int64 column | 1 in Polars; refused in ursus, as both refuse it in NDJSON (§9) |
| `schema_overrides={"a": pl.Float32}`, `{"d": pl.Date}`, nested types | `WithNDJSONSchemaOverrides(...)`: the same types |
| `schema_overrides={"z": ...}`, no such column | refused: `unknown column "z"` |

## 2. Tests

**`TestReadJSON`,** Polars 1.44.1's answers: an array of objects; spread over lines; a
byte-order mark; a key twice; text beside a number; an empty object first; one column
of three; one object; no object, and objects of no key; eleven refusals; a key past
the inferred schema, projected away, within a struct, inferred from every object, and
skipped by a given schema; a value its column cannot take; schema overrides of a
Float32, a String, a Date, nested types, over a given schema, of no column, and
NDJSON's; a file and `Explain`'s `JSON`; `WriteJSON`'s document read back.

**`TestReadJSONInParts`:** 3 MB of random objects, each indented over lines, read as
the NDJSON of the same objects is on one thread and eight and at a batch size of 777;
its first three rows; and, three quarters in, in a part past the first, an object with
a key the schema has not and a malformed one, each refused at its number, the first
skipped by a given schema. Under the race detector a third of the size.

**`TestObjectLines`, `TestObjectLinesRefuses`, `TestObjectLinesRandom`:** the scan's
lines, under reads of one byte to all of them into buffers of one byte up: strings
holding brackets, quotes and runs of backslashes, newlines among an object's spaces,
the byte-order mark, an empty array; nineteen refusals with their words and bytes;
200 random documents. **`TestObjectLinesVector`:** the vector scan equal to the byte
scan, byte for byte, over 60 random documents of long strings, escapes, brackets in
strings, nesting, newlines, and a newline in a string, which JSON refuses, read whole
and in random reads; skipped where there is no vector scan.

**Teeth (25 of 25 bite),** each run where it can bite, the byte scan's at
`GODEBUG=simd=128` too: a newline between objects kept; no newline after a lone object
at the end; a comma before the array's end let through; text after the document let
through; a byte-order mark not passed over; a backslash not escaping the byte after it;
an escape across reads forgotten; an object's newline kept; a bracket in a string
counted; a key past the inferred schema skipped; the top level's keys not the
schema's; a struct within not strict; overrides not over a given schema; an override
of no column let through; errors named `read_ndjson`; an error's place a line; a JSON
scan named NDJSON; and the vector scan's: a block begun in a string read as begun
outside; a newline in a string made a space; a bracket in a string counted; an end
missed where the closing brackets are as many as open; the depth not carried past a
block; a string not carried past a block; a block of backslashes read by vector (first
drawn so that it did not build, and redrawn); the newlines before an object's end
left. Two teeth of the first draft's scan, which counted runs of backslashes, went
with it.

## 3. Performance

Half a million objects of five keys, `{"id", "name", "score", "tags", "ok"}`: 44 MB
as Python's `json.dump` writes them, 63 MB indented; eight threads, the median of
seven, against Polars 1.44.1:

| | ursus | Polars |
| --- | --- | --- |
| `ScanJSON` | 66.9 ms | 321.8 |
| indented | 81.9 | 326.4 |
| one column | 44.0 | 343.8 |
| `ScanNDJSON` of the same rows | 57.0 | 153.5 |

The scan alone reads 2.5 GB/s, 17.4 ms of the 44 MB, where a byte at a time reads 820
MB/s, 53.5 ms. Before the vector scan the read took 112.8 ms on eight threads, bound by
it, and 143.7 on one; a scan of eight bytes a word at a time, tried first, was slower
than a byte at a time, the next quote or bracket nearer than eight bytes in most JSON.

**Gate:** test-all 135 ok, race 27 ok (the root package 692 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
