# Step 520 — as built

**A Parquet page decompresses to no more than it declares.** The item step 517 left
(§7 of [`step-517-as-built.md`](./step-517-as-built.md)) is fixed: ursus replaces four
codecs of arrow-go's compress registry with codecs that decode a page into the buffer
its header declares and refuse more as corrupt, KindIO, before allocating it. The
registry is the process's, so each replacement refuses only what its stream holds past
its buffer (§3). This step's own fuzz run found a dictionary page's count that step
510's walk read otherwise than Thrift, and it is fixed too (§4).

## 1. The rule

A page's header declares its uncompressed size, a claim, which `boundPages` bounds by
the file's largest column chunk (D14, step 510). A codec's own header is a second
claim: a Zstandard frame's content size, a Snappy block's length. A stream with no
such header, a Zstandard frame without a content size, gzip or Brotli, says nothing
of its size until it is decoded, and a Brotli stream declares a window, a ring buffer
its decoder makes. Each is held to the page now: a page decodes into the buffer its
header declares, and a stream that holds more is refused as soon as its codec can
tell (§2). A page declaring 0 bytes decodes into an empty buffer.

LZ4_RAW decodes into its buffer and no further (pierrec's `UncompressBlock`);
arrow-go reads neither Hadoop's LZ4 nor LZO. Both the reader's own pages and
arrow-go's column reader take their pages from the same page reader, so the rule holds
for every chunk.

## 2. The design (`internal/source/parquet/codecs.go`)

`boundedCodec` wraps the codec registered for Snappy, gzip, Brotli and Zstandard
(`boundedStreaming` for one that streams, as all four of arrow-go's do). Encoding, its
bound, a streaming reader or writer are the wrapped codec's. Decoding into a buffer is
its own:

- **Zstandard:** klauspost's decoder made once with `WithDecodeAllCapLimit(true)`, the
  same concurrency as arrow-go's, decoding into `dst[:0]`: a frame's content size past
  the capacity is refused before anything is allocated, a frame without one once a
  block takes its output past it. `ErrDecoderSizeExceeded` is the refusal.
- **Snappy:** `DecodedLen` against `cap(dst)` first, then klauspost's `Decode` into
  it.
- **gzip:** klauspost's reader into `dst` (`io.ReadFull`, a short stream an error, as
  arrow-go's), then one byte more refuses it.
- **Brotli:** the wrapped codec's streaming reader into `dst` as arrow-go's loop reads
  it (a short stream is what it holds), then one byte more refuses it. The stream's
  window is first lowered to the least of its code's length that holds `len(dst)`
  (`brotliWindow`): a window's code is the stream's first 1, 4 or 7 bits, and one of
  the same length moves no later bit, so the ring buffer is at most 256 KiB or the
  page. What is decoded is the same wherever the output fits `dst`: a distance is a
  back-reference up to min(window−16, bytes decoded so far) and a dictionary word past
  it, and the bytes decoded so far are at most `len(dst)`, within either window.
- **a nil buffer from arrow-go's page reader** is a page of 0 bytes: decoded into
  `[]byte{}`, so it holds nothing. From any other caller nil keeps the wrapped codec's
  meaning (`calledByPageReader`, the stack's frames named
  `file.(*serializedPageReader).`, asked only for a nil buffer).
- **the probe past the buffer** reads again on a read of nothing and no error, four
  times at most, since Brotli's reader can answer one at its stream's end; so an empty
  Brotli page after a page of bytes reads. TestAPage…'s honest pages hold it (§5).

A refusal reads "corrupt: a Zstandard page decompresses past the 15 bytes its header
declares", under arrow-go's "parquet: could not decompress page" and ursus's KindIO.

### What is left

- klauspost checks a Zstandard frame without a content size once a raw or RLE block is
  appended, so a corrupt one can append one block, at most 128 KiB, past the buffer's
  capacity before it is refused, the slice grown once: at most about a quarter more
  than the page's buffer and that block. A compressed block whose output passes the
  capacity is refused by klauspost's own check, "output bigger than max block size",
  not as past the page (11 of the generated streams, §5).
- A codec's working memory, bounded by its format: gzip's 32 KiB window, Brotli's
  Huffman tables (some megabytes at most), and its ring buffer as lowered.

## 3. The registry, and how it is answered

The brief preferred a design without the registry's side effect. None was found:

- arrow-go takes a page reader's codec from `compress.GetCodec`, a map of the
  process's, inside `RowGroupReader.GetColumnPageReader`; nothing passes a codec in.
- `PageReader.Reset(stream, nrows, codec, ctx)` takes a codec's enum, which could name
  a codec registered under a value no file uses, but it needs the chunk's stream, made
  inside `GetColumnPageReader` and not exported: a second `ReaderProperties.GetStream`
  is a second read, or a copy, of every column chunk.
- `file.NewPageReader`, deprecated, makes a page reader by hand without the file's
  page limits (D14's bound) and without its dictionary's offset.
- Rewriting each chunk's codec in the footer `measured` serves arrow-go, to a private
  value of the same varint length, was rejected for what it touches.
- A check of each page's codec header beside `countDictionaries` can bound a content
  size and a length header, but not a Zstandard frame without a content size, which
  parquet-mr's streaming writer leaves out, nor a gzip or Brotli stream, without
  decompressing each page twice; and it parses each page header beside arrow-go, as
  the walk of §4 shows a parser can disagree.

So the package's init replaces four codecs, each refusing by the codec's own reading
of a buffer: Zstandard and Snappy refuse only where klauspost would make a buffer of
its own past `cap(dst)`; gzip and Brotli refuse a byte past `len(dst)`; a nil buffer
is "allocate" for every caller but arrow-go's page reader, for which it is a page of 0
bytes. arrow-go's page reader requires a page's output to fill its buffer, so no page
that read before is refused now; arrow-go's page writer only encodes. A codec
registered for one of the four before ursus's init is wrapped, its decoding replaced
by klauspost's; one registered after it replaces ursus's, and the bound with it. The
package doc (`types.go`, "arrow-go's codec registry") and `codecs.go` say so, and
`TestBoundedCodecsDecodeAsTheCodecsTheyReplace` holds each codec to it.

## 4. A dictionary count step 510's walk read otherwise than Thrift

Found by this step's first local run of `FuzzOwnPagesMatchArrow` (§8): a file of 129
bytes, a BYTE_ARRAY chunk read with `ownPages` off, whose dictionary page's count is a
varint of 10 bytes. Thrift's compact protocol (`TCompactProtocol.ReadI32`) cuts a
varint to its low 32 bits and then undoes the zigzag. `thriftReader` read it with
`binary.Uvarint`, which calls a tenth byte past 64 bits an overflow, so step 510's
`countDictionaries` stopped walking the chunk and the count went unchecked. Through
the public API the same page goes to arrow-go's column reader where the footer lists
an encoding the own pages do not read.

`thriftReader` read Thrift otherwise in four ways, each now as `TCompactProtocol`
reads it: a varint of up to 10 bytes, bits past 64 dropped (not an overflow); an i16
or i32 cut to 32 bits before the zigzag is undone (it was undone on 64 bits and cut
after: 2 written with noise past 32 read as −2147483646); a field header whose type
nibble is 0 is a STOP, whatever its high nibble; a list's, a map's and a binary's size
cut to 32 bits. A footer's and a page header's values are the same in every file a
writer makes; the walk checks what arrow-go then reads.

## 5. The tests

Failing first, each under `prlimit --as=4294967296`, the binaries built with the
package's init switched off (`init` renamed), or with step 510's `thriftReader` for
the count of §4:

| test | package | what | oracle |
| --- | --- | --- | --- |
| `TestAPageDecompressingPastItsSizeAllocatesNothingOfIt`, new | readfuzz | 7 streams (a Zstandard frame whose content size claims more than the page; one without a content size that decodes past it; a Snappy block whose length claims more; gzip of many members and Brotli of a few bytes, each decoding past it; Brotli in a window larger than its page; LZ4_RAW decoding past it) × a data page and a dictionary page × declaring the page's bytes and none × the own pages (a REQUIRED INT64) and arrow-go's (a List's INT64 element), each read every way FuzzScanParquet reads an input; CI's file. Each refused as an I/O error naming the declared bytes (LZ4_RAW's as before). Honest pages under 7 encodings (arrow-go's 5 codecs, Zstandard without a content size, two frames), a dictionary and its indices, and an empty page first and between two for the own pages, read as the same pages uncompressed | `fuzzcheck.Run`'s bounds and bounded allocator; the uncompressed file, a differential |
| `TestADictionaryCountInALongVarintIsCountedAsThriftReadsIt`, new | readfuzz | §4 through the public API: the fuzzer's count, and a count past what its page holds, padded to 10 bytes with noise past 32 bits, read by both decoders, refused; 2 so written reads as 2 written plainly | `fuzzcheck.Run`; the plain file |
| `TestBoundedCodecsDecodeAsTheCodecsTheyReplace`, new | parquet | 30 generated inputs (random, runs, words; 0 to 300,001 bytes, about a Zstandard block's 128 KiB), as writers compress them (each codec's fastest and default level; Zstandard without a content size and as two frames; gzip as two members), cut short and a byte flipped, into buffers of their size, one short, one short with capacity past it, one long, and empty; nil buffers; encoding at two levels and its bound | arrow-go's codec, a differential: where it decodes into the buffer the bounded one decodes alike; the bounded one decodes only what the stream holds whole; it refuses as past only a stream that holds more than its buffer |
| `TestEveryParquetFileReadsAsWithArrowGosCodecs`, new | parquet | the clean files (§6), with the own pages and without, in batches of 100, with ursus's codecs and with arrow-go's swapped back in; ursus's writer's bytes under each of 6 codecs with either | arrow-go's codecs, a differential |
| `TestPageHeadersAreReadAsArrowGoReadsThem`, new | parquet | 5,000 dictionary page headers in the compact protocol as a writer may write them: varints padded to 10 bytes, noise past 32 bits, past 64 in a tenth byte, field ids in the long form, a STOP with high bits, unknown i32, binary and list fields with noisy sizes; `readPageHeader` reads each as arrow-go's page reader does, its type, sizes, count and length | arrow-go's page reader (Thrift), a differential |
| `FuzzScanParquet` seeds | readfuzz | `pqForgedSeeds` grows from 22 to 57: §4's 4 and 31 of the codecs' (6 small streams × 2 kinds × 2 declarations, 7 honest pages); CI's file; 83 in all | as the target's |
| `FuzzOwnPagesMatchArrow` corpus | parquet | 8 forged codec pages, flat INT64; §4's input; 40 seeds in all | as the target's |

With the init switched off, every subtest of the first but LZ4_RAW's failed, gzip and
Brotli declaring the page's bytes on the message only; with it on, all pass in 0.3 s.
`FuzzScanParquet`'s seeds of this step and CI's file failed too, and 6 of the 8 twin
entries. With step 510's reader, §4's test and twin entry failed, and the header
differential failed at its first header ("readPageHeader reads {typ:-2147483646
…}").

## 6. The clean-file differential

No file a writer made is refused. The files: the 10 fixtures of `testdata/parquet`;
the flip sweep's two files ursus wrote (uncompressed, Snappy); and 15 written once by
`bench/scripts/oracle/record_codecs.py` into `testdata/parquet/codecs` (132 KB): the
same 500 rows (Int64 with nulls, Float64, a String in a dictionary, a List(Int64) with
null lists and elements, Boolean) under Snappy, gzip, Brotli, Zstandard and LZ4_RAW,
from pyarrow 25.0.1 with V1 and V2 data pages and from Polars 2.1.0, in pages of 512
bytes. Run twice, the script writes the same bytes.

- In the test binary: each read with ursus's codecs and with arrow-go's, with the own
  pages on and off, answers alike, the two refused fixtures by the same words; and the
  frame of `pyarrow_zstd_v2.parquet`, written by ursus under each of the 6 codecs it
  writes, is the same bytes with either and reads alike.
- Before and after: a program over the public API (`ScanParquetBytes(…).Collect`,
  each frame's schema and rows hashed), built once on `origin/master` (73c0046) and
  once on this branch, over the same 27 files and ursus's 6 written files: the two
  outputs are identical. 25 files read (the 15 codec files and ursus's 6 as one frame of
  500 rows, one hash), 2 are refused as before (`pyarrow_bad_array`,
  `pyarrow_flba_delta`), and ursus's written bytes under each codec have the same
  SHA-256 before and after.

ursus's own pages decompress through arrow-go's page reader, not on their own, so
they need nothing more. Its writer encodes through the codecs it was given, whose
encoding is arrow-go's: the same bytes.

## 7. The overhead

One INT64 column of 1,000,000 values (`i*7919 % 100003`) ursus wrote, one row group,
Zstandard (1,165,427 bytes) and Snappy (3,873,582), each read whole on one thread
through the package's `Source`, 2 reads to warm and 21 timed, with arrow-go's codecs
swapped in and with ursus's, four rounds alternating which went first, under the
shared lock, in milliseconds:

| | round | arrow-go median | bounded median | arrow-go least | bounded least |
| --- | --- | --- | --- | --- | --- |
| Zstandard | 0 | 8.58 | 8.42 | 7.99 | 7.99 |
| | 1 | 8.78 | 8.33 | 8.02 | 8.00 |
| | 2 | 8.50 | 10.00 | 8.03 | 9.20 |
| | 3 | 16.27 | 12.20 | 9.12 | 8.80 |
| Snappy | 0 | 8.00 | 9.59 | 5.82 | 5.81 |
| | 1 | 6.54 | 6.89 | 5.29 | 5.17 |
| | 2 | 5.93 | 5.81 | 5.37 | 5.14 |
| | 3 | 5.71 | 5.89 | 5.16 | 5.08 |

No overhead outside the noise: the mean of the least is 8.29 against 8.50 ms for
Zstandard (+2.5%) and 5.41 against 5.30 for Snappy (−2.0%), the medians' 10.53 against
9.74 and 6.55 against 7.05, rounds 2 and 3 of Zstandard noisy for both. The work added
is a varint read for Snappy and a capacity check per block for Zstandard.

## 8. Fuzz runs

One at a time, under the shared lock and `prlimit --as=4294967296`, 20 s each,
`-fuzzminimizetime=3s`, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:

| run | target | inputs | new interesting | outcome |
| --- | --- | --- | --- | --- |
| 1 | `FuzzScanParquet` | 29,713 | 25 | none |
| 1 | `FuzzOwnPagesMatchArrow` | 6,490 | 19 | **the count of §4**, after 18 s |
| 2, after §4's fix | `FuzzScanParquet` | 25,174 | 22 | none |
| 2 | `FuzzOwnPagesMatchArrow` | 36,975 | 27 | none |

## 9. Teeth

Each mutation made to a copy of the code, both packages' test binaries built, the file
restored from the copy and its md5 checked; the binaries run under the cap.

| # | mutation | caught by | |
| --- | --- | --- | --- |
| A | the Zstandard check off: `DecodeAll` without its cap limit | TestAPage…'s Zstandard cases and CI's file; FuzzScanParquet's seeds; the twin's corpus; the codec differential, "the bounded codec decodes 1 bytes" into a buffer of its own | **bites** |
| B | the stream limit off: gzip and Brotli read their whole stream (`io.ReadAll`) | TestAPage…'s gzip and Brotli; 6 FuzzScanParquet seeds; 2 twin entries; the codec differential for both; the clean files, `pyarrow_brotli_v2.parquet` read wrong ("a dictionary value runs past its page") | **bites** |
| C | the Snappy check off | TestAPage…'s Snappy cases; FuzzScanParquet's seed 45; a twin entry; the codec differential | **bites** |
| D | a nil buffer from the page reader decoded as any caller's | TestAPage…'s pages declaring none; FuzzScanParquet's seed 42; the twin's entries | **bites** |
| E | the Brotli window not lowered | TestAPage…'s Brotli in a window larger than its page; 4 FuzzScanParquet seeds | **bites** |
| F | one probe past the buffer | TestAPage…'s honest Brotli pages with an empty page between; the codec differential, "refuses it as past the buffer, and it holds 0 bytes" | **bites** |
| G | an i32 zigzagged before it is cut to 32 bits, as step 510 read it | the header differential; §4's test and twin entry and FuzzScanParquet's seeds of it | **bites** |
| H | a varint read by `binary.Uvarint`, as step 510 read it | the same three | **bites** |
| I | a STOP only a byte of 0, as step 510 read it | the header differential | **bites** |

## 10. Checks

After the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd
GOAMD64=v3`:

- the touched packages: `./internal/readfuzz/` ok, 2.1 s; `./internal/source/parquet/`
  ok, 6.1 s;
- `go test ./internal/...`: ok, 28 packages;
- `go test .` in full: ok, 190 s, under the lock;
- `GOEXPERIMENT=simd go vet ./...`: clean;
- `go run ./internal/gen/levels`: ok;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2, as on master;
- `go run ./internal/gen/testinventory -check`: 1,225 lines, as the baseline; the new
  tests count none;
- `gofmt -l .`: empty.

And again once master (steps 515 and 518, #68 and #71) was merged in: `go test
./internal/...` ok, 28 packages; `go test .` ok, 180 s, under the lock; vet, levels,
api (0 gone), testinventory (1,225) and gofmt as above.

The race detector and the widths are left to CI.
