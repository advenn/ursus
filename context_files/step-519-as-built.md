# Step 519 — as built

**Arrow IPC metadata is walked before arrow-go reads it.** The first finding step 511
left for the maintainer (§5 of [`step-511-as-built.md`](./step-511-as-built.md)):
arrow-go v18.7.0 trusts every length an IPC footer or message declares, so a forged
file of 0.5 to 2 KB ended the process six ways, through `ScanIPC`, `ScanIPCFiles`,
`ScanIPCStream`, `ReadIPCSchema` and every other entry point that opens IPC. A new
package, `internal/arrowmeta`, walks every footer and message in the bytes arrow-go
is about to read, and refuses what arrow-go would trust as corrupt, `ErrIO`; it is
also Parquet's walk of a footer's Arrow schema now (step 510's `schemaFields`,
moved). No well-formed input is refused: 3,600 files and streams arrow-go wrote, of
every type it writes, and 26 more read end to end, each read as arrow-go reads it.

## 1. The six findings

| seed (`internal/readfuzz/testdata/fuzz/`) | arrow-go v18.7.0 | ended | refused now |
| --- | --- | --- | --- |
| `FuzzScanIPCFile/finding-schema-fields-2e31` (650 B), `FuzzScanIPCStream/…` (440 B) | `schemaFromFB`, `make([]arrow.Field, schema.FieldsLength())` (`arrow/ipc/metadata.go`) | out of memory | "a schema's fields declare 2147483647 elements of 4 bytes at byte 116, in 192 bytes" |
| `…/finding-field-children-2e31` (810 B, 552 B) | `fieldFromFB`, `make([]arrow.Field, field.ChildrenLength())` | out of memory | "a field's children declare 2147483647 elements …" |
| `…/finding-schema-metadata-2e31` (754 B, 496 B) | `metadataFromFB`, two `make([]string, CustomMetadataLength())` | the system's OOM killer | "a schema's custom metadata declare 2147483647 elements …" |
| `…/finding-variadic-buffers-2e40` (802 B, 608 B) | `loadCommon`, `make([]*memory.Buffer, 0, 2+variadicCount)` of a view column (`file_reader.go`) | out of memory | "a record batch declares 1099511627776 variadic buffers for a view column, and has 3 buffers" |
| `FuzzScanIPCFile/finding-dictionary-variadic-buffers` (1,275 B) | the same, for a dictionary batch, which a file's dictionary block is read as whatever its header says (`readDictionary`) | out of memory | its footer: "a footer's record batch blocks at byte 16 points to byte 62755, past the 346 bytes"; the dictionary batch's count is refused too where the footer is whole (`TestForgedMetadataIsRefused`) |
| `…/finding-field-its-own-child` (810 B, 552 B) | `fieldFromFB` recursing on a field among its descendants, an offset past 2^31 wrapping back | stack overflow | "a field's child at byte 148 points back to byte 124: offsets point forward, and a field cannot be its own descendant" |

Each is refused through every public entry point that opens IPC (`ScanIPCBytes`,
`ScanIPC`, `ScanIPCFiles`, `ReadIPCSchema`; `ScanIPCStreamBytes`, `ScanIPCStream`,
`ScanIPCStreamFrom`), within `fuzzcheck.Run`'s bounds (`TestIPCMetadataFindingsAreRefused`,
74 reads, the kitchen-sink forgeries of `patchMeta` among them; `ReadIPCSchema`,
which reads no record batch, answers where the forgery is in one). Each was first
replayed from a test binary under `prlimit --as=4294967296`. The fuzz targets no
longer skip them: `oversized` (`ipcmeta_test.go`) skips only a record batch of more
rows than its input could hold, `finding-record-of-2e40-rows-of-no-data`, and the
Zstandard window findings stay skipped, the maintainer's. That seed's bytes are
refused by the walk now, its nodes vector pointing past its message; a well-formed
batch of 2^40 Null rows is not, and the decision stays open.

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
- **Depth.** Fields nest no deeper than `MaxDepth`, 2,000, a top-level field at 1.
  arrow-go's `fieldFromFB` recurses once a level with no limit; 16,000 overflowed the
  fuzz targets' 8 MiB stack (511). A List nested 1,000 deep, 1,001 fields, still reads
  (`TestScanIPCDeepSchema`), and 2,000 fields read where 2,001 are refused
  (`TestFieldsNestNoDeeperThanMaxDepth`).
- **Counts.** Every vector's count is what its bytes hold, of elements of their size:
  fields, children, key-values, features, union type ids, nodes, buffers, blocks,
  variadic counts.
- **A budget.** A field costs 64, a key-value 32, a string its bytes, against 16 times
  the buffer plus 1 MiB. Offsets may point forward to one table from many, so a 4 KB
  schema of structs 40 deep, each struct's two children one, is 2^40 fields to
  arrow-go, which would build each. Each costs each time it is reached, and the walk
  refuses it at its budget (`TestASharedFieldCostsEachTimeItIsReached`). This was not
  among step 511's findings; it is the same class, found in designing the walk. No
  builder shares tables, and a field takes 32 bytes at least, a key-value 16, each
  more than its cost over 16, so no well-formed buffer reaches the budget.
- **A record batch.** Its buffers lie within its body (the block's body length in a
  file, which is what arrow-go reads, the message's in a stream). Its variadic counts
  number at least its view columns, nested ones among them and a dictionary's values
  not (a dictionary batch's are its dictionary's), as arrow-go reads one for each, in
  the vector or past it; and each count, and their sum, is no more than its buffers.
  Its codec is LZ4 or Zstandard. Its rows are not checked (the open finding above).
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
  the schema's dictionaries comes before any record batch, as arrow-go reads the
  first messages after the schema as dictionary batches whatever their headers say;
  nothing but dictionary and record batches follows.
- **Parquet's footer schema**: `arraysOf` takes `arrowmeta.SchemaFields`, which walks
  the schema message whole, children among them, where step 510's walk read only the
  top-level fields. A footer whose Arrow schema is refused keeps its Parquet types, as
  before.

A refusal is `KindIO`: "*name* is corrupt: in its footer, a schema's fields declare
2147483647 elements of 4 bytes at byte 116, in 192 bytes". `openIPC`, `openIPCStream`
and a file's record batches return it as it is, without arrow-go's words around it or
the hint that the input may be the other format. A panic in the walk itself is
ursus's bug, an internal error, which the fuzz targets would fail on.

## 3. No clean input is refused

| test | inputs | oracle |
| --- | --- | --- |
| `arrowmeta.TestTheWalkPassesWhatArrowGoWrites` | 3,600: 300 seeds of `gen`'s records, one to five columns nested three deep, of every type arrow-go writes (views of several buffers, dictionaries in lists and structs, sparse and dense unions, runs, list views, decimals, intervals, UUID and JSON extensions, field and schema metadata), little-endian, and of the fixed-width types big-endian; each a file and a stream, uncompressed, LZ4 and Zstandard | read through `File` or `NewMessageReader`, each reads record for record as arrow-go reads it alone (`array.RecordEqual`). 3,534 read back as the record written; 876 of those read hold a dictionary, 1,248 a view, 1,758 are big-endian. The other 42 arrow-go refuses alone, and so does ursus: a big-endian Decimal or MonthDayNano interval of no rows, whose missing buffer arrow-go's byte swap dereferences. The 24 that read but not as written are arrow-go's own misreadings, of nested types in a union or a list view |
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
| `FuzzScanParquet` | 12,963, stopped at 18 s; 28,278 | 19; 24 | the first stopped on the Parquet reader's finding below, not the walk's; the second clean |

Each run spent 6–7 s gathering a baseline of the 233 to 832 inputs in Go's fuzz
cache, and most of the rest minimising new ones. Nothing they saved was kept.

## 5. Teeth

Each mutation made in `walk.go`, its tests built and run under `prlimit
--as=4294967296`, and `walk.go` put back from a saved copy, its SHA-256 checked.

| mutation | test | |
| --- | --- | --- |
| the descendant check: a child the walk cannot follow passed over, not refused | `finding-field-its-own-child`, file and stream: `runtime: goroutine stack exceeds 8388608-byte limit`, `fatal error: stack overflow` in arrow-go's `fieldFromFB` | **bites** |
| the forward rule alone taken out, a backward offset followed | the same seeds, and `TestForgedMetadataIsRefused/a_field_its_own_child`: still refused, "fields nest past 2000 levels"; the depth bound stands behind the forward rule | backstop |
| the count check: a vector its bytes cannot hold walked as empty | `finding-schema-fields-2e31` (file) and `finding-field-children-2e31` (stream): `fatal error: out of memory`, a block of 188,978,561,024 bytes; `finding-schema-metadata-2e31`: 34,359,738,368 | **bites** |
| the variadic counts unchecked against the buffers | `finding-variadic-buffers-2e40`, and `TestForgedMetadataIsRefused`'s dictionary batch in a file and a stream: out of memory, 8,796,097,216,512 bytes. `finding-dictionary-variadic-buffers` is still refused, by its footer (§1) | **bites** |
| a dictionary's values' views counted in a record batch's | `TestTheWalkPassesWhatArrowGoWrites`: a clean file refused, "a record batch has 1 variadic buffer counts for its 3 view columns" (`TestTheWalkPassesCleanInputs` holds no such column) | **bites** |
| the budget never spent | `TestASharedFieldCostsEachTimeItIsReached`: the walk itself visits 2^40 fields, "test timed out after 1m0s" | **bites** |

## 6. Findings, left

- **A Null column in an IPC file is refused.** arrow-go's IPC reader builds a Null
  array of no buffers (`loadNull`), which its own `array.Validate`, which ursus's
  arrowin calls, refuses, "expected at least 1 buffers for null, got 0". ursus's own
  `WriteIPC` of a Null column does not read back, nor would a Polars file's. Not the
  walk's; `everyType` leaves the column out.
- **A footer may list one block many times.** Each is read, so a file whose footer
  lists one record batch thousands of times is read as thousands of batches: memory
  quadratic in the file's size, not linear. No writer does it; arrow-go and Polars
  would read it. Whether to refuse blocks that overlap is the maintainer's.
- **`FuzzScanParquet`** stopped once on a 283-byte file whose INT64 page holds fewer
  values than it declares: five of its eight rows are left unwritten, and read from
  the poisoned buffer, where a read into zeroed buffers gives zeros. The Parquet
  reader's, with no Arrow schema in it, and still so after step 517 merged; not kept
  here, for the Parquet steps.
- arrow-go misreads its own IPC writing of dictionaries of runs, list views, unions
  and dictionaries, and of some nested types in unions and list views; `gen` makes
  dictionaries of text, views, leaves and views in a list or a struct.

## 7. Checks

On the branch, and again after master was merged in (steps 512 and 517):

- `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`: `go test ./internal/...`
  ok; `go test .` in full, under the lock, ok.
- `GOEXPERIMENT=simd go vet ./...`: clean. `go run ./internal/gen/levels`: ok, with
  `internal/arrowmeta` at 1.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 0 gone.
- `go run ./internal/gen/testinventory -check`: as the baseline, 1,225.
- `gofmt -l .`: empty.
