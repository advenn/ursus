# Step 538 — as built

**arrow-go v18.9.0.** The maintainer decided (2026-11-20) to move ursus from arrow-go
v18.7.0 to v18.9.0, released 2026-11-16, and to retire any guard the upgrade made
unnecessary, where that is safe. This merges after v0.8.0 is tagged, into 0.8.1.

The upgrade is made in the root module, `qcheck` and `bench/engines/go`, and after the
tag in `bench/micro` (section 11).

**No guard is retired.** Each is ursus's own check of input it did not write, made
before the bytes are decoded, whatever the decoder behind it (section 3). Each is
cheap where it stays, and none uses an API the release deprecates beyond
`file.NewColumnReader`, which step 517 already used and the 0-bit index and delta
checks still need.

The upgrade brought one **ursus bug** to light, fixed: the reader's own pages refused
a fixed-width column chunk whose dictionary page holds no value, which v18.9.0's
writer, and so `WriteParquet`, writes for a dictionary-encoded column of only nulls
when its pages are compressed. `TestParquetRoundTrip` failed on the bump alone; and
pyarrow writes one too, so `testdata/oracle/footers/pyarrow-03.parquet` is refused on
master and by every release, whatever arrow-go. And **two Parquet pages ursus read are
now refused**, which ursus cannot read without a delta decoder of its own (section
5): an INT32 DELTA_BINARY_PACKED page whose deltas span more than 32 bits, which
pyarrow refuses too; and a DELTA_LENGTH_BYTE_ARRAY page of an OPTIONAL column counting
more lengths than its values, which pyarrow reads. Both are behaviour changes; the
CHANGELOG lists them under "Changed".

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
| the writer keeps a dictionary of no value for a dictionary-encoded chunk of only nulls, where its pages are compressed (v18.7.0 wrote PLAIN) | parquet writer | the own pages refused such a chunk of a fixed-width column: fixed (section 4) |
| the writer keeps the dictionary of an Int64 chunk of 50,000 distinct values a row group, where v18.7.0 fell back to PLAIN | parquet writer | two allocation tests' fixture pinned to PLAIN, as it was written (section 4) |
| an INT32's deltas are packed within 32 bits, and a DELTA_BINARY_PACKED page of INT32 deltas wider than 32 bits is refused | delta encoding | such a page refused, as pyarrow refuses it (section 5) |
| DELTA_LENGTH_BYTE_ARRAY decodes every length its count gives at `SetData` and refuses a negative one | delta encoding | a count past an OPTIONAL page's values refused where its padding's length is negative (section 5) |
| Thrift v0.25.0 holds a container's count to the bytes left at the least an element of its type takes | footer, page headers | the footer walk's differentials updated (section 4) |
| ALP encoding for FLOAT and DOUBLE | decoding | ursus's `allowedEncodings` leaves it out: such a column is refused as unsupported, as before |

Nothing else in the notes reaches ursus's paths: compute, Flight, Avro, CSV and the
scalar and cast fixes are packages ursus does not import for reading or writing.

## 3. The guards

Every guard ursus built against crafted input stays, with its tests and inputs: the
walks of a Parquet footer, schema, page and dictionary and of an Arrow IPC footer,
message and buffer (steps 510, 511, 517, 519–521, 531 and 533), the bounded allocator
and codecs (511, 520), the Zstandard window's cap (528), the Null shaping (522), and
the recovery at every entry point. The walks and bounds refuse input whose counts and
lengths its bytes cannot hold, before anything is sized by them; the Null shaping
reads a Null column in either shape a writer gives it; the recovery makes a panic an
error. None depends on what the decoder behind it does with such input.

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

**The differentials against arrow-go** take its answers as v18.9.0 gives them, each
keeping its oracle: `TestCheckedPagesRefuseAnIndexPastTheDictionary` (step 517), and
`TestFooterWalkReadsAListAsArrowGoDoes` and `TestFooterWalkDifferential` (step 521),
where a list whose header gives another element type the walk still passes, and
arrow-go reads it or refuses it by Thrift v0.25.0's container check ("needs at least …
bytes, only …").

**Tests whose premises followed v18.7.0's writer** take v18.9.0's:

- `TestArrowGoDeltaPagesReadAsTheirPlainTwin`: v18.9.0's writer packs `u32`'s deltas
  within 32 bits, so no page may be wider.
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

## 5. The two pages now refused

Both are refused as an `ErrIO` naming the read, with the decoder's reason.

- **An INT32 DELTA_BINARY_PACKED page whose deltas span more than 32 bits**, its
  deltas taken as an int64's and packed 33 or 34 bits wide where consecutive values
  differ by more than 2^31 (a UInt32 column of large values). Step 531 read it, and
  tested it. pyarrow 25.0.1 fails such a page ("Unexpected end of stream"), and now
  ursus refuses it ("minimum delta … exceeds INT32 range", "delta bit width 33
  exceeds decoder width 32"). The pyarrow differential's
  `an-int32-spanning-its-range--honest.parquet` goes from (ursus S, pyarrow E) to
  (E, E). Files written so no longer read.
- **A DELTA_LENGTH_BYTE_ARRAY page of an OPTIONAL column whose count is past its
  values**, within its page's values and its last miniblock's padding, which the
  format allows and no writer is known to write. Every length the count gives is
  decoded, and a negative one, here the length in the padding, refused. pyarrow
  reads it; the differential's
  `an-optional-byte-array-with-nulls-delta-length-byte-array--a-count-one-past-the-values.parquet`
  goes from (S, S) to (E, S), a known disagreement now (`decoderRefuses`).

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
| `shapeNulls` answering its input (the Null shaping off) | `TestShapeNullsCopiesOnlyThePathToANullOfNoBuffer`, `TestANullArrayOfNoBuffersReads`, `TestOtherwiseMalformedNullsAreRefused`, and at the root `TestIPCNullColumnsRoundTrip`: "expected at least 1 buffers for null, got 0" |
| `countDictionaries`' count rule off | `TestADictionaryPageClaimingMoreValuesThanItsBytesIsRefused`, `TestADictionaryCountInALongVarintIsCountedAsThriftReadsIt`: the forged pages not refused |
| `pastIndex` off | `TestADictionaryIndexPastItsDictionaryAllocatesNothingOfIt`, 14 of 14: the refusal does not name the index |
| `checkDelta`'s refusal off | `TestAMalformedDeltaPageIsRefused`: the header forgeries refused without "corrupt: column", and one forgery not refused |

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
the bounded allocator allows a file of its size (32,768 times, step 511). A worker's
heap beside it under the 4 GiB cap is the fuzzing harness's limit, not the upgrade's;
the input is kept in `s538/` and not added as a seed.

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

## 11. After the v0.8.0 tag (2026-11-23)

The pull request was held as a draft until v0.8.0 was tagged, and then master was
merged in:
- **The CHANGELOG.** Its two entries, Changed (arrow-go v18.9.0) and Fixed (a
  dictionary of no value), moved out of v0.8.0's section, which is released, into
  a new "Unreleased — toward v0.8.1".
- **`bench/micro/go.mod`.** It still required arrow-go v18.7.0, which this step had
  missed. Its `replace` of ursus by the working tree selects the root's v18.9.0, so
  `go build` there failed with "updates to go.mod needed": `bench.yml`'s probe
  job would have failed to build svcprobe, and no CI job builds that module
  otherwise. `go mod tidy` there moved its indirect requirements to the root's
  versions.
- **Checks after the merge:**
  - build and vet in the root, `bench/micro`, `bench/engines/go` and `qcheck`;
  - levels;
  - `api -check`: 2,481 features, 0 added and 0 gone since v0.8.0;
  - testinventory and gofmt;
  - `go test ./internal/...`: ok, 30 packages; the root package: ok, 206 s.
