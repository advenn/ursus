# Step 521 — as built

**Parquet's Count and read agree, and a footer's lists are sized before they are
read.** CI's fuzz run 43649609407 on PR #71, step 517 already on master, found two
Parquet bugs, both through the public API on master 65fc8b2. F12: a file whose `Count`
was 5 read as 0 rows, with no error. F13: a footer of 78 bytes made arrow-go allocate
763 MiB before it was refused. Both are fixed. F12's cause was older than step 517:
ursus's own page reader had had it since step 192. F13 had a relative the brief
guessed at: a list's count hidden in what a walk by the header's types reads as
another type (§2.3). The brief called them F11 and F12; step 520's record already
has an F11, so they are F12 and F13 here.

Step 520 (#73, not yet merged) rewrote the Thrift reader the walk uses, so that it
reads as Thrift's compact protocol does. It fixes neither finding. On its branch,
origin/fix/parquet-codec-bound at d3b3952, F13's input still hit the watchdog at
800,021,024 bytes, and F12's input still failed "Count is 5, and the file reads as 0
rows". So this branch merges that branch in and builds both fixes on its reader.

The fuzz runs of this step found four more inputs, none of them this step's (§4). Two
are the Thrift decoding step 520 fixes, and they are kept here as seeds now that its
reader is merged in. Two are older bugs that remain open: an
UNCOMPRESSED page shorter than it declares reads memory it never wrote, and a corrupt
Null column is an internal error.

## 1. F12: Count and the read disagree, silently

### 1.1 The file

`f11/scan-count-input`, 88 bytes, now the seed
`internal/readfuzz/testdata/fuzz/FuzzScanParquet/page-bytes-missing-read-as-no-rows`.
It begins "0000" and ends "PAR1". Its footer is 67 bytes at offset 13: one row group
of 5 rows, one REQUIRED INT64 column named "", with encodings PLAIN, RLE,
RLE_DICTIONARY and DELTA_BINARY_PACKED. The dictionary page sits at offset 4 and the
data page at 24, in 79 bytes. The page header at offset 4 is a dictionary page claiming
24 bytes, and its own header runs into the footer. The column chunk ends before any of
those 24 bytes.

### 1.2 The cause

arrow-go's page reader reads a page's bytes with `io.ReadFull`
(`serializedPageReader.decompress`). Where none of them is there, `io.ReadFull` answers
`io.EOF`, which the page reader keeps as its error, so `ColumnChunkReader.Err()` is
`io.EOF`. `ReadBatch` answers 0 levels and no error. Step 517's `batchErr` passes
`Err()` on, as it must, and `uerr.Wrap` keeps the `io.EOF` in the error's chain. Every
consumer in the engine (`exec.drain`, `physical.Pull`, …) tests
`errors.Is(err, io.EOF)` for the end of its input. So the scan's error read as the
scan's end, and the query answered the rows before the page: none. Step 416's count is
`readBatch`'s `rows == 0` check, "row group %d of %s declares %d more rows than its
column chunks hold". It runs after the column's read, so it was never reached.

The brief's suspects:

- **`batchErr`, step 517:** yes, for every chunk arrow-go's column reader decodes.
- **The own-pages path:** yes too. `pageChunk.nextPage` wrapped the page reader's
  `Err()` the same way, a gap since step 192. The fuzzer's file did not reach it only
  because its footer lists DELTA_BINARY_PACKED. With that encoding byte set to PLAIN,
  it read as 0 rows through the own pages.
- **The leading magic, never checked:** that is so, but it is not the cause. arrow-go
  does not check it, and neither do pyarrow 25.0.1 and DuckDB 1.5.5: both read this
  file's footer. ursus does not check it either.
- **A chunk with no pages:** the chunk has a page header and no page bytes, which is
  this case.

### 1.3 The fix

`truncated(err)` (`dictindex.go`) turns an error that is `io.EOF` into
`io.ErrUnexpectedEOF`. It is applied at both places a page reader's error enters a read:
`batchErr`, and `pageChunk.nextPage` (`pages.go`). The read now fails with
`scan_parquet: reading a column chunk: unexpected EOF`, an `ErrIO`. A normal end of a
chunk is not `io.EOF`: there the page reader's `Next` finds no header and keeps no
error, so `Err()` is nil and nothing changes.

### 1.4 What pyarrow and DuckDB do with the file

| | pyarrow 25.0.1 | DuckDB 1.5.5 |
| --- | --- | --- |
| the footer's count | `metadata.num_rows` 5; `dataset(...).count_rows()` 5 | `select count(*)` 5 |
| the rows | `read_table` and `iter_batches`: `OSError: Unexpected end of stream: Page was smaller (0) than expected (24)` | `select *`: `TProtocolException: Invalid data` |

Both count the footer's 5 rows and refuse the read. ursus now does the same. Its
`Count` reads no column chunk (step 370) and stays 5. Its read is refused as corrupt.
Under ursus's rule a forged file is a refusal; nothing promised it would read. The
fuzz target already allows this: where the whole frame does not read, `Count` is not
compared with it.

## 2. F13: a 78-byte footer allocates 763 MiB before it is refused

### 2.1 The file

`f11/twin-input`, 78 bytes, now the seed
`internal/source/parquet/testdata/fuzz/FuzzOwnPagesMatchArrow/footer-1e8-row-groups-over-the-leading-magic`.
Its footer is 67 bytes, which is the file's size less 11, so it begins at offset 3 and
leaves the leading "PAR1" no room. Its list of row groups claims 10^8 structs.
Through the public API, before the fix, one read allocated 800,022,256 bytes against
fuzzcheck's bound of 64.1 MiB. `FuzzOwnPagesMatchArrow` hit the watchdog's runaway
limit at 800,013,824 bytes, all of it in `parquet.(*FileMetaData).ReadField4`, which
runs `make([]*RowGroup, 0, size)`. `FuzzOwnPagesMatchArrow` reads each input twice,
with the own pages and without, which matches the 1.5 GB of CI's profile.

### 2.2 The cause

`measureFooter` left to arrow-go, unwalked, any footer longer than the file less 12
bytes: the 8 bytes of length and magic after it, and the leading magic's 4. arrow-go's
`parseMetaData` reads a footer of up to the file less 8. So a footer 1 to 4 bytes
longer than the walk's bound, which takes the leading magic's place, was read by
arrow-go and never walked. The walk itself refuses this one: 10^8 elements in 67 bytes. Thrift
bounds a list's count only by its 100 MB message limit,
`checkContainerSizeForProtocol`, at 1 byte per element for every type except a double
(8) and a UUID (16). So 10^8 row groups passed it.

### 2.3 The relative: a list read as another type

arrow-go's generated reader reads a list's elements as the types parquet.thrift gives
them. The element type in the list's header is discarded:
`_, size, err := iprot.ReadListBegin(ctx)`. The walk read a list by its header's type.
So a list of row groups whose header says it holds binaries is, to the walk, binaries
whose lengths are its first bytes, and to arrow-go, row groups. `pqHiddenGroups` is a
file of 66 bytes built this way. Its one element is a binary of 25 bytes to the walk,
and to arrow-go a row group whose first byte, 0x19, starts its list of column chunks,
claiming 10^8. Before the fix the walk passed it, and arrow-go allocated 800,006,144
bytes.

This works at every list arrow-go reads. A list of structs hides the list again after
an element arrow-go reads whole. A list of integers is said to hold a binary, whose
length arrow-go reads as an integer. A list of binaries is said to hold a struct whose
field 0 is a binary. In each case, the next bytes arrow-go reads as the struct's next
field are bytes of the element to the walk. Each of the 14 lists below hid itself this
way. With a count of 10^5, arrow-go allocated 405,080 to 1,611,856 bytes reading each
(`TestFooterWalkReadsAListAsArrowGoDoes`'s log): 4 to 16 bytes per element, the
slice it makes before it reads one.

### 2.4 The fix

- **Every footer arrow-go reads is walked:** a file over 8 bytes and a footer of 1 to
  the file's size less 8 bytes, arrow-go's bound. A footer arrow-go refuses is still
  left to it.
- **The walk reads parquet.thrift's lists as arrow-go does.** `footerFields` names,
  for each struct that holds a list in a field of its own or of a struct it holds, the
  fields arrow-go reads as lists or as such structs. The table was enumerated from
  every `ReadListBegin` of arrow-go v18.7.0's `parquet/internal/gen-go` that is
  reachable from `FileMetaData`. The page indexes', which are read apart, are left
  out. That is 14 lists in 6 structs, reached through 5 struct fields:

  | struct | lists | structs holding one |
  | --- | --- | --- |
  | FileMetaData | 2 schema, 4 row_groups, 5 key_value_metadata, 7 column_orders | |
  | RowGroup | 1 columns, 4 sorting_columns | |
  | ColumnChunk | | 3 meta_data, 8 crypto_metadata |
  | ColumnMetaData | 2 encodings, 3 path_in_schema, 8 key_value_metadata, 13 encoding_stats | 16 size_statistics, 17 geospatial_statistics |
  | SizeStatistics | 2, 3 the level histograms | |
  | GeospatialStatistics | 2 geospatial_types | |
  | ColumnCryptoMetaData | | 2 ENCRYPTION_WITH_COLUMN_KEY |
  | EncryptionWithColumnKey | 1 path_in_schema | |

  A list's elements are read as their schema type, whatever the header says. A field
  arrow-go does not read this way, whether another field or one whose header gives it
  another type, is skipped as its header says, as Thrift's `Skip` does. A second
  schema list replaces the first, as arrow-go keeps the last. The schema's depth (step
  423) is measured from the same walk.
- **Each list's count is checked against the bytes after its header**, each element
  taking at least a byte, and each map's against half of them, at any depth. A count
  past them is refused before an element is read, with "a list in the footer claims
  more elements than the bytes after it hold" (`errFooterList`). The walk would refuse
  such a list anyway, by running out of bytes, and this check refuses it at the
  header. The error, `scan_parquet: corrupt: walking the footer`, is an `ErrIO`.

## 3. The tests

Failing first. On master, `TestAPageWhoseBytesAreMissingIsRefused` failed in all 10 of
its kinds, and the F12 seed failed with "Count is 5, and the file reads as 0 rows".
`TestAFooterListClaimingMoreThanItsBytesIsRefused` failed at 763 MiB. The F13 seed died
at the watchdog. All pass now.

- **The seeds**: CI's two inputs, under the names above. `pqForgedSeeds`, and through
  it `FuzzScanParquet`'s seed corpus, gains the 10^8 row-group footer at offset 0 and
  `pqHiddenGroups`.
- **`TestAPageWhoseBytesAreMissingIsRefused`** (readfuzz, public API). It reads F12's
  input on 1 and on 4 threads, which must be refused, with an error that is not
  `io.EOF`. Then it builds a file of three pages for each of step 517's seven kinds of
  chunk that arrow-go decodes, and for the three of them that the own pages read once
  the footer's extra encoding is dropped. The pages are a dictionary, its indices and a
  PLAIN page. Each page is cut after each of its bytes, none of them first, its header
  still claiming the whole page and the pages after it gone. A cut at a page's end is
  step 416's case. Every cut must be refused on 1 and on 4 threads. The whole file must
  read, with `Count` equal to its height, and a cut file's `Count`, which comes from
  the same footer, must be the whole file's or an error.
- **`TestAFooterListClaimingMoreThanItsBytesIsRefused`** (readfuzz, public API). Its
  inputs are F13's, `pqHiddenGroups`, and the 10^8 footer placed 0, 1, 2 and 3 bytes
  into its file. Each runs under `fuzzcheck.Run` with readfuzz's limits, 64 MiB plus
  1 KiB per byte. `CollectSchema`, `Collect` and `Count` must each be an `ErrIO`.
- **`TestFooterWalkDifferential`** (internal) tests the walk against arrow-go's own
  `metadata.NewFileMetaData` over 48 footers. They are 25 random files written by
  ursus's writer, with one to six columns of flat, List and Struct types, nulls,
  random row groups, codecs and statistics, and a logged seed. Then 12 random files
  written by pyarrow 25.0.1, in `testdata/oracle/footers/` and made by
  `bench/scripts/oracle/write_footers.py`, seed 521. These have size statistics, page
  indexes, sorting columns, Arrow and custom key-value metadata, maps and dictionaries.
  Then the 10 Parquet fixtures, and `everyList`, a footer holding every list of the
  table above (no writer writes a column's key-value metadata, its geospatial
  statistics or its key). The checks:
  - every footer reads in arrow-go, and the walk passes it;
  - every strict prefix of one, some 54,600, is refused by both;
  - every list the walk reads, some 2,160, is changed to claim one element past the
    bytes after its header, which both refuse and the walk refuses with
    `errFooterList`;
  - the same lists claiming 10^8 are refused by the walk;
  - the same lists said to hold each of the 12 other types are read by both.
- **`TestFooterWalkReadsAListAsArrowGoDoes`** (internal). Each of `everyList`'s 14
  lists is replaced by one that hides itself again, claiming 10^8 and then 10^5. The
  walk must refuse both. At 10^5, arrow-go must allocate at least 4 bytes per element,
  which proves the disguise works on it. The test fails if `everyList` and
  `footerFields` stop naming the same lists.
- **`TestMeasureFooterWalksEveryFooterArrowGoReads`** (internal). `everyList` 0 to 4
  bytes into its file must be walked and read by arrow-go, and its 10^8 row groups
  refused there. A footer longer than its file less 8 must be left to arrow-go, which
  refuses it.
- **Well-formed files read the same before and after.** A probe, outside the tree,
  read each of the following on master 65fc8b2 and on this branch, on 1 and 4
  threads, with `Count`:
  - `FuzzScanParquet`'s seed corpus: the fixtures, a frame of every flat type under
    six codecs, `tinyParquet`, steps 510's and 517's forged files and their honest
    versions;
  - the pyarrow footers' files and the byte-flip files;
  - each `pqIndexKind`'s honest files.

  That is 89 inputs here and 87 on master. 112 reads were identical, rendered row by
  row and compared by SHA-256. The only differences are the 10^8 row-group seed's
  error text, "walking the footer: a list … claims more elements", and the two new
  seeds, both refused. `twinSeeds`, 19 files read with the own pages on and off at
  batches of 1,023 and 332, was identical. After the merge, the same comparison
  against step 520's branch gave the same result: 124 inputs here and 122 there,
  step 520's codec seeds among them, and 126 identical reads.

## 4. The fuzz runs

Each run took the heavy lock, used the test binary under
`prlimit --as=4294967296`, ran 20 s per target, one target at a time, with
`-fuzzminimizetime=3s`, and was replayed under the cap.

- **This branch.** Both targets stopped within a second.
  - `FuzzScanParquet` found `bc6d3a00984e89e0` after 15,863 executions.
  - `FuzzOwnPagesMatchArrow` found `7a1bdeb8b260452a`.

  Replayed under the cap, both ended the process: `fatal error: out of memory` in
  arrow-go's `NewDictConverter`, at 45,097,156,608 and 51,539,607,552 bytes. Each is a
  dictionary page claiming 2^31−1 values that step 510's count (`dictpages.go`,
  `readPageHeader`) read otherwise than Thrift:
  - in the first, a 5-byte varint with bit 32 set, which ursus zigzag-decoded in 64
    bits and then cut to −1, where Thrift cuts to 32 bits first;
  - in the second, a header that continues with 0x10 after its dictionary header. To
    Thrift, a type of 0 is a stop whatever the high bits; ursus took it for a field,
    refused the header, and left the page unchecked.

  Both reproduce on master. Both belong to the Thrift-decoding family step 520 is
  fixing (its F11, a count in a 10-byte varint). With step 520's `footer.go` and
  `dictpages.go` diff applied to a scratch copy of master, both replay to PASS, and
  they pass on this branch now that step 520's branch is merged in. They are kept as
  seeds: `FuzzScanParquet/dictionary-count-varint-past-32-bits` and
  `FuzzOwnPagesMatchArrow/dictionary-header-ended-by-a-stop-of-0x10`.
- **This branch with step 520's Thrift decoding ported**, in a scratch tree. Two more
  inputs, neither caused by this step:
  - `FuzzScanParquet`, `7ac304015bdf360b`, 90 bytes: "row 0 differs, got
    -6510615555426900571, want 0". A read into poisoned buffers answered 0xA5 bytes,
    so it read memory it had not written. The page is UNCOMPRESSED, claims 3,072 bytes
    uncompressed and 0 compressed. arrow-go's no-op codec copies the 0 bytes into its
    3,072-byte buffer and returns the whole buffer
    (`compress.nocodec.Decode`), so the page is whatever that buffer held. pyarrow
    ("Unexpected end of stream") and DuckDB ("Invalid data") refuse it. This
    reproduces on master with a clean forged file: a PLAIN page of 8 INT64 claiming 64
    bytes and holding none read as eight values of 0xA5…. Its odd footer is walked now
    because the walk decodes and reads lists as Thrift and arrow-go do, so the fuzzer
    reached it here. Step 520 replaces arrow-go's Snappy, gzip, Brotli and Zstandard
    codecs, not the no-op codec, so this stays open after step 520.
  - `FuzzOwnPagesMatchArrow`, `bc1dbd675896f2b2`, 2,276 bytes, a mutation of
    `pyarrow_null_column.parquet`: ErrInternal, "parquet: column "nothing" of type Null
    has no level reader" (`colreader.go`, `newColReader`). It reproduces on master and
    on master with step 520.

  Both still fail on this branch after the merge. They are not committed. They are
  kept in `~/ursus-scratch-archive/step521-findings/`.

## 5. The teeth

Each fix was broken in the tree, the step's tests were run under the cap, and the file
was restored from a copy saved before (`cmp` clean each time). This was done before the
merge, and again after it on step 520's reader with the same failures; the figures
below are from before:

| broken | failed |
| --- | --- |
| `batchErr` returns the reader's error as is | the F12 seed ("Count is 5, and the file reads as 0 rows"); `TestAPageWhoseBytesAreMissingIsRefused`, F12's input and the 7 arrow-go kinds |
| `pageChunk.nextPage` wraps `Err()` as is | `TestAPageWhoseBytesAreMissingIsRefused`, the 3 own-pages kinds |
| `measureFooter` back to the file less 12 | the F13 seed (watchdog, 800,013,824 bytes); `FuzzScanParquet` on the offset-0 seed (800,012,384 bytes); `TestAFooterListClaimingMoreThanItsBytesIsRefused` (800,014,336); `TestMeasureFooterWalksEveryFooterArrowGoReads` |
| list elements walked by the header's type | `TestFooterWalkReadsAListAsArrowGoDoes`, all 28 cases; `FuzzScanParquet` on `pqHiddenGroups` (800,006,144 bytes); `TestAFooterListClaimingMoreThanItsBytesIsRefused`; `TestFooterWalkDifferential`, 8,193 lists said to hold another type that arrow-go reads and the walk refused |
| a list's count against the whole footer, not the bytes after it | `TestFooterWalkDifferential`, 2,904 lists claiming a byte past theirs, refused only by running out of bytes, not at the header |

## 6. Checks

All run after the last edit. The two Parquet packages ran first on their own:
`./internal/source/parquet` 17.4 s and `./internal/readfuzz` 2.4 s, both ok.

## 7. Left

- **This branch holds step 520's branch, merged in**, so #73 lands with it or before
  it. The conflicts were in `footer.go`'s `thriftReader`. They were resolved by
  keeping step 520's decoding (`varint`, `varint32`, `i32`, a stop at any type of 0,
  sizes cut to 32 bits) and adding this step's walk, its counts against the bytes
  left, and `onList`. `pqForgedSeeds` holds both steps' seeds.
- **An UNCOMPRESSED page shorter than it declares reads memory it never wrote** (§4,
  `7ac304015bdf360b`). This is codec territory, step 520's neighbour. On this branch
  the fuzzer reaches it within a second, because the walk now passes footers that the
  old reader refused. So CI's fuzz workflow may report it until it is fixed. One fix is
  to refuse an UNCOMPRESSED page whose compressed size is not its uncompressed size,
  which every writer keeps equal.
- **A corrupt Null column is an internal error** (§4, `bc1dbd675896f2b2`).
