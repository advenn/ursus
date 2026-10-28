# Step 404 — as built

**Arrow IPC from bytes and openers.** The first half of C4 of `v0.8-scope.md`; the
`fs.FS` half is step 405's.

## 1. What was wrong

Parquet is read from a path, a list, a glob, bytes (`ScanParquetBytes`) and the
caller's openers (`ScanParquetFrom`, step 168), CSV and NDJSON alike. An Arrow IPC
file was read by path alone (`ScanIPC`, `ScanIPCFiles`, `ScanIPCGlob`), and a stream
by one path (`ScanIPCStream`): a service holding a file in memory, in an object
store or behind an HTTP server wrote it to disk first, or built an arrow-go reader
for `ScanArrow` by hand.

## 2. How

- **`ScanIPCFrom([]IPCFile)`**: `IPCFile` is `ParquetFile`'s shape, a name and an
  `Open` returning an `io.ReaderAt` and its size, given the query's context, read
  through an `io.NewSectionReader`, and closed after where it is an `io.Closer`.
  Several are one frame under `ScanIPCFiles`' rules, the first one's columns by name;
  `WithIPCHivePartitioning(true)` reads keys from their names.
- **`ScanIPCBytes(b, name)`**, a file in memory.
- **`ScanIPCStreamFrom([]IPCStreamFile)`** and **`ScanIPCStreamBytes`**: a stream read
  from the start, `IPCStreamFile` a name and an `Open` returning an `io.ReadCloser`,
  as `CSVFile`'s.
- **One path for them all:** `scanIPCPaths` makes openers of its paths for
  `scanIPCOpened`, which the new readers call with theirs; `openIPC` reads a file of
  any `ReadAtSeeker`, `openIPCStream` a stream of any reader.
- **The query's context:** `arrowsrc.NewContext` gives an Arrow source's factory the
  context of the query that opens it, the schema's or a scan's, as a Parquet file's
  opener is given it; `New`, `ScanArrow`'s, wraps a factory of none.

## 3. Measured

Nothing to time: the readers are `ScanIPC`'s, opened another way. `make api`: 10
features added, none gone.

## 4. Tests

- **`TestAnIPCFileIsReadFromBytesAndOpeners`** (root): a frame written as an IPC file,
  and as a stream, reads back from its bytes whole, and filtered and projected; two
  read through openers are one frame in the order given, collected twice, every
  reader opened closed and given the query's context.
- **`TestAnIPCOpenerIsRefusedByName`**: no files, a file with no `Open`, an `Open` that
  fails, a stream's bytes read as a file and a file's as a stream, and a file of
  other columns are refused, each naming the file and, where it applies, the other
  reader.
- **`TestIPCFilesFromOpenersReadTheirHiveKeys`**: `day=1` and `day=2` from the names.

**Teeth: 8 of 8 bite:** the query's context not passed; a file's reader not closed; a
stream's not closed; the file's size short; the names not the paths; a failed
`Open` unnamed; no `Open` not refused; the stream's hint lost.

**Gate:** the light gate, 29 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1.
