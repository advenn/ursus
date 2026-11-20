# Step 538 — as built

**arrow-go v18.9.0, and what it does not fix.** The maintainer decided (2026-11-20)
to move ursus from arrow-go v18.7.0 to v18.9.0, released 2026-11-16, and to retire
the workarounds for the findings it fixes, where that is safe. This merges after
v0.8.0 is tagged, into 0.8.1.

The upgrade is made in the root module, `qcheck` and `bench/engines/go`. Every
reproducer of the arrow-go audit was run against the release, not the main commit
the audit checked: the release differs from that commit in two files, and both
**revert** main's fix of step 522's Null column. Of the 12 findings the audit found
fixed on main, the release fixes 10; **F18 only in part** (a DELTA_BINARY_PACKED
page counting fewer values than it holds still hangs arrow-go's column reader, found
here with ursus's check switched off), and the Null column not at all. The other 20
reproduce on v18.9.0 as on main.

**No workaround is retired.** Each of the twelve either still guards something
v18.9.0 does not fix, or makes a refusal arrow-go does not make, or gives the error
its finding's test requires, which arrow-go's own refusal does not; each is cheap
where it stays, and none uses an API the release deprecates beyond what step 517
already used (`file.NewColumnReader`, still needed by the 0-bit index and delta
checks). Section 3 has the table.

The upgrade brought one **ursus bug** to light, fixed: the reader's own pages refused
a fixed-width column chunk whose dictionary page holds no value, which v18.9.0's
writer, and so `WriteParquet`, writes for a dictionary-encoded column of only nulls
when its pages are compressed. `TestParquetRoundTrip` failed on the bump alone; and
pyarrow writes one too, so `testdata/oracle/footers/pyarrow-03.parquet` is refused on
master and by every release, whatever arrow-go. And
it brought **two Parquet pages ursus read that arrow-go now refuses**, which ursus
cannot read without a delta decoder of its own (section 5): an INT32
DELTA_BINARY_PACKED page whose deltas span more than 32 bits, as arrow-go v18.7.0's
own writer packed them and pyarrow refuses; and a DELTA_LENGTH_BYTE_ARRAY page of an
OPTIONAL column counting more lengths than its values, which pyarrow reads. Both are
behaviour changes; the CHANGELOG lists them under "Changed".

## 1. The bump

`go get github.com/apache/arrow-go/v18@v18.9.0` and `go mod tidy` in each module.

- **Root `go.mod`**: arrow-go v18.7.0 → v18.9.0; the two other direct dependencies
  move because arrow-go v18.9.0 requires newer ones, klauspost/compress v1.19.0 →
  v1.20.3 and golang.org/x/text v0.37.0 → v0.43.0; the indirect ones likewise
  (Thrift v0.24.0 → v0.25.0, brotli v1.2.2 → v1.2.8, go-json v0.10.6 → v0.12.0,
  cpuid v2.4.0 → v2.4.2, lz4 v4.1.27 → v4.1.33, x/sync v0.22.0 → v0.25.0, x/sys
  v0.47.0 → v0.49.0). No module is added.
- **Root `go.sum`**: those, and the module graph's grpc v1.85.0, protobuf v1.36.12,
  genproto's rpc of 2026-08-17, x/net v0.61.0, testify v1.12.1 with objx v0.5.3 and
  go.yaml.in/yaml/v3 v3.0.5 (testify's, in place of gopkg.in/yaml.v3; go-spew and
  go-difflib drop out). Each version is the one arrow-go v18.9.0's `go.mod` names;
  nothing else changed.
- **`qcheck/go.mod`**: the same moves. **`bench/engines/go/go.mod`**: the same, and
  `go mod tidy` there also listed duckdb-go-bindings' libraries for the other
  platforms and dropped chdb-go's go-prompt, go-tty and their kin, which the module
  no longer reaches.

## 2. The release notes, against what ursus relies on

v18.8.0 and v18.9.0 list some 350 changes. Those that change what ursus reads or
writes, each found by the test suite or the probe of section 6:

| change | where | effect on ursus |
| --- | --- | --- |
| `loadNull` and the C Data import give a Null array no buffer again: main's fix reverted in the release | `arrow/ipc/file_reader.go`, `arrow/cdata/cdata.go` | step 522's Null shaping stays; without it every IPC Null column is refused again (section 7) |
| the writer keeps a dictionary of no value for a dictionary-encoded chunk of only nulls, where its pages are compressed (v18.7.0 wrote PLAIN) | parquet writer | the own pages refused such a chunk of a fixed-width column: fixed (section 4) |
| the writer keeps the dictionary of an Int64 chunk of 50,000 distinct values a row group, where v18.7.0 fell back to PLAIN | parquet writer | two allocation tests' fixture pinned to PLAIN, as it was written (section 4) |
| DELTA_BINARY_PACKED deltas wrap at the physical type's width (#1027); the decoder refuses an INT32 minimum delta past INT32's range and a miniblock wider than 32 bits (#1012) | delta encoding | INT32 delta pages of v18.7.0's writer spanning more than 32 bits refused, as pyarrow refuses them (section 5) |
| DELTA_LENGTH_BYTE_ARRAY decodes every length its count gives at `SetData` and refuses a negative one or lengths past the payload (#1013, #1014) | delta encoding | a count past an OPTIONAL page's values refused where its padding's length is negative (section 5) |
| Thrift v0.25.0 holds a container's count to the bytes left at the least an element of its type takes (#1355) | footer, page headers | F11 and F13 fixed; the footer walk's differential premises updated (section 4) |
| `dictConverter` bounds its dictionary by the page's bytes and checks an index first | dictionary decoding | F4 and F9 fixed: no allocation; ursus's refusals kept (section 3) |
| ALP encoding for FLOAT and DOUBLE (#1289) | decoding | ursus's `allowedEncodings` leaves it out: such a column is refused as unsupported, as before |
| `ipc.NewReader` keeps the message limits | IPC stream | step 511's finding fixed; ursus reads streams through `arrowmeta`'s reader, which keeps tighter ones, as before |

Nothing else in the notes reaches ursus's paths: compute, Flight, Avro, CSV and the
scalar and cast fixes are packages ursus does not import for reading or writing.

## 3. The 32 findings

The audit's reproducers (`arrow-go-report/repro/`, one module, `go.v189.mod` added
for the release) and its 64 decoded seeds and 17 forged files through
`tools/triage`, each under `prlimit --as=4294967296` and a 60 s timeout. The release
answers every seed and forged file exactly as main did (81 runs, 0 differ), and every
reproducer as main did (`results/repro-run-v18.9.0.txt`,
`results/triage-v18.9.0.txt`). F18's count and step 522's Null column were then
checked against ursus's own tests with its guard switched off (section 7).

| finding | v18.9.0 | ursus's workaround | kept or retired, and why |
| --- | --- | --- | --- |
| A1 IPC schema fields count | fatal OOM 188 GB | `arrowmeta` walk | kept: not fixed |
| A2 IPC field children count | fatal OOM 188 GB | `arrowmeta` walk | kept: not fixed |
| A3 IPC custom metadata count | fatal OOM 34 GB | `arrowmeta` walk | kept: not fixed |
| A4 IPC record batch variadic buffers | fatal OOM 8.8 TB | `arrowmeta` walk | kept: not fixed |
| A5 IPC dictionary batch variadic buffers | fatal OOM 137 GB | `arrowmeta` walk | kept: not fixed |
| A6 IPC field its own descendant | stack overflow | `arrowmeta` walk | kept: not fixed |
| A7 IPC schema DAG of shared children | fatal OOM | `arrowmeta` walk | kept: not fixed |
| A8 IPC footer flatbuffer offset | panic escapes `NewFileReader` | recovers at the entry points | kept: not fixed |
| A9 Parquet ARROW:schema children | fatal OOM 283 GB | `arrowmeta.SchemaFields` | kept: not fixed |
| B1 IPC buffer's decompressed length | fatal OOM 1.1 TB | bounded allocator | kept: not fixed |
| B2 IPC footer listing a block many times | as main, quadratic | bounded allocator | kept: not fixed |
| B3 IPC big-endian empty Decimal | as main (code unchanged) | recovers | kept: not fixed |
| C1 Parquet chunk's compressed size | fatal OOM 1.1 TB | `checkChunks` | kept: not fixed |
| C2 Parquet TIME of no unit | panic escapes `NewParquetReader` | recovers | kept: not fixed |
| C3 Parquet schema `num_children` | fatal OOM 32 GiB | the schema-tree walk (533) | kept: not fixed |
| D1 Parquet page bytes missing | silent: 0 rows of 5 | `truncated()` | kept: not fixed |
| D2 Parquet 0-bit dictionary index | reads "01" for "00" | `zeroWidthMisread` | kept: not fixed |
| E1 Parquet Snappy length header | fatal OOM 4 GB | `boundedCodec` | kept: not fixed |
| E2 Parquet zstd frame content size | as main (code unchanged) | `boundedCodec` | kept: not fixed |
| E3 Parquet gzip or Brotli page of 0 bytes | 4 MiB / 32 MiB decoded | `boundedCodec` | kept: not fixed |
| F4 dictionary page count (510) | fixed: no allocation, and the page's values read, 5 rows | `countDictionaries` | **kept**: arrow-go reads a forged page ursus refuses, as its own pages refuse it; retiring it would read files ursus refuses, `TestADictionaryPageClaimingMoreValuesThanItsBytesIsRefused` and `TestADictionaryCountInALongVarintIsCountedAsArrowGoReadsIt` fail without it; one header walk a chunk |
| F9 dictionary index past its dictionary (517) | fixed: "dict eof exception", nothing allocated, over the 14 forged files and 2,000 generated pages | `pastIndex` in `checkedPages` | **kept**: its test wants an error naming the index, which arrow-go's does not; the 14 forged files fail it without the check, refused but as "dict eof exception". The pass cost a dictionary-encoded List arrow-go decodes 5.2% by the median, 3.8% by the least (517) |
| F11 Thrift varint and stop (520) | fixed | the walk reads varints as Thrift does | **kept**: a fix to ursus's own walk, which F4, F14 and C3 need |
| F13 footer list counts (521) | fixed by Thrift v0.25.0 | the footer walk's counts | **kept**: the walk stays for C3, and the count is one comparison a list |
| F14 UNCOMPRESSED page sizes differ (521) | fixed: "said 3072, got 24" | `countDictionaries` | **kept**: the same walk as F4, one comparison a page |
| F18 DELTA_BINARY_PACKED (531) | **partly**: the block of 31 refused; a REQUIRED page counting fewer values than it holds hangs `ReadBatch`, an OPTIONAL one reads 0 values and no error | `checkDelta` | **kept**: not fixed; `TestAMalformedDeltaPageIsRefusedNotHungOn` hangs without it |
| F19 row group of fewer chunks (521) | fixed: an index error | the chunk count check | kept: one comparison, and its error names the row group |
| step 511: IPC stream limits | fixed | `arrowmeta.NewMessageReader` | kept: the walk is A1–A7's, and its limits are the stream's size |
| step 511: message body of 2^40 | fixed | the same reader | kept: as above |
| schema root children mismatch | fixed: "malformed schema" | the schema-tree walk (533) | kept: C3's |
| step 522: IPC Null column of no buffer | **not fixed: reverted in the release** | `nullShaped`, `shapeNulls` | **kept**: every IPC Null column is refused without it |
| F15: a Null column with no reader (521) | as main | its refusal | kept: one check |

## 4. What changed in ursus

**The own pages read a dictionary of no value.** `pageChunk.readDictionary` copied a
fixed-width dictionary into `dictFixed` with `append([]byte(nil), ...)`, nil for no
value, and `nextPage` took a nil `dictFixed` and nil `dictOffs` for a chunk with no
dictionary page, refusing its data page as "a page refers to a dictionary the chunk
does not have". arrow-go v18.9.0's writer, with a codec, writes a dictionary page of
no value and an RLE_DICTIONARY page of no index for a chunk of only nulls; v18.7.0
fell back to PLAIN there. `pageChunk.hasDict` now records the dictionary page; a
page of nulls holds no index, and an index into a dictionary of no value is refused
as corrupt, as any past a dictionary. `TestParquetRoundTrip` and
`TestParquetPruningAgreesWithoutIt` failed on the bump alone, ursus's own writer's
files; a file from any writer doing the same was refused on master too.

**Tests whose premise was v18.7.0's bug or its writer.** Each keeps its oracle and
says what v18.9.0 does now:

- `TestCheckedPagesRefuseWhereArrowGoGrowsItsDictionary`: arrow-go's own read grew
  its dictionary for an index of 2^20 and more (F9). Now it must never grow it, 0 of
  the 2,000 pages did, and must fail each well-formed page where an index it reads is
  past the dictionary; the generator's reach is held by the 203 pages of an index of
  2^20 and more arrow-go refuses, where it was held by the 236 it grew.
- `TestFooterWalkReadsAListAsArrowGoDoes`: arrow-go read a hidden list of 10^5,
  allocating 4 bytes an element (F13). Now it must refuse it, allocating less.
- `TestFooterWalkDifferential`: a list whose header gives another element type, the
  walk passes and arrow-go read. The walk still passes each; arrow-go reads it or
  refuses it by Thrift v0.25.0's container check ("needs at least … bytes, only …").
- `TestArrowGoDeltaPagesReadAsTheirPlainTwin`: arrow-go's writer packed `u32`'s
  deltas 33 bits wide; v18.9.0's wraps them at 32, so no page may be wider.
- `TestCheckedPagesRefuseMalformedDeltaRuns`: its generator packs an INT32's deltas
  wrapped at 32 bits, as the format and the writer do, and a count past the values of
  a run arrow-go decodes whole is held to arrow-go's own read only, not to the
  generator's values (section 5).
- `TestPagesAreDecompressedIntoPooledBuffers` and
  `TestColumnsBesideATextConjunctAreReadAtItsKeptRows`: their file's Int64 columns
  are written PLAIN, `WithDictionaryFor(..., false)`, as v18.7.0's writer wrote them
  and DuckDB writes lineitem's; v18.9.0's kept their dictionaries, and copying them
  read 1.99 to the byte produced where the bound is 1.6. With the pin: 1.23 and
  0.91, master's 1.23 and 0.84.

**Comments** on each kept guard say what v18.9.0 does (`dictindex.go`,
`deltapage.go`, `dictpages.go`, `arrowin/record.go`, `colreader.go`, `ipc.go`).

## 5. The two pages arrow-go now refuses

Both are refused as an `ErrIO` naming the read, with arrow-go's reason.

- **An INT32 DELTA_BINARY_PACKED page whose deltas span more than 32 bits.** arrow-go
  v18.7.0's writer took an INT32's deltas as an int64's and packed them 33 or 34
  bits wide where consecutive values differ by more than 2^31 (a UInt32 column of
  large values); it read them back, and ursus with it (531 tested it). pyarrow
  25.0.1 fails such a page ("Unexpected end of stream"), and v18.9.0 refuses it
  ("minimum delta … exceeds INT32 range", "delta bit width 33 exceeds decoder width
  32"). The pyarrow differential's `an-int32-spanning-its-range--honest.parquet`
  goes from (ursus S, pyarrow E) to (E, E). Files v18.7.0 and older wrote so no
  longer read.
- **A DELTA_LENGTH_BYTE_ARRAY page of an OPTIONAL column whose count is past its
  values**, within its page's values and its last miniblock's padding, which the
  format allows and no writer is known to write. v18.9.0 decodes every length the
  count gives and refuses a negative one, here the length in the padding. pyarrow
  reads it; the differential's
  `an-optional-byte-array-with-nulls-delta-length-byte-array--a-count-one-past-the-values.parquet`
  goes from (S, S) to (E, S), a known disagreement now (`arrowGoRefuses`).

The pyarrow tally of the 208 forged files: master ED 13, EE 140, ES 16, SE 1, SS 38;
here ED 13, EE 141, ES 17, SS 37.

## 6. Everything else reads the same

The whole suite passes (section 10), and a probe reads every Parquet and IPC file
the same before and after, but where the two sections above say otherwise. The probe
(`~/ursus-scratch-archive/s538/probe`), built once against `origin/master` and once
against this branch, reads each file through the public API, `ScanParquet`,
`ScanIPC` or `ScanIPCStream`: `Count`, and `Collect` on one thread and on four, each
row's values hashed with the schema, or the error. Its 795 files: the 75 Parquet and
IPC files in `testdata`, and 720 generated, 360 written by arrow-go v18.7.0 and the
same 360 by v18.9.0 from one seed (`s538/gen`): 240 Parquet files of random schemas,
1 to 5 columns of 20 types, Lists and Structs, 0 to 3,000 rows, nulls none, some or
all, every codec, dictionary on and off, PLAIN, DELTA_BINARY_PACKED,
DELTA_LENGTH_BYTE_ARRAY, DELTA_BYTE_ARRAY and BYTE_STREAM_SPLIT a column, V1 and V2
pages, pages of 64 B to 1 MiB, with and without the Arrow schema; and 120 IPC files
and streams, Null columns among them, LZ4, Zstandard or none, 1 to 3 batches.

785 read identically, rows and errors alike. The 10 that differ:

| files | master (v18.7.0) | this branch (v18.9.0) |
| --- | --- | --- |
| `testdata/oracle/footers/pyarrow-03.parquet`, pyarrow's | refused: "a page refers to a dictionary the chunk does not have" | 2 rows, as Polars 2.1 reads them |
| 8 of the 240 Parquet files v18.9.0 wrote | refused, the same | read |
| `g036.parquet` of v18.7.0's, an INT32 under DELTA_BINARY_PACKED | read | refused: "minimum delta … exceeds INT32 range" |

The first row is the bug of section 4 on master, in a file pyarrow wrote: pyarrow
writes a dictionary page of no value for a column of nulls, so master and every
release refuse such files, whatever arrow-go. 147 of the generated Parquet files are
refused alike before and after, at their schema: nested columns ursus does not read
yet.

## 7. The teeth

| tooth | what fails |
| --- | --- |
| the own pages' `!c.hasDict` put back as `dictOffs == nil && dictFixed == nil` | `TestOwnPagesReadADictionaryOfNoValue`, `TestOwnPagesReadAsArrowGoDoes` ("a page refers to a dictionary the chunk does not have"); on the bump alone, `TestParquetRoundTrip` and `TestParquetPruningAgreesWithoutIt` |
| the pool test's fixture unpinned | `TestPagesAreDecompressedIntoPooledBuffers`, 1.99 to the byte; `TestColumnsBesideATextConjunctAreReadAtItsKeptRows`, 12.78 MB where 4.58 |
| `shapeNulls` answering its input (the Null shaping off) | `TestShapeNullsCopiesOnlyThePathToANullOfNoBuffer`, `TestANullArrayOfNoBuffersReads`, `TestOtherwiseMalformedNullsAreRefused`, and at the root `TestIPCNullColumnsRoundTrip`: "expected at least 1 buffers for null, got 0": v18.9.0 does not fix 522 |
| `countDictionaries`' count rule off (F4) | `TestADictionaryPageClaimingMoreValuesThanItsBytesIsRefused`, `TestADictionaryCountInALongVarintIsCountedAsArrowGoReadsIt`: "read as 5 rows, and no error" |
| `pastIndex` off (F9) | `TestADictionaryIndexPastItsDictionaryAllocatesNothingOfIt`, 14 of 14: refused, nothing allocated, but "dict eof exception", not naming the index |
| `checkDelta`'s refusal off (F18) | `TestAMalformedDeltaPageIsRefusedNotHungOn`: "a block of 31 values" and the other header forgeries refused by arrow-go without "corrupt: column"; "a count of 0" of the REQUIRED INT64 hangs, fuzzcheck's 10 s; arrow-go's `RowGroupReader.Column` and `ReadBatch` alone hang 60 s on it (`arrow-go-report/inputs/step538`) |

Each was put back from a copy (`~/ursus-scratch-archive/s538/teeth/`) and the tests
pass again.

## 8. Fuzz runs

One at a time, under the shared lock and `prlimit --as=4294967296`, 20 s each,
`-fuzzminimizetime=3s`, four workers, from coverage-instrumented binaries
(`go test -c -fuzz=…`), `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`
(`s538/fuzz.sh`):

| target | seeds | execs | new interesting | outcome |
| --- | --- | --- | --- | --- |
| `FuzzScanParquet` | 115 | 38,569 | 29 | PASS |
| `FuzzOwnPagesMatchArrow` | 52 | 33,667 | 29 | PASS |
| `FuzzScanIPCFile` | 47 | 9,266 | 27 | PASS |
| `FuzzScanIPCStream` | 41 | 8,263 | 24 | PASS |

A first round from binaries built without the instrumentation (plain `go test -c`)
mutated blind; in it `FuzzScanIPCFile` lost a worker, "exit status 2", at 4 s, on a
57,983-byte input. Replayed alone under the same cap it passes, on this branch and on
`origin/master` alike: its read holds 705 MB at its peak, two blocks of 356 MB that
the bounded allocator allows a file of its size (32,768 times, step 511), and fails
"out of memory" under a cap of 2 GiB on both. A worker's heap beside it under the 4
GiB cap is the fuzzing harness's limit, not the upgrade's; the input is kept in
`s538/` and not added as a seed.

## 9. Measurements

One Parquet file and one IPC file, written once by arrow-go v18.7.0
(`s538/bench/mk`), each 1,000,000 rows of an Int64, a Float64 with nulls, a String
of 500 values and a List(Int64): Parquet with Snappy in 4 row groups (15.1 MB),
the IPC file with LZ4 in 4 batches (30.1 MB). Each read whole by
`Collect(WithThreads(1))` (`s538/bench/rb`), 2 reads to warm and 21 timed, before
and after interleaved, 4 rounds, under the lock:

| read | before, medians, ms | after, medians, ms | before, least, mean | after, least, mean |
| --- | --- | --- | --- | --- |
| Parquet | 102.67, 100.69, 98.86, 103.33 | 97.96, 99.59, 100.29, 98.85 | 93.07 | 91.24 |
| IPC | 82.65, 80.39, 81.06, 79.57 | 79.98, 78.88, 81.66, 82.22 | 73.60 | 74.17 |

Parquet 2% faster by both, within a round's spread of 4%; IPC the same. No change
past the noise, so no "Faster" line.

## 10. Checks

After the last code edit, `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`
(`~/ursus-scratch-archive/s538/gate.sh` and its logs):

| check | outcome |
| --- | --- |
| `go test ./internal/...` | ok, 30 packages |
| `go test -timeout 40m .`, under the lock | ok, 245.2 s |
| `cd qcheck && go vet ./... && go test ./...`, under the lock | ok, 7 packages |
| `GOEXPERIMENT=simd go vet ./...` | clean |
| `go run ./internal/gen/levels` | ok, no new package |
| `GOEXPERIMENT=simd go run ./internal/gen/api -check` | 2,481 features, 92 added and 0 gone since v0.7.2 |
| `go run ./internal/gen/testinventory -check` | ok, 1,223 hand-typed lines, the baseline's 1,223 |
| `$(go env GOROOT)/bin/gofmt -l .` | nothing |

On the bump alone, before any change here, `./internal/...` failed in `readfuzz`
(the delta tests, section 5) and `internal/source/parquet` (eight tests, section 4),
and the root package in `TestParquetRoundTrip` and
`TestParquetPruningAgreesWithoutIt` (the dictionary of no value).
`bench/engines/go` was not built here: it links DuckDB, which is CI's.
