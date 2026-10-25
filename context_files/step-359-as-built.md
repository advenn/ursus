# Step 359 — as built

**A column of each row's file path for CSV and NDJSON:** Polars' `include_file_paths`,
`WithCSVIncludeFilePaths(name)` and `WithNDJSONIncludeFilePaths(name)`.

## 1. How

**The schema.** `source.WithPathColumn` puts a String column of the name after the
files' columns, in the CSV source's schema (inferred or given) and in the NDJSON
source's. A name the files' columns already have is refused, as Polars refuses it
("column name for file paths conflicts with column name from file").

**The read.** The path column is no file's.

- Each reader builds its batches from the files' columns of the projection, `out`,
  and emits `emit`: those columns, with the path column at `pathPos` where the
  projection holds it.
- The column mapping, `wanted`, and the JSON keys a document may have are the
  files' columns alone.
- A record of a field more is still refused, and a JSON key of the path column's
  name past the inferred lines is a key the schema lacks.

**Which file a batch is from:**

- **CSV, parallel path:** a block knows its stream (`block.stream`).
- **CSV, serial path:** a batch now ends where a stream does, when the path is
  projected. It used to run on into the next file, and the batch's path would have
  named the second file for rows of the first.
- **NDJSON:** a block is one file's.

The path is the stream's name as given (`pathOf`): the path passed, a glob's or a
directory's as it found it, or the name given with a reader's bytes. Polars names
bytes "in-mem".

**`kernel.Repeater`.** Built a batch at a time, a 110-byte path cost a scan of a
million rows 40 ms (83 ms without it, 118 to 146 with it), where Polars' costs
nothing. Now each file's path, one row, is repeated once, for the most rows a batch
has asked of it: a String's text is copied in doubling runs, and any other type is
gathered. Each batch gets a zero-copy slice of that repeat. Step 360's IPC datasets
repeat their partition keys the same way.

| Polars 1.44.1 | ursus |
| --- | --- |
| `scan_csv(glob, include_file_paths="path")` | each row's path, as found, after `a` and `b`, as Polars |
| a list, `./p2.csv` among it | as given, as Polars |
| `select("path")`, `filter(path == p2)`, `head(2)` | as Polars |
| a name the files have, `"a"` | refused, as Polars |
| `scan_ndjson` of a glob, a list, a schema given | as Polars |
| bytes | their name; Polars' "in-mem" |

## 2. Tests

- **`TestIncludeFilePaths`,** on one thread and on four:
  - 15 reads;
  - the schema;
  - three files of 20,000 to 60,000 rows of quoted line ends, cut into blocks, each
    row's path its own file's, counted by a group-by;
  - a long record and a later JSON key refused;
  - a name taken refused, for CSV and NDJSON.
- **`TestRepeaterSlicesOneRepeat`:** a String, another String, an Int64 and a null,
  repeated for batches of 0 to 5 rows.

**Teeth: 12 of 12 bite.**

- **CSV:**
  - a serial batch across two files;
  - a block's stream the first;
  - a stream's path the source's;
  - a given schema without the path;
  - the path read from the file.
- **Shared:** a name taken let through; the path at the wrong place.
- **NDJSON:**
  - without the path;
  - the first file's path;
  - a given schema or an inferred one without the path;
  - the path read from the lines.

## 3. Performance

A million rows of four columns, 42 MB, with a 110-byte path, median of seven runs,
taken three times once the machine was quiet:

| | without the column | with it | Polars, without and with |
| --- | --- | --- | --- |
| a batch at a time | 83.0–83.6 ms | 118.1–145.9 | 34.1, 34.3 |
| repeated once and sliced | 78.2–84.6 | 102.1–114.0 | |

What is left is the collected frame's: `data.ConcatStrings` copies the column's 110 MB
of text into one, 0.30 s of 6.1 s of samples over seven runs. Polars keeps the column
as a scalar, so it adds nothing. This is recorded in feature-gaps §8. A scan without
the column is as before.

**Gate:** test-all 140 ok, race 28 ok (the root package 618 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
