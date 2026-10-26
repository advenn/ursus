# Step 367 — as built

**Partitioned writes:** Polars' `write_parquet(path, partition_by=...)` and
`sink_parquet(pl.PartitionBy(path, key=..., include_key=..., max_rows_per_file=...))`,
and sink_csv's, sink_ipc's and sink_ndjson's, L2 of `v0.6.8-scope.md`.

## 1. How

**The options** (`partitionwrite.go`), a `PartitionOption` each, which `SinkParquet`,
`SinkCSV`, `SinkIPC` and `SinkNDJSON` take:

- `WithPartitionBy(keys...)`;
- `WithPartitionIncludeKey(bool)`, true by default, as Polars' `include_key`;
- `WithMaxRowsPerFile(n)`, with keys or without, as Polars' `max_rows_per_file`.

With either of the first and the last, the sink's path is a directory. `SinkNDJSON`
takes `NDJSONSinkOption`s now, a `CollectOption` or a `PartitionOption`, where it took
`CollectOption`s alone. A `Write*` to a stream refuses a partition option, naming the
sinks.

**The dataset,** as Polars 1.44.1 writes one, probed at step 365 and here:

- a directory `name=value` a key, in the keys' order, its files numbered from
  `00000000`, named `.parquet`, `.csv`, `.ipc` or `.jsonl`, `.csv` when compressed too;
- a value percent-encoded, in upper case, where it holds `/`, `=`, a space, `%`, `:`,
  a byte below a space, DEL or a byte past ASCII, and every other byte as it is
  (`hive.Escape`, beside `hive.ParseFor`); a key's name likewise, where Polars writes
  it as it is, a slash in it a directory, read back as another key;
- a null `__HIVE_DEFAULT_PARTITION__`, an empty text `k=`;
- a date `2024-01-02`; an instant its wall clock with its unit's digits,
  `2024-01-02 03:04:05.123456`, and its offset where it has a zone, `+09:00`; a time of
  day with its fraction, which Polars drops; anything else its text as a cast gives
  it, a float's Polars' `-0.0`, a Boolean `true`, a Decimal `1.50`;
- the key columns kept in each file, or not;
- no file for an empty answer; the directory is made.

A List, an Array, a Struct or a Binary key is refused, as is a key named twice, and
every column a key with none kept.

**The write** (`partitioner`), streamed:

- each batch's rows are encoded by the group-by's key encoder and grouped by key, in
  their order, and each key's rows go to its file, a new key's path spelled from the
  batch's text of its keys;
- a file is written to a temporary name in its directory and renamed when it ends: at
  its rows, at the end, or when a 65th file would be open, the least recently written
  then ending, and a later row of its key beginning the key's next file;
- a failed query removes the files still open and leaves those already ended, as
  Polars does.

The four formats' writers serve as file writers: Parquet's and CSV's as they are, IPC's
through `ipcPart`, its options shared with `writeIPC` (`ipcWriteCfg.options`), and
NDJSON's through `ndjsonPart`, which encodes a batch as `WriteNDJSON` does, on the
caller's goroutine.

**The hive reader** decodes a key's name as it decodes its value, and reads an empty value of a key of text as the empty string, as
Polars reads `k=`, where it read null; in a key of any other type it is null, and an
empty value no longer takes part in the key's inference. The files source takes a key's
nullability from its typed values.

## 2. Tests

- **`TestPartitionedSinkWritesAHiveDataset`:** ten rows by a key of text and an
  integer key, the text `a/b`, `c=d`, `e f`, `g%h`, `ü`, `:x`, empty, null and `#?&+`:
  the ten files named as Polars names them, read back as the frame; a key named
  `a b/c`, its name escaped and read back from the path; a file of its own with its
  keys, and without them, the dataset read back as the frame again.
- **`TestPartitionedKeysOfEveryKind`:** a date, an instant with no zone, one in Tokyo,
  one in nanoseconds, −0.0, a Boolean, a Decimal and a time of day, each path exact.
- **`TestPartitionedFilesOfAtMostSoManyRows`:** a key's rows in their order across
  files of two rows; files of three rows with no key, in the directory itself.
- **`TestPartitionedSinkOfEachFormat`:** CSV, IPC and NDJSON datasets, named, read back.
- **`TestPartitionedSinkOfManyKeys`:** 150 keys interleaved, a key's file closed and
  begun again, every row read back, no temporary file left.
- **`TestPartitionedSinkRefuses`:** six refusals; a cast failing in the second batch
  leaves no temporary file; an empty answer writes no file and makes the directory.
- **`TestParse`** and its new case: an empty key of text the empty string, of an
  integer key null, the key still an integer.

**Against Polars, outside the repository** (the scratchpad's `partx`): a frame by a key
of text (with `/`, `=`, a space, `ü`, `:`, empty and null), an integer key (with a
null) and a date, written by each:

- the two trees are **identical**, name for name;
- Polars reads ursus's dataset as the frame, and ursus reads Polars'.

**Teeth: 18 of 18 bite:** a slash, a byte past ASCII, or a key's name not escaped; the
escape in lower case; a key's name not decoded; an empty text key read as null, or
typing its key; a null spelled `null`; files numbered without their zeros; the keys
kept when told not to; a file never ended at its rows; no limit on open files; a failed
write's files left; a file never renamed; every instant in microseconds; a zone's
offset dropped; a time of day without its fraction; a partition given to a stream.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok, the root's rerun after `goroutineSites` named the JSON writer's two
goroutines by their function's new name, `writeJSONCfg`; levels, vet ×3 and the bench
engine tests clean; the 22 PDS-H answers match DuckDB.
