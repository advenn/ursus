# Step 360 — as built

**Several IPC files as one frame, and IPC hive datasets:** Polars' `scan_ipc` of a
list, a glob and a directory, with `hive_partitioning`, `hive_schema`,
`try_parse_hive_dates` and `include_file_paths`.

## 1. How

**`internal/source/files`** is a new source: several files of one format read as one
scan, in order, each by a source of its own (`files.File{Src, Path}`). After the
files' columns come columns that every row of a file shares (`files.Shared`):

- a hive dataset's partition keys, found by `hive.ParseFor` and typed by step 347's
  rules (`Partitions.Types`, `Values`);
- the path column.

The scan reads the first file's columns. A later file is read by name, checked as
it is opened (`fits`). One it lacks, one more, or one of another type is refused, as
Polars refuses them. A partition key of a file column's name takes that column's
place. A path column of a column's or a key's name is refused.

**The read:**

- Each file is read for the projection's file columns alone. A projection of shared
  columns alone reads none: the projection is `[]`, never nil.
- The shared columns go in their places, each file's value repeated by step 359's
  `kernel.Repeater`.
- **Pruning:** a conjunct of the shared columns alone is classified Inexact
  (`ClassifyPredicates`). Each file is judged on its values before it is opened
  (`keep`), and the Filter above stays.
- **Read-ahead:** on several threads, up to one file a thread is read ahead, each
  on a goroutine with a channel of four batches, and the batches come out in the
  files' order (`nextAhead`, `fill`, `readAhead`). A file's error is the last thing
  it sends, so it surfaces in its turn. `Close` cancels the read-ahead and waits for
  it. One thread reads a file at a time on the caller's goroutine, as before.

**`hive.ParseFor(op, …)`** names the scan in its errors and in `Values`'. `Parse`
is `ParseFor("scan_parquet", …)`.

**At the root:**

- `ScanIPC(path, opts...)`: a directory read whole as a hive dataset, through
  `datasetFiles`; one file without options is read as before;
- `ScanIPCFiles(paths, opts...)` and `ScanIPCGlob(pattern, opts...)`;
- `IPCScanOption`: `WithIPCHivePartitioning`, `WithIPCHiveSchema`,
  `WithIPCHiveDates`, `WithIPCIncludeFilePaths`.

Hive partitioning is on by default for a directory, and off for a file, a list or a
glob, as Polars has it.

| Polars 1.44.1 | ursus |
| --- | --- |
| a glob; a list, in its order | as Polars |
| a later file's columns in another order | read by name, as Polars |
| a later file lacking a column, with one more, of another type | refused, as Polars |
| a directory of `k=…/j=…` | `a`, `k`, `j`: Int64, Int64, String, as Polars |
| a glob below it; with `hive_partitioning=True` | no keys; the keys, as Polars |
| `include_file_paths`, a filter on a key, a key alone, `hive_schema={"k": Int8}`, `head(2)` | as Polars |
| a path column of a key's name | refused |

## 2. Tests

- **`TestScanIPCFiles`,** on one thread and on four:
  - 15 reads;
  - the directory's schema;
  - a file that is no IPC file, in a key's directory a filter rules out, never
    opened, and read where no filter rules it out;
  - four refusals.

**Teeth: 18 of 20 bite.**

- **The files' columns:**
  - a later file read by position;
  - a column missing, another type, or a column more let through.
- **Shared columns:**
  - no partition keys;
  - a key's type never given;
  - no path column, or a path's name taken let through;
  - the first file's shared values for every file.
- **Pruning:** no file ruled out, serial or read ahead.
- **The read-ahead:** the window read last first; a file's error lost.
- **The root:** a directory without its keys; hive partitioning not asked; a
  directory read as a file.

The two silent ones are speed-ups only:

- a file read for every column when only shared ones are asked;
- a file's limit not kept, which the Limit above keeps.

## 3. Performance

Ten files of 200,000 rows of an integer, a float and text, a key each, 62 MB, median
of seven:

| | a file at a time | read ahead | Polars |
| --- | --- | --- | --- |
| read whole | 74.4–81.9 ms | 62.8 | 18.3 |
| summed by key | 32.8–34.5 | 20.2–21.6 | 13.6 |
| `k == 3` | 8.8–9.7 | 8.5–9.2 | 3.0 |
| one file of the two million rows | 84.8 | 77.0 | 41.6 |

The dataset read whole is as fast as one file of its rows, so the source adds
nothing to it. What is left is the IPC read's own gap: a batch at a time, copied
(feature-gaps §8). The whole read collects into one frame, which is serial.

**Gate:** test-all 140 ok (every width and the scalar build), levels, vet ×3 and the
bench engine tests clean; the 22 PDS-H answers match DuckDB. The race pass was
stopped to spare the laptop, and is left to CI from here on.
