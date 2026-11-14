# Step 510 — as built

**Fuzz targets for the Parquet reader, and the byte-flip sweep.** Steps 448, 449 and
444 of [`v0.7.2-plan.md`](./v0.7.2-plan.md) (M3; H4.5 parts 1 and 2, H2.9b), run on
master's code before v0.8.0, as step 506 §3 says, on step 508's harness. D14 is
decided (§3). Four allocations a forged file could make, three of them a fatal "out
of memory" no recover catches, are fixed, and a TIME read past its day, which the
first CI run found (§4).

## 1. What

- **`internal/readfuzz`**, tests only:
  - `FuzzScanParquet` (448), 32 seeds;
  - `TestParquetByteFlipsAgainstDuckDBAndPyarrow` (444), over
    `testdata/oracle/flips/`;
  - the findings' tests: `TestAPageClaimingMoreThanItsChunkIsRefusedBeforeItsBuffer`,
    `TestAChunkClaimingMoreThanItsFileIsRefused`,
    `TestADictionaryPageClaimingMoreValuesThanItsBytesIsRefused`,
    `TestATimePastItsDayIsRefused`;
  - `forge_test.go`: a writer of Thrift's compact protocol and Parquet files of one
    column built whole, whose page headers or footer claim what their bytes do not
    hold. A larger claim takes more varint bytes, so a written file cannot be patched
    in place.
- **`internal/source/parquet`**, tests:
  - `FuzzOwnPagesMatchArrow` (449), 26 seeds;
  - `TestTheFootersArrowSchemaIsReadAsArrowGoReadsIt`,
    `TestAFieldClaimingBillionsOfChildrenAllocatesNoneOfThem`;
  - `writePagesFixture` and its helpers take a `testing.TB`, so a fuzz target's seeds
    can use them.
- **`internal/source/parquet`**, code, internal only:
  - `ReadPagesWith`, the hook through which readfuzz swaps the page pool for a
    bounded, poisoned allocator. `pages` is a `memory.Allocator` now.
  - `boundPages` (D14, §3), `checkChunks`, `countDictionaries` with
    `measured.watchChunks`, `checkTimes`;
  - `schemaFields`, a bounded walk of the footer's Arrow schema. arraysOf used arrow-go's
    IPC reader (§4).
- **`bench/scripts/oracle/record_flips.py`**: records DuckDB's and pyarrow's outcome
  for each flipped byte into `testdata/oracle/flips/flips.json`. It runs standalone,
  beside the package's Polars groups, which neither import it nor are imported by
  it.
- `FuzzHybrid` is kept as it was.

## 2. The targets

| target | package | oracle | seeds | seed time |
| --- | --- | --- | --- | --- |
| `FuzzScanParquet` | `internal/readfuzz` | Run's outcomes. Every page buffer comes from a `fuzzcheck.Allocator` with `Poison` that refuses one request over 16 MiB + 16 bytes per input byte. If the whole frame reads on one thread, then: four threads give the same frame; a re-read into zeroed buffers gives the same frame; `Head(1)` is its first row; a `Select` of its last column is that column; a `Filter` with pruning off equals the same `Filter` of the frame in memory; with pruning on, the Filter's rows are a subsequence of those; and `Count` equals the height. `CollectSchema` equals the frame's schema, nullability included. Everything runs with `WithVerify`. | 32: the 10 `testdata/parquet` fixtures; a frame of seven types under each of the six codecs ursus writes, in row groups of 8; `tinyParquet`, `inflatedParquet` and half of it; 8 forged files (§4); 4 non-files; the fuzzer's crasher (§4, F2) | 0.09 s |
| `FuzzOwnPagesMatchArrow` | `internal/source/parquet` | Twin readers: ursus's own page decoding (`ownPages`) against arrow-go's column reader. Each batch is rendered value by value through `ValueStr`, so a Categorical is compared by its values, not its codes. The fuzzer picks the batch size, and the same bounded, poisoned allocator is used. An input only one of the two reads is not judged: either may refuse a corruption the other does not check for. `API` is off. | 27: `writePagesFixture` at 120 rows in the nine layouts of `TestOwnPagesReadAsArrowGoDoes` (17 of the 19 seeds read both ways); the 10 fixtures; 7 forged files as corpus entries; CI's input (F8) | 0.06 s |
| `TestParquetByteFlipsAgainstDuckDBAndPyarrow` | `internal/readfuzz` | Each byte of two files ursus wrote is flipped (XOR 0x5A): H2's 300 rows of an Int64, a nullable Float64 and a String, one file uncompressed (5,484 bytes) and one with Snappy (3,180). Each flip is read inside Run. Where pyarrow 25.0.1 reads the original, ursus must not answer another frame. DuckDB 1.5.5 is recorded too (F5). ursus must read the original as both engines read it. | 8,664 flips | 1.0 s; every eighth offset under `-race` |

Where the forged seeds come from: the plan's seeds, "a dictionary page claiming
2^31−1 values" and "a page claiming 2^28 bytes", each read by both decoders, plus
the D14 twin "a page claiming 2^26 compressed" and the 10^8 Thrift list the plan
lists under its unverified suspicions. The 10^8 list is a footer whose list of row
groups claims 10^8 entries; step 423's footer walk refuses it before arrow-go reads
it, allocating nothing. A chunk claiming 2^40 bytes is the eighth.

The poison check found nothing. Over the seeds, and every one of the ~185,000
inputs fuzzed that read whole, a read into poisoned buffers and one into zeroed
buffers agreed, so `pool.go`'s assumption that arrow-go writes a page's bytes before
it reads them held.

## 3. D14, decided

The maintainer's coordinator decided D14 on the critic's first option: lower
`ReaderProperties.MaxUncompressedPageSize`, bounded by the column chunk's declared
size, rather than record the request or give 448 a ratchet line for it. The fix is
`boundPages`, called in `openFile` once the footer is read and checked:

- `MaxUncompressedPageSize` becomes the largest uncompressed size the footer declares
  for a column chunk. For an uncompressed chunk, the chunk's compressed size bounds it
  too, since its pages are its bytes as they lie.
- `MaxCompressedPageSize` becomes the largest compressed size it declares.
- Each is at least 1 MiB (`minPageBound`), a margin for a writer that counts a chunk
  short, as parquet-mr ≤ 1.2.8 did (PARQUET-816). Each is at most arrow-go's default:
  256 MiB uncompressed, 64 MiB compressed.

The bound is the file's, not each chunk's. arrow-go copies the limits from the file's
properties as each page reader is made, and the parallel reader's workers make the
page readers of a file's row groups at the same time, so a limit set per chunk would
race. A page lies inside its chunk, so no page of a file is larger than its largest
chunk.

What it fixes: a page claiming 2^28 bytes in a file of 161 asked the pool for 256
MiB, and the pool keeps buffers that large (class 28). A page claiming 2^26 bytes
compressed asked for 64 MiB. Each is refused now, as KindIO. arrow-go's own message,
"uncompressed page size 268435456 exceeds configured limit 1048576", is chained
under it.

What it leaves:

- A footer that lies as well as the header still sets the bound, up to 256 MiB. A
  compressed chunk can declare any uncompressed size, and a codec's true ratio
  (Zstandard's, Brotli's) leaves no tighter bound that a real file could not exceed.
- The bounded allocator would refuse that request, and the fuzzer would report it as
  a finding. None has been found.

The plan's D21 rule (16 MiB + 16× per request) is the allocator's, as step 508 set
it; nothing is excluded from it.

## 4. Findings

| # | what | input | outcome |
| --- | --- | --- | --- |
| F1 (D14) | arrow-go sizes a page's buffer from its header before reading the page: `file.(*serializedPageReader).Next` and `GetDictionaryPage`, `ResizeNoShrink(lenUncompressed)`; `decompress`, `ResizeNoShrink(lenCompressed)` | seeds: a page claiming 2^28 bytes, one claiming 2^26 compressed | **fixed** (§3) |
| F2 | The footer's `ARROW:schema` was decoded by arrow-go's `ipc.NewReader`. In `arrow/ipc.fieldFromFB` (metadata.go), `make([]arrow.Field, n)` takes n from the flatbuffer, unverified: a field claiming 3,221,225,472 children asked for 283 GB, and the process died with "fatal error: runtime: out of memory" | found by the fuzzer after 591 inputs: a 906-byte mutation of a fixture, kept as `testdata/fuzz/FuzzScanParquet/arrow-schema-billions-of-children` | **fixed**: `arraysOf` reads the schema with `schemaFields`, which checks every offset and length against the bytes and reads only what it needs of each top-level field (a FixedSizeList's size; a dictionary of text). Checked against arrow-go's reader on 52 schemas arrow-go wrote, with and without the continuation marker, each also cut at every length. Upstream: arrow-go v18.7.0 `arrow/ipc/metadata.go` `fieldFromFB` |
| F3 | arrow-go reads a column chunk into a slice of its declared size before the read comes up short: `parquet.(*ReaderProperties).GetStream`, `make([]byte, nbytes)` with nbytes the footer's `total_compressed_size` | seed: a chunk claiming 2^40 bytes, a terabyte; fatal "out of memory" | **fixed**: `checkChunks` refuses, as corrupt, a footer that places a chunk outside the file. Upstream: arrow-go v18.7.0 `parquet/reader_properties.go` `GetStream` |
| F4 | arrow-go decodes a dictionary page into a slice of the count its header claims: `parquet/internal/encoding.NewDictConverter`, `make([]T, 0, dict.ValuesLeft())`. 2^31−1 values in 12 bytes is 48 GB; fatal "out of memory". This reaches every chunk arrow-go decodes: Boolean, INT96 and FIXED_LEN_BYTE_ARRAY columns, nested columns, and any column whose footer lists an encoding the own pages do not read. The own pages check the count (step 417) | found by the twin: the forged dictionary seed, read with `ownPages` off | **fixed**: `measured`, the file's ReaderAt since step 423, walks each chunk's page headers as arrow-go reads the chunk. A dictionary page whose uncompressed bytes cannot hold its count, at the fewest bits a PLAIN value of the column takes, fails the read. This needs no extra read and no deprecated `file.NewColumnReader`. Upstream: arrow-go v18.7.0 `encoding.NewDictConverter` |
| F5 | **The plan's 444 claim, and DuckDB.** DuckDB 1.5.5 reads a row group whose num_rows says 257 of its 300 rows as 257 rows, with no error, as ursus did before step 416; pyarrow 25.0.1 reads 300. H2's `flipdiff.py` had DuckDB read all 300 because it rewrote one path for every flip: DuckDB's external file cache, keyed by path and modification time, served the unflipped bytes. A first recording here showed the same thing: 293 offsets of the Snappy file changed between two runs | the num_rows byte, Snappy 3123, uncompressed 5427 | **recorded**: `record_flips.py` turns the cache off and writes a fresh path per flip, and two runs give identical output. 444's rule requires pyarrow's reading, not both engines'. Required, DuckDB would let step 416's bug back (§5, tooth G) |
| F6 | ursus refuses five offsets per file that both engines read: step 416's checks of the file's num_rows and each chunk's num_values against the row group's | `ESS` in the tally | by design (416); not a failure |
| F7 | arrow-go panics on corrupt footers and page headers: `schema.(*Schema).NumColumns`, `schema.getLogicalType`, a nil `DataPageHeader`'s `GetNumValues` | seen in the sweep and fuzzing | ursus refuses each cleanly, as KindIO "corrupt" (`corrupt`); Run logs them as tolerated |
| F8 | A Parquet TIME holding a tick outside [0, 24h) was read as a Time as it lay. A Time is a time of day (dtype), and the writer refuses any other. Read in, 26:00 renders as 02:00:00, sorts after 23:00:00 and equals no 02:00:00 (data.CheckTimeRange's doc). data's boundary check, on in the targets, called the batch ursus's own bug (ErrInternal); it checks top-level columns only, so a Time inside a List read silently even there | found by **CI's first fuzz run** of `FuzzOwnPagesMatchArrow` (run 43634666391): a `writePagesFixture` seed's `tus` column, mutated, tick 116,548,294,544,194 µs at row 62. Kept unminimized, since CI's minimizing ran out of time, as `testdata/fuzz/FuzzOwnPagesMatchArrow/time-past-its-day` (47,669 bytes encoded) | **fixed**, a bug by ursus's own rule: `readBatch` refuses a Time outside its day as a corrupt value (KindValue), at the top level and inside a List, an Array or a Struct, through both decoders. `TestATimePastItsDayIsRefused` covers a forged TIME(MICROS) read both ways, and a List(Time) ursus wrote with one tick patched in place. The comparison was not too strict: the twins agreed, and the invariant fired on what both built. Arrow IPC's time64 import is step 511's, and is not checked here |

Every fix is internal, adds nothing to the public API (`api -check`: 0 gone), and
refuses only files that would otherwise end the process or allocate past their size.
`TestPanicsAreErrors`, the fixtures and the root's Parquet tests read as before.

**For the maintainer, not changed here:** fuzz.yml runs each target for 30 s at Go's
default `-fuzzminimizetime` of 60 s. These two targets spend a millisecond or more per
input, so each worker spends most of a run minimizing new interesting inputs.
Locally, the first 20-second run of `FuzzScanParquet` executed 40 inputs. With
`-fuzzminimizetime 1s` the same run executed 11,366 and found 66 new interesting
inputs. A short minimize time in fuzz.yml would leave the slower targets time to
fuzz. fuzz.yml is shared with steps 509 and 511, so it is left as it is.

## 5. Teeth

Each mutation was made to a copy of the code, the target run, and the code restored
from the copy, checked by md5. Never by git.

| # | mutation | caught by | |
| --- | --- | --- | --- |
| A | `boundPages` not called | `FuzzScanParquet` seeds 2^28 and 2^26: "the bounded allocator refused a request for 268435456 bytes", and 67108864; `TestAPageClaiming…`, all 4 cases | **bites** |
| B | `checkChunks` not called | `FuzzScanParquet`'s 2^40 seed: fatal "out of memory" in `GetStream`; `TestAChunkClaiming…` | **bites** |
| C | arraysOf decodes with `ipc.NewReader` again | the fuzzer's crasher: fatal "out of memory" in `fieldFromFB` | **bites** |
| D | a PLAIN copy reads one byte into the page buffer's slack (`r.plain[1:bytes+1]`) | `FuzzScanParquet`, 8 seeds: 7 at the poison differential (a re-read into zeroed buffers differs at row 7), 1 at `Head(1)`; `FuzzOwnPagesMatchArrow`, 9 seeds | **bites** |
| E | a fixed-width dictionary gather reads odd indices as even (`dict[id&^1]`) | `FuzzOwnPagesMatchArrow`, 6 seeds, "batch 0 differs"; `FuzzScanParquet` none, since it has no twin of arrow-go | **bites** (449) |
| F | `watchChunks` not called | `TestADictionaryPageClaiming…`, `FuzzScanParquet`'s seed and the twin's corpus entry: fatal "out of memory" in `NewDictConverter` | **bites** |
| G | step 416's row-count check not called | the flip sweep: "ursus_snappy.parquet, byte 3123 flipped: ursus answers another frame, where pyarrow reads the original (DuckDB: D)", and the uncompressed file's byte 5427. These are the plan's offsets | **bites** (444) |
| H | `checkTimes` not called | CI's input: ErrInternal, "column "tus" (Time(us)) holds tick 116548294544194 at row 62"; `TestATimePast…`: ErrInternal for the flat TIME (Run fails it), and the List(Time) "read as 1 rows, and no error" | **bites** |

## 6. Local fuzzing

One run at a time, under the shared lock, each at most 20 s, with `GOFLAGS=-p=1
GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:

| run | target | inputs | outcome |
| --- | --- | --- | --- |
| 1 | `FuzzScanParquet` | 40 | stalled minimizing (§4) |
| 2 | `FuzzScanParquet`, binary without coverage, 8 workers | 591 | **F2** |
| 3 | `FuzzScanParquet` | 153,972 | none; most were footers broken at once |
| 4 | `FuzzScanParquet`, `GODEBUG=fuzzdebug=1` | 41 | showed the minimizing |
| 5–7 | `FuzzScanParquet`, `-fuzzminimizetime 1s` | 11,366; 13,808; 5,310 | none |
| 1–2 | `FuzzOwnPagesMatchArrow`, `-fuzzminimizetime 1s` | 2,825; 6,571 | none |

F3 and F4 were found by seeds written for the plan's edges, not by the fuzzer. F4 was
found by the twin reading 448's forged dictionary with `ownPages` off.

CI's fuzz workflow on the PR (run 43634666391) found F8 in `FuzzOwnPagesMatchArrow`,
from the generated corpus of a 30-second run. After the fix, a local run of 20 s
(`-fuzzminimizetime 1s`) executed 16,893 inputs and found nothing.

## 7. Checks

After the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd
GOAMD64=v3`:

- the touched packages: `./internal/readfuzz/` ok, 1.4 s; `./internal/source/parquet/`
  ok, 5.3 s;
- `go test ./internal/...`: ok;
- `go test .` in full: ok, 223 s;
- `GOEXPERIMENT=simd go vet ./...`: clean;
- `go run ./internal/gen/levels`: ok;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2, as on master;
- `go run ./internal/gen/testinventory -check`: 1,225 lines, as the baseline;
- `gofmt -l .`: empty.

The race detector and the widths are left to CI. The generated corpus under
`$(go env GOCACHE)/fuzz` is 34 MB in all, 1.6 MB of it `FuzzScanParquet`'s and 3.8 MB
`FuzzOwnPagesMatchArrow`'s.
