# Step 531 — as built

**A malformed DELTA_BINARY_PACKED page is refused, not hung on.** F18 of
[`step-521-as-built.md`](./step-521-as-built.md), left open there, is fixed. A
DELTA_BINARY_PACKED page whose header counted 0 values, in a REQUIRED column's page of
5, hung arrow-go's column reader forever, on master too. Each delta-packed run of a
DELTA_BINARY_PACKED, DELTA_LENGTH_BYTE_ARRAY or DELTA_BYTE_ARRAY data page is now
checked before arrow-go decodes it, in step 517's `checkedPages`, and a run the format
does not allow is refused as corrupt, an I/O error. On the way, two runs the format
allows were found to make arrow-go allocate gigabytes; they are bounded too (§2.3).
Every delta page pyarrow and arrow-go write reads as before.

## 1. The finding, and where arrow-go loops

The input is `~/ursus-scratch-archive/step521-findings/FuzzOwnPagesMatchArrow-3194fa9b880f15d1-delta-page-hang`,
425 bytes, kept now as the seed `FuzzOwnPagesMatchArrow/delta-page-hang`. Replayed on
this branch's base under `prlimit --as=4294967296` and `timeout`, fuzzcheck called it
a hang after 10 s. Every stack ends in the same loop:

```
parquet/file.(*columnChunkReader).readBatchInPage   column_reader.go:764
parquet/file.(*columnChunkReader).readBatch          column_reader.go:747
parquet/file.(*Int64ColumnChunkReader).ReadBatch     column_reader_types.gen.go:93
```

The file has one REQUIRED INT64 column whose footer lists DELTA_BINARY_PACKED, so
arrow-go's column reader decodes it, not the reader's own pages (`ownable`). Its data
page holds 5 values. Its body is 6 bytes: block size 31, 10 miniblocks, a count of 0
values, first value 0, and two bytes more.

arrow-go v18.7.0's `deltaBitPackDecoder.SetData` takes the header's count for its
values (`d.nvals = int(d.totalValues)`). `Decode` answers `min(len(out), nvals)`, which
is 0, with no error. `columnChunkReader.readBatch` loops
`for totalLvls < batchSize && c.HasNext() && err == nil`. A REQUIRED column has no
definition levels, so each pass reads 0 levels and 0 values, and the page never ends.
**The hang is the count, not the block of 31** that step 521's record named. The block
of 31 is wrong too, and it is what pyarrow 25.0.1 names first ("the number of values in
a block must be multiple of 128, but it's 31"). A block of 128 with a count of 0 hangs
the same (§3, the forged files).

arrow-go's decoder checks nothing else of a run either. Each forged run of §3 was read
with the check switched off, each file in its own process under the cap and a 6-second
timeout. This table is over the nine runs forged: one in each of seven kinds of chunk,
and both runs of the eighth, a DELTA_BYTE_ARRAY page.

| forgery | arrow-go without the check |
| --- | --- |
| a count of 0, a count one short | hang in 3 (REQUIRED INT64, INT32, a Struct's field); other values, no error, in 2 (OPTIONAL INT64, a List's element); a recovered panic in 4 (byte arrays) |
| a block of 2^32 values | fatal "out of memory" in 9 of 9: 4 or 8 GiB, a slice of a miniblock's 2^30 values (`unpackNextMini`) |
| a DELTA_LENGTH_BYTE_ARRAY or DELTA_BYTE_ARRAY count of 2^40 | fatal "out of memory" in 4: a slice of 4 TiB of lengths, made at `SetData` before one is decoded |
| a miniblock 65 bits wide | other values, no error, in 2 (DELTA_LENGTH_BYTE_ARRAY: its `SetData` drops the error of the run of lengths and reads the lengths it did not get as 0); an error or a recovered panic in 7 |
| the page ending inside a miniblock's values | other values, no error, in 5: arrow-go's bit reader reads past a page's end as 0s; a recovered panic in 4 |
| the page ending inside a miniblock's padding | read as the PLAIN twin in 5 (pyarrow refuses all 9) |
| a block of 31, 3 or 256 miniblocks to a block of 128 | a recovered panic: a block runs past its bit widths |
| a block of 0, no miniblock, a page ending inside a block's bit widths | an error, or a recovered panic |
| blocks of 64 in 2 miniblocks, 8 miniblocks of 16 | read as the PLAIN twin: arrow-go decodes a layout the format does not allow |

The two runs of §2.3, which the format allows, were probed the same way on a build
without their bounds. One miniblock of 2^31 values 0 bits wide was a fatal "out of
memory" in 9 of 9 (16 GiB for an INT64). A page claiming 2^26 values past its chunk's,
in one such miniblock, allocated 256 or 512 MiB in 6 of 9 and read other values.

## 2. The design

### 2.1 Where the check goes

Step 517's `checkedPages` is a page reader around arrow-go's, made for every chunk
arrow-go decodes (`checkedColumn`). Its `Next` sees each page decompressed, as arrow-go
will decode it, before arrow-go's column reader takes it in, and refuses one by
answering false with the reason in `Err`. A delta page is checked there now
(`deltapage.go`), beside the dictionary page's indices. The reader's own pages decode
no delta page: `ownable` gives a chunk listing any encoding but PLAIN, RLE and the
dictionary's to arrow-go. So nothing is added to them.

The levels are taken apart once, as the dictionary check took them apart, by
`checkedPages.levels`, and the values that are not null counted by `present`
(`presentValues`, step 517). Both checks share them now; `check`'s behaviour is
unchanged. `checkedPages` also keeps the column's physical type, which picks arrow-go's
decoder, and its chunk's values from the footer (§2.3).

### 2.2 The rules

A run is DELTA_BINARY_PACKED's layout. It is a header (block size, miniblocks a block,
count, first value), then blocks. Each block is a least delta, a bit width a
miniblock, and the miniblocks holding values, each padded to its full count. A
DELTA_LENGTH_BYTE_ARRAY page packs its lengths so, and a DELTA_BYTE_ARRAY page its
prefixes' lengths, then its suffixes' as a DELTA_LENGTH_BYTE_ARRAY. `deltaRun` reads a
run as arrow-go's decoder reads it and refuses the page where:

- the block size is not a positive multiple of 128 that 32 bits hold (arrow-go keeps a
  block's count in a `uint32`, and pyarrow reads the header's fields as 32 bits);
- the miniblocks do not divide a block into a multiple of 32 values each;
- the count is more than the page's values or its column chunk's, or fewer than its
  values that are not null;
- a miniblock holds more values than the count and than 65,536 (§2.3);
- a miniblock holding a value is wider than 64 bits;
- the page ends inside a block's least delta or its bit widths, or inside a miniblock
  holding a value, its padding counted.

"As arrow-go reads it" decides which blocks are read. A DELTA_BINARY_PACKED page's
values are decoded as the column reader asks for them, the page's values that are not
null, and only their blocks are looked at. A run of lengths is decoded whole at
`SetData`, the header's count of them. The suffixes' run begins where arrow-go's
decoder of the prefixes stopped, at the end of their last miniblock, padding and all. A
header that does not parse is left to arrow-go, which refuses the page.

What is not refused:

- **A miniblock holding no value**, in the last block, may give any bit width; the
  format says readers must accept it, and its bytes are absent. It is not looked at.
- **An INT32's miniblock up to 64 bits wide** is read. arrow-go's writer takes an
  INT32's deltas as an int64's (`deltaBitPackEncoder.Put`). Where they span more than
  32 bits it writes miniblocks 33 or 34 bits wide, and arrow-go reads them back.
  pyarrow fails such a page ("Unexpected end of stream"). The brief's bound is 64.
- **A count past the values that are not null**, within the page's, is read. Its extra
  values are decoded from the padding and not used.

The padding is required because pyarrow requires it: it refuses a run cut inside the
last miniblock's padding, and arrow-go's run of prefix lengths ends there. arrow-go
alone reads the cut page as its PLAIN twin (§1).

### 2.3 Two runs the format allows, bounded

Writing the forgeries found two allocations that no rule above stops:

- **A huge miniblock 0 bits wide.** arrow-go makes a slice of a miniblock's values for
  the first miniblock it decodes (`make([]T, 0, valsPerMini)`), and decodes every value
  of each miniblock, which a width of 0 holds in no byte. A page of 200 values in one
  miniblock of 2^31 asked for 16 GiB. The format sets no bound on a block. arrow-go and
  pyarrow write 32 values a miniblock (a block of 128 in four), as parquet-mr does by
  default. So a miniblock of more values than both its run's count and 65,536
  (`maxMiniblock`) is refused. Below that, the slice is at most 512 KiB, or a slice of
  the count.
- **A page claiming more values than its column chunk.** A page's values are its
  header's claim. arrow-go's page reader takes a page claiming more than its chunk's
  values (`serializedPageReader.Next` adds them to `rowsSeen`), and a
  DELTA_LENGTH_BYTE_ARRAY's slice of lengths is the count's size. So the count is
  bounded by the chunk's values too, which the footer gives and `checkRowCounts` (step
  416) holds to its row group's rows.

Each allocation of the decoder is then within what the footer declares, or 512 KiB.
pyarrow reads both runs: it decodes values into its output, and it reads only as many
values as the levels ask for.

### 2.4 The error

`corrupt: column v: a DELTA_BINARY_PACKED page's values: blocks of 31 values, where the
format takes a positive multiple of 128`, as `checkedPages`' errors read. The colreader
wraps it as `ErrIO` ("reading a column chunk"). A DELTA_BYTE_ARRAY page says "prefix
lengths" or "suffix lengths", and a DELTA_LENGTH_BYTE_ARRAY page "lengths".

The cost is one pass over a page's definition levels, which `presentValues` counts as
the dictionary check does, and over its runs' headers: a varint and a byte a
miniblock, each miniblock skipped whole. No value is decoded. The levels' pass was
most of it, so `packedCount` now counts levels of 1 bit, an OPTIONAL column's, by
their bits set, eight bytes a load, which the dictionary check gains too;
`TestPackedCountOfOneBitCountsEachBit` holds it to a brute force (§4).

## 3. The tests and their oracles

- **The seed**, `FuzzOwnPagesMatchArrow/delta-page-hang`, F18's input. Without the
  check it hangs (§6).
- **`TestAMalformedDeltaPageIsRefusedNotHungOn`** (readfuzz, public API). Nine kinds of
  chunk arrow-go decodes a delta page of (`pqDeltaKinds` in `forge_test.go`): INT64
  REQUIRED and OPTIONAL with a null in seven rows; INT32, and an INT32 whose deltas
  span more than 32 bits, honest only; the INT64 element of a List; the INT64 field of
  a Struct; a BYTE_ARRAY under DELTA_LENGTH_BYTE_ARRAY, REQUIRED and OPTIONAL; and one
  under DELTA_BYTE_ARRAY. Each has a page of 200 rows. A REQUIRED kind's run is 199
  deltas, in a block of 128 and one of 71, whose last miniblock holds 7 values, and a
  miniblock after it holds none.
  It gets 23 forgeries (`pqDeltaForgeries`), applied to each run, so to both of
  DELTA_BYTE_ARRAY's. That is 208 files. The forgeries:
  - three honest layouts;
  - an empty miniblock 255 bits wide;
  - F18's block of 31, and blocks of 0 and of 2^32;
  - blocks of 64 in 2 miniblocks, and 8 miniblocks of 16 values;
  - no miniblock, and 3 or 256 to a block of 128;
  - a miniblock 65 bits wide;
  - counts of 0, one short, one past the values, one past the page's, and 2^40;
  - the page ending inside a block's bit widths, inside a miniblock's values, and
    inside its padding;
  - one miniblock of 2^31 values 0 bits wide;
  - a page claiming 2^26 values past its chunk's.

  Each runs under `fuzzcheck.Run` with readfuzz's limits, ten seconds and 64 MiB. A
  forgery the format does not allow must be an `ErrIO` naming the page's encoding as
  corrupt. One it allows must read as the kind's PLAIN page of the same values, the
  differential. Its encoder, `pqDeltaOf`, packs as the format lays a run out;
  `TestTheDeltaForgeriesAreWhatTheySay` guards what the forgeries assume of it (the last
  miniblock not full, its bits not ending on a byte, an empty miniblock after it).
- **`TestDeltaPagesAgainstPyarrow`** (readfuzz). The same 208 files read by pyarrow
  25.0.1, recorded once in `testdata/oracle/delta/verdicts.json` by
  `bench/scripts/oracle/record_delta.py`. The Go test writes the files for it under
  `URSUS_DELTA_FORGERIES=<dir>` and checks each file's SHA-256 against the record. The
  verdict is S (read as its twin), D (read as other values) or E (refused). Where
  pyarrow refuses, the reader must refuse; where pyarrow reads the twin, the reader
  must too; the reader must never answer D. The outcomes, (reader, pyarrow):

  | EE | SS | ES | ED | SE |
  | --- | --- | --- | --- | --- |
  | 140 | 38 | 16 | 13 | 1 |

  The 30 that differ are three known disagreements (`deltaDisagreement`), each a rule
  above:
  - **ES 16**: a count past the page's values, which pyarrow reads;
  - **ED 13**: §2.3's two runs, which pyarrow reads, as the forged run's values;
  - **SE 1**: the INT32 packed 33 bits wide.

  pyarrow refuses everything else the reader refuses, F18's block of 31 among them, and
  the padding cut.
- **`TestPyarrowDeltaFilesReadAsTheirPlainTwin`** (readfuzz). pyarrow 25.0.1's delta
  pages, `testdata/parquet/delta/` from `bench/scripts/oracle/write_delta.py`. One table
  of 500 rows: INT64 and INT32 spanning their types' ranges, with nulls and without, a
  date, a timestamp and a UInt32 under DELTA_BINARY_PACKED; strings and binaries under
  DELTA_LENGTH_BYTE_ARRAY and DELTA_BYTE_ARRAY; a List and a Struct. V1 pages
  uncompressed and V2 under Zstandard, row groups of 200, pages of 256 bytes. Each must
  read as its PLAIN twin on 1 and 4 threads, and every column must list a delta
  encoding.
- **`TestArrowGoDeltaPagesReadAsTheirPlainTwin`** (internal). `writePagesFixture`'s
  columns, 4,000 rows written by arrow-go's writer, which ursus's writer is made of,
  every integer column under DELTA_BINARY_PACKED and its strings and binaries under the
  two others. V1 and V2, small pages, Zstandard, four null patterns. Each is read batch
  for batch as the same rows written PLAIN. Its `u32` column spans INT32's range, so
  arrow-go packs it 33 bits wide, which the test requires to happen.
  `checkDeltaRead` requires each delta column to be read through `checkedPages`.
- **`TestCheckedPagesRefuseMalformedDeltaRuns`** (internal, step 517's pattern).
  3,000 generated pages, 600 under the race detector, seed logged: INT64 and INT32
  columns REQUIRED, OPTIONAL and in a List under DELTA_BINARY_PACKED, and BYTE_ARRAY
  ones under the two others. V1 and V2, levels RLE or BIT_PACKED. Blocks of 128 to
  1,024 in miniblocks of 32 to 1,024, or one miniblock of 65,536; deltas 0 to 64 bits.
  A third of the pages have empty miniblocks of any width. Most are forged once:
  - the block size or the miniblocks wrong, a layout arrow-go decodes and the format
    does not allow, a miniblock 65 bits wide or more, or a huge miniblock;
  - a count short, or past the page's or the chunk's;
  - a count past the values within the padding;
  - the page cut inside the blocks or the header.

  Each page is read through arrow-go's column reader, inside `fuzzcheck.Run`'s wall
  bound. `checkedPages` must refuse exactly the pages the generator broke where the
  check looks. Where it does not refuse, the read must answer what arrow-go's own read
  answers: values, levels and error. Where the page is honest, or forged only where
  the format allows, it must answer the generator's values and levels too. Each
  forgery must make at least 20 pages; a run makes 100 to 470 of each.
- **Seeds.** `FuzzScanParquet` starts from each kind's honest page and its count of 0
  (`pqForgedSeeds`). `FuzzOwnPagesMatchArrow` gains F18's input and six `forged-delta-*`
  pages.
- **Polars** writes no delta page. Polars 2.1.0's `write_parquet` takes no encoding,
  and wrote PLAIN and dictionaries for Int64, Int32 and String columns, sorted or not.
  So its files are not delta files here.

## 4. The overhead

Three files of 1,000,000 rows in four row groups, written by arrow-go's writer under a
delta encoding: an INT64 REQUIRED (1.4 MB), an INT64 OPTIONAL with a null in ten rows
(2.5 MB), and an OPTIONAL String under DELTA_LENGTH_BYTE_ARRAY (4.5 MB). Each was read
whole through the Source on one thread, and its data pages were put through
`checkDelta` alone, as arrow-go hands them over. The figure is the least of 41 runs
each, under the shared lock, three rounds. The machine was shared and loaded (load 13
on 8 cores from other users' jobs), so a whole read varied from 24 to 60 ms. The check
alone is the steadier figure:

| file | the check, before the popcount | the check, after | share of the read, after |
| --- | --- | --- | --- |
| INT64 REQUIRED | 0.17–0.20 ms | 0.18–0.21 ms | 0.6–0.8% |
| INT64 OPTIONAL | 2.23–2.32 ms | 0.78–1.34 ms | 2.0–2.7% |
| String OPTIONAL, DELTA_LENGTH_BYTE_ARRAY | 2.02–3.70 ms | 0.62–0.67 ms | 2.0–2.3% |

A REQUIRED column's check reads only the runs' headers. An OPTIONAL column's also
counts its definition levels; before `packedCount` counted 1-bit levels by popcount,
that cost 4.9 to 9.3% of a read. A first measurement, whole reads with the check off
and on interleaved, could not separate the check from the machine's noise: the two
medians crossed from round to round.

## 5. The fuzz runs

Each target ran alone, under the shared lock, with these settings:

- `prlimit --as=4294967296`;
- `GOMAXPROCS=4`, the binaries built with `GOFLAGS=-p=1 GOEXPERIMENT=simd GOAMD64=v3`;
- `-test.fuzztime=20s -test.fuzzminimizetime=3s`.

| run, code | `FuzzOwnPagesMatchArrow` | `FuzzScanParquet` |
| --- | --- | --- |
| 1, the check without §2.3's bounds | 343,723 execs, PASS | 206,347 execs, PASS |
| 2, with them | 254,654 execs, PASS | `4c22d5fafe9eafd7` after 12 s, F20 (§7) |
| 3, final | 220,937 execs, PASS | 224,279 execs, PASS |

F20 was replayed under the cap. It is not a delta page: arrow-go allocates while
it builds the footer's schema, before any page is read, on a path this step does not
touch.

## 6. The teeth

Each mutation was made from a saved copy of `deltapage.go`, built, and the file
restored and compared with `cmp`, never by git. Six tests were run against each
mutant: the forged files (F), the pyarrow differential (P), pyarrow's files (Y), the
generated pages (G), arrow-go's files (A) and the seed (S).

| mutation | fails | how |
| --- | --- | --- |
| the check off (`deltaEncoded` false) | F, P, G, S | F and P: fatal "out of memory", 8 and 4 GiB; G: fatal "out of memory", 11 GB; S: "hang: no answer after 10s" |
| no multiple of 128 | F, P, G | F: "blocks of 64 in 2 miniblocks: read as 200 rows, and no error"; P: "the reader answers S, pyarrow E (the number of values in a block must be multiple of 128…)"; G: a "block" page, "checkedPages answered <nil>" |
| no multiple of 32 a miniblock | F, P, G | F: "8 miniblocks of 16 values: read as 200 rows"; P: "the reader answers S, pyarrow E (the number of values in a miniblock must be multiple of 32…)"; G: a "layout" page not refused |
| a count short of the values passes | F, P, G | each: "hang: no answer after 10s" |
| empty miniblocks' widths checked | F, P, G | F: "honest, a miniblock holding no value 255 bits wide" refused; P: "the reader answers E, pyarrow S"; G: "spare widths" pages refused |
| widths at most 32 | F, P, Y, G, A | every honest page with a wide miniblock refused, pyarrow's and arrow-go's files among them |
| the count exactly the values | F, P, G | F: "a count one past the values" refused; P: "the reader answers E, pyarrow S"; G: "count past the values" pages refused |
| the padding not required | F, P, Y, G | F: "the page ending inside a miniblock's padding: read as 200 rows"; P: "the reader answers S, pyarrow E (Unexpected end of stream)"; Y: pyarrow's DELTA_BYTE_ARRAY pages refused, the suffixes' run misplaced; G: a "cut" page not refused |
| no chunk bound | F, P, G | F and P: "runaway allocation: 512 MiB and counting", fuzzcheck; G: a "chunk" page not refused |
| no miniblock bound | F, P, G | F: fatal "out of memory", 16 GiB; P: 8 GiB; G: a "huge miniblock" page not refused |

| `packedCount`'s popcount dropping the last byte's bits | the brute force, G | `TestPackedCountOfOneBitCountsEachBit` fails; G: "count short" pages not refused, their values not null undercounted |

The mutants of `deltapage.go` were built from its final text; the popcount (§2.4),
which came after, is in `dictindex.go` and was mutated on its own.

Y and A refuse nothing a writer wrote, so they fail only when the check is too strict.
They pass under every mutant that only loosens it. They fail under the two that
tighten it wrongly, where pyarrow's or arrow-go's own pages carry what is tightened.

## 7. Observations and what is left

- **F20, a new finding, left: a schema element's `num_children` sizes a slice before
  it is checked.** The second `FuzzScanParquet` run found `4c22d5fafe9eafd7` after
  12 s: "runaway allocation: 270,831,592 bytes (258.3 MiB)", a footer of 125 bytes.
  Its root `SchemaElement` claims some 17 million children, in a 6-byte varint, and
  arrow-go's `schema.FromParquet` runs `make([]Node, 0, elem.GetNumChildren())` before
  it finds there are not that many elements. The footer walk (steps 423 and 521) does
  not hold `num_children` to the elements after it. No page is read, so it is not this
  step's; the reading path is master's, untouched here. pyarrow 25.0.1 refuses the file
  ("Malformed schema: not enough elements"). A fix would refuse, in the walk, a
  schema element claiming more children than elements follow it. The input is kept in
  `~/ursus-scratch-archive/step531-findings/`, not as a seed, so CI's fuzz workflow,
  which is advisory, may report it.
- **arrow-go's writer packs an INT32's deltas up to 34 bits wide.** Its encoder takes
  the deltas as an int64's. pyarrow 25.0.1 fails such a page ("Unexpected end of
  stream: InitBlock EOF"). ursus's writer writes no delta page, so no ursus file is
  affected; it is arrow-go's to fix.
- **A page's count of values is trusted up to its chunk's.** A footer declaring a
  chunk of 2^31 values, with a page of as many in one miniblock of 65,536 values 0 bits
  wide, still costs arrow-go a slice of 2^31 lengths. That is a file declaring 2^31
  rows, a question of capacity, as a row count is anywhere.
- **pyarrow and the reader part on three runs** (§3), each refused here for arrow-go's
  sake: a count past the page's values, a miniblock past 65,536 values and its count,
  and a page claiming more values than its chunk. No writer writes them.

## 8. Checks

After the last edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:

- `go test ./internal/...`: ok. `./internal/source/parquet` and `./internal/readfuzz`
  were run again after the last test edit, which only trims the race detector's runs:
  ok.
- `go test .` in full, under the lock: ok (584 s).
- `cd qcheck && go vet ./... && go test ./...`: ok.
- `GOEXPERIMENT=simd go vet ./...`: clean.
- `go run ./internal/gen/levels`: ok. No new package.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2.
- `go run ./internal/gen/testinventory -check`: ok, 1,223 lines against a baseline of
  1,223. A typed column count in the first draft, which the ratchet caught, became
  `checkDeltaRead`'s count of the columns written delta-encoded.
- `$(go env GOROOT)/bin/gofmt -l .`: printed nothing.
