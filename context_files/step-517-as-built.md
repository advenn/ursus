# Step 517 — as built

**A Parquet dictionary index past its dictionary allocates nothing.** F9 of
[`step-510-as-built.md`](./step-510-as-built.md), which step 510 left for the
maintainer, is fixed: a page reader ursus wraps around arrow-go's checks each
dictionary-encoded data page's indices against its dictionary before arrow-go's column
reader decodes the page. A forged file of 128 bytes made ursus allocate 8 GiB through
the public API, and a wider type or a larger index ended the process with a fatal "out
of memory". CI's fuzz workflow found this class on every pull request since. On the
way, arrow-go was found to drop a page reader's error at a batch's first page, so a
refused page read as a row group short of rows; ursus asks the column reader for it now
(§2.3). One more allocation, a different one, which CI's last run found in
`FuzzScanParquet`, is left for the maintainer (§7, F10).

## 1. The finding

arrow-go v18.7.0 looks a dictionary index up through
`parquet/internal/encoding.(*dictConverter[T]).ensure`, which runs
`make([]T, idx+1-len(dict))` where the index is at or past the dictionary's capacity,
before anything checks the index against the dictionary: `IsValidSingle`, `IsValid` and
`Fill` each call it first. An RLE run saying index 2^30 in a dictionary of two values
asked for 2^30 values: 8 GiB of int64, 12 GiB of INT96, 24 GiB of slice headers for a
FIXED_LEN_BYTE_ARRAY or a BYTE_ARRAY. It reaches every chunk arrow-go decodes, not
ursus's own pages (`pages.go`, `ownable`): a nested column, a struct's field, INT96,
FIXED_LEN_BYTE_ARRAY, and any chunk whose footer lists an encoding the own pages do not
read. The own pages check each index first (`pageChunk.indices`).

Step 510's record said BYTE_ARRAY's converter does not grow this way. It does: the
converter is the same generic type, and the forged BYTE_ARRAY file asked the runtime
for 25,769,803,800 bytes under the cap (§3).

CI's two inputs of run 43639097835, kept under descriptive names:

- `FuzzOwnPagesMatchArrow`: `testdata/fuzz/FuzzOwnPagesMatchArrow/dictionary-index-past-its-dictionary-mutated`,
  CI's mutation of step 510's F9 input, 11,916 bytes. Its UINT_64 chunk, read with
  `ownPages` off, holds a V2 page whose RLE run of 27 bits says index 236,464,192 in a
  dictionary of 100. Before the fix it allocated 1,892,837,416 bytes against a bound of
  75.6 MiB; now nothing. Step 510's F9 input, skipped by its SHA-256 until now, runs as
  a seed again: `twinFindings` is empty.
- `FuzzScanParquet`: `testdata/fuzz/FuzzScanParquet/zstd-frame-claiming-1p2gb-trailer-para`,
  623 bytes. CI's worker died while minimizing, and what it saved is the minimizer's
  last candidate, whose file ends "PARA" where a Parquet file ends "PAR1": it is
  refused at once, before this step and after, within every bound. It is not F9. With
  its last byte "1" again, the file is F10 (§7).

## 2. The design

### 2.1 The way in

Step 510 named two: a page reader ursus wraps, through arrow-go's deprecated
`file.NewColumnReader`; or decompressing each such page a second time from what
`measured` already sees. The first is built.

- **The bytes checked are the bytes arrow-go decodes.** Each page the column reader
  takes from `checkedPages` has been decompressed already, by arrow-go, into the buffer
  it decodes, and its header parsed by arrow-go: its encoding, its count of values, its
  level encodings or level lengths. The second way would decompress each page again
  and parse its header and levels with a parser of ursus's own beside arrow-go's, which
  could disagree with it on a forged page and miss what arrow-go then reads.
- **No second decompression.** The check is one pass over a dictionary-encoded page's
  definition levels and its indices.
- **One place.** `openColumnIn`'s leaf reader, where a chunk goes to arrow-go and not to
  the own pages, calls `checkedColumn` in place of `RowGroupReader.Column`. It makes
  the same reader `Column` makes, with the constructor `Column` wraps
  (`newTypedColumnChunkReader` and `NewColumnReader` set the same fields), the same
  allocator, now carried in `fileView.mem`, and the file's buffer pool, which
  `file.Reader.BufferPool` exports for a reader that makes its own column readers.
  `NewColumnReader` is deprecated as "unsafe to use outside of this package"; arrow-go
  is pinned in go.mod, so its removal would show as a build that fails.

### 2.2 The check (`dictindex.go`)

`checkedPages` embeds arrow-go's page reader. On each `Next`:

- a dictionary page sets the dictionary's count, its header's `num_values`, which is
  the capacity `NewDictConverter` gives the dictionary;
- a data page encoded PLAIN_DICTIONARY or RLE_DICTIONARY is checked; if no dictionary
  page came first, arrow-go reads one out of turn through `GetDictionaryPage` before it
  decodes the page, so `checkedPages` reads it first, for its count. Through arrow-go's
  own page reader that read succeeds only where `Next` gave the dictionary first, so
  this is for a reader that seeks; the generated pages test it;
- an index arrow-go would read at or past the count refuses the page: `Next` answers
  false and `Err` gives "corrupt: column v: a data page's dictionary index 1073741824 is
  past its dictionary of 2 values", wrapped KindIO as F4's refusal is.

Which indices arrow-go reads, so that no file it reads is refused:

- the page's first n, n its values that are not null: the levels arrow-go counts at the
  column's maximum definition level, read as its `LevelDecoder` reads them (a V1 page's
  repetition and definition levels, RLE with their 4-byte length or the deprecated
  BIT_PACKED as arrow-go unpacks it; a V2 page's level lengths, its definition levels
  read on to the page's end as arrow-go's decoder reads them). A level that cannot be
  read is counted a value, so the count only grows where the levels are corrupt;
- past them, a bit-packed run's padding can hold anything and is not read;
- each run as arrow-go's `RleDecoder` and bit reader read it: an RLE run's value the
  whole bytes it is stored in, unmasked; a bit-packed run's values past the page's end
  0; a bit-packed run wider than 32 bits an error there. Past a header `RleDecoder`
  refuses, where arrow-go fails the page on the values it came short of, the check
  reads on, which checks more and never less;
- a bit width wider than the dictionary needs is legal, and only the values are
  checked; a PLAIN page after the dictionary's, where a writer fell back, is not checked.

Linear: one extra pass over each dictionary-encoded data page of a chunk arrow-go
decodes, its levels a run at a time and its indices eight bytes a load. A PLAIN page
costs a type switch; the own pages' chunks never come here.

Safe under the parallel reader: a `checkedPages` is one column chunk's, made and read by
the worker decoding its row group. Nothing is shared but the file's buffer pool, a
`sync.Pool`, as `Column` shares it, and no property is set. Step 510's race, limits
copied from shared properties as each page reader is made, does not arise: the check
reads nothing from the properties.

### 2.3 A refusal that says why (`batchErr`)

A page refused at a batch's first page was reported through the public API as "row
group 0 of forged.parquet declares 5 more rows than its column chunks hold", for six of
the seven kinds. arrow-go's `columnChunkReader.readBatch` loops
`for totalLvls < batchSize && c.HasNext() && err == nil`, so an error that ends the
chunk before the loop's first turn is dropped and the batch answers that it read
nothing; `ColumnChunkReader.Err` holds it, as that interface documents. ursus never asked. `batchErr` does now, at each of the nine
`ReadBatch` and `ReadBatchInPage` sites of the arrow-go readers: a batch that read
nothing and answered no error gives the reader's `Err`. At a chunk's true end `Err` is
nil, so a file that reads is unchanged; a page that does not decompress, which read as
the same shortfall, names its reason too.

`batchErr` also ends a read arrow-go would otherwise continue wrongly: where a page
fails after arrow-go has counted its values (its levels set, its decoder not), the next
`ReadBatch` decodes them with what it had. A first build of the check refused an
out-of-turn dictionary's page from `GetDictionaryPage`, and the next `ReadBatch` panicked
on a nil decoder. `checkedPages` refuses in `Next`, before arrow-go takes a page in, for
that reason.

## 3. The tests

Failing first, each under `prlimit --as=4294967296`, the test binaries built with the
check switched off (`checkedColumn` back to `rg.Column`): every case of the new test
ended in `fatal error: out of memory` in `dictConverter[...].ensure`, "cannot allocate
8594128896-byte block" for an INT64; CI's twin input allocated 1.8 GB against a bound of
75.6 MiB.

| test | package | what | oracle |
| --- | --- | --- | --- |
| `TestADictionaryIndexPastItsDictionaryAllocatesNothingOfIt`, unskipped | readfuzz | forged files of a dictionary of two values whose indices say 2^30, in an RLE run and in a bit-packed one, through the public API, for each kind of chunk arrow-go decodes (`pqIndexKinds`): INT64 with DELTA_BINARY_PACKED listed, REQUIRED and OPTIONAL with a null; the INT64 element of a List; the INT64 field of a Struct; INT96; FIXED_LEN_BYTE_ARRAY(4); BYTE_ARRAY with DELTA_LENGTH_BYTE_ARRAY listed. Each is refused as an I/O error naming the index. Each kind's honest pages, indices in the dictionary at 31 bits, a bit-packed run whose padding holds 2^30, and a dictionary page, its indices and a PLAIN page after them, read | `fuzzcheck.Run`'s allocation bound (64 MiB + 1 KiB a byte); the PLAIN pages of the values the indices pick, a differential |
| `TestCheckedPagesRefuseWhereArrowGoGrowsItsDictionary`, new | parquet | 2,000 generated dictionary-encoded data pages of an INT64 column (REQUIRED, OPTIONAL, a List's element; V1 and V2; definition levels RLE or BIT_PACKED; indices in RLE and bit-packed runs at widths up to 32, some at the dictionary's count, some 2^20 and more, some only in padding; a tenth cut short or holding a header arrow-go refuses; a fifth with the dictionary out of turn), each read whole through arrow-go's column reader over arrow-go's pages and through `checkedPages`: through `checkedPages` no read grows a dictionary; where arrow-go's grew one, `checkedPages` refused the page; where it did not refuse, the reads answer alike, values, levels and error; for a page not cut short, it refused exactly the pages where an index arrow-go reads is past the dictionary, which arrow-go's growth confirms for the far ones. 637 refused, 236 grew arrow-go's dictionary, 1,072 well formed and read; 0.5 s | arrow-go's own column reader, and the allocation it makes (8 MiB and more is a dictionary grown to 2^20) |
| `FuzzScanParquet` seeds | readfuzz | `pqForgedSeeds` grows from 8 to 22, the 14 forged refusals above; CI's corpus file; 47 seeds in all | as the target's |
| `FuzzOwnPagesMatchArrow` seeds | parquet | CI's twin input; `forged-dictionary-index-2p30` and `-bit-packed`, an INT64 chunk the own pages read with `ownPages` on and arrow-go with it off; step 510's F9 input unskipped; 31 seeds in all | as the target's |

`forge_test.go` writes a leaf in a List of the three-level layout or in a Struct, an
OPTIONAL leaf and a FIXED_LEN_BYTE_ARRAY's length; the eight forged files step 510 wrote
are byte for byte as before (their SHA-256s checked against `origin/master`'s forge, and
against the twin corpus's `forged-*` files).

## 4. The overhead

One file ursus wrote, a List(Int64) of 200,000 rows of five values, 1,000,000 values of
100 distinct, dictionary-encoded V1 pages with Snappy, 54,736 bytes in 2 row groups,
read whole on one thread by `ScanParquetBytes(...).Collect(WithThreads(1))`: 2 reads
to warm, then 21 timed. The check switched off and on, four rounds interleaved, under
the shared lock:

| | median of 21, ms | least of 21, ms |
| --- | --- | --- |
| before | 18.25, 18.40, 18.65, 18.85; mean 18.53 | 17.67, 17.81, 17.83, 17.84 |
| after | 19.63, 19.05, 19.58, 19.73; mean 19.50 | 18.46, 18.66, 18.41, 18.31 |
| overhead | **+5.2%** | **+3.8%** |

Under the 15% the brief sets. Each index here is in a bit-packed run of 7 bits, the
check's dearest case, read eight bytes a load. A first file of 1,000 distinct values
was written as PLAIN pages (the writer's dictionary fell back), so it measured only the
type switch, within noise.

## 5. Fuzz runs

One at a time, under the shared lock and `prlimit --as=4294967296`, 20 s each,
`-fuzzminimizetime=3s`, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:

| target | inputs | new interesting | outcome |
| --- | --- | --- | --- |
| `FuzzOwnPagesMatchArrow` | 29,477 | 25 | none |
| `FuzzScanParquet` | 3,884 | 18 | none |

Neither saved an input.

## 6. Teeth

Each mutation made to a copy of the code, both packages' test binaries built, the file
restored from the copy and its md5 checked; the binaries run under the cap.

| # | mutation | caught by | |
| --- | --- | --- | --- |
| A | `checkedColumn` not used: `rg.Column` as before | the new readfuzz test and `FuzzScanParquet`'s seeds: fatal "out of memory", "cannot allocate 8594128896-byte block" in `ensure`; `FuzzOwnPagesMatchArrow`: step 510's F9 input, runaway allocation of 1.8 GB; CI's mutated input alone, 1,892,857,344 bytes over the bound; `forged-dictionary-index-2p30` and `-bit-packed` alone, fatal "out of memory" | **bites** |
| B | every level counted a value, the definition levels not read | the readfuzz test's OPTIONAL kind, its padding 2^30 refused; the differential, 375 pages refused that no reader reads past | **bites** |
| C | `batchErr` does not ask `Err` | the readfuzz test, 6 of 7 kinds "not an I/O error naming the index" (BYTE_ARRAY's in-page read returns the error itself); the differential, 637 pages | **bites** |
| D | bit-packed runs not checked | the readfuzz test, both seed runs: fatal "out of memory"; the differential, 512 pages | **bites** |
| E | a dictionary out of turn not read first | the differential, 129 pages, "read through checkedPages, its dictionary grew by 14671632 bytes" | **bites** |
| F | an RLE run's index at the dictionary's count taken for in it (`>` for `>=`) | the differential, 2 pages | **bites** |

## 7. Left for the maintainer

**F10.** A page whose codec's own header claims more than the page: CI's
`FuzzScanParquet` input with its last byte "1" again is a Polars file of two columns,
Zstandard, 623 bytes, whose page header says 15 bytes uncompressed and whose Zstandard
frame header claims a content size of 1,211,898,114 bytes. arrow-go's codec calls
klauspost's `zstd.(*Decoder).DecodeAll`, which makes `dst` of the claimed content size
before it decodes a block (decoder.go, `make([]byte, len(dst), len(dst)+
int(frame.FrameContentSize)+...)`), up to its default limit of 64 GiB: 1.2 GB here, the
harness's runaway check ending the process at 4 times its bound. The page's buffer,
which boundPages bounds (D14), is passed as `dst` and does not bound it. By their code,
klauspost's Snappy (`s2.Decode`, a length header of up to 2^32−1) sizes its output the
same way, and a frame with no content size, a gzip, Brotli or LZ4 stream, can decode far
past its page's declared size before arrow-go compares the lengths. Not F9, and not
fixed here: a fix bounds each codec's output at the page's declared size, which means
codecs ursus registers in arrow-go's `compress` registry or a check of each page's
codec header beside `countDictionaries`, a design of its own. Until then fuzz.yml can
find this class again.

## 8. Checks

After the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd
GOAMD64=v3`:

- the touched packages: `./internal/readfuzz/` ok, 1.7 s; `./internal/source/parquet/`
  ok, 6.5 s;
- `go test ./internal/...`: ok, 28 packages, under the lock;
- `go test .` in full: ok, 267 s, under the lock;
- `GOEXPERIMENT=simd go vet ./...`: clean;
- `go run ./internal/gen/levels`: ok;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2, as on master;
- `go run ./internal/gen/testinventory -check`: 1,225 lines, as the baseline;
- `gofmt -l .`: empty.

The race detector and the widths are left to CI.
