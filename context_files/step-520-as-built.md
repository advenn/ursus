# Step 520 — as built

**A Parquet page decompresses to no more than it declares.** F10 of
[`step-517-as-built.md`](./step-517-as-built.md), which step 517 found and left, is
fixed: ursus replaces four codecs of arrow-go's compress registry with codecs that
decode a page into the buffer its header declares and refuse more as corrupt, KindIO,
before allocating it. CI's file of 623 bytes, a Zstandard frame claiming 1.2 GB in a
page of 15, made the reader ask for 1.2 GB; a Snappy block, a Zstandard frame with no
content size, and a page declaring no bytes under gzip or Brotli reached as far. The
registry is the process's, so each replacement is a strict tightening of the codec it
replaces (§3). This step's own fuzz run found a second allocation, F11, a dictionary
page's count that step 510's walk read otherwise than Thrift, and it is fixed too
(§4).

## 1. The finding

arrow-go's page reader (`file.serializedPageReader`) sizes a page's buffer by its
header, which `boundPages` bounds by the file's largest column chunk (D14, step 510),
hands it to the chunk's codec through `compress.Decode`, and refuses a page whose
output is not that size only once the codec has decoded it. Four of its five codecs
did not stop at the buffer:

| codec | what it did | reached |
| --- | --- | --- |
| Zstandard | klauspost's `DecodeAll` made `dst` of the frame's content size before decoding a block (`make([]byte, len(dst), len(dst)+FCS+16)`), up to its default limit of 64 GiB; a frame with no content size grew `dst` a block at a time, to the same limit | CI's file: 1,211,898,114 bytes for a page of 15 |
| Snappy | klauspost's `s2.Decode` made `dst` of the block's length header where `cap(dst)` was short, up to 2^32−1 | 1 GiB for a header claiming 2^30 |
| gzip, Brotli | arrow-go's codecs read into `dst` and refuse a byte past it, but read their whole stream (`io.ReadAll`) where `dst` is nil | — |
| all four | a page declaring 0 bytes is decompressed into a nil `dst`: `memory.Buffer.Bytes()` of a page reader's buffer never grown. To a codec nil means "allocate what the stream holds" | 807 MiB and counting from 135 KB of gzip |

LZ4_RAW decodes into `dst`, nil or not, and no further (pierrec's `UncompressBlock`);
arrow-go reads neither Hadoop's LZ4 nor LZO. Both the reader's own pages and
arrow-go's column reader take their pages from the same page reader, so every chunk
was exposed.

Brotli's decoder had one more: it makes a ring buffer of the stream's window, or less
where its first meta-block says it holds less, so a stream of 14 bytes claiming a
meta-block of 16 MiB in a window of 16 MiB made 16.8 MB for a page of 40 bytes, every
time the page was read: 81 MiB in one FuzzScanParquet input, within arrow-go's own
codec's bound on `dst`.

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
  `[]byte{}`, so it holds nothing. From any other caller nil is still "allocate", the
  wrapped codec's (`calledByPageReader`, the stack's frames named
  `file.(*serializedPageReader).`, asked only for a nil buffer).
- **the probe past the buffer** reads again on a read of nothing and no error, four
  times at most: Brotli's reader answers one at its stream's end, and arrow-go's
  codec, probing once, refused an empty Brotli page whose buffer was not nil, one after
  a page of bytes ("decoded data exceeds destination size 0"). TestAPage…'s honest
  pages showed it (§5).

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
  decompressing each page twice; and it parses each page header beside arrow-go, the
  walk F11 shows can disagree with it (§4).

So the package's init replaces four codecs, and each is a strict tightening by the
codec's own reading of a buffer: Zstandard and Snappy refuse only where klauspost made
a buffer of its own past `cap(dst)`; gzip and Brotli refuse a byte past `len(dst)`, as
arrow-go's did; a nil buffer is "allocate" for every caller but arrow-go's page
reader, for which it is a page of 0 bytes. arrow-go's page reader requires a page's
output to fill its buffer, so a page refused now was refused before, after its
output; arrow-go's page writer only encodes. The one answer that is not a tightening
is the empty Brotli page above, which arrow-go refused and ursus reads. A codec
registered for one of the four before ursus's init is wrapped, its decoding replaced
by klauspost's; one registered after it replaces ursus's, and the bound with it. The
package doc (`types.go`, "arrow-go's codec registry") and `codecs.go` say so, and
`TestBoundedCodecsDecodeAsTheCodecsTheyReplace` holds each codec to it.

## 4. F11: a dictionary count step 510's walk read otherwise than Thrift

Found by this step's first local run of `FuzzOwnPagesMatchArrow` (§7), its worker
ended while minimizing: a file of 129 bytes, a BYTE_ARRAY chunk read with `ownPages`
off, whose dictionary page's count is the varint `fe ff ff d6 d6 d6 d6 d6 d6 0f`.
Thrift's compact protocol (`TCompactProtocol.ReadI32`) cuts a varint to its low 32
bits and then undoes the zigzag: 896,532,479. `thriftReader` read it with
`binary.Uvarint`, which calls a tenth byte past 64 bits an overflow, so step 510's
`countDictionaries` stopped walking the chunk, and arrow-go's `NewDictConverter` asked
for 21,516,779,520 bytes of slice headers: a fatal "out of memory". Through the public
API the same page reaches arrow-go where the footer lists an encoding the own pages do
not read.

`thriftReader` read Thrift otherwise in four ways, each now as `TCompactProtocol`
reads it: a varint of up to 10 bytes, bits past 64 dropped (not an overflow); an i16
or i32 cut to 32 bits before the zigzag is undone (it was undone on 64 bits and cut
after: 2 written with noise past 32 read as −2147483646); a field header whose type
nibble is 0 is a STOP, whatever its high nibble; a list's, a map's and a binary's size
cut to 32 bits. A footer's and a page header's values are the same in every file a
writer makes; the walk checks what arrow-go then reads. Kept as
`testdata/fuzz/FuzzOwnPagesMatchArrow/dictionary-count-in-a-10-byte-varint`.

## 5. The tests

Failing first, each under `prlimit --as=4294967296`, the binaries built with the
package's init switched off (`init` renamed), or with step 510's `thriftReader` for
F11:

| test | package | what | oracle |
| --- | --- | --- | --- |
| `TestAPageDecompressingPastItsSizeAllocatesNothingOfIt`, new | readfuzz | 7 streams (a Zstandard frame claiming 2^30 holding the page; one without a content size of 4,096 RLE blocks, 512 MiB; a Snappy block claiming 2^30; 128 gzip members, 128 MiB; Brotli of 64 MiB, 102 bytes; Brotli in a window of 16 MiB, 14 bytes; LZ4_RAW of 1 MiB) × a data page and a dictionary page × declaring the page's bytes and none × the own pages (a REQUIRED INT64) and arrow-go's (a List's INT64 element), each read every way FuzzScanParquet reads an input; CI's file. Each refused as an I/O error naming the declared bytes (LZ4_RAW's as before). Honest pages under 7 encodings (arrow-go's 5 codecs, Zstandard without a content size, two frames), a dictionary and its indices, and an empty page first and between two for the own pages, read as the same pages uncompressed | `fuzzcheck.Run`'s bounds and bounded allocator; the uncompressed file, a differential |
| `TestADictionaryCountInALongVarintIsCountedAsArrowGoReadsIt`, new | readfuzz | F11 through the public API: the fuzzer's count and 2^31−1 padded to 10 bytes with noise past 32, read by both decoders, refused; 2 so written reads as 2 written plainly | `fuzzcheck.Run`; the plain file |
| `TestBoundedCodecsDecodeAsTheCodecsTheyReplace`, new | parquet | 30 generated inputs (random, runs, words; 0 to 300,001 bytes, about a Zstandard block's 128 KiB), as writers compress them (each codec's fastest and default level; Zstandard without a content size and as two frames; gzip as two members), cut short and a byte flipped, into buffers of their size, one short, one short with capacity past it, one long, and empty; nil buffers; encoding at two levels and its bound | arrow-go's codec, a differential: where it decodes into the buffer the bounded one decodes alike; the bounded one decodes only what the stream holds whole; it refuses as past only a stream that holds more than its buffer |
| `TestEveryParquetFileReadsAsWithArrowGosCodecs`, new | parquet | the clean files (§6), with the own pages and without, in batches of 100, with ursus's codecs and with arrow-go's swapped back in; ursus's writer's bytes under each of 6 codecs with either | arrow-go's codecs, a differential |
| `TestPageHeadersAreReadAsArrowGoReadsThem`, new | parquet | 5,000 dictionary page headers in the compact protocol as a writer may write them: varints padded to 10 bytes, noise past 32 bits, past 64 in a tenth byte, field ids in the long form, a STOP with high bits, unknown i32, binary and list fields with noisy sizes; `readPageHeader` reads each as arrow-go's page reader does, its type, sizes, count and length | arrow-go's page reader (Thrift), a differential |
| `FuzzScanParquet` seeds | readfuzz | `pqForgedSeeds` grows from 22 to 57: F11's 4 and 31 of step 520 (6 small streams × 2 kinds × 2 declarations, 7 honest pages); `zstd-frame-claiming-1p2gb`, CI's file; 83 in all | as the target's |
| `FuzzOwnPagesMatchArrow` corpus | parquet | 8 forged codec pages, flat INT64; F11's input; 40 seeds in all | as the target's |

With the init switched off, the 57 subtests of the first:

| streams | outcome |
| --- | --- |
| Zstandard claiming 2^30, 8; CI's file | fatal "out of memory": "cannot allocate 1077936128-byte block", 1212153856 for CI's |
| Zstandard without a content size, 8 | runaway allocation, 348.7 MiB and counting, 4 times 80.1 MiB |
| Snappy claiming 2^30, 8 | runaway allocation, 2,048.3 MiB and counting |
| gzip and Brotli of 64 MiB, declaring none, 8 | runaway allocation, 807.6 and 324.8 MiB and counting |
| Brotli in a window of 16 MiB, 8 | allocated 81.4 MiB declaring the page's bytes, 254.5 declaring none, over 64.1 |
| gzip and Brotli of 64 MiB, declaring the page's bytes, 8 | refused within the bounds by arrow-go's own codecs; failed on the message only |
| LZ4_RAW, 8 | pass, as after |

and the honest Brotli pages with an empty page between, 2, refused. With the init on,
all pass in 0.3 s. `FuzzScanParquet`'s seed 41, the first of step 520's, ran away to
1,024 MiB, and CI's file to 1,156 MiB; 6 of the 8 twin entries failed the same way.
F11's test and twin entry: fatal "cannot allocate 21516779520-byte block" with step
510's reader; the differential failed at its first header ("readPageHeader reads
{typ:-2147483646 …}").

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
| 1 | `FuzzOwnPagesMatchArrow` | 6,490 | 19 | **F11** (§4), after 18 s, the worker ended while minimizing |
| 2, after F11's fix | `FuzzScanParquet` | 25,174 | 22 | none |
| 2 | `FuzzOwnPagesMatchArrow` | 36,975 | 27 | none |

## 9. Teeth

Each mutation made to a copy of the code, both packages' test binaries built, the file
restored from the copy and its md5 checked; the binaries run under the cap.

| # | mutation | caught by | |
| --- | --- | --- | --- |
| A | the Zstandard check off: `DecodeAll` without its cap limit, as arrow-go's | TestAPage…'s Zstandard cases and CI's file: fatal "cannot allocate 1077936128-byte block", 1212153856; FuzzScanParquet's seeds, the same; the twin's corpus, 2,048 MiB over 64 MiB; the codec differential, "the bounded codec decodes 1 bytes" into a buffer of its own | **bites** |
| B | the stream limit off: gzip and Brotli read their whole stream (`io.ReadAll`) | TestAPage…'s gzip, runaway 807.6 MiB, and Brotli, 317.2 MiB; 6 FuzzScanParquet seeds, 175.6 MiB over 64.1; 2 twin entries; the codec differential for both; the clean files, `pyarrow_brotli_v2.parquet` read wrong ("a dictionary value runs past its page") | **bites** |
| C | the Snappy check off | TestAPage…'s Snappy cases, runaway 2,048 MiB; FuzzScanParquet's seed 45; the twin's `forged-snappy-length-2p30`; the codec differential | **bites** |
| D | a nil buffer from the page reader decoded as any caller's | TestAPage…'s pages declaring none: fatal "out of memory"; FuzzScanParquet's seed 42, runaway 1,024 MiB; the twin's entries | **bites** |
| E | the Brotli window not lowered | TestAPage…'s Brotli in a window of 16 MiB, 81.4 MiB over 64.1; 4 FuzzScanParquet seeds | **bites** |
| F | one probe past the buffer, as arrow-go's codecs make it | TestAPage…'s honest Brotli pages with an empty page between; the codec differential, "refuses it as past the buffer, and it holds 0 bytes" | **bites** |
| G | an i32 zigzagged before it is cut to 32 bits, as step 510 read it | the header differential; F11's test and twin entry and FuzzScanParquet's F11 seeds: fatal "cannot allocate 21516779520-byte block" | **bites** |
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
