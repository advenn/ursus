# Step 519 — as built

**Arrow IPC metadata is walked before arrow-go reads it.** The first item step 511
left for the maintainer (§5 of [`step-511-as-built.md`](./step-511-as-built.md)): an
IPC footer or message is untrusted, and every length it declares is a claim. A new
package, `internal/arrowmeta`, walks every footer and message in the bytes arrow-go
is about to read, through `ScanIPC`, `ScanIPCFiles`, `ScanIPCStream`, `ReadIPCSchema`
and every other entry point that opens IPC, and refuses as corrupt, `ErrIO`, a count
its bytes cannot hold, an offset that does not point forward, and fields nested past
a bound; it is also Parquet's walk of a footer's Arrow schema now (step 510's
`schemaFields`, moved). No well-formed input is refused: 3,600 files and streams
arrow-go wrote, of every type it writes, and 26 more read end to end, each read as
arrow-go reads it.

## 1. The forgeries refused

| forgery | refused now |
| --- | --- |
| a schema's fields claiming more than their bytes hold | "a schema's fields declare *n* elements of 4 bytes at byte *p*, in *m* bytes" |
| a field's children claiming more than their bytes hold | "a field's children declare *n* elements …" |
| a schema's custom metadata claiming more than its bytes hold | "a schema's custom metadata declare *n* elements …" |
| a record batch's variadic buffer counts for a view column past its buffers | "a record batch declares *n* variadic buffers for a view column, and has 3 buffers" |
| the same, for a dictionary batch in a file | its footer: "a footer's record batch blocks at byte 16 points to byte 62755, past the 346 bytes"; the dictionary batch's count is refused too where the footer is whole (`TestForgedMetadataIsRefused`) |
| a field among its own descendants, an offset past 2^31 wrapping back | "a field's child at byte 148 points back to byte 124: offsets point forward, and a field cannot be its own descendant" |

Each is refused through every public entry point that opens IPC (`ScanIPCBytes`,
`ScanIPC`, `ScanIPCFiles`, `ReadIPCSchema`; `ScanIPCStreamBytes`, `ScanIPCStream`,
`ScanIPCStreamFrom`), within `fuzzcheck.Run`'s bounds (`TestForgedIPCMetadataIsRefused`,
74 reads, the kitchen-sink forgeries of `patchMeta` among them; `ReadIPCSchema`,
which reads no record batch, answers where the forgery is in one). The fuzz targets
no longer skip them: `oversized` (`ipcmeta_test.go`) skips only a record batch of more
rows than its input could hold, and the Zstandard window inputs stay skipped, the
maintainer's. The input of many rows that step 511 kept is refused by the walk now,
its nodes vector pointing past its message; a well-formed batch of 2^40 Null rows is
not, and the decision stays open.

## 2. The walk

`internal/arrowmeta`, level 1, uerr its only ursus import (`go run ./internal/gen/levels`).
Plain byte reads, no flatbuffers package. Positions are `uint32` and add as
flatbuffers' Go accessors add them, wrapping, so the walk reads what arrow-go would.

It walks the Message, Footer, Schema, Field, KeyValue, DictionaryEncoding, the type
tables, RecordBatch, BodyCompression and DictionaryBatch tables, each field arrow-go
reads, and checks:

- **Bytes.** Every table's soffset and vtable, every field's bytes, and every offset to
  a table, vector or string lie within the buffer. A buffer of 2^31 bytes or more is
  refused, so no wrapped position is ambiguous.
- **Forward.** Every offset points past itself, as a builder writes them. A field's
  children are reached only forward, so none is its own descendant.
- **Depth.** Fields nest no deeper than `MaxDepth`, 2,000, a top-level field at 1, so
  a reader that recurses once a level stays well inside the fuzz targets' 8 MiB stack.
  A List nested 1,000 deep, 1,001 fields, still reads (`TestScanIPCDeepSchema`), and
  2,000 fields read where 2,001 are refused (`TestFieldsNestNoDeeperThanMaxDepth`).
- **Counts.** Every vector's count is what its bytes hold, of elements of their size:
  fields, children, key-values, features, union type ids, nodes, buffers, blocks,
  variadic counts.
- **A budget.** A field costs 64, a key-value 32, a string its bytes, against 16 times
  the buffer plus 1 MiB. Offsets may point forward to one table from many, so a 4 KB
  schema of structs 40 deep, each struct's two children one, is a tree of 2^40 fields
  to a reader that follows each offset. Each costs each time it is reached, and the
  walk refuses it at its budget (`TestASharedFieldCostsEachTimeItIsReached`). It was
  found in designing the walk. No builder shares tables, and a field takes 32 bytes
  at least, a key-value 16, each more than its cost over 16, so no well-formed buffer
  reaches the budget.
- **A record batch.** Its buffers lie within its body (the block's body length in a
  file, which is what arrow-go reads, the message's in a stream). Its variadic counts
  number at least its view columns, nested ones among them and a dictionary's values
  not (a dictionary batch's are its dictionary's), one for each; and each count, and
  their sum, is no more than its buffers. Its codec is LZ4 or Zstandard. Its rows are
  not checked (the open decision above).
- **Types.** A field has a type table, of a tag Arrow has.

What is walked, where:

- **A file** is served to `ipc.NewFileReader` by `arrowmeta.File`. `OpenFile` reads
  the last ten bytes and the footer, walks the footer, and serves arrow-go those
  bytes from then on, whatever the source holds by then. arrow-go reads each block's
  metadata in one `ReadAt` of exactly the block's offset and metadata length, so
  `File.ReadAt` walks the bytes of such a read, in the buffer it hands arrow-go,
  before arrow-go has them: every dictionary block when the file opens, each record
  block when its batch is read. A dictionary block must hold a dictionary batch and a
  record block a record batch. A file arrow-go refuses before reading a footer (too
  short, no magic, a footer length past the file) is left to arrow-go.
  `TestEveryBlockIsWalked` holds arrow-go to its read pattern: every block it reads
  is walked once.
- **A stream** is read by `arrowmeta.NewMessageReader`, which replaces
  `ipc.NewMessageReader` under `ipc.NewReaderFromMessageReader`, with the same limits
  (64 and 256 MiB, or the stream's size where it is known), the same end-of-stream
  rules, and its body from ursus's `ReadMem`. Each message's metadata is walked before
  its body is allocated: the first must be a schema; a dictionary batch for each of
  the schema's dictionaries comes before any record batch, as the format orders them;
  nothing but dictionary and record batches follows.
- **Parquet's footer schema**: `arraysOf` takes `arrowmeta.SchemaFields`, which walks
  the schema message whole, children among them, where step 510's walk read only the
  top-level fields. A footer whose Arrow schema is refused keeps its Parquet types, as
  before.

A refusal is `KindIO`: "*name* is corrupt: in its footer, a schema's fields declare
*n* elements of 4 bytes at byte *p*, in *m* bytes". `openIPC`, `openIPCStream`
and a file's record batches return it as it is, without arrow-go's words around it or
the hint that the input may be the other format. A panic in the walk itself is
ursus's bug, an internal error, which the fuzz targets would fail on.

## 3. No clean input is refused

| test | inputs | oracle |
| --- | --- | --- |
| `arrowmeta.TestTheWalkPassesWhatArrowGoWrites` | 3,600: 300 seeds of `gen`'s records, one to five columns nested three deep, of every type arrow-go writes (views of several buffers, dictionaries in lists and structs, sparse and dense unions, runs, list views, decimals, intervals, UUID and JSON extensions, field and schema metadata), little-endian, and of the fixed-width types big-endian; each a file and a stream, uncompressed, LZ4 and Zstandard | read through `File` or `NewMessageReader`, each reads record for record as arrow-go reads it alone (`array.RecordEqual`). 3,534 read back as the record written; 876 of those read hold a dictionary, 1,248 a view, 1,758 are big-endian. The other 66 read through the walk as they read without it |
| `arrowmeta.TestTheWalkReadsOnlyItsBytes` | every flatbuffer of 20 seeds' uncompressed files and streams, cut at every length, and every byte set to 0x00, 0x7f, 0x80 and 0xff: 537,280 walks | the walk refuses or passes, and never panics |
| `readfuzz.TestTheWalkPassesCleanInputs` | 15 files and 11 streams: `testdata/ipc`'s six, Polars 1.44.1's (the repository records no Polars 2.1 IPC bytes); step 511's kitchen sink in each codec, a List 60 deep, 1,000 integers, custom metadata; the kitchen sink's fixed-width columns big-endian; ursus's own `WriteIPC` and `WriteIPCStream` of the kitchen sink with an Int128, a Categorical and a struct of a list, text and a Categorical, in each codec | `ScanIPCBytes` or `ScanIPCStreamBytes` under the targets' bounds reads each, its frame what arrow-go reads of the bytes cell for cell (`checkIPC`) |
| `parquet.TestTheFootersArrowSchemaIsReadAsArrowGoReadsIt` | step 510's 52 schemas, with and without the continuation marker, and each cut at every length | `arraysOf`'s answer is arrow-go's; every cut refused |
| `readfuzz.TestOversizedPassesCleanInputs` | step 511's | the targets' own walk skips none |

## 4. The fuzz runs

Each target alone, under the shared lock, four workers, `-fuzzminimizetime=3s`, from
a binary built with `go test -c -fuzz`, run under `prlimit --as=4294967296`.

| target | inputs executed | new interesting | |
| --- | --- | --- | --- |
| `FuzzScanIPCFile` | 6,505; 5,347 | 14; 16 | clean |
| `FuzzScanIPCStream` | 4,467; 4,780 | 15; 11 | clean |
| `FuzzScanParquet` | 12,963, stopped at 18 s; 28,278 | 19; 24 | the first stopped on the Parquet input below, not the walk's; the second clean |

Each run spent 6–7 s gathering a baseline of the 233 to 832 inputs in Go's fuzz
cache, and most of the rest minimising new ones. Nothing they saved was kept.

## 5. Teeth

Each mutation made in `walk.go`, its tests built and run under `prlimit
--as=4294967296`, and `walk.go` put back from a saved copy, its SHA-256 checked.

| mutation | test | |
| --- | --- | --- |
| the descendant check: a child the walk cannot follow passed over, not refused | the field its own child, file and stream | **bites** |
| the forward rule alone taken out, a backward offset followed | the same inputs, and `TestForgedMetadataIsRefused/a_field_its_own_child`: still refused, "fields nest past 2000 levels"; the depth bound stands behind the forward rule | backstop |
| the count check: a vector its bytes cannot hold walked as empty | the schema's fields (file), the field's children (stream) and the schema's custom metadata | **bites** |
| the variadic counts unchecked against the buffers | the view column's variadic buffers, and `TestForgedMetadataIsRefused`'s dictionary batch in a file and a stream. The dictionary batch in a file is still refused, by its footer (§1) | **bites** |
| a dictionary's values' views counted in a record batch's | `TestTheWalkPassesWhatArrowGoWrites`: a clean file refused, "a record batch has 1 variadic buffer counts for its 3 view columns" (`TestTheWalkPassesCleanInputs` holds no such column) | **bites** |
| the budget never spent | `TestASharedFieldCostsEachTimeItIsReached`: the walk itself visits 2^40 fields, "test timed out after 1m0s" | **bites** |

## 6. Findings, left

- **A Null column in an IPC file is refused.** arrow-go's IPC reader builds a Null
  array of no buffers (`loadNull`), which its own `array.Validate`, which ursus's
  arrowin calls, refuses, "expected at least 1 buffers for null, got 0". ursus's own
  `WriteIPC` of a Null column does not read back, nor would a Polars file's. Not the
  walk's; `everyType` leaves the column out. **Fixed by
  [step 522](./step-522-as-built.md):** a Null array of no buffers is given the nil
  buffer before `array.Validate`, and `everyType` holds a Null column again.
- **A footer may list one block many times.** Each is read, so a file whose footer
  lists one record batch thousands of times is read as thousands of batches. No
  writer does it. Whether to refuse blocks that overlap is the maintainer's.
- **`FuzzScanParquet`** stopped once on a 283-byte file whose INT64 page holds fewer
  values than it declares. The Parquet reader's, with no Arrow schema in it, and
  still so after step 517 merged; not kept here, for the Parquet steps (step 521
  refuses such a page).
- `gen` makes dictionaries of text, views, leaves and views in a list or a struct,
  and no dictionary of runs, list views, unions or dictionaries.

## 7. Checks

On the branch, and again after master was merged in (steps 512, 515, 517 and 518):

- `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`: `go test ./internal/...`
  ok; `go test .` in full, under the lock, ok.
- `GOEXPERIMENT=simd go vet ./...`: clean. `go run ./internal/gen/levels`: ok, with
  `internal/arrowmeta` at 1.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 0 gone.
- `go run ./internal/gen/testinventory -check`: as the baseline, 1,225.
- `gofmt -l .`: empty.
