# Step 517 — as built

**A Parquet dictionary index past its dictionary allocates nothing.** The check step
510 left for the maintainer (§4 of [`step-510-as-built.md`](./step-510-as-built.md))
is built: a page reader ursus wraps around arrow-go's checks each dictionary-encoded
data page's indices against its dictionary before arrow-go's column reader decodes the
page. On the way, a page refused at a batch's first page was found to read as a row
group short of rows: the column reader's `Err`, which holds the page's error as its
interface documents, was never asked; ursus asks it now (§2.3). A different claim,
which CI's last run found in `FuzzScanParquet`, is left for the maintainer (§7).

## 1. The check's reach

A data page's dictionary indices are claims: each must lie inside its dictionary, and
an index past it is refused before the page is decoded. The own pages (`pages.go`,
`ownable`) check each index first (`pageChunk.indices`). Every chunk arrow-go's column
reader decodes instead had no such check: a nested column, a struct's field, INT96,
FIXED_LEN_BYTE_ARRAY, and any chunk whose footer lists an encoding the own pages do not
read. BYTE_ARRAY's chunks are among them (§3).

CI's two inputs:

- `FuzzOwnPagesMatchArrow`'s, CI's mutation of step 510's input, 11,916 bytes. Its
  UINT_64 chunk, read with `ownPages` off, holds a V2 page whose RLE run of 27 bits
  says an index past its dictionary of 100. It is refused now. Step 510's input of the
  same kind is no longer skipped: `twinFindings` is empty.
- `FuzzScanParquet`'s, 623 bytes, the minimizer's last candidate, whose file ends
  "PARA" where a Parquet file ends "PAR1": it is refused at once, before this step and
  after, within every bound. With its last byte "1" again, the file is §7's.

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
  the capacity the column reader gives the dictionary;
- a data page encoded PLAIN_DICTIONARY or RLE_DICTIONARY is checked; if no dictionary
  page came first, arrow-go reads one out of turn through `GetDictionaryPage` before it
  decodes the page, so `checkedPages` reads it first, for its count. Through arrow-go's
  own page reader that read succeeds only where `Next` gave the dictionary first, so
  this is for a reader that seeks; the generated pages test it;
- an index arrow-go would read at or past the count refuses the page: `Next` answers
  false and `Err` gives "corrupt: column v: a data page's dictionary index … is past
  its dictionary of 2 values", wrapped KindIO as step 510's refusal of a dictionary
  count is.

Which indices arrow-go reads, so that no file it reads is refused:

- the page's first n, n its values that are not null: the levels arrow-go counts at the
  column's maximum definition level, read as its `LevelDecoder` reads them (a V1 page's
  repetition and definition levels, RLE with their 4-byte length or the deprecated
  BIT_PACKED as arrow-go unpacks it; a V2 page's level lengths, its definition levels
  read on to the page's end as arrow-go's decoder reads them). A level that cannot be
  read is counted a value, so the count only grows where the levels are corrupt;
- past them, a bit-packed run's padding can hold anything and is not read;
- each run as arrow-go's `RleDecoder` and bit reader read it, so the check sees the
  index the decoder will use. Past a header `RleDecoder` refuses, the check reads on,
  which checks more and never less;
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
the seven kinds. A batch that ends at the chunk's error before its first value answers
that it read nothing, and `ColumnChunkReader.Err` holds the error, as that interface
documents. ursus never asked. `batchErr` does now, at each of the nine `ReadBatch` and
`ReadBatchInPage` sites of the arrow-go readers: a batch that read nothing and answered
no error gives the reader's `Err`. At a chunk's true end `Err` is nil, so a file that
reads is unchanged; a page that does not decompress, which read as the same shortfall,
names its reason too.

`batchErr` also ends the read at the first refused page, so no later `ReadBatch` goes
on past it. `checkedPages` refuses in `Next`, before the column reader takes a page in,
so a refusal never leaves a page half taken.

## 3. The tests

Failing first, each under `prlimit --as=4294967296`, the test binaries built with the
check switched off (`checkedColumn` back to `rg.Column`): every case of the new test
failed, and so did CI's twin input.

| test | package | what | oracle |
| --- | --- | --- | --- |
| `TestADictionaryIndexPastItsDictionaryAllocatesNothingOfIt`, unskipped | readfuzz | forged files of a dictionary of two values whose indices lie far past it, in an RLE run and in a bit-packed one, through the public API, for each kind of chunk arrow-go decodes (`pqIndexKinds`): INT64 with DELTA_BINARY_PACKED listed, REQUIRED and OPTIONAL with a null; the INT64 element of a List; the INT64 field of a Struct; INT96; FIXED_LEN_BYTE_ARRAY(4); BYTE_ARRAY with DELTA_LENGTH_BYTE_ARRAY listed. Each is refused as an I/O error naming the index. Each kind's honest pages, indices in the dictionary at 31 bits, a bit-packed run whose padding holds an index past it, and a dictionary page, its indices and a PLAIN page after them, read | `fuzzcheck.Run`'s allocation bound (64 MiB + 1 KiB a byte); the PLAIN pages of the values the indices pick, a differential |
| the differential of `dictindex_internal_test.go`, new | parquet | 2,000 generated dictionary-encoded data pages of an INT64 column (REQUIRED, OPTIONAL, a List's element; V1 and V2; definition levels RLE or BIT_PACKED; indices in RLE and bit-packed runs at widths up to 32, some at the dictionary's count, some far past it, some only in padding; a tenth cut short or holding a header the page reader refuses; a fifth with the dictionary out of turn), each read whole through arrow-go's column reader over arrow-go's pages, alone and through `checkedPages`: through `checkedPages` no read allocates more than its page needs; where it did not refuse, the reads answer alike, values, levels and error; for a page not cut short, it refused exactly the pages where an index the reader reads is past the dictionary. 637 refused, 1,072 well formed and read; 0.5 s | arrow-go's own column reader |
| `FuzzScanParquet` seeds | readfuzz | `pqForgedSeeds` grows from 8 to 22, the 14 forged refusals above; CI's corpus file; 47 seeds in all | as the target's |
| `FuzzOwnPagesMatchArrow` seeds | parquet | CI's twin input; two forged files of an index past its dictionary, in an RLE run and in a bit-packed one, an INT64 chunk the own pages read with `ownPages` on and arrow-go with it off; step 510's input unskipped; 31 seeds in all | as the target's |

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
| A | `checkedColumn` not used: `rg.Column` as before | the new readfuzz test and `FuzzScanParquet`'s seeds; `FuzzOwnPagesMatchArrow`: step 510's input, CI's mutated input, and the two forged files, each alone | **bites** |
| B | every level counted a value, the definition levels not read | the readfuzz test's OPTIONAL kind, the index in its padding refused; the differential, 375 pages refused that no reader reads past | **bites** |
| C | `batchErr` does not ask `Err` | the readfuzz test, 6 of 7 kinds "not an I/O error naming the index" (BYTE_ARRAY's in-page read returns the error itself); the differential, 637 pages | **bites** |
| D | bit-packed runs not checked | the readfuzz test's bit-packed cases; the differential, 512 pages | **bites** |
| E | a dictionary out of turn not read first | the differential, 129 pages | **bites** |
| F | an RLE run's index at the dictionary's count taken for in it (`>` for `>=`) | the differential, 2 pages | **bites** |

## 7. Left for the maintainer

**A page whose codec's own header claims more than the page.** CI's
`FuzzScanParquet` input with its last byte "1" again is a Polars file of two columns,
Zstandard, 623 bytes, whose page header says 15 bytes uncompressed and whose Zstandard
frame header claims a far larger content size. A codec's own header, a Zstandard
frame's content size or a Snappy block's length, is a claim as the page header's is,
and a stream without one can decode past its page's declared size; ursus held neither
to the page. Not fixed here: a fix bounds each codec's output at the page's declared
size, which means codecs ursus registers in arrow-go's `compress` registry or a check
of each page's codec header beside `countDictionaries`, a design of its own. **Fixed in
step 520** ([record](./step-520-as-built.md)): ursus's codecs in arrow-go's registry
decode a page into the buffer it declares and refuse more.

## 8. Checks

After the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd
GOAMD64=v3`:

- the touched packages: `./internal/readfuzz/` ok, 1.7 s; `./internal/source/parquet/`
  ok, 6.5 s;
- `go test ./internal/...`: ok, 28 packages, under the lock, and again once master
  (steps 511 and 516, #66 and #67) was merged in;
- `go test .` in full: ok, 267 s, under the lock; 208 s after the merge;
- `GOEXPERIMENT=simd go vet ./...`: clean;
- `go run ./internal/gen/levels`: ok;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2, as on master;
- `go run ./internal/gen/testinventory -check`: 1,225 lines, as the baseline;
- `gofmt -l .`: empty.

The race detector and the widths are left to CI.
