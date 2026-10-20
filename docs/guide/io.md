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

`ScanCSVGlob(pattern)` reads every file a pattern matches as one frame, in their
names' order, and `ScanCSVFiles(paths)` a list:

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

## Larger than memory

A sink streams: a query whose answer is larger than memory writes it with
`SinkParquet` or `SinkCSV`, the spilling operators under a memory limit, as
[Memory and spilling](../concepts/memory.md#a-larger-than-memory-query) shows.
