# Step 502 — as built

**The I/O wave converted to recorded oracles.** Wave 502 of
[`oracle-v2-scope.md`](./oracle-v2-scope.md) §3, in step 496's framework
([`step-496-as-built.md`](./step-496-as-built.md)): thirteen files' hand-typed expected
values are Polars 2.1.0's answers now, recorded in thirteen groups of `testdata/oracle`
by `bench/scripts/oracle/<group>.py` and spelled in `oracle_<group>_test.go`, with
`ipc_test.go`'s Polars-written fixtures besides. The wave's answers are texts as often
as frames: what `write_csv`, `write_ndjson` and `write_json` write, and the file names
a partitioned sink lays down, each compared byte for byte. Every reader's and writer's
options are data in the recording, and the Go side passes the same. What a file keeps
asserts a refusal, a plan, a count, a Go-API shape, a rule ursus has on purpose
(feature-gaps.md §9), or compares ursus with itself where Polars has no answer: the
serial reading path against the blocked one.

**How it was built.** The step was finished by a second agent from a first agent's
uncommitted work: the first had converted the thirteen files, recorded the groups and
drafted this record when it was cut off; the second verified that work (both
recordings byte-identical to the first's, every case passing or skipped as named),
then moved every reader's and writer's options into the recording, gave a refusal its
place and its kind back where the conversion had kept only its reason, asked Polars
through a DB-API connection what SQLite cannot hold, recorded what Polars 2.1 reads
from `testdata/ipc`, and made `hive_test.go`'s last typed answer a differential.

## 1. What

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `io_byhand_test.go` | 325 | 142 | about 33 | 1 | 31 | 2 |
| `csvread_test.go` | 126 | 89 | 30 | 0 | 41 | 2 |
| `csvskip_test.go` | 93 | 70 | 25 | 0 | 27 | 2 |
| `csvwrite_test.go` | 313 | 148 | about 70 | 0 | 51 | 6 |
| `ndjsonread_test.go` | 573 | 420 | about 50 | 0 | 51 | 1 |
| `jsonread_test.go` | 290 | 178 | about 33 | 2 | 35 | 2 |
| `jsonwrite_test.go` | 119 | 87 | 7 | 0 | 8 | 2 |
| `ipcfiles_test.go` | 117 | 87 | 20 | 0 | 18 | 2 |
| `ipc_test.go` (its Polars files) | 159 | 145 | 8 | 0 | 4 | 0 |
| `hive_test.go` | 252 | 149 | about 20 | 0 | 23 | 1 |
| `hiveparent_test.go` | 47 | 8 | 5 | 0 | 6 | 1 |
| `filepaths_test.go` | 99 | 86 | 18 | 2 | 15 | 2 |
| `partitionwrite_test.go` | 317 | 167 | about 31 | 1 | 24 | 2 |
| `database_test.go` | 499 | 447 | 61 | 35 | 37 | 10 |
| | 3,329 | 2,223 | about 410 | 41 | 371 | 35 |

371 cases in thirteen groups (`ipc_test.go`'s four in the ipcfiles group), 35 over
generated inputs (`Gen`, seeds 5021 to 5033, 40 to 300 rows, nulls and each type's
edges), 72 refusals recorded with the phrases ursus's refusal must name, 75 written
texts; the JSON files are 5 KB to 231 KB. The hand-typed value lines are counted from
the diff, a removed line that carries an expected value, inputs and error text left
out. The 41 that stay are each a rule ursus has on purpose and Polars has not (§3): 35
of them `database_test.go`'s mapping of a Go driver's own types.

**The format grew a text answer** (§2 of the scope, as built): a query that returns a
`str` is recorded as `"want": {"text": ...}` and compared byte for byte, so a CSV, an
NDJSON document or a directory's listing is an answer as a frame is. **A refusal's
`contains` may be a list** of phrases, each of which ursus's refusal names: its reason
beside its place (`object 151, key a`, `at line 2`) and its reader (`read_json`), as
the tests before named them; a single phrase is as it was.

**Options are data.** A reader's or a writer's keyword arguments are the param
`options` (`iotext.options`: a type as ursus spells it, a schema as its `[name, type]`
pairs in order, overrides by name), and what a case asks of the frame it read is the
params `select`, `head` and `tail`. The Go side never spells an option: `in.csvOptions`,
`in.ndjsonOptions` (NDJSON and JSON), `in.databaseOptions`, `in.parquetOptions`,
`in.ipcOptions` and `in.csvWriteOptions` translate the recorded arguments into ursus's
options, and one no translation takes fails the case, so the two sides read and write
with the same options by construction; `in.shaped` applies the shaping. Two teeth were
tried: the skip_lines translation dropped, and csvread's refusals held to the wrong
kind; 22 cases failed, and passed again when restored.

**The loader** (`oracle_test.go`) grew, unexported: `in.bytes(name)`, the param
`name_b64` where a CSV's bytes are not UTF-8 (read under utf8-lossy), else the text;
`in.texts(name)`, a param of strings, a dataset's layout, each file's relative path to
its text or to the input frame it holds; `in.written(text, err)`, a writer's answer as
the registry's frame, or its refusal kept as the query's (`oracleInputs.failed`); an
`oracleDifference`'s `text`, ursus's documented written text; and `oraclePhrases`,
`contains` as one phrase or a list. `oracle_files_test.go` writes a case's layout under
a temporary directory with ursus's own writers (`in.textFiles`, `in.frameFiles`),
strips the directory's root off a path column as the recording's query did, holds the
options' translations and `oracleRefusedAs(kind, q)`, which holds a recorded refusal
to its kind too (`ErrValue`, `ErrSchema`, `ErrType`, as the tests before did), and the
two differences every I/O group names: `oracleHeaderless(n)` and `oracleCount(col)`.
`bench/scripts/oracle/iotext.py` is the Python side of the same: `csv_case`, `dataset`
and `textfiles` (each taking its scan's arguments as `scan`), `options`, `shaped`,
`listing`, and `in_dir`, which replaces the temporary directory's name in a refusal's
text with `<dir>`, so that a recording is the same bytes every run.

**The database group asks two connections.** Through SQLite, as the first agent built
it, each case's values in a table of one untyped column; and through `Rows`, a DB-API
connection of no database whose every query answers its values as they are, the Go
side's fake driver's mirror, so that Booleans, a Boolean beside an integer or text, an
instant naive or with an offset, and integers past Int64 are Polars 2.1's answers too,
generated besides (`booleans_gen`, `instants_gen`, `instants_with_an_offset_gen`,
`integers_past_int64_gen`). An instant is recorded as `{"datetime": its ISO text}`,
fed to the Go driver as a `time.Time` in UTC when naive and in a fixed zone of its
offset otherwise; an integer past Int64 as a uint64.

**Polars' own IPC files.** `ipc_test.go` read `testdata/ipc`'s four fixtures, Polars
1.44.1's `write_ipc` (plain, LZ4, Zstandard) and `write_ipc_stream` of one frame,
against a frame typed by hand; the ipcfiles group's `polars_file`, `polars_file_lz4`,
`polars_file_zstd` and `polars_stream` record what Polars 2.1.0 reads from the same
bytes, the param path, and `TestArrowIPC` keeps the four read alike and the schema
`ReadIPCSchema` gives.

**Groups register their differences in their own file.** Each `oracle_<group>_test.go`
adds its entries to `oracleDifferences` in its `init`, so three waves converted at once
never edit one map.

## 2. Differences named

Each documented in `feature-gaps.md` §9 or §11 (step 487 §3):

- **A headerless CSV's columns are `column_1`, … here, `column_0`, … in Polars 2**
  (step 487 §3): `io_byhand/i10_blank_line_no_header`, `csvread/blank_line_first_no_header`,
  `csvskip/no_header`, `no_header_one`, `skip_lines_no_header` and `two_files_lines`,
  each the recorded names mapped to ursus's.
- **A count is a Uint64 here, Polars' a UInt32** (`internal/expr.ResolveAggBinding`):
  `hive/a_count` (`len`) and `filepaths/a_count_a_file_gen` (`n`).
- **A partition key's name is escaped as its value is** (§9, step 367):
  `partitionwrite/a_keys_name_escaped` writes `a%20b%2Fc=1/00000000.parquet` here, where
  Polars writes `a b/c=1/00000000.parquet`, the slash making a directory.
- **A time of day's key keeps its fraction** (§9, step 367): `partitionwrite/a_key_of_tm`
  writes `tm=03%3A04%3A05.123456/...` here, `tm=03%3A04%3A05/...` in Polars.

Spelled around in the query: a partial `hive_schema`, which ursus completes by inference
and Polars refuses (§9), so `hive/a_hive_schema` and `ipcfiles/a_keys_type_given` name
every key; a zoned instant, which Polars reaches by `replace_time_zone("UTC")` and
`convert_time_zone` where ursus casts; `infer_schema_length=None` in every database
case, every row typing the column as ursus reads every row (§9), recorded among its
options and refused by `in.databaseOptions` as anything else; and a field beginning
with a space, which ursus quotes and Polars writes bare (§9), kept out of the generated
texts the writer is held to.

## 3. What stays, and why

- **Refusals Polars cannot be asked:** a CSV separator past ASCII, which Polars refuses
  as an option (`TestCSVReadOptions`); a negative float precision, refused at Polars'
  Python layer (`TestCSVWriteRefusals`, which held five refusals as
  `TestCSVWriteInstants` and keeps this one; the other four are recorded, each held to
  a value error); a Binary column written as JSON, which Polars 2.1 panics on
  (`TestWriteJSON`); a partition key named twice and every column a key, which Polars
  writes, a List key, which it panics on, a writer of one stream and a negative row
  count (`TestPartitionedSinkRefuses`).
- **Frames of no columns,** which the format does not hold: `raise_if_empty=False` of an
  empty file and `skip_lines` past the end, a JSON document of no object.
- **Rules ursus has on purpose** (feature-gaps.md §9, or the step that made them), each
  said so in its comment: `[{}, {}]` two rows and a late `true` or `1.5` in an Int64
  column refused (`TestReadJSON`, its 2 lines); a Go driver's own types, a uint64
  alone, a Valuer, a struct, an int32 and a float32, text not UTF-8, and the types a
  driver names (INT4, NUMERIC, DATE, TIME, ...), which no Python value is, and an
  instant beside text, which Polars refuses (`TestReadDatabase`,
  `TestReadDatabaseDriverTypes`); the overrides Polars truncates, ignores or refuses
  where ursus casts or refuses (`TestReadDatabaseOverrides`); a frame of no rows and no
  columns written as nothing (`TestIOByHand`, step 77's I22; Polars' bare newline is a
  finding, §4); bytes read under a name of the caller's, "in-mem" in Polars
  (`TestIncludeFilePaths`, its 2 lines); a key's files numbered on past 64 open ones
  (`TestPartitionedSinkOfManyKeys`, its 1 line).
- **Differentials:** the serial reading path against the blocked one, one thread against
  four, over each kind of text the oracle reads (`TestCSVReadOptions`,
  `TestCSVSkipAndQuoting`, `TestScanIPCFiles`, `TestIncludeFilePaths`); batches of one
  row against the whole (`TestWriteJSON`); a hive directory's hidden and underscore
  entries against the same dataset without them, and forty hive files against their
  `Concat` (`TestHivePartitions`); `testdata/ipc`'s four fixtures against each other
  (`TestArrowIPC`); a partitioned dataset of each format read back as the frame it was
  (`TestPartitionedSinkOfEachFormat`); `TestReadNDJSONInParts` and
  `TestReadJSONInParts`, against `encoding/json`, unchanged.
- **Plans, counts and errors' places:** `Explain`'s text, the files a filter skips
  (`FileStats`), a value error's physical line, a later file's error naming its line and
  file, a schema's refusal naming no inference hint, a sink's file whole or absent, 150
  keys' files numbered on, a batch total summed as its lines were written, a
  10,000-row column.
- **Go API:** `i128.Parse`'s overflow, the driver types `database/sql` names
  (`TestReadDatabaseDriverTypes`, unchanged), a transaction's, a connection's and a
  statement's rows, `ReadIPCSchema`.
- **Helpers gone:** `cellsOn`, `schemaOf`, `ioRefused`; `i128Col`, `ioEqual`, `refusal`,
  `csvText`, `csvProbeFrame`, `csvOut`, `hivePart`, `rowsOf`, `files`, `samePartFrame`
  stay, used here or by other files. `hiveparent_test.go` is a comment pointing at its
  group: every case of it is recorded.

## 4. Found

Each recorded, skipped in the registry with `oracleFinding`, and the maintainer's to
decide; none is documented in `feature-gaps.md` §9 or §11, so none is named a
difference and no recording was changed.

- **A column of only `NaN` infers Float64 in Polars 2.1** (`io_byhand/control_only_nan_inferred`);
  ursus infers String, as the old control case asserted. Step 77's record (§3, I21)
  says Polars infers that as a float, so the difference is old and meant; it is in no
  list of differences.
- **Polars 2.1's CSV round trip of an offset with seconds lands 30 s away**
  (`io_byhand/i8_monrovia_1970_round_trips`): Monrovia's offset was −00:44:30 until 1972;
  Polars writes the instant 1970-01-01T00:44:30Z as `1970-01-01T00:00:00.000000-0045`, the
  wall clock and an offset rounded to the minute, and reads back 00:45:00Z. ursus writes
  an offset with seconds in UTC (I8, step 345's audit) and reads it back exactly. The
  Amsterdam case of 1929 agrees: there Polars writes the UTC clock with `+0000`.
- **A value that is not an object, in a Struct column under `ignore_errors`, is null
  in Polars 2.1** (`ndjsonread/ignored_in_a_struct`); ursus answers a struct of nulls,
  `{x: null, y: null}`, as the old test asserted of Polars 1.44.1.
- **A frame of no rows and no columns: Polars 2.1 writes a bare newline**
  (`io_byhand/i22_no_rows_no_columns_written`), as it does for one with rows and no
  columns, which ursus refuses (I22); ursus writes nothing, step 77's rule.

Found and documented: every other case of the thirteen groups is Polars 2.1.0's answer
too, the CSV writer's every text, the partition layouts' every escape, the JSON
documents' every byte, the database's every type, through SQLite and through rows,
and what Polars 2.1 reads from Polars 1.44.1's IPC files included.

## 5. Checks

`make oracle` recorded the thirteen groups twice to the same bytes (`md5sum -c`, 13 of
13), after the first agent's one fix: a refusal naming the temporary directory
(`iotext.in_dir`) and one generated key that was the text `__HIVE_DEFAULT_PARTITION__`,
whose directory is the null partition's, two writers on one file. `go test -run
TestOracle .`: the 371 cases pass but the four findings, skipped as named. `go test .`
in full (187 s), `go vet ./...`, `go run ./internal/gen/api -check` (0 gone; nothing
exported added), `gofmt -l .` empty, all under `GOEXPERIMENT=simd GOAMD64=v3`.
`TestEveryCitedTestExists` passes: no Go comment cites a removed name. After master's
waves 497 to 501 were merged in, the same again on the merged tree (`go test .` 209 s),
and `make oracle` of every group, theirs and these, wrote the same bytes as are
checked in.
