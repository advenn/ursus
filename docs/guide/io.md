---
description: >-
  Reading and writing CSV and Parquet with ursus, the Go dataframe library: scan
  options, globs, readers from memory or object storage, projection and predicate
  pushdown into Parquet, streaming sinks, Arrow in and out, JSON, and databases
  through database/sql.
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
| `WithSeparator(c)`, `WithQuote(c)`, `WithQuoting(b)`, `WithComment(prefix)` | the dialect |
| `WithHasHeader(b)`, `WithColumnNames(names...)`, `WithSkipRows(n)`, `WithSkipLines(n)`, `WithSkipRowsAfterHeader(n)` | the header and the rows skipped |
| `WithSchema(s)`, `WithSchemaOverrides(m)`, `WithInferRows(n)`, `WithInferSchema(b)` | the types |
| `WithNullValues(vals...)` | the text that means null |
| `WithTruncateRaggedLines(b)`, `WithMaxRecordSize(n)`, `WithIgnoreErrors(b)`, `WithRaiseIfEmpty(b)` | malformed input |
| `WithTryParseDates(b)` | dates, instants and times of day inferred (new in 0.6.7) |
| `WithDecimalComma(b)`, `WithEncoding(e)` | a float's comma, and text not UTF-8 (new in 0.6.7) |

*New in 0.6.7:* `WithTryParseDates(true)` infers a column of dates as a Date, of
instants as a Datetime in microseconds, in UTC where they carry an offset, and of
times of day as a Time, by the patterns Polars' `try_parse_dates` reads:
`2024-01-02`, `2024/1/2` and `02.01.2024`, the year first or the day; `2024-01-02
03:04:05.123`, a `T` or a space between; offsets `Z`, `+09:00` and `+0900`. A month
first is not one: `01/02/2024` is the first of February. A Date, Datetime or Time
column a schema gives is read by the same patterns, scanned by hand, three times as
fast as Polars reads one.

*New in 0.6.7:* the rest of Polars' reading options.

- `WithIgnoreErrors(true)`, as `ignore_errors`, reads a value its column's type
  cannot take as a null. A record of too many fields is still refused, as Polars
  refuses it.
- `WithInferSchema(false)`, as `infer_schema=False`, reads every column a schema or
  an override does not type as a String.
- `WithRaiseIfEmpty(false)`, as `raise_if_empty=False`, reads an empty file as a
  frame of no columns, where by default it is refused.
- `WithDecimalComma(true)`, as `decimal_comma`, reads `1,5` as 1.5, a float's or a
  Decimal's; a value with a point is then no number.
- `WithEncoding(ursus.UTF8Lossy)`, as `encoding="utf8-lossy"`, reads each invalid
  UTF-8 sequence as U+FFFD. Without it, text that is not UTF-8 is refused, as Polars
  refuses it. The check reads thirty-two bytes a step and costs a read nothing
  measurable.

*New in 0.6.7:* `WithSkipLines(n)`, as `skip_lines`, skips the first lines by their
line ends alone, a quote in them any byte, where `WithSkipRows(n)` skips records;
the two are not both set. `WithSkipRowsAfterHeader(n)`, as `skip_rows_after_header`,
skips the records after the header, a blank line one. Both skip in each file of
several. `WithQuoting(false)`, as `quote_char=None`, reads a quote as any byte, so a
separator or a line end always ends a field.

*Changed in 0.6.7:* a record of too few fields is padded with nulls, and a blank
line after the header is a record of nulls, as Polars reads them; both were
refused or skipped. `WithTruncateRaggedLines(true)` now concerns a record of too
many fields alone, whose extra fields it drops.

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

*New in 0.6.7:* `WithCSVIncludeFilePaths(name)`, as Polars' `include_file_paths`,
adds a String column of each row's file path after the files' columns: the path as
it was given, or as a glob or a directory found it; `WithNDJSONIncludeFilePaths(name)`
does so for NDJSON. A column the files have already of the name is refused, as Polars
refuses it. Each file's path is repeated once and sliced for every batch, so the
column costs a scan nothing measurable.

`WriteCSV(ctx, w, options...)` writes a query's answer to any `io.Writer`, and
`SinkCSV(ctx, path)` to a file:

--8<-- "Example_ioCSVWrite.md"

*New in 0.6.7:* the writer takes Polars' `write_csv` options:

| Option | Polars' | For |
|---|---|---|
| `WithQuoteStyle(ursus.QuoteAlways)`, `QuoteNever`, `QuoteNonNumeric`; `WithWriteQuote(c)` | `quote_style`, `quote_char` | when a field is quoted, and with what |
| `WithDateFormat(f)`, `WithDatetimeFormat(f)`, `WithTimeFormat(f)` | `date_format`, `datetime_format`, `time_format` | a temporal value's strftime format |
| `WithFloatPrecision(n)`, `WithFloatScientific(b)`, `WithWriteDecimalComma(b)` | `float_precision`, `float_scientific`, `decimal_comma` | a float's text |
| `WithIncludeBOM(b)` | `include_bom` | a UTF-8 byte-order mark first |
| `WithCSVCompression(ursus.CSVGzip)`, `CSVZstd`; `WithCSVCompressionLevel(n)`; `WithCSVCheckExtension(b)` | `compression`, `compression_level`, `check_extension` | the file compressed, its path's name checked |

Each value is written as Polars writes it: an instant `2024-01-02T03:04:05.123456`, to
its unit, with its offset `+0900` where it has a zone, a time of day to the
nanosecond, and a float in Polars' text, `123456789.0` and `1e-7`. `ScanCSV` reads
each back.

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

*New in 0.6.8:* a hive dataset written, as Polars' `partition_by` writes one.
`SinkParquet(ctx, dir, ursus.WithPartitionBy("year", "month"))` writes each key's rows
to the directory `year=2024/month=1` below `dir`, in files numbered from `00000000`;
`SinkCSV`, `SinkIPC` and `SinkNDJSON` take the same options. A value is escaped as
Polars escapes it and a null is `__HIVE_DEFAULT_PARTITION__`, so each reads the
other's dataset. `WithPartitionIncludeKey(false)` leaves the keys out of the files, as
`include_key=False`, and `WithMaxRowsPerFile(n)` ends a file at n rows, with keys or
without. The rows stream: each file is written under a temporary name and renamed as
it ends.

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

*New in 0.6.7:* several IPC files as one frame, as Polars' `scan_ipc` reads them.
`ScanIPCFiles(paths)` reads a list and `ScanIPCGlob(pattern)` a glob's matches, as
the first file's columns: a later file's are read by name, and one it lacks, one more
or one of another type is refused, as Polars refuses them. `ScanIPC(dir)` reads every
file below a directory as a hive dataset, each `key=value` directory a column after
the files' own, of the type its values infer; a filter on the keys skips the files it
rules out before any is opened. `WithIPCHivePartitioning`, `WithIPCHiveSchema`,
`WithIPCHiveDates` and `WithIPCIncludeFilePaths` are Polars' `hive_partitioning`,
`hive_schema`, `try_parse_hive_dates` and `include_file_paths`.

*New in 0.6.8:* a String or a Binary column of more than 2 GiB of characters, as a
`Collect`, a sort or a join of much text makes, is held, where it was refused. It
exports as Arrow's LargeString or LargeBinary, and below 2 GiB as a String or a
Binary, where Polars always exports the large type. A batch a reader reads, a Parquet
column chunk's, a CSV block's or an Arrow record's rows, still holds at most 2 GiB of
text a column, and past it the read says to read in smaller batches.

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

## Databases

*New in 0.6.7:* `ReadDatabase(ctx, db, query, args...)` runs a query on a `*sql.DB`, a
`*sql.Tx` or a `*sql.Conn` and reads its rows as a frame, as Polars' `read_database`;
`ReadDatabaseRows(rows)` reads a result you have, a prepared statement's, say, and
closes it. Any `database/sql` driver serves. A column's type is its values', met as
Polars meets them: integers an Int64, floats a Float64, text a String, bytes a Binary,
and instants a Datetime in microseconds, of no zone where each is in UTC, as a driver
gives a timestamp without one, and in UTC where one is in a zone; an integer beside a
float is a Float64, text beside a number text, and bytes beside an integer an error.
Every row is read for it, where Polars reads 100. Then a column is of the type its
driver names, where every value fits it: an `INT4` an Int32, a `NUMERIC(10, 2)` a
Decimal(10, 2), a `DATE` a Date, a `TIME` a Time, a `TIMESTAMPTZ` a Datetime in UTC,
and the bytes a driver gives for a `VARCHAR` or a `UUID` text. A result of no rows has
its driver's types, and a column the driver calls `NOT NULL` is not nullable.
`ReadDatabaseRows(rows, ursus.WithDatabaseSchemaOverrides(m))` gives some columns'
types, as Polars' `schema_overrides`. The frame is read whole, as Polars' is.

*New in 0.6.8:* `WriteDatabase(ctx, db, table)` writes a query's rows, or a frame's,
to a table, as Polars' `write_database`, and answers how many it wrote. A table
already there is refused, replaced or appended to, as
`WithIfTableExists(ursus.IfExistsFail)`, `IfExistsReplace` or `IfExistsAppend` says,
Polars' `if_table_exists`; one that is not is created, of the types `ReadDatabase`
reads back. The dialect, SQLite, PostgreSQL, MySQL or SQL Server, is named by the
driver, or by `WithDialect(ursus.DialectPostgres)` and its kin. The rows go in
INSERTs of as many as the dialect's parameters allow, in one transaction: a
`*sql.DB`'s or a `*sql.Conn`'s own, or your `*sql.Tx`.

## Larger than memory

A sink streams: a query whose answer is larger than memory writes it with
`SinkParquet` or `SinkCSV`, the spilling operators under a memory limit, as
[Memory and spilling](../concepts/memory.md#a-larger-than-memory-query) shows.
