# Step 532 — as built

**Three of master's fixes backported to the 0.7 line for v0.7.3.** The maintainer's
decision of 2026-11-15: a Struct of no fields keeps its rows (master's step 518), an
Arrow IPC file with a Null column reads (522), and an Arrow time past its day is
refused, not wrapped (525). One commit each, in that order, each passing alone; the
API is v0.7.2's, line for line. The tag is the maintainer's.

## 1. What each fix was on 0.7

Each bug is on the 0.7 line as master's records say, through 0.7's public API:

- **518.** `ScanJSONBytes` of `[{"a":{}}]`, and every JSON or NDJSON object field
  holding `{}`, failed the read: "batch declares 1 rows but its columns hold 0 (this
  is a bug in ursus)". `data.NewStruct` took a struct's length from its first field.
- **522.** Every Arrow IPC file and stream holding a Null column, at any depth, was
  refused: `column "n": the Arrow data is malformed`, arrow-go's `array.Validate`
  wanting a buffer the IPC reader never builds. Polars writes an all-null column as
  Null, so its files were refused often; pyarrow's and ursus's own `WriteIPC` too.
- **525.** Arrow import wrapped every Time32 and Time64 into [0, 24h): an IPC file
  holding 26:00 read as 02:00, with no error. And 0.7 has no Parquet `checkTimes`
  (master's step 510): a Parquet TIME past its day read as it lay, at the top level
  a batch data's boundary check calls ursus's own bug where that check is on, and in
  a List or a Struct with no error at all.

## 2. What was ported, and what was adapted

### Step 518, `e400ba1`

Master's §7 lists the fix; all of it is here, applied from master's diff where the
files are the same, by hand where 0.7's are not split:

- `data.NewStructLen(name, n, fields, valid)`, `NewStruct` taking its length from its
  first field or, with none, its validity, and the fields kept a non-nil slice.
- The 22 production callers told the length: 21 as master's diff has them, and
  `source/parquet/column.go`'s `structCol.finish` (master's `nested.go`) by hand, as
  is `kernel/strmany.go`'s, whose 0.7 form builds the struct's validity as
  `bitmap.AllSet(n)`.
- `kernel.NullColumn`'s and Arrow import's (`arrowin.structOf`) refusals of a struct
  of no fields lifted; `TestTypeRefusals` loses that case.
- The Parquet writer refuses a Struct of no fields by name, at any depth
  (`holdsEmptyStruct`), as Polars does. Without it the writer wrote such a frame
  with no error (§4).
- `DataFrame.ToStruct` of a frame of no columns gives the frame's rows, its doc saying
  so, and `FromRows` of a `struct{}` field its rows' count.

Left on master, as its §7 allows a patch release: a struct of no fields as a key
(`dtype.IsHashable`), `select_fields` of no name (`MatchToSchema` to a `Struct()`),
the `Struct()` godoc's paragraph and the guide's. On 0.7 a group-by, a join and
`Over` keyed on one stay clean refusals ("cannot group by col("a"), which is a
Struct()"). A frame's `Unique` checks no key's type, and on 0.7 answers one already:
`{}` one row and null another, the same as keyed on its witness (§3).

### Step 522, `af1e0a6`

- `internal/arrowin/record.go`'s change, applied from master's diff as it is: the
  file was master's before step 522, byte for byte, as master's §8 says, and the
  patch applied with no fuzz. `validate` hands `array.Validate` a copy in which each
  Null array of no buffer holds the nil buffer (`nullShaped`, `shapeNulls`); every
  other array is the same `*array.Data` handed to the same call.
- `internal/arrowin/nullarrays_test.go` and `nullshape_internal_test.go` as they
  stand; their helpers are on the branch.
- The 21 files of `testdata/ipc` master's `bench/scripts/oracle/record_ipcnull.py`
  wrote under Polars 2.1.0 and pyarrow 25.0.1, byte for byte, each SHA-256 checked
  against master's blob.

### Step 525, `6c30b12`

- `internal/data/timeday.go`, `data.FindOutsideDay`, as master has it, its doc one
  clause longer for the 0.7 line.
- Arrow import: `arrowin.column` refuses a time outside its day once the column is
  built (`checkTimes`), an `ErrValue` naming the column, the value and the record's
  row; `times` copies the ticks as they lie; the package doc's paragraph.
- **The Parquet reader's `checkTimes`, in master's final form**, called from
  `readBatch` in `source/parquet/parquet.go` (master's `reader.go`). Master's step 525
  only rewrote it over `FindOutsideDay`; 0.7 never had it, step 510 being master's.
  Step 525's "Left" names it as what a backport needs, so it is here, and the two
  readers share the rule on 0.7 as on master. The CHANGELOG line says so.

## 3. The tests, and their oracles

0.7 has no step-496 oracle harness, no `internal/readfuzz` and no `internal/arrowmeta`,
so master's recorded groups, fuzz seeds and the walks' tests stay there. What came,
each a differential or a Go oracle:

| test | oracle | from master |
| --- | --- | --- |
| `TestAStructOfNoFieldsKeepsItsRows` (`internal/arrowin`) | a generated arrow-go struct array of no fields: its rows and nulls, whole and windowed, and its export | as it stands |
| `TestAStructOfNoFieldsIsCompared` (`ursustest`) | a generated validity, equal to itself, apart with a row flipped and a row longer | as it stands |
| `TestEmptyStructDifferential` (root) | each answer over a frame holding a `Struct()` is the same question's over the frame without it, the struct null exactly where its witness is | cut to 0.7, below |
| `TestANullArrayOfNoBuffersReads`, `TestOtherwiseMalformedNullsAreRefused`, `TestShapeNullsCopiesOnlyThePathToANullOfNoBuffer` (`internal/arrowin`) | arrow-go's own record, written and read back by arrow-go; Validate's refusals of other malformed Null shapes; `testing.AllocsPerRun` | as they stand |
| `TestIPCNullColumnsRoundTrip` (root) | the frame written | cut to 0.7, below |
| `TestIPCNullFilesReadAsArrowGoReadsThem` (root) | arrow-go's reading of each of the 21 files | new, for master's recorded `ipcnull` group |
| `TestATimeOutsideItsDayIsRefused` (`internal/arrowin`) | refusals naming the input's own value and row | as it stands, in place of `TestTimeIsWrappedIntoTheDay`, which pinned the wrap |
| `TestArrowTimesOutsideTheDay` (root) | arrow-go's arrays, cell for cell, or the first value outside the day they hold | moved from readfuzz, below |

The cuts and additions:

- **`TestEmptyStructDifferential`:** ten generated frames, the struct built five ways
  (`JsonDecode`, NDJSON, JSON, `FromRows`, IPC), each through 29 operations at the
  default batch size and at 7, 611 subtests, 6.4 s. Master's IPC reads from bytes,
  0.7 from a file. Master's `join_on_it`, `group_key`, `group_two_keys` and `over`
  need the key the backport leaves out: each is a "refused" check here, a clean
  error, never `ErrInternal`, with the same question keyed on the witness answering.
  `unique` answers on 0.7 and stays an operation.
- **`TestIPCNullColumnsRoundTrip`:** master's six seeds, 108 subtests, each file read
  by `ReadIPCSchema`, `ScanIPC`, `ScanIPCFiles` and `ScanIPCGlob`, each stream by
  `ScanIPCStream` and `ScanArrow`, both by `ScanArrowRecords` of arrow-go's records:
  the entry points 0.7 has. The projection reads a file by `ScanIPC`.
- **`TestIPCNullFilesReadAsArrowGoReadsThem`:** each of the 21 files read through
  every entry point above, each the same frame and a file's `ReadIPCSchema` its
  schema; the frame's columns are arrow-go's schema's fields in order, its height
  arrow-go's records' rows, and its rows arrow-go's value for value, each side
  written as JSON by its own code (`array.RecordToJSON`, `WriteNDJSON`) and compared
  as decoded values. pyarrow's Map reads as the `List(Struct(key, value))` it is on
  disk on both sides, and its dictionary of Null as nulls. Master holds the same files
  to Polars' recording of them.
- **`TestArrowTimesOutsideTheDay`:** master's generator and seeds, so the same 384
  cases (Time32(s), Time32(ms), Time64(us), Time64(ns), each top level, in a List, in
  a Struct of two and in a List of such Structs): 132 read, 153 read past a time
  outside the day where no value is, 99 refused, as on master. Each is read by
  `ScanArrowRecords`, `ScanArrow`, `ScanIPC` and `ScanIPCStream`. It has its own
  `cellOf` and `sameValue` for the types it generates, readfuzz's being master's. And
  each case's records are written to Parquet by arrow-go's own column writers, their
  Dremel levels shredded from arrow-go's arrays (`writeParquetRecords`), and read by
  `ScanParquet`: arrow-go's cells, or an `ErrValue` naming the first value. pqarrow
  would have done it in four lines and added `golang.org/x/net`, grpc, protobuf and
  genproto to the root `go.mod`. Parquet has no seconds, and 0.7's Parquet reader
  refuses a List of Structs whatever it holds ("repeated (list) columns are not
  supported"), so those two are left to the Arrow readers: 216 cases through Parquet.
  0.28 s.

## 4. The teeth

Each mutation made from a saved copy, the tests run, and the file put back from the
copy, its SHA-256 checked; never by git.

| mutation | what fails | |
| --- | --- | --- |
| 518: the old length rule in `data/column.go` (`n` from `fields[0]`, else 0) | `TestAStructOfNoFieldsKeepsItsRows`: "a Struct() of 0 rows"; `TestAStructOfNoFieldsIsCompared`: "30 rows, has 0"; `TestEmptyStructDifferential`: all 11 subtests, each frame refused as it is built ("produced 0 rows for a 80-row batch", "batch column "a" has 0 rows, but column "i" has 235") | **bites** |
| 518: `holdsEmptyStruct` always false | the 10 "refused" subtests: "parquet of a Struct of no fields: <nil>", the writer writing a group of no columns with no error | **bites** |
| 518: `NullColumn` refusing a struct of no fields again | `join_inner` and `join_full` at both batch sizes in all 10 trials, the 10 "refused" subtests (the join keyed on the witness), and the spilled `join_build_side`: "cannot build a null column of Struct()" | **bites** |
| 522: the old `record.go`, whole | `TestIPCNullColumnsRoundTrip`: 72 of 108, every frame of rows; `TestIPCNullFilesReadAsArrowGoReadsThem`: 18 of 21, "ursus refuses polars_null.arrow, which arrow-go reads: … column "n": the Arrow data is malformed" (the three that pass hold no record batch: `polars_null_empty.arrow`, `pyarrow_null_empty.arrow{,s}`); arrowin's tests do not build, `shapeNulls` undefined | **bites** |
| 522: `validate` handing Validate the array itself, the helpers kept | `TestANullArrayOfNoBuffersReads`: column "null" malformed; `TestOtherwiseMalformedNullsAreRefused`: a Null array with a child refused for its buffers, not its child | **bites** |
| 525: the wrap back (commit 2's `builder.go`), the check kept | `TestATimeOutsideItsDayIsRefused`: six refusals "accepted" and the window's; the differential: "time32[s] top level, case 5, ScanArrowRecords: record 0's row 3 holds -1, outside [0, 86400), and was read" | **bites** |
| 525: Arrow import's check not called | the same seven in `TestATimeOutsideItsDayIsRefused`; the differential: data's boundary check, on in the root tests, answers `ErrInternal` where an `ErrValue` is wanted | **bites** |
| 525: Parquet's check not called | the differential: "time32[ms] top level, case 5, ScanParquet: want an ErrValue … got … this is a bug in ursus"; with the top level skipped as well, "time32[ms] in a List, case 2, ScanParquet: record 1's row 3 holds -2147483648, outside [0, 86400000), and was read", the silent read 0.7 had | **bites** |
| 525: a List's element index reported as its row | `TestATimeOutsideItsDayIsRefused`, "26:00 in a List" names row 3; the differential: "time32[s] in a List, case 2", row 11 where 7 | **bites** |
| 525: a Struct's first field winning over its least row | the differential: "time32[s] in a Struct, case 8" names another value and row | **bites** |

10 of 10 bite.

## 5. Fuzzing

0.7 has no fuzz target over JSON, NDJSON or IPC (`FuzzHybrid` and `FuzzEvery` only),
so none ran. Master's three CI inputs were replayed through 0.7's public API on the
branch, under `prlimit --as=4294967296`: `FuzzScanJSON/empty-object-field`
(`[{"":{}}]`) reads as one `Struct()` row `{}`; `FuzzScanNDJSON/empty-object-beside-repeated-keys`
reads, its `"1"` a `Struct()`; `FuzzScanIPCFile/time64-past-its-day` is refused, "column
"time64" holds the Time(ns) 1751093230692204799 in row 3 of its record, outside [0,
86400000000000)", where commit 2, before the fix as 0.7.2 is, reads row 7 as
6h47m10.692204799s, the tick wrapped into the day. Not kept: 0.7 has no target to
seed.

## 6. Left out, and why

- master's recorded groups `emptystruct` and `ipcnull`, their recorders, and their
  loaders: step 496's harness is master's;
- the fuzz seeds and readfuzz's and arrowmeta's changes: neither package is on 0.7;
- the additions of step 518 listed in §2;
- step 525's "record's place in a stream reader's error", left on master too.

## 7. Checks

After the last edit, `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`, all
passing:

- release-0.7's CI commands: `go build ./...`, `go vet ./...`, `go run
  ./internal/gen/levels` ("levels: ok"), `make api` (the strict diff against
  `api/v0.7.2.txt`: empty), and `go test -run TestRiskGate ./internal/arrowx/`;
- `go test ./...` under the shared lock: 34 packages ok, the root package in 1,242 s
  on a laptop shared with four other steps; `gofmt -l .` empty;
- each commit alone: commit 1's touched packages and its root package in full (327
  s); commit 2's arrowin and its root package in full, from a worktree of its own at
  `af1e0a6` (795 s). A first run of commit 2's root package in this worktree failed
  `TestEveryCitedTestExists`, which reads the sources as it runs, because step 525's
  test edit had landed while it ran; the run from its own tree passed.

The widths, the scalar build and the race detector are CI's.
