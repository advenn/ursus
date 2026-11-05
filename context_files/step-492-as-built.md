# Step 492 — as built

**`ScanLines` and its spellings, Polars 2.x's `scan_lines` and `read_lines`: a file's
lines as one String column.** The last of the five features step 487 chose against
Polars 2.1: logs, word lists, any text read a row a line.

## 1. The change

- **The public API,** `linesread.go`, the spellings NDJSON's reader has:
  - `ScanLines(path string, opts ...LinesOption) *LazyFrame`, a file or a directory;
  - `ScanLinesFiles(paths []string, opts ...LinesOption) *LazyFrame`;
  - `ScanLinesGlob(pattern string, opts ...LinesOption) *LazyFrame`;
  - `ScanLinesBytes(b []byte, name string, opts ...LinesOption) *LazyFrame`;
  - `ScanLinesFrom(files []LinesFile, opts ...LinesOption) *LazyFrame` and
    `type LinesFile struct{ Name string; Open func(ctx) (io.ReadCloser, error) }`;
  - `ScanLinesFS(fsys fs.FS, pattern string, opts ...LinesOption) *LazyFrame`;
  - `type LinesOption`, `WithLinesName(name string)`, Polars' `name=`, "line" by
    default, and `WithLinesIncludeFilePaths(name string)`, its `include_file_paths`.
- **Not built:** an eager `ReadLines`. No format here has one, `Collect` is each
  one's; `n_rows` and `row_index_name` are `Head` and `WithRowIndex`, as for every
  scan; `glob=` is `ScanLinesGlob`.
- **The source is NDJSON's,** a mode of `internal/source/ndjson`, as JSON documents
  are (step 345): `Options.Lines` and `LineName`. So a scan of lines has what that
  reader has: blocks of whole lines, the first 1 MiB and each next twice the last,
  cut into a part a thread and parsed at once, the next block read meanwhile; the
  query's cancellation between blocks; `Head`'s first rows read and little more; a
  count that builds no column (`RowsOnly`); gzip, zlib and Zstandard decompressed;
  a path column; several files, each its own last line. NDJSON's source holds no
  memory of the budget's, and neither does this: the engine charges the batches.
- **`reader.lines`:** a part's lines, cut at each newline with one "\r" before it
  dropped, as `bufio.ScanLines` cuts them, into offsets and characters,
  `data.NewStringOwned`. The part is checked as UTF-8 at once; a line of one that
  is not is refused, "the line is not UTF-8 at line 2". A count reads no text, and
  checks none.
- **Errors** are `read_lines`'s, and the plan's node `LINES SCAN`.

## 2. Polars 2.1.0's answers

| bytes | lines |
| --- | --- |
| `a\nb\nc\n`, `a\nb\nc` | a, b, c |
| empty | none |
| `\n`, `\n\n` | one empty line, two |
| `a\r\nb\r\n`, `a\r\nb` | a, b |
| `a\rb\n` | `a\rb` |
| `a\r` | a |
| `a\r\r\n` | `a\r` |
| `a\n\nb\n\n` | a, empty, b, empty |
| a UTF-8 byte-order mark, then `a\nb\n` | the mark kept: `﻿a`, b |
| `a\x00b\n`, `  a  \n\t\n` | as they are |
| `a\xff\nb\n` | refused, "invalid utf8"; its count 2 |

- The column is `line`, a String; `name=` names it, `""` too.
- A directory's files are read in order, each file's last line its own; a gzip file
  is decompressed; a path column of the lines' name is refused, as is a missing file.

ursus answers each as Polars does.

## 3. Tests

`lines_test.go`, `TestScanLines`: the table above, the schema, the name, the
refusal and the count of text not UTF-8; a directory with its paths, files in their
order, a glob, gzip, an `fs.FS` and openers; the two refusals. And the oracle,
`bufio.Scanner`: 3 MB of generated lines past the first blocks of 1 and 2 MiB, a
line of 2 MiB among them, "\r\n" ends, lone "\r"s, empty lines, text of four
widths, and a last line without its newline, read on one thread, on four in batches
of 1,000 and in its own; its first five rows; its count; and a sort of its 75,469
lines under a 256 KiB limit, which spills five files.

## 4. Teeth: 4 of 4 bite

| mutation | `TestScanLines` |
| --- | --- |
| a last line without a newline lost | **fails:** `a\nb\nc` is a, b; Polars a, b, c |
| the "\r" before a newline kept | **fails:** `a\r\nb\r\n` is `a\r`, `b\r` |
| every "\r" before a newline dropped | **fails:** `a\r\r\n` is a, Polars `a\r` |
| UTF-8 not checked | **fails:** `ok\n\xff\n` answers, Polars refuses it |

**Gate:** `./internal/...`, `./dtype/...`, `./i128/...` and the root package in full,
and vet.
