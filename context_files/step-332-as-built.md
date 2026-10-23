# Step 332 — as built

**Arrow IPC,** as Polars reads and writes it (`polars-parity.md` §5): files and
streams, read, written and sunk, and a file's schema; and a Parquet file's schema.

## 1. The API

| ursus | Polars | |
| --- | --- | --- |
| `ScanIPC(path)` | `scan_ipc`, `read_ipc` | an IPC file, Feather version 2, its batches one at a time |
| `ScanIPCStream(path)` | `read_ipc_stream` | an IPC stream |
| `lf.WriteIPC(ctx, w, opts...)` | `write_ipc` | a file to any `io.Writer`, streaming |
| `lf.WriteIPCStream(ctx, w, opts...)` | `write_ipc_stream` | a stream |
| `lf.SinkIPC(ctx, path, opts...)` | `sink_ipc` | a file, to a temporary name renamed on success |
| `WithIPCCompression(IPCLZ4 \| IPCZstd)` | `compression=` | uncompressed by default, as Polars' |
| `ReadIPCSchema(path)` | `read_ipc_schema` | from the footer, no record read |
| `ReadParquetSchema(ctx, path)` | `read_parquet_schema` | from the footer |

**Interchange, both ways:**

- **Polars' files read:** `testdata/ipc` holds Polars 1.44.1's `write_ipc`, uncompressed,
  LZ4 and Zstandard, and its `write_ipc_stream`, of one frame of eight types. Polars
  writes strings as string views and lists as large lists, both read.
- **ursus's files read by Polars:** written uncompressed, LZ4 and Zstandard, as files
  and streams, each `equals` Polars' own reading of the fixture, schema included
  (checked by hand at this step, by a program over the overlay; Python is not a test
  dependency).

**Where ursus differs:**

- **Paths only, not file objects,** for the scans, which open the file each time the
  query is planned or run; `ScanArrow` takes any reader.
- **No memory mapping:** the batches are read and copied, as `ScanArrow` copies them.
- **One file a scan:** no globs, lists or hive partitions yet (`feature-gaps.md` §7).

## 2. How

`ipc.go`, over arrow-go's `ipc` package and ursus's Arrow conversions:

- **The scans** are `ScanArrow` with an opener per run. A file's `FileReader` is
  walked as a `RecordReader` by `fileRecords`, each batch owned through
  `RecordBatchAt`, and the file closed on the last release. A stream's reader closes
  its file the same way.
- **The writers** run the query as `WriteParquet` does: each batch made a record by
  `arrowout.Record` and written by arrow-go's `FileWriter` or `Writer`, whose `Close`
  writes the footer or the end of the stream.
- **`SinkIPC`** writes through a temporary file renamed on success, as `SinkParquet`
  does: a file without its footer cannot be read.

## 3. Tests

**`TestArrowIPC`:**

- Polars' four fixtures, every column compared, the list and the struct by value, and
  the schema;
- each codec written as a file and as a stream and read back;
- the codecs' bytes all different;
- a file of a batch a row read back in order;
- a sink, whole; a failed sink leaving neither the file nor its temporary;
- a stream scanned as a file and a file as a stream, each refused naming the other
  scan; a missing file.

## 4. Performance

Against Polars 1.44.1, a million rows of an Int64, a Float64 and a short String, the
median of 7:

| operation | ursus | Polars |
| --- | --- | --- |
| `WriteIPC` to memory | 14.3 ms | 20.0 ms |
| `ScanIPC` of ursus's file, collected | 20.4 ms | — |
| `ScanIPC` of Polars' file, collected | 29.7 ms | 16.2 ms, mapped or not |

Writing is faster than Polars', and ursus's file is smaller, 24 MB against 32: its
strings are offsets where Polars' are 16-byte views.

Reading is slower, by profile:

- **one batch at a time:** `ScanArrow`'s reader converts on one goroutine, under its
  lock;
- **copied:** every buffer is read from the file and copied into ursus's columns,
  where Polars maps them;
- **views converted:** Polars' string views become offsets, a third of the CPU on
  Polars' file.

A file's batches can be read in any order (arrow-go's `RecordBatchAt` is safe to call
at once), so a reader of its own could convert them in parallel. `feature-gaps.md` §8
records it.

## 5. Docs

- **I/O:** a paragraph under "Arrow".
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md` §7 and §8, `polars-parity.md`.**

## 6. Teeth, all 7 biting

- a file's batches read backwards;
- the last batch left unread;
- LZ4 asked for and not applied;
- Zstandard written as LZ4;
- a stream written as a file;
- a file left without its footer;
- a wrong-format hint missing.

**Gate:** test-all 130 ok, race 26 ok (the root package 530 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
