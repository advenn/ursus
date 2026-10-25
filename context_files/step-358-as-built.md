# Step 358 — as built

**CSV rows and lines skipped, and quoting off:** Polars' `skip_lines`,
`skip_rows_after_header` and `quote_char=None`. `eol_char` is left.

## 1. How

Three fields of `csv.Options`:

- **`SkipLines`** wraps a stream in a `lineSkipper` before its BOM is looked for.
  It discards whole lines by their `\n` alone, so a quote in them is any byte, as
  Polars' `skip_lines` reads them. The scanner's line count starts past them, so an
  error names the file's physical line. `SkipRows` and `SkipLines` are refused
  together, as Polars refuses them: the schema's `once` checks, before a given
  schema returns.
- **`SkipRowsAfterHeader`** skips records after each stream's header (`openNext`),
  or at its start where there is no header, and the inference skips the same
  records. A blank line counts as a record: inference now sets the scanner's width
  after the header, as the reader does, so both skip the same records.
- **`NoQuote`** reads a quote as any byte. It is a field of the scanner and the
  splitter (`noQuote`, so a scanner or a splitter made by hand still quotes). The
  scanner's field loop, the exact splitter's `recordEnd`, and its scan of unquoted
  fields for a quote that opens the next one all skip quotes. The UTF-8 span trusts
  any quote byte then.

At the root: `WithSkipLines`, `WithSkipRowsAfterHeader` and `WithQuoting(false)`.

| Polars 1.44.1 | ursus |
| --- | --- |
| `skip_rows_after_header=1`, `=5`, after `skip_rows=1`, without a header | as Polars |
| `skip_rows_after_header` over a blank line, a quoted record, in each of two files | as Polars |
| `skip_lines` over a quote, a comment, CRLF, no header, in each of two files | as Polars |
| `skip_lines` and `skip_rows` both | refused, as Polars |
| `skip_lines` past the end | refused, as Polars; with `raise_if_empty=False` no columns, as Polars |
| `quote_char=None` of `"x,1`, `"x","y"`, a quote's line end, `""`, doubled quotes, `"x,y"`, `"1"` | as Polars |
| `skip_rows=2` of `junk "q` then a line | refused as empty; skipped here (§9) |

## 2. Tests

- **`TestCSVSkipAndQuoting`,** on one thread and on four, Polars' answers:
  - 23 reads;
  - two files skipping each their own rows and their own lines;
  - two refusals;
  - `skip_lines` past the end under `raise_if_empty=False`;
  - a bad value's physical line after skipped lines.
- **`TestTheSplitterReadsQuotesAsBytesWithQuotingOff`:** the exact splitter cuts a
  record at each line end of quoted text at every block size, and `newSplitter`
  takes the scanner's quoting.
- **`TestParallelReadsAsSerialDoes`** gains quoting off over a quote's line end, and
  lines and rows skipped.

**Teeth: 14 of 14 bite.**

- **Rows after the header:** not skipped in a read, or in inference; inference's
  blank lines not records.
- **Lines:**
  - not skipped, or one too few;
  - the rest of a read dropped;
  - the skipped lines not counted;
  - both skips taken.
- **Quoting:**
  - a quote read in a record;
  - quoting never off, through a buffer or over a block;
  - a quote followed in the exact splitter's record, or after a separator;
  - the splitter's quoting not taken from its scanner.

## 3. Performance

The field loop gains one test of a flag. A million rows of four columns, against
step 357 built alike, alternated nine times each way while GoLand and PyCharm were
indexing: text, 8 threads, 104.2 ms against 100.7, and 103.5 against 103.6; ASCII
102.1 against 101.8; text on 1 thread 204.3 against 201.3. Level.

**Gate:** test-all 140 ok, race 28 ok (the root package 611 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
