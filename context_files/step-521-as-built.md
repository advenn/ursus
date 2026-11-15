# Step 521 — as built

**Parquet's Count and read agree, and a footer's lists are sized before they are
read.** CI's fuzz run on PR #71, step 517 already on master, found two Parquet bugs,
both through the public API on master 65fc8b2: a file whose `Count` was 5 read as 0
rows, with no error (§1); and a footer of 78 bytes whose list of row groups claims
more than its bytes hold went unwalked (§2). Both are fixed. The first's cause was
older than step 517: ursus's own page reader had had it since step 192. The second had
a relative the brief guessed at: a list's count hidden in what a walk by the header's
types reads as another type (§2.3).

Step 520 (#73, not yet merged) rewrote the Thrift reader the walk uses, so that it
reads as Thrift's compact protocol does. It fixes neither bug: on its branch,
origin/fix/parquet-codec-bound at d3b3952, both inputs still failed, the first with
"Count is 5, and the file reads as 0 rows". So this branch merges that branch in and
builds both fixes on its reader.

The fuzz runs of this step found four more inputs, none of them caused by this step
(§4). Two are the Thrift decoding step 520 fixes. The other two are older bugs, fixed
here too (§4a) because CI's fuzz workflow would have found them on this pull request:
- an UNCOMPRESSED page whose two declared sizes differ, which is refused now;
- a corrupt Null column, which was an internal error.

A later fuzz run found a page of dictionary indices 0 bits wide with a byte after a
run's header, which `checkedPages` refuses now (§4c).

The fifth fuzz run found two more (§4d): a page header that does not parse is looked
for no further than its chunk now, and a DELTA_BINARY_PACKED page with a block of 31
values, which the format does not allow, is left open.

The sixth run found a row group with fewer column chunks than its schema has
columns, which was an internal error on master; it is fixed (§4e). The same section
shows that a CI input from PR #72 is §2's.

Step 519's own fuzzing found a 93-byte file of the UNCOMPRESSED kind, and step 519's
CI two more of §2's footers. All three pass here (§4b).

## 1. Count and the read disagree, silently

### 1.1 The file

CI's input, 88 bytes. It begins "0000" and ends "PAR1". Its footer is 67 bytes at
offset 13: one row group of 5 rows, one REQUIRED INT64 column named "", with encodings
PLAIN, RLE, RLE_DICTIONARY and DELTA_BINARY_PACKED. The dictionary page sits at offset
4 and the data page at 24, in 79 bytes. The page header at offset 4 is a dictionary
page claiming 24 bytes, and its own header runs into the footer. The column chunk ends
before any of those 24 bytes.

### 1.2 The cause

A page none of whose bytes are there ends its chunk's read with an error, and that
error was `io.EOF`, which `io.ReadFull` answers when it reads nothing. `ReadBatch`
answers 0 levels and no error. Step 517's `batchErr` passes `Err()` on, as it must,
and `uerr.Wrap` keeps the `io.EOF` in the error's chain. Every consumer in the engine
(`exec.drain`, `physical.Pull`, …) tests `errors.Is(err, io.EOF)` for the end of its
input. So the scan's error read as the scan's end, and the query answered the rows
before the page: none. Step 416's count is `readBatch`'s `rows == 0` check, "row group
%d of %s declares %d more rows than its column chunks hold". It runs after the
column's read, so it was never reached.

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

An error from a page reader is never the end of a read. `truncated(err)`
(`dictindex.go`) turns an error that is `io.EOF` into `io.ErrUnexpectedEOF`. It is
applied at both places a page reader's error enters a read:
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

## 2. A footer's lists, sized before they are read

### 2.1 The file

CI's second input, 78 bytes. Its footer is 67 bytes, which is the file's size less
11, so it begins at offset 3 and leaves the leading "PAR1" no room. Its list of row
groups claims far more structs than its 67 bytes hold.

### 2.2 The cause

A footer may be as long as its file less 8 bytes, the length and magic after it.
`measureFooter` walked only footers up to the file less 12, leaving room for the
leading magic's 4, and left a longer one unwalked. So a footer 1 to 4 bytes longer
than the walk's bound, which takes the leading magic's place, was read and never
walked. The walk itself refuses this one, a count its bytes cannot hold.

### 2.3 The relative: a list read as another type

A generated Thrift reader reads a list's elements as the types parquet.thrift gives
them, whatever the element type in the list's header says:
`_, size, err := iprot.ReadListBegin(ctx)`. The walk read a list by its header's type.
So a list of row groups whose header says it holds binaries is, to the walk, binaries
whose lengths are its first bytes, and to the footer's reader, row groups.
`pqHiddenGroups` is a file of 66 bytes built this way. Its one element is a binary of
25 bytes to the walk, and to the reader a row group whose first byte, 0x19, starts its
list of column chunks, claiming more than its bytes hold. Before the fix the walk
passed it.

Every list of the footer can be hidden so. A list of structs hides the list again
after an element read whole. A list of integers is said to hold a binary, whose length
the reader reads as an integer. A list of binaries is said to hold a struct whose
field 0 is a binary. In each case, the next bytes the reader reads as the struct's
next field are bytes of the element to the walk. Each of the 14 lists below hid
itself this way.

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
its kinds, and §1's input failed with "Count is 5, and the file reads as 0 rows".
`TestAFooterListClaimingMoreThanItsBytesIsRefused` failed, and so did §2's input. All
pass now.

- **The seeds**: `pqForgedSeeds`, and through it `FuzzScanParquet`'s seed corpus,
  gains a footer whose row groups claim more than its bytes hold, at offset 0, and
  `pqHiddenGroups`.
- **`TestAPageWhoseBytesAreMissingIsRefused`** (readfuzz, public API). It reads §1's
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
  inputs are §2's, `pqHiddenGroups`, and the forged footer placed 0, 1, 2 and 3 bytes
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
  - the same lists claiming far past their bytes are refused by the walk;
  - the same lists said to hold each of the 12 other types are read by both.
- **`TestFooterWalkReadsAListAsArrowGoDoes`** (internal). Each of `everyList`'s 14
  lists is replaced by one that hides itself again, claiming more elements than its
  bytes hold. The walk must refuse each. The test fails if `everyList` and
  `footerFields` stop naming the same lists.
- **`TestMeasureFooterWalksEveryFooterArrowGoReads`** (internal). `everyList` 0 to 4
  bytes into its file must be walked and read by arrow-go, and a version claiming
  more row groups than its bytes hold refused there. A footer longer than its file
  less 8 must be left to arrow-go, which refuses it.
- **Well-formed files read the same before and after.** A probe, outside the tree,
  read each of the following on master 65fc8b2 and on this branch, on 1 and 4
  threads, with `Count`:
  - `FuzzScanParquet`'s seed corpus: the fixtures, a frame of every flat type under
    six codecs, `tinyParquet`, steps 510's and 517's forged files and their honest
    versions;
  - the pyarrow footers' files and the byte-flip files;
  - each `pqIndexKind`'s honest files.

  That is 89 inputs here and 87 on master. 112 reads were identical, rendered row by
  row and compared by SHA-256. The only differences are the forged footer seed's
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

  Each is a dictionary page whose count step 510's walk (`dictpages.go`,
  `readPageHeader`) read otherwise than Thrift, and so left unchecked:
  - in the first, a 5-byte varint with bit 32 set, which ursus zigzag-decoded in 64
    bits and then cut to −1, where Thrift cuts to 32 bits first;
  - in the second, a header that continues with 0x10 after its dictionary header. To
    Thrift, a type of 0 is a stop whatever the high bits; ursus took it for a field,
    refused the header, and left the page unchecked.

  Both reproduce on master. Both belong to the Thrift-decoding family step 520 is
  fixing (its §4, a count in a 10-byte varint). With step 520's `footer.go` and
  `dictpages.go` diff applied to a scratch copy of master, both replay to PASS, and
  they pass on this branch now that step 520's branch is merged in.
- **This branch with step 520's Thrift decoding ported**, in a scratch tree. Two more
  inputs, neither caused by this step:
  - `FuzzScanParquet`, `7ac304015bdf360b`, 90 bytes: an UNCOMPRESSED page whose
    declared sizes differ, 3,072 bytes uncompressed and 0 compressed, read with no
    error where pyarrow ("Unexpected end of stream") and DuckDB ("Invalid data")
    refuse it. This reproduces on master with a clean forged file: a PLAIN page of 8
    INT64 claiming 64 bytes and holding none. Its odd footer is walked now because the
    walk decodes and reads lists as Thrift does, so the fuzzer reached it here. Step
    520's codecs are Snappy, gzip, Brotli and Zstandard, not UNCOMPRESSED, so this
    stays open after step 520.
  - `FuzzOwnPagesMatchArrow`, `bc1dbd675896f2b2`, 2,276 bytes, a mutation of
    `pyarrow_null_column.parquet`: ErrInternal, "parquet: column "nothing" of type Null
    has no level reader" (`colreader.go`, `newColReader`). It reproduces on master and
    on master with step 520.

  Both still failed on this branch after the merge. A third run, on the merged
  branch, found them again within a second (`a674bf96f2ddbbd7` and
  `b8b1debba7d84dcd`). Since CI's 30 s runs, which are coverage-guided, would find
  them on this pull request, both are fixed here (§4a).

## 4a. The two older bugs, fixed

- **An UNCOMPRESSED page whose two sizes differ is refused.** An UNCOMPRESSED page
  holds its bytes as they lie, so its compressed size and its uncompressed size are
  one, as every writer writes them, and a page's declared size must match its bytes.
  The walk of a chunk's page headers that step 510 made (`countDictionaries`, run as
  arrow-go reads the chunk) now refuses such a page in an UNCOMPRESSED chunk:
  "corrupt: an uncompressed page of %d bytes says %d". This follows DuckDB 1.5.5,
  which refuses every such page ("Page size mismatch"), and in each case tried it is
  at least as strict as pyarrow 25.0.1. pyarrow refuses a page holding none of its
  bytes ("Unexpected end of stream") and reads a page that claims 8 bytes more or
  fewer than it holds. The same probe was run against step 520's
  branch over 139 inputs, now with its codec fixtures (`testdata/parquet/codecs`).
  All 156 reads were identical. Step 510's forged pages that claim more than their
  chunk are now refused by this check first, with a different message.
- **A Null column with no reader is refused, not an internal error.** arrow-go makes
  no column reader for a physical type it does not know, and only a corrupt footer
  gives a column one. The mutated `pyarrow_null_column.parquet` gave one to its Null
  column. `newColReader` refused a column of any other type with no reader as
  unsupported, but a Null column as ErrInternal, "has no level reader". It now
  refuses a Null column with the same error as any other column.

Tests, failing first:
- **`TestAnUncompressedPageWhoseSizesDifferIsRefused`** (readfuzz, public API). It
  reads the fuzzer's input, then each page of each kind's three-page file. Each page
  is made to claim 1 and 8 bytes more than it holds, 1 fewer, and to hold none of its
  bytes, and every variant must be refused. All reads go into poisoned buffers, on 1
  and on 4 threads, and the whole file must still read.
- **`TestAColumnWithNoReaderIsRefusedWhateverItsType`** (internal). A Null, a String
  and a Float64 column with no reader must each be refused with an Int32 column's
  error, their own type named.

## 4b. Step 519's three inputs

Each was replayed under the cap on master 65fc8b2 and on this branch. To find which
fix covers each, the branch was also replayed with each fix broken in turn:

| input | master | this branch | fails again with | so it is |
| --- | --- | --- | --- | --- |
| `FuzzScanParquet-1b90642d769d0719`, 93 bytes | "row 3 differs" | PASS | the UNCOMPRESSED check off (§1's fix off: still PASS) | §4a's |
| `ci-440e8e5e13688644`, 77 bytes, `FuzzScanParquet` | fails | PASS | the bound back to the file less 12 | §2's |
| `ci-c061c2feb23064c9`, 78 bytes, `FuzzOwnPagesMatchArrow` | fails | PASS | the bound back to the file less 12 | §2's |

- **`1b90642d769d0719` does not share §1's cause.** Its one page is UNCOMPRESSED and
  claims 3,072 bytes uncompressed but 24 compressed. No `io.EOF` is involved: the page
  reader read all 24 bytes it was asked for. The check of §4a covers it. pyarrow
  refuses it ("Unexpected end of stream"), and so does DuckDB ("TProtocolException:
  Invalid data").
- **The two footers leave no room for the leading magic.** Each is 67 bytes, one 2
  bytes and one 3 bytes into its file, its row groups claiming more than its bytes
  hold. Both are §2.2's shape.

## 4c. A dictionary index of 0 bits followed by a byte

The fourth fuzz run, on the final code, ran `FuzzScanParquet` for its 20 s clean,
774,154 executions. `FuzzOwnPagesMatchArrow` found `0ed62e1f827ad456` after 9 s.

- **The file.** Its BYTE_ARRAY column's data page holds indices 0 bits wide, an RLE
  run followed by a stray byte. pyarrow 25.0.1 reads each index as 0. DuckDB 1.5.5
  refuses the file's footer.
- **Why it reached the readers only now.** On master and on step 520's branch, the
  walk refused the footer ("not Thrift's compact protocol"). This branch's walk reads
  it as Thrift does, so both readers now read the page.
- **The rule.** An index of 0 bits is 0, and an RLE run of them stores its value in
  no byte. A byte after a run's header is then a stray one, or the header of a run
  after it, never the run's value.
- **The fix.** In `checkedPages`, `zeroWidthMisread` walks a 0-bit page's runs as
  `pastIndex` does, and refuses the page where a byte other than 0 follows an RLE
  run's header. A page of one run with nothing after it still reads. The reader's own
  pages are unchanged. The well-formed files' probe (§3, §4a) gave the same 156
  reads.
- **Test: `TestADictionaryIndexOfZeroBitsReadsAsIndexZero`.** It covers each kind and
  its own-pages variant. The indices are one run of 0 bits, alone, then with a stray
  0, 1 or 255 after it, then two runs, then two runs and a stray 1. Each must read as
  the PLAIN page of the dictionary's first value, which is the differential, or,
  where `checkedPages` checks the chunk, be refused.
- **Tooth.** With the check off, the test failed in all 7 kinds `checkedPages`
  checks, and so did the fuzzer's input.

## 4d. The fifth run

The fifth run, on the final code with origin/master merged in, found one input in
each target. Both were replayed under the cap.

- **A page header that does not parse, fixed.** `FuzzScanParquet`,
  `0ffc32c5ae40d70c`.
  - On master it passes, because the walk refuses its footer. On step 520's branch it
    passes too.
  - The file's dictionary page header does not parse, and the page reader looked for
    it past its chunk.
  - **Fix.** A header lies inside its chunk, so `boundHeaders` lowers each page
    reader's largest header to its chunk's bytes, and to no less than the first look
    of 16 KiB, through `PageReader.SetMaxPageHeaderSize`: a look past the chunk's
    bytes finds nothing more. It covers `checkedColumn` and the own pages' page
    reader.
  - pyarrow refuses the file ("Dictionary page must be before data page"), and so does
    DuckDB ("Invalid data").
  - The well-formed files' probe gave the same 156 reads.
  - **Tooth.** With the bound off, the input failed again.
- **A DELTA_BINARY_PACKED block of 31 values, left open.** `FuzzOwnPagesMatchArrow`,
  `3194fa9b880f15d1`, on master as well.
  - Its INT64 chunk's footer lists DELTA_BINARY_PACKED, so arrow-go's column reader
    decodes it. The data page's DELTA_BINARY_PACKED header gives a block of 31
    values, where the format requires a multiple of 128.
  - pyarrow refuses the page ("the number of values in a block must be multiple of
    128, but it's 31"), and so does DuckDB ("invalid block sizes for
    DELTA_BINARY_PACKED").
  - A fix checks a DELTA page's header in `checkedPages` before the page is decoded,
    as step 517 checks a dictionary page's indices; step 531 builds it.

## 4e. The sixth run, and PR #72's CI input

This run was on the final code, after §4d's fix. Its `FuzzOwnPagesMatchArrow` ran the
full 20 s clean, 672,694 executions. Its `FuzzScanParquet` found `b56a73f3595cbd2a`
after 6 s.

- **A row group of too few chunks, fixed.** The footer's one row group lists no
  column chunk, and its schema has one column. The read failed as ursus's own bug:
  "column "" maps to leaf 0, and the row group has 0" (`checkEncodings`). This happens
  on master too.
  - A footer whose row group lists more chunks than the schema has columns had
    `checkRowCounts` ask the schema for a column it does not have.
  - pyarrow 25.0.1 refuses the file ("The file only has 0 columns"), and DuckDB 1.5.5
    refuses its footer.
  - **Fix.** `checkRowCounts` refuses a row group whose count of chunks is not the
    schema's count of columns, as corrupt. Both the read and `Count` take that
    refusal.
  - **`TestARowGroupMissingAColumnChunkIsRefused`** (readfuzz, public API) reads the
    input on 1 and 4 threads and runs `Count`; each must be an I/O error, not an
    internal one.
  - **`TestARowGroupOfAnotherCountOfChunksIsRefused`** (internal) takes `everyList`
    without its chunks' crypto metadata, which passes. It gives the row group 0, 1 and
    3 chunks for 2 columns, and each must be refused, without a panic.
  - **Tooth.** With the check off, both tests failed, the 3-chunk case with a panic,
    and so did the fuzzer's input.
  - The well-formed files' probe, run against current master (de826b9, step 519
    merged), gave the same 156 reads.
- **PR #72's input, `a0ab8b455da4b005`, is §2's, and is covered.** It is 34 bytes.
  As CI saved it, it ends "PARP": the minimiser's last candidate, which the minimiser
  had changed. Every version refuses it at once, as step 517 found with "PARA".
  - With its last byte "1" again, its footer is 25 bytes, begins 1 byte into the
    file, and claims more row groups than its bytes hold: §2.2's shape. On master
    de826b9, which has step 520's reader, it failed. On this branch it passes.
  - With §2's bound back to the file less 12, it failed again.

## 5. The teeth

Each fix was broken in the tree, the step's tests were run under the cap, and the file
was restored from a copy saved before (`cmp` clean each time). This was done before the
merge, and again after it on step 520's reader with the same failures; the figures
below are from before:

| broken | failed |
| --- | --- |
| `batchErr` returns the reader's error as is | §1's input ("Count is 5, and the file reads as 0 rows"); `TestAPageWhoseBytesAreMissingIsRefused`, §1's input and the 7 kinds arrow-go's column reader decodes |
| `pageChunk.nextPage` wraps `Err()` as is | `TestAPageWhoseBytesAreMissingIsRefused`, the 3 own-pages kinds |
| `measureFooter` back to the file less 12 | §2's input; `FuzzScanParquet` on the offset-0 seed; `TestAFooterListClaimingMoreThanItsBytesIsRefused`; `TestMeasureFooterWalksEveryFooterArrowGoReads` |
| list elements walked by the header's type | `TestFooterWalkReadsAListAsArrowGoDoes`, all 28 cases; `FuzzScanParquet` on `pqHiddenGroups`; `TestAFooterListClaimingMoreThanItsBytesIsRefused`; `TestFooterWalkDifferential`, 8,193 lists said to hold another type that arrow-go reads and the walk refused |
| a list's count against the whole footer, not the bytes after it | `TestFooterWalkDifferential`, 2,904 lists claiming a byte past theirs, refused only by running out of bytes, not at the header |
| the UNCOMPRESSED page check off (§4a) | `TestAnUncompressedPageWhoseSizesDifferIsRefused`, the fuzzer's input and all 10 kinds; `FuzzScanParquet` on the fuzzer's input |
| the header bound off (§4d) | `FuzzScanParquet` on the fuzzer's input |
| the chunk count check off (§4e) | `TestARowGroupMissingAColumnChunkIsRefused`; `TestARowGroupOfAnotherCountOfChunksIsRefused`, 0 and 1 chunks and a panic at 3; `FuzzScanParquet` on the fuzzer's input |
| the zero-width check off (§4c) | `TestADictionaryIndexOfZeroBitsReadsAsIndexZero`, the 7 kinds `checkedPages` checks; `FuzzOwnPagesMatchArrow` on the fuzzer's input |
| a Null column with no reader an internal error again (§4a) | `TestAColumnWithNoReaderIsRefusedWhateverItsType`; `FuzzOwnPagesMatchArrow` on the fuzzer's input |

## 6. Checks

All were run after the last edit, on the branch merged with master de826b9 (steps 519
and 520).

- **The seventh fuzz run**, under the lock and the cap: `FuzzScanParquet` ran its 20 s
  clean, 864,992 executions, and `FuzzOwnPagesMatchArrow` ran its 20 s clean, 636,571.
- `go test ./internal/...`: ok, 29 packages.
- `go test .` in full, under the lock: ok, 178.5 s.
- `GOEXPERIMENT=simd go vet ./...`: clean.
- `go run ./internal/gen/levels`: ok.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added, 0
  gone since v0.7.2.
- `go run ./internal/gen/testinventory -check`: ok, 1,225 lines, the baseline's.
- `$(go env GOROOT)/bin/gofmt -l .`: printed nothing.

## 7. Left

- **This branch holds step 520's branch, merged in**, so #73 lands with it or before
  it. The conflicts were in `footer.go`'s `thriftReader`. They were resolved by
  keeping step 520's decoding (`varint`, `varint32`, `i32`, a stop at any type of 0,
  sizes cut to 32 bits) and adding this step's walk, its counts against the bytes
  left, and `onList`. `pqForgedSeeds` holds both steps' seeds.
- **A DELTA_BINARY_PACKED page whose block size is not a multiple of 128** (§4d),
  which pyarrow and DuckDB refuse, is not checked yet; left to step 531.
- **A V2 page in a compressed chunk that is marked not compressed** (`is_compressed`
  false) is outside §4a's check, which looks only at UNCOMPRESSED chunks: its declared
  size is not held to its bytes.
