# Step 511 — as built

**Fuzz targets for NDJSON, JSON and Arrow IPC.** Steps 447 and 450 of
[`v0.7.2-plan.md`](./v0.7.2-plan.md) (M3, H4.4 and H4.6), and 451's mechanism, run on
master's code before v0.8.0, as step 506's plan says, on step 508's harness. Six
targets, each held to an outside answer: the standard library's `encoding/json`, or
arrow-go's own reading of the same bytes, or a differential. They found three ways a
file of a few kilobytes ended the process, out of memory, which are fixed; a public
entry point that let arrow-go's panic reach its caller, fixed; a read whose memory grew
with the cube of a line's depth, fixed; and two fatal crashes inside arrow-go's
reading of IPC metadata, and four other findings, left for the maintainer and skipped
by name.

## 1. The targets

| package | target | input | oracle |
| --- | --- | --- | --- |
| `internal/source/ndjson` | `FuzzObjectLines` | a JSON document, a read size, a buffer size | the document read whole against read in pieces, and sixty-four bytes at a time against a byte at a time (`vectorScan`), byte for byte and error for error; `encoding/json`: a document `json.Valid` calls an array of objects, or one, is its objects a line each, as `json.Unmarshal` into `[]json.RawMessage` finds them, their newlines spaces; any other valid document is refused |
| `internal/kernel` | `FuzzJSONDecode` | a string | `parseJSON` reads what `json.Valid` accepts and nothing else; what it reads is `json.Unmarshal`'s, an object's last value of a repeated key (`json_path_match`'s rule), its numbers' literals kept; keeping the first value (`json_decode`'s) has the same shape; `writeJSON` writes JSON that reads back as the same value, a number as the same float; `json_path_match("$")` is the value, a string unquoted; `json_decode` into nine types and three more paths answer or refuse |
| `internal/readfuzz` | `FuzzScanNDJSON` | any bytes, through `ScanNDJSONBytes` | each line against `encoding/json`'s tokens (`checkRows`): a frame read is a row a non-blank line, each line an object `encoding/json` reads nested no deeper than 1,000, each cell its key's first value by its column's type (an integer an Int64 or an Int128 by `math/big`, a number a Float64 by `strconv.ParseFloat`, a String of a number the same value, a List's elements and a Struct's fields by theirs); rows of scalars whose keys' types meet as `ScanNDJSON`'s doc lists are inferred a schema; a schema inferred from every row reads them all |
| `internal/readfuzz` | `FuzzScanJSON` | any bytes, through `ScanJSONBytes` | a document `json.Valid` refuses (a byte-order mark left out) is refused, one that is neither an array of objects nor an object too; the objects `json.Unmarshal` finds are held to `checkRows` as lines are |
| `internal/readfuzz` | `FuzzScanIPCFile` | any bytes, through `ScanIPCBytes` | arrow-go: where ursus reads a frame, `ipc.NewFileReader` reads every record under the same bounds, and ursus's columns are its fields, its rows theirs, each cell the value its array holds (`cellOf`: integers and floats by width and bits, a half by IEEE 754's definition, text, bytes, dates and instants as instants, times and durations, lists, list views, maps and structs by their elements, a dictionary's value; a decimal null for null); one input in eight also through `ScanIPC` and `ScanIPCFiles` of a file, which must read as the bytes did, and `ReadIPCSchema`, which must equal `CollectSchema` |
| `internal/readfuzz` | `FuzzScanIPCStream` | any bytes, through `ScanIPCStreamBytes` | as the file's, through `ipc.NewReaderFromMessageReader`; one input in eight also through `ScanIPCStream` of a file |

Every target runs its read under `fuzzcheck.Run`. The text readers' limits are
readfuzz's (`limits(n)`: 10 s, 64 MiB plus 1 KiB a byte). The IPC targets read
through a bounded `fuzzcheck.Allocator` put under ursus's own allocator
(`arrowin.ReadBase`), its limit the one ursus grants (`arrowin.ReadLimit`, §4), so a
request ursus's bound lets past fails the input; their allocation bound adds twice
that limit, the most one buffer of a well-formed input may take. readfuzz's targets
run on an 8 MiB stack, its `init` (the plan's 447), so a parser that recursed once a
level again ends the binary on an input of a hundred kilobytes, which
`TestScanJSONDeep` holds, and not only on one of hundreds of megabytes.

The plan's 419 (`json_decode`'s nesting) is not needed on master: it supposed
`encoding/json`'s `Token`, which `readJSON` reads through, has no depth limit. Go
1.27's refuses past 10,000 levels; 100,000 levels, unclosed, are refused under an 8
MiB stack (`TestJSONDecodeDeep`). A limit of 1,000, NDJSON's, was written and taken
out: there was no crash to fix, and it would have refused values the docs read.

The plan's 451 as written is not needed either: arrow-go v18.7.0 checks each footer
block against the file's size (`validateFileBlock`), and a block claiming 2^40 bytes
is refused allocating nothing. Its mechanism, an allocator bounded by the input, is
what the decompressed-length finding needed (§4).

## 2. Seeds

| target | corpus files | generated in the target | run time |
| --- | --- | --- | --- |
| `FuzzObjectLines` | 22: the document tests' cases, brackets and escapes in strings, a byte-order mark, every refusal, long strings and a backslash at a 64-byte block's end for the vector scan | — | 0.00 s |
| `FuzzJSONDecode` | 21: numbers past every width, escapes, surrogate halves alone and paired, raw bytes not UTF-8, repeated keys, an object of 21 keys (the member map), trailing commas and text, a leading zero, `NaN`, a control character | — (`TestJSONDecodeDeep`: 10,000 levels of arrays and of objects, 10,001, and 100,000 unclosed) | 0.00 s, and 0.14 s |
| `FuzzScanNDJSON` | 35: every type, integers meeting floats, text meeting anything, Int128 and past it, a Boolean beside a number, missing and repeated keys, blank and CRLF lines, escapes and lone surrogates, bytes not UTF-8 in values and keys, malformed lines, nested lists and structs, the coercion findings, a byte-order mark, an empty file; two findings' inputs | 8: arrays 999 and 1,000 deep in a line, 999 in a document, 106 lines past the inference window with a new key and a new type, the same as a document, and one file in gzip, zlib and Zstandard (`TestScanJSONDeep`: 50,000 levels of arrays, 20,000 of objects) | 0.18 s |
| `FuzzScanJSON` | 25: arrays, one object, an empty array, a byte-order mark, newlines inside objects, every refusal of `TestObjectLinesRefuses`' kind, a key past the inferred schema, brackets in strings, 30 levels; a finding's input | the same 8 | 0.23 s |
| `FuzzScanIPCFile` | 8: the findings' inputs, of 0.5 to 2 KB | 24: `testdata/ipc`'s five Polars files; a kitchen sink of 33 columns, every type arrowin reads (half subnormals, string and binary views, a ListView, a map, a dictionary), uncompressed, LZ4 and Zstandard; a List 60 deep with a row and 70 deep without; half a file, the magic alone, nothing; the body length patched to 2^40 in each place it is written; a compressed buffer's decompressed length 2^40 in each codec; a schema's fields, a struct's children and a schema's custom metadata 2^31−1, a view column's variadic buffers 2^40, a field its own child (`TestScanIPCDeepSchema`: 1,000 deep) | 0.03 s |
| `FuzzScanIPCStream` | 7: as the file's | 21: as the file's, and a message's metadata length 2^31−1 | 0.02 s |

Inputs over about 20 KB are ordinary tests, not seeds: the fuzzer minimises each new
input it finds interesting for up to a minute by default, and spent most of a 20 s run
on a 100 KB seed's variants, at 0 executions a second.

## 3. The local fuzz runs

Each target was fuzzed for at most 20 s at a time, alone, under the shared lock, four
workers. The runs that ended clean after the last change to each:

| target | inputs executed |
| --- | --- |
| `FuzzObjectLines` | 204,235 |
| `FuzzJSONDecode` | 188,601 |
| `FuzzScanNDJSON` | 46,798 (minimising 3 s), 88,415 (not minimising) |
| `FuzzScanJSON` | 57,711, 74,574 |
| `FuzzScanIPCFile` | 68,579, 51,679 (not minimising) |
| `FuzzScanIPCStream` | 69,286, 56,791 (not minimising) |

Eight earlier runs stopped on a finding (§4, §5), the first 2,046 inputs in.
CI's `fuzz.yml` runs each target 30 s with Go's default minimisation, which these
targets' interesting inputs make slow: `-fuzzminimizetime` of a few seconds would let
it execute more.

## 4. Findings fixed

| finding | how found | fix |
| --- | --- | --- |
| **An IPC buffer declaring 2^40 bytes decompressed ended the process.** arrow-go sizes a compressed buffer's decompression by the eight bytes the buffer declares (`ipcSource.buffer`, `arrow/ipc/file_reader.go`), unchecked: a 1.7 KB LZ4 file declaring 2^40 bytes ended `ScanIPC` and its kin with `fatal error: runtime: out of memory`; 2^35 took 32 GiB of address space | the plan's 450 probe, then seeds | `arrowin.ReadMem`, the allocator every IPC read is made with now (`ipc.WithAllocator` in `openIPC` and `openIPCStream`): no one request past 32,768 times the input's size, Zstandard's most (a four-byte block regenerates 128 KiB; LZ4's is 255), plus 1 MiB, so no well-formed input is refused; past that an `ErrIO` naming the corrupt input. Of a stream of unknown size, 32,768 times the bytes read so far, a buffer being decompressed from bytes already read, plus arrow-go's 256 MiB body. `finding-lz4-buffer-declares-2e40` |
| **An IPC stream's message past arrow-go's limits ended the process.** `ipc.NewReader` builds its message reader without the metadata and body limits `ipc.NewMessageReader` keeps (arrow-go v18.7.0's `reader.go`), so `defaultMaxBodySize` held for no stream: a 2.7 KB stream whose message declared a 2^40-byte body ended the process, out of memory; and an IPC file read as a stream allocated 1.3 GB, the metadata length its magic `ARROW1` spells | a probe, then the seed `ARROW1\0\0` | `openIPCStream` builds the reader from a message reader, with the limits: arrow-go's 64 MiB and 256 MiB, or the stream's own size where it is known (`ScanIPCStream` of a regular file, `ScanIPCStreamBytes`). `finding-message-body-declares-2e40` |
| **`ReadIPCSchema` of a corrupt footer panicked into its caller.** arrow-go's flatbuffers accessors panic on a schema whose offsets run past the footer, and `ReadIPCSchema`, which no query's recovery wraps, has none of its own; `TestEveryEntryPointRecovers` covers `LazyFrame` only | `FuzzScanIPCFile`, 13,639 inputs in | `openIPC` recovers a panic opening the file, closes it, and attributes it as Parquet's `corrupt` does: arrow-go's an `ErrIO` naming the file, ursus's its bug. `TestReadIPCSchemaRecovers`, `finding-footer-field-name-panics` |
| **An NDJSON line nested deep cost the cube of its depth.** `newBuilder` wrote each level's type as text, its refusals' name, and built each level's natural type whole: a 6 KB line of objects 999 deep allocated 4.0 GB and took 1.23 s, a 2 KB line of arrays 1.2 GB and 0.51 s; step 412 had left "the quadratic cost of a deep type" | the 999-deep seed, a runaway | the name is written when a value is refused, and `naturalID` gives the natural type's ID without building it: 4.3 MiB and 13 ms, and 15 MiB and 0.21 s. What remains of the arrays' time is `data.Spans`, walked a level at a time by the memory ledger's `retainColumn`, 66 ms a read; it allocates nothing, and is left. `finding-deep-struct-type` |

## 5. Findings left, for the maintainer

Each is skipped by its target, `t.Skip("finding: …")`, where the input is known to be
one, and keeps its input as a seed.

- **arrow-go sizes slices by IPC metadata's lengths, and recurses on its schema,
  unchecked: five ways an input of 0.5 to 2 KB ends the process.** Go's flatbuffers has no
  verifier, and arrow-go v18.7.0 trusts every length:
  - `schemaFromFB`, `make([]arrow.Field, schema.FieldsLength())`, and `fieldFromFB`,
    `make([]arrow.Field, field.ChildrenLength())` (`arrow/ipc/metadata.go`): 2^31−1
    fields or children, `fatal error: runtime: out of memory`;
  - `metadataFromFB`, two `make([]string, CustomMetadataLength())`, of a schema's, a
    field's or a record batch message's key-values: 2^31−1 grew until the system's
    out-of-memory killer ended the process;
  - `loadCommon`, `make([]*memory.Buffer, 0, 2+variadicCount)` of a view column
    (`file_reader.go`), the count an int64 read for each view column whether or not
    the vector declares it, and a file's dictionary blocks read as dictionary batches
    whatever their header says (`readDictionary`): out of memory;
  - `fieldFromFB` recurses once a level with no limit: a field among its own
    descendants, which an offset past 2^31 makes, since flatbuffers' positions add in
    `uint32`, overflowed Go's 1 GiB stack, `fatal error: stack overflow`, from 552
    bytes (D7's IPC half, which step 423 took arrow-go's 64-level loader to cover; the
    loader runs after the schema is read).

  A fix is a walk of ursus's own over the Message, Footer, Schema, Field, KeyValue,
  RecordBatch and DictionaryBatch tables, every offset and length checked, before
  arrow-go reads a footer or a message, as step 423 measures a Parquet footer and step
  510 reads Parquet's `ARROW:schema`; for a stream, a reader that checks each message's
  metadata as it passes, and for a file each block's before `RecordBatchAt`. That is
  some hundreds of lines; `oversized` (`internal/readfuzz/ipcmeta_test.go`) is the walk
  the targets skip by, and `TestOversizedPassesCleanInputs` holds it to skip no
  well-formed input. An upstream issue on the four sites is the other road. Seeds:
  `finding-schema-fields-2e31`, `finding-field-children-2e31`,
  `finding-schema-metadata-2e31`, `finding-variadic-buffers-2e40`,
  `finding-dictionary-variadic-buffers`, `finding-field-its-own-child`.
- **A record batch of 2^40 rows of no data.** A Null column holds no buffer, nor does
  a record of no columns, so arrow-go's validation passes any length; a 557-byte stream
  declaring 2^40 rows had arrowsrc copy batches of 8,192 nulls, allocating, until the
  harness ended the process at four times its bound. Polars holds a Null column of any
  length in constant space. `oversized` skips a record declaring more rows than eight a
  byte of input. `finding-record-of-2e40-rows-of-no-data`.
- **A Zstandard frame's window is allocated before a block is decoded.** klauspost's
  decoder makes the whole window the frame declares, up to its `MaxWindowSize` of 512
  MiB, at its first block (`(*history).ensureBlockRing`, `zstd/history.go`): 17 bytes
  made `ScanNDJSONBytes` allocate 514 MiB, and `ScanCSV`'s and `ScanJSON`'s
  decompression is the same `internal/source.Decompressed`. The reference decoder, and
  Polars through it, refuse a window past 128 MiB by default;
  `zstd.WithDecoderMaxWindow(128 << 20)` would match them and refuse files ursus reads
  today, so it is the maintainer's. The NDJSON and JSON targets skip a frame declaring
  more than 32 MiB. `finding-zstd-window-512mib`, `finding-zstd-window-64mib`.
- **Inference types an array's mixed elements as the reader will not read them.**
  `coerce` follows Polars' rules for an array's elements, as `ScanNDJSON`'s doc states,
  so `[1, true]` is a `List(Int64)`, `[1, [2]]` a `List(List(Int64))` and
  `[{"b": 1}, {"b": true}]` a `List(Struct{b: Int64})`; the builders then refuse `true`
  as an Int64 and `1` as a `List(Int64)`, at the very line the schema was inferred
  from, with the hint that "a later line's type was not among the first lines'".
  Reading true as 1, as Polars' deserialiser may, is a choice of value for a Polars
  recording to settle. `coerce-bool-int`, `coerce-value-list`, `coerce-struct-field`.
- **An integer past Int128 is inferred an Int128 and refused as one.** `typeOf` types
  any integer past Int64 as Int128, and the builder refuses
  170141183460469231731687303715884105728 as one. `past-int128`.

## 6. Teeth

Each mutation was made in the code a target guards, the seeds run, and the file put
back from a saved copy.

| mutation | test | |
| --- | --- | --- |
| `ReadMem.Allocate` checks nothing | `FuzzScanIPCFile`'s compressed seeds and `finding-lz4-buffer-declares-2e40`: the bounded allocator refuses 2^40 bytes | **bites** |
| the stream reader built by `ipc.NewReader`, dropping the limits | `FuzzScanIPCStream`'s seed `ARROW1\0\0`: a runaway 1.3 GB | **bites** |
| `openIPC`'s recovery taken out | `TestReadIPCSchemaRecovers`: a panic reached the caller of the public API | **bites** |
| the original `ipc.go` (no bound, no limits) | `finding-lz4-buffer-declares-2e40` and `finding-message-body-declares-2e40`: `fatal error: runtime: out of memory` | **bites** (fails first) |
| a half's subnormal decoded at 2^-23 in arrowin | `FuzzScanIPCFile`'s kitchen sink: "ursus reads 1.1920929e-07, and arrow-go holds 5.9604645e-08" | **bites** |
| the vector scan forgets a block began in a string | `FuzzObjectLines/long-strings`: the vector scan differs from the byte scan | **bites** |
| a negative integer under 1,000 read positive | `FuzzScanNDJSON/types`: "-2 read as 2" | **bites** |
| a NUL written unescaped by `json_path_match` | `FuzzJSONDecode/strings`: writeJSON wrote text that is not JSON | **bites** |
| NDJSON's nesting limit lifted | `TestScanJSONDeep`: the 8 MiB stack overflows | **bites** |
| `newBuilder` naming and building each level's type again | `FuzzScanNDJSON`'s 999-deep seed: a runaway 286 MiB | **bites** |
| `oversized` reading sixteen variadic counts past the vector | `TestOversizedPassesCleanInputs`: two Polars files skipped as oversized | **bites** |

## 7. Checks

- The touched packages' tests, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd
  GOAMD64=v3`: `./internal/readfuzz/`, `./internal/kernel/`,
  `./internal/source/ndjson/`, `./internal/arrowin/`: ok.
- `go test ./internal/...`: ok. `go test .` in full: ok.
- `GOEXPERIMENT=simd go vet ./...`: clean. `go run ./internal/gen/levels`: ok.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 0 gone.
- `go run ./internal/gen/testinventory -check`: as the baseline.
- `gofmt -l .`: empty.
