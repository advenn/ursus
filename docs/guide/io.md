---
description: >-
  Reading and writing CSV and Parquet with ursus, the Go dataframe library: scan
  options, globs, readers from memory or object storage, projection and predicate
  pushdown into Parquet, streaming sinks, and Arrow in and out.
---

# CSV, Parquet and Arrow

Every reader is a scan: it gives a `LazyFrame`, and reads only when the query runs,
only the columns the query uses.

## CSV

`ScanCSV(path, options...)` reads a file, and `ScanCSVReader(bytes, name)` text already
in memory. The types are inferred from the first rows, and the options say the rest:

--8<-- "Example_ioCSVOptions.md"

| Option | For |
|---|---|
| `WithSeparator(c)`, `WithQuote(c)`, `WithComment(prefix)` | the dialect |
| `WithHasHeader(b)`, `WithColumnNames(names...)`, `WithSkipRows(n)` | the header |
| `WithSchema(s)`, `WithSchemaOverrides(m)`, `WithInferRows(n)` | the types |
| `WithNullValues(vals...)` | the text that means null |
| `WithTruncateRaggedLines(b)`, `WithMaxRecordSize(n)` | malformed input |

*New in 0.6.7:* a file compressed by gzip, zlib or Zstandard is read as itself,
known by its first bytes whatever its name, as Polars reads one:
`ScanCSV("sales.csv.gz")`. It is decompressed as it is read, never held whole, and
parsed in parallel as a plain file is. NDJSON and JSON files are read so too.

`ScanCSVGlob(pattern)` reads every file a pattern matches as one frame, in their
names' order, and `ScanCSVFiles(paths)` a list. *New in 0.6.7:* a pattern's `**`
matches any number of directories, `logs/**/*.csv`, in every scan's glob, and
`ScanCSV(dir)` reads every file below a directory, as `ScanNDJSON(dir)` and
`ScanParquet(dir)` do:

--8<-- "Example_ioCSVGlob.md"

`WriteCSV(ctx, w, options...)` writes a query's answer to any `io.Writer`, and
`SinkCSV(ctx, path)` to a file:

--8<-- "Example_ioCSVWrite.md"

## Parquet

`ScanParquet(path)` reads the footer when the query is planned, and at run time only
the column chunks the query needs. A filter on a column with statistics skips whole
row groups that cannot hold a match:

--8<-- "Example_ioParquet.md"

`projection` is the columns read, two of six, and `predicate` the filter the reader
tests against each row group's statistics, skipping every row group that cannot hold
a match.

- `ScanParquetGlob(pattern)`, `ScanParquetFiles(paths)` and `ScanParquetBytes(b, name)`
  read several files, or bytes in memory;
- List and Struct columns are read and written, in the encoding PyArrow and Polars use;
- `WriteParquet(ctx, w)` and `SinkParquet(ctx, path)` write, with `WithCompression`,
  Snappy by default, `WithRowGroupRows` and `WithStatistics`.

*New in 0.6.7:* `ScanParquet(dir)` reads every file below a directory as one frame,
as Polars' `scan_parquet` does, and reads a hive dataset's partitions: each directory
named `key=value` gives the rows of the files beneath it a column `key`, after the
files' own, its type inferred from every file's value as Polars infers it, an integer,
a float, a Boolean, a date or an instant, or text. `__HIVE_DEFAULT_PARTITION__` is a
null. A filter on the keys skips the files it rules out before any is opened.
`WithHiveSchema(s)` gives keys their types, `WithHiveDates(false)` reads dates as
text, and `WithHivePartitioning(true)` reads the keys of a file's, a list's or a
glob's whole paths, where by default only a directory's are read.
`WithIncludeFilePaths(name)` adds a column of each row's file path.

*New in 0.6.7:* files whose columns differ are read as one, as Polars' schema
evolution reads them: as the first file's columns, or `WithParquetSchema(s)`'s; a
column a file lacks as nulls with `WithInsertMissingColumns(true)`; a column a file has
beyond them passed over with `WithIgnoreExtraColumns(true)`; and an Int32 where the
schema's is an Int64, or a Float32 where it is a Float64, widened with
`WithUpcastIntegers(true)` or `WithUpcastFloats(true)`. Each is refused otherwise, as
Polars refuses it, naming the file and the column.

## From object storage

ursus has no S3 or GCS client of its own: each store wants its vendor's SDK, and ursus
takes on none. `ScanParquetFrom` reads through any `io.ReaderAt`
you open instead, so a `ReadAt` over ranged GETs reaches any store, and only the
footer and the needed column chunks travel:

--8<-- "Example_ioParquetFrom.md"

`ScanCSVFrom` takes a stream the same way, and the writers take any `io.Writer`, an
upload included.

## Arrow

`df.Record()` exports a frame as an Arrow record without copying, and
`lf.CollectRecords(ctx)` streams records. `ScanArrowRecords(records...)` imports
records, and `ScanArrow(fn)` any `array.RecordReader`: an IPC stream, Flight, the C
Data Interface. Import copies, so nothing released afterwards reaches a frame.

--8<-- "Example_ioArrow.md"

38 of Arrow's 45 types map to ursus's; Decimal256, intervals, unions and run-end
encoding are refused by name.

*New in 0.6.7:* Arrow IPC, as Polars reads and writes it. `ScanIPC(path)` reads an IPC
file, Feather version 2, the format of Polars' `write_ipc`, and `ScanIPCStream(path)`
an IPC stream, `write_ipc_stream`'s; both read string views, large lists and
compressed buffers, which Polars writes. `WriteIPC(ctx, w)`, `WriteIPCStream(ctx, w)`
and `SinkIPC(ctx, path)` write them, uncompressed as Polars' are by default, or with
`WithIPCCompression(ursus.IPCLZ4)` or `IPCZstd`. `ReadIPCSchema(path)` and
`ReadParquetSchema(ctx, path)` read a file's schema from its footer alone.

## JSON

*New in 0.6.7:* `ScanNDJSON(path)` reads newline-delimited JSON, an object a line, as
Polars' `scan_ndjson` and `read_ndjson`. `ScanNDJSONFiles(paths)` and
`ScanNDJSONGlob(pattern)` read several files as one frame, the schema the first's;
`ScanNDJSONFrom(files)` reads files you open, from an object store, say; and
`ScanNDJSONBytes(b, name)` reads NDJSON in memory. A key is a column, in the order the
keys are first seen, and its type is inferred from the first 100 lines as Polars infers
it: an integer is an Int64, or an Int128 past it; a number with a fraction or an
exponent a Float64; an array a List, and an object a Struct. A key holding text and
anything else is a String, the other value as Polars writes it: `1.5` for `1.50`, and
an array as JSON. `WithInferSchemaLines(n)` infers from more lines, 0 from all of them;
`WithNDJSONSchema(s)` reads the lines as a schema you give, of any type, a Date from
its text; and `WithNDJSONIgnoreErrors(true)` reads a value its column cannot take as a
null, as Polars' `ignore_errors`. An error names the line and the key. A file is read
in blocks, each cut into a part a thread and the parts parsed at once, and only the
columns a query uses are built.

*New in 0.6.7:* `ScanJSON(path)` reads a JSON file of an array of objects, a row an
object, or of one object, one row, as Polars' `read_json`, and `ScanJSONBytes(b, name)`
one in memory. Each object is read as a line of NDJSON is, under the same options, its
schema inferred from the first 100 objects. As Polars, it refuses a key those objects
have not, which NDJSON's reading skips, as a given schema's does here; an element that
is not an object, or is null; and text after the array. An error names the object, and
the file is streamed: each object is made a line where it is read, and the lines are
parsed in parallel, so the document is never held whole. For both,
`WithNDJSONSchemaOverrides(m)` gives some columns' types and infers the rest, as
Polars' `schema_overrides`.

*New in 0.6.7:* `WriteNDJSON(ctx, w)` writes each row as a JSON object on a line of its
own, as Polars' `write_ndjson`, and `WriteJSON(ctx, w)` one array of them, as
`write_json`; `SinkNDJSON(ctx, path)` streams to a file, whole or not at all. Each
value is written as Polars writes it: a float in Polars' text, NaN and the infinities
`null`; a Decimal, a date and an instant quoted, `"2024-01-02 03:04:05.123456"`, a
zoned instant with its offset, `"2024-01-02T03:04:05+05:00"`; a duration in ISO 8601's
seconds, `"PT90S"`; a List an array and a Struct an object. A Binary column is refused,
as Polars refuses it.

## Larger than memory

A sink streams: a query whose answer is larger than memory writes it with
`SinkParquet` or `SinkCSV`, the spilling operators under a memory limit, as
[Memory and spilling](../concepts/memory.md#a-larger-than-memory-query) shows.
