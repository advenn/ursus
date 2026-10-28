# Step 405 — as built

**Every format from an `fs.FS`.** The second half of C4 of `v0.8-scope.md`.

## 1. What was wrong

No reader took an `fs.FS`. A program that embeds its reference data (`embed.FS`), a
test of fixtures in an `fstest.MapFS`, and an `os.DirFS` or a zip archive's
`zip.Reader` were read by writing the files to disk first, or by building an opener
of each by hand for the `From` readers.

## 2. How

- **`ScanCSVFS`, `ScanNDJSONFS`, `ScanParquetFS` and `ScanIPCFS`** take an `fs.FS` and
  a pattern, as `template.ParseFS` does: a file's name in it; a directory's, read
  whole as the path readers read one, every file below it but those whose names
  begin '.' or '_', refused where they are of two extensions (`fsDataset`, as
  `datasetFiles`), Parquet's and IPC's hive keys read below it (`scanParquetFrom`
  and `scanIPCFrom` take the root); or a glob, `fs.Glob`'s, an element of `**`
  matching any number of directories as the path globs' do (`fsGlob`, `matchElems`
  given its matcher). **`ScanJSONFS`** and **`ScanIPCStreamFS`** read one file, as
  `ScanJSON` and `ScanIPCStream` do.
- **Through each format's `From` reader:** a file is opened from the `fs.FS` each time
  the query is planned or run. A Parquet or IPC file that reads at an offset, as
  `embed.FS`'s, `os.DirFS`'s and `fstest.MapFS`'s do, is read so, a column chunk at a
  time, and closed through the scan (`readerAtFile`); one that does not is read
  whole into memory first.
- **Refused by name:** no file system; a name `fs.ValidPath` does not take, rooted or
  of `..`, with a hint of what an `fs.FS` name is; a glob matching nothing.
- **Object storage stays the caller's**, through the `From` readers' openers, as the
  scope said.

## 3. Measured

Nothing to time: the readers are the `From` readers'. `make api`: 6 features added,
none gone.

## 4. Tests

- **`TestAnEmbeddedFileReadsAsItsPath`** (root): an IPC file, an IPC stream and a
  Parquet file embedded in the test binary read as the same files by their paths.
- **`TestEveryFormatReadsFromAnFS`**: three frames written as CSV, NDJSON, Parquet and
  IPC under `data/day=1`, `data/day=2` and `data/day=2/more` of an `fstest.MapFS`, a
  `_SUCCESS` beside them, read by a name, a glob, a `**` glob and the directory, are
  the frames in their names' order; the Parquet and IPC directories' day keys sum to
  900. Every file opened is closed, in a file system whose files read at an offset
  and in one whose do not; a Parquet or IPC file that does is read through `Read`
  not at all.
- **`TestAnFSPatternIsRefusedByName`**: no file system, a rooted name, a `..` name, a
  glob of no match, a file not there, with `fs.ErrNotExist` through the error, and a
  directory of two extensions.

**Teeth: 10 of 10 bite:** a file that reads at an offset read whole; a directory with
no hive root; '_' names read; `**` not walked; no `ValidPath` check; no match not
refused; a file read at offsets not closed; one read whole not closed; two
extensions read; no file system not refused.

**Gate:** the light gate, 29 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1.
