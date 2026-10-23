# Step 333 — as built

**JSON written,** as Polars writes it (`polars-parity.md` §5): `WriteNDJSON`,
`WriteJSON` and `SinkNDJSON`, each value in Polars' own text, to the byte.

## 1. The API

| ursus | Polars | |
| --- | --- | --- |
| `lf.WriteNDJSON(ctx, w, opts...)` | `write_ndjson` | a JSON object a row, a line each, streaming |
| `lf.WriteJSON(ctx, w, opts...)` | `write_json` | one array of the rows' objects; `[]` for none |
| `lf.SinkNDJSON(ctx, path, opts...)` | `sink_ndjson` | to a file, by a temporary renamed on success |

A key a column, in the columns' order. **The values, as Polars writes them:**

| type | text |
| --- | --- |
| integers, Int128, UInt64 | the number, `18446744073709551615` |
| floats | Polars' text, `1.0`, `1e+20`, `1e-7`, `-0.0`; NaN and the infinities `null` |
| String, Enum | quoted; `"` `\` and control characters escaped, UTF-8 kept, nothing escaped for HTML |
| Decimal | quoted, `"1.25"` |
| Date, Time | `"2024-01-02"`, `"03:04:05.000006"`; a year past 9999 or before 0 signed, `"+10000-01-01"` |
| Datetime | `"2024-01-02 03:04:05.123456"`, the fraction in 3, 6 or 9 digits, none when whole |
| zoned | `"2024-01-02T08:34:05.123456+05:30"`, the offset `+00:00` for UTC |
| Duration | ISO 8601's seconds, `"PT86403.000005S"`, `"-PT0.5S"`, `"P0D"` for none |
| List, Struct | an array, an object |
| a null | `null` |

**Where ursus differs:**

- **A Binary column is refused** by name, before any row is written: Polars panics,
  "not yet implemented: Writing BinaryView to JSON".

## 2. How

`jsonwrite.go`:

- **A batch at a time, in parallel:** each batch is encoded on a goroutine of its own,
  a few ahead, into a buffer of its own. One writer goroutine writes the buffers in
  the batches' order, through a 64 KiB `bufio.Writer`, so the bytes are the same
  however many encode. An array's commas between batches are the writer's.
- **The appenders:** each column's is built once a batch, and each row written in
  place: the key's quoted name, then the value.
- **The text:** `strconv` and `kernel.AppendFloatText` write the numbers; the temporal
  text is chrono's, its fixed-width fields written a digit at a time;
  `appendJSONString` escapes as JSON requires and no more.
- **A panic** in a value's text or in the caller's writer comes back as an error, as
  the engine's own would.

## 3. Tests

**`TestWriteJSON`:**

- one frame of thirteen types, every value and a null of each, as NDJSON and as
  JSON, Polars' bytes;
- a row a batch, encoded at once, the same bytes, as NDJSON and as an array;
- no rows: nothing, and `[]`;
- dates past 9999, before 0, and in year 0;
- durations of none and of a minute;
- a sink, whole; a failed sink leaving nothing;
- Binary refused.

**The goroutine contract** (`TestEveryGoroutineIsCovered`) found the writer's two `go`
statements unlisted at the pre-gate run; each now has a test of a panic on it, in
`jsonwrite_internal_test.go`:

- `TestJSONEncoderPanicIsAnError`: an Enum index past its categories, made by hand,
  panics in its text on the encoding goroutine;
- `TestJSONWriterPanicIsAnError`: a writer that panics, on the writing goroutine.

Each comes back as an error, the process alive.

## 4. Performance

Against Polars 1.44.1, a million rows of an Int64, a Float64, a short String and a
Datetime, 79 MB of text, the median of 7:

| operation | first draft | as built | Polars |
| --- | --- | --- | --- |
| `WriteNDJSON` to memory | 217.8 ms | 66.9 ms | 83.8 ms |
| `WriteJSON` to memory | 221.0 ms | 65.0 ms | 283.3 ms |

**What changed:**

- the batches encoded in parallel, where one goroutine wrote every row;
- a fixed-width field written a digit at a time, where `strconv`'s buffer, then
  padding, was a third of the time over instants.

Both now beat Polars: its `write_json` runs on one thread.

## 5. Docs

- **I/O:** a "JSON" section.
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md` §7, `polars-parity.md`.**

## 6. Teeth, all 13 biting

- NaN written as a number;
- HTML escaped;
- a newline left unescaped;
- a zoned instant in UTC;
- a zoned instant with a space;
- a duration's trailing zeros kept;
- no length as `PT0S`;
- a fraction always in nine digits;
- a signed year in five digits;
- an array's rows without their commas;
- an array's batches without their commas;
- a line without its newline;
- the batches' texts never queued for the writer.

**Gate:** test-all 130 ok, race 26 ok (the root package 556 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
