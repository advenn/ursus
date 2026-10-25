# Step 357 — as built

**The rest of Polars' `read_csv` options:** `ignore_errors`, `infer_schema=False`,
`raise_if_empty=False`, `decimal_comma` and `encoding="utf8-lossy"`. Text is checked
as UTF-8 by default, and a short record and a blank line are read as Polars reads
them. Two breaks.

## 1. How

**The options** are fields of `csv.Options`, and `newBuilder` takes the options:

- `IgnoreErrors` turns a value error into a null in each of the three paths that
  convert (`appendTo`, a block's or the serial path's, and `convert`, a staged
  batch's). Text that is not UTF-8 is refused still, as Polars refuses it.
- `NoInference` makes `classify` give a String.
- `AllowEmpty` makes `inferSchema` give a schema of no columns where a file has no
  header or no rows.
- `DecimalComma` reads a float's and a Decimal's text through `commaPoint`, which
  swaps a comma for a point and refuses a point. `classify` infers a float by the
  same rule.
- `Lossy` has `stringBuilder` replace each maximal invalid subsequence with U+FFFD
  (`appendLossy`, `maximalSubpart`), as Rust's `from_utf8_lossy` does.

A column's name that is not UTF-8 is read lossily either way, as Polars reads it.
Value errors from the staged path now share `valueErrorAt`, so all three paths give
the same hints.

**At the root:** `WithIgnoreErrors`, `WithInferSchema`, `WithRaiseIfEmpty`,
`WithDecimalComma`, `CSVEncoding` (`UTF8`, `UTF8Lossy`) and `WithEncoding`. The
writer's `WithDecimalComma` from step 354, not yet released, is renamed
`WithWriteDecimalComma`.

**The breaks:**

- **Short records and blank lines.** A record of too few fields is padded with
  nulls, as Polars pads it, where it was refused unless `TruncateRaggedLines` was
  set; that option now concerns a record of too many fields alone. A blank line,
  once the width is known, is a record of one empty field, padded to nulls; it was
  a record only in a one-column file (`scanner.parse`, and the splitter's row
  count). Before the header, a blank line is skipped, as Polars skips it.
- **UTF-8.** A String's or a Categorical's text that is not UTF-8 is refused, as
  Polars refuses it. It used to be kept as its bytes, which no text kernel reads
  rightly.

**The UTF-8 check costs nothing measurable.** Checked field by field, the read of a
million rows of two text columns went from 76 ms to 85 ms: one `utf8.Valid` call per
field. Instead:

- The scanner checks a span of whole records at once: from a record's start to the
  last line end read (`checkSpan`), whenever a record ends past the last span.
- A text builder trusts the span's verdict through a pointer (`stringBuilder.known`,
  set by `trustUTF8` from `scanner.UTF8`) and checks a field by itself only in a
  span that fails.
- The trust holds only where the separator and the quote are ASCII. Then a line
  end, a separator and a quote never fall inside a character, so a field cut from
  valid text is valid.
- A staged batch's column is checked as one in `colStage.validUTF8`: its bytes are
  valid, and no field begins with a continuation byte, which a field cut short would
  leave to the next one.

**`kernel.ValidUTF8`.** Go's `utf8.Valid` reads eight bytes of ASCII a step until
the first non-ASCII byte, and one byte a step from there on, so a 4 MB block of text
with a non-ASCII character in every word was checked at 1.6 GB/s. The kernel uses
Keiser and Lemire's lookup algorithm, as simdjson and simdutf8 do:

- it reads 32 bytes a step on AVX2, with three 16-entry tables per 128-bit group
  (`PermuteOrZeroGrouped`);
- the bytes one to three before each byte come from `ConcatPermute128Scalars` and
  `ConcatShiftBytesRightGrouped`;
- saturated subtraction marks where a third or a fourth byte is due;
- a 64-byte run of ASCII passes with one test;
- the tail is read padded with zeros, which are ASCII, so a character cut at the
  end is an error.

Other widths and builds fall back to `utf8.Valid` (`utf8_scalar.go`, and
`utf8Vector` false under GODEBUG=simd=128 or 0).

| Polars 1.44.1 | ursus |
| --- | --- |
| `ignore_errors=True` of `x` in Int64, `2020-13-45` in Date | null, as Polars |
| a record of too few fields; a blank line, in the middle or last, CRLF too | nulls, as Polars, by default |
| a record of too many fields under `ignore_errors` | refused, as Polars |
| `infer_schema=False`, with an override | Strings but the override, as Polars |
| `raise_if_empty=False` of `""`, `"\n"`, a header alone | no columns; no columns; String columns of no rows, as Polars |
| `decimal_comma` of `1,5`, `1,5e3`, `"1,5"` quoted, a Decimal(10,2) and a Float32 override | 1.5, 1500, 1.5, 1.50, 2.25, as Polars |
| `decimal_comma` of `1.5`, `1.234,5` | String, as Polars |
| `utf8-lossy` of `\xff\xfe`, `\xe2\x82A`, `€\xff` | `��`, `�A`, `€�`, as Polars |
| text not UTF-8, by default and under `ignore_errors` | refused, as Polars; the hint names `UTF8Lossy` |
| a name not UTF-8 | read with U+FFFD, as Polars |
| a file of blank lines and no header | a column of nulls, a row a line; no rows here |

## 2. Tests

- **`TestCSVReadOptions`,** on one thread and on four, Polars' answers:
  - the five options, their edge cases and their refusals;
  - short records and blank lines;
  - the staged path under a row limit;
  - a character cut across two fields, bare and quoted;
  - a separator past ASCII that falls inside a character.
- **`TestValidUTF8MatchesTheStandardLibrary`** against `utf8.Valid`:
  - 200,000 random strings rich in lead and continuation bytes;
  - text cut at every byte;
  - every invalid kind (a lone continuation, cut characters, overlongs, surrogates,
    past U+10FFFF, too many, too few) and the valid edges, after 0 to 99 bytes of
    ASCII, with ASCII past two vectors after them.
- **`TestParallelReadsAsSerialDoes`** gains blank lines and short records in three
  columns, the new options together, and a bad value after blank lines and quoted
  line ends. Those make the splitter cut exactly, and the error's row must agree.
- **Flipped:**
  - `TestScanCSVRaggedLines` and the ragged case of
    `TestScanCSVErrorsAreIdenticalAcrossThreads`, now a long record;
  - the trailing blank line of `TestScanCSVDegenerateShapesAcrossThreads`, now two
    rows;
  - `TestIOByHand`'s blank line in two columns, now a record of nulls.

**Teeth: 33 of 34 bite.**

- **Options:**
  - `ignore_errors` not read, and bad text nulled under it, in a record and in a
    stage;
  - `infer_schema` ignored;
  - an empty file and a missing header refused;
  - a comma not inferred, and not read for a Float64, a Float32 or a Decimal;
  - a point read under a comma.
- **Lossy text and names:**
  - lossy text kept as it is, a subpart one byte, lossy never asked;
  - text never checked;
  - a name kept as it is.
- **The span and the stage:**
  - a span always UTF-8, or trusted past ASCII;
  - a stage's starts unchecked, or a stage always UTF-8;
  - a staged error without its hint.
- **Short records and blank lines:**
  - a short record refused, in a record and in a stage;
  - a blank line skipped, or not counted in a block.
- **The kernel:**
  - the align's operands swapped;
  - the groups crossed wrong;
  - a third byte never due;
  - an ASCII vector after a cut;
  - a surrogate or an overlong let through;
  - the tail unread.

The silent one is the span ending at the last line end, made the buffer's end. That
is a speed-up only: a span cut inside a character fails the check, and its fields
are checked one by one.

## 3. Performance

A million rows of an integer, two text columns and a float, 42 MB, read with
`ScanCSV`, median of seven runs alternated seven times, against step 356 built alike
from `git archive`:

| | step 356 | now | Polars |
| --- | --- | --- | --- |
| text with a non-ASCII character a word, 8 threads | 76.5 ms | 77.2 | 22.3 |
| the same, 1 thread | 200.7 | 201.4 | |
| ASCII alone, 8 threads | 103.0 | 100.4 | 19.8 |

The ASCII runs were taken while GoLand and PyCharm were indexing, so both sides are
slow; they alternated.

`kernel.ValidUTF8` against `utf8.Valid`, 35 KB in cache:

- text of a non-ASCII character a word: 14.8 GB/s against 1.6;
- ASCII: 41.7 GB/s against 48.9.

The read is 3.5 times Polars' time, which is recorded in feature-gaps §8.

**Gate:** test-all 140 ok, race 28 ok (the root package 547 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
