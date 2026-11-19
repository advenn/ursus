# Step 528 — as built

**A Zstandard frame's window is capped at 128 MiB, as the reference decoder's.** Step
511's finding (`step-511-as-built.md` §5), decided by the maintainer on 2026-11-15: a
CSV, NDJSON or JSON stream, or an Arrow IPC buffer, whose Zstandard frame declares a
window past 128 MiB is refused, an `ErrIO` naming the limit, before klauspost's decoder
makes the window. This refuses files ursus read before, those zstd writes in its
`--long` modes past 27, which Polars refuses too; it is CHANGELOG's "### Changed". A
Parquet page is not held to it, against the brief's first list (§2.3), because Polars
2.1 and pyarrow read any window there and ursus makes none.

```
$ printf 'a,b\n1,x\n' | zstd --long=28 > t.csv.zst    # 25 bytes declaring 256 MiB
master   ScanCSV("t.csv.zst").Collect   allocated 514.1 MiB, and read 1 row
528      ScanCSV("t.csv.zst").Collect   allocated   0.1 MiB, and refused:
  ursus: scan: reading schema from csv source
    caused by: ursus: scan_csv: reading near line 1
    caused by: a Zstandard frame declares a window of 256 MiB, past 128 MiB, the most
               the reference decoder (libzstd) and Polars read by default
zstd -d    t.csv.zst : Window size larger than maximum : 268435456 > 134217728
Polars 2.1 OSError: Frame requires too much memory for decoding
```

## 1. The problem

A Zstandard frame declares the window its decoder keeps, the bytes its matches reach
back into (RFC 8878 §3.1.1.1.2): up to 3.75 TB by the format, a single segment's its
content size. klauspost's streaming decoder makes the whole window at the frame's first
block, whatever the frame holds: `(*frameDec).reset` sizes the history from the
declared window alone (the window and 1 MiB under `WithDecoderLowmem`, its default;
twice a window under 2 MiB), and `(*history).ensureBlock`, or the stream's executing
goroutine, makes it. Its default limit is 512 MiB (`MaxWindowSize`). Measured on
master, the allocation of one `Collect` (`/gc/heap/allocs:bytes`):

| input | bytes | master | 528 |
| --- | ---: | ---: | ---: |
| CSV, zstd CLI, window 1 MiB | 25 | 4.1 MiB | 4.1 MiB |
| CSV, window 128 MiB | 25 | 258.1 MiB | 258.1 MiB |
| CSV, window 256 MiB | 25 | 514.1 MiB | 0.1 MiB, refused |
| CSV, window 512 MiB | 25 | 1026.1 MiB | 0.1 MiB, refused |
| CSV, a single segment of 140 MiB (`--long=28 --stream-size`) | 13,389 | (decodes 140 MiB) | 0.1 MiB, refused |
| NDJSON or JSON, window 256 MiB | 43–45 | 516 MiB | 1.0 MiB, refused |
| IPC file, pyarrow's, its frames declaring 128 MiB | 4,754 | 129.2 MiB | 129.2 MiB |
| IPC file, declaring 512 MiB | 4,754 | 513.2 MiB | 0.0 MiB, refused |
| IPC file, declaring 1 GiB | 4,754 | arrow-go panicked, "window size exceeded" | refused |
| Parquet, pyarrow's, its pages' frames declaring 128 to 512 MiB | 4,450 | 0.2 MiB | 0.2 MiB |
| Parquet, declaring 1 GiB | 4,450 | refused, "window size exceeded" | the same (§7) |

A text reader decompresses its stream twice, inferring the schema and reading the rows,
so a CSV pays its window twice. Arrow IPC decompresses through arrow-go's own decoder,
`zstd.NewReader(nil)` made with klauspost's defaults (`getDecompressor`,
`arrow/ipc/compression.go`), which ursus cannot configure, once a batch.

What the reference and Polars do (zstd CLI 1.5.7, Polars 1.44.1 and 2.1.0, pyarrow
25.0.1, on the same files):

| input | `zstd -d` | Polars 1.44.1 | Polars 2.1.0 | pyarrow |
| --- | --- | --- | --- | --- |
| CSV, NDJSON, JSON, window 128 MiB | reads | reads | reads | — |
| window 256 or 512 MiB | "Window size larger than maximum" | refuses | refuses | — |
| CSV, a single segment of 140 MiB | refuses, 146800640 > 134217728 | refuses | refuses | — |
| IPC file, frames declaring 128 MiB | — | reads | reads | reads |
| frames declaring 256 MiB to 2 GiB | — | refuses | refuses | reads |
| Parquet, frames declaring 128 MiB | — | reads | reads | reads |
| frames declaring 256 MiB to 2 GiB | — | refuses | **reads** | reads |

## 2. The design

### 2.1 `internal/zstdframe` (level 0, klauspost alone)

- `MaxWindow = 128 << 20`, libzstd's `ZSTD_WINDOWLOG_LIMIT_DEFAULT`, 27.
- A walker of a Zstandard stream's frames, fed a piece at a time: each frame's magic,
  its Frame_Header_Descriptor and the rest of its header, from which it reads the
  window (a Window_Descriptor's, or a single segment's Frame_Content_Size, as libzstd
  and klauspost's stream path take it); each block's header, its bytes skipped (one
  for RLE); the checksum; skippable frames passed over. Bytes that are not a frame,
  a reserved bit or block type, end the walk without an error: the decoder refuses
  them itself.
- `NewReader(r, opts...)`: klauspost's decoder reading `r` through `checking`, the
  walker over the bytes as the decoder reads them, so a read that completes a header
  declaring more than `MaxWindow` fails with `*WindowError{Window}` before the decoder
  has the header; and the decoder made with `WithDecoderMaxWindow(MaxWindow)`, a second
  guard. Why a walk and not klauspost's error: klauspost answers
  `ErrWindowSizeExceeded` for a header past its limit and also for a corrupt block (a
  block, or a literal section, larger than the window, `blockdec.go`), and
  `ErrDecoderSizeExceeded` for a single segment's size and for other limits, so its
  error cannot say which; the walk can, and names the window declared.
- `Check(b)`: the walk of a byte slice, refusing as `NewReader` does. `Windows(b)`: each
  frame's window, unrefused, for the fuzz targets' skips.

### 2.2 Where it is applied

- `internal/source.Decompressed`, the CSV, NDJSON and JSON readers' decompression,
  decodes through `zstdframe.NewReader`. The error reaches the user as each reader
  wraps a stream's error: `scan_csv: reading near line N`, `read_ndjson: reading
  <name>`, with the `*WindowError` as its cause.
- Arrow IPC (`internal/arrowmeta`): the metadata walk of step 519 records, for a batch
  whose compression is ZSTD, where each of its buffers lies in its body
  (`walker.zstd`). Each buffer of a declared length above 0 (arrow-go decodes nothing
  of −1, not compressed, or of 0) is checked with `zstdframe.Check` once the body is
  read and before arrow-go has it: the stream's `messageReader` after it reads the body;
  `File`, which serves arrow-go's file reader, in `ReadAt` of the body, which arrow-go
  reads in one call at the offset past the block's metadata and of the body's length
  (`TestEveryZstdBodyIsChecked` holds it to that), the buffers kept by that offset from
  the walk of the block's metadata. The refusal is KindIO: "reading <name>: buffer N of
  a record batch in the block at byte M", the hint "write it again with a Zstandard
  window of 128 MiB or less". Every frame of a buffer is walked, though arrow-go stops
  decoding at the buffer's length; a frame past that length, which no writer writes, is
  refused too.

### 2.3 Not a Parquet page

The brief listed step 520's Parquet codec. It is left as it was:

- klauspost's `DecodeAll`, which the bounded codec calls, honours a window limit (it
  refuses past `maxWindowSize`, 512 MiB by default) but keeps no window: it decodes
  into the page's buffer, held to the page's declared size since step 520. The files
  above allocate 0.2 MiB whatever they declare up to 512 MiB.
- Polars 2.1.0 and pyarrow 25.0.1 read a Parquet page whose frame declares 256 MiB, 512
  MiB, 1 GiB and 2 GiB; Polars 1.44.1 refused them. The maintainer's reason for the cap,
  that the reference decoder and Polars refuse such a frame, does not hold for a page,
  and the cap would refuse files both read, with no memory saved.

The zstdwindow oracle group's `window_256mib_parquet` pins it: Polars 2.1 reads it, so
ursus must.

### 2.4 Below the cap

A frame declaring up to 128 MiB still costs its window from a few bytes. klauspost has
no way to grow the window as output arrives:

- `WithDecoderLowmem`, its default, already makes the least, the window and 1 MiB;
  without it, twice the window;
- a single segment's window is the content size it declares (`frameDec.reset`), a
  claim as cheap to forge as a window, and a frame with a content size smaller than its
  window is not shrunk to it, as libzstd shrinks its buffer;
- `WithDecoderMaxMemory`, in a stream, lowers the window allowed
  (`newFrameDec` takes the smaller) and nothing else.

libzstd too allocates a stream's window when it reads the header. So the fuzz targets
keep a skip for windows between their bound and 128 MiB (§3.3).

## 3. The tests, and their oracles

### 3.1 The recorded oracle: the `zstdwindow` group

`bench/scripts/oracle/record_zstdwindow.py` writes `testdata/zstd/` (17 files, 22 KB),
the same bytes on every run, refusing another zstd CLI or pyarrow:

- the zstd CLI 1.5.7's streams of a 12-row CSV read from its standard input, so that the
  window it declares is the one asked for: 1, 8 and 64 MiB (`--zstd=wlog`), 128, 256
  and 512 MiB (`--long`); the NDJSON and the JSON of the rows at 128 and 256 MiB; and a
  CSV of 140 MiB given its size (`--long=28 --stream-size`), which zstd writes as a
  single segment declaring 140 MiB, 5 KB;
- pyarrow's IPC file and stream and Parquet file of 360 rows, Zstandard, each frame's
  header changed in place to declare 128 or 256 MiB (`declare`: a short single
  segment's content size becomes a Window_Descriptor, which leaves the frame what it
  was).

`bench/scripts/oracle/zstdwindow.py` records Polars 2.1.0's `read_csv`, `read_ndjson`,
`read_json`, `read_ipc`, `read_ipc_stream` and `read_parquet` of each: 10 frames and 7
refusals. `oracle_zstdwindow_test.go` reads each file by its scan; a refusal is
`ErrIO` naming "past 128 MiB". zstd's refusal reaches Python as an `OSError`, which the
group records as a `ValueError` of its message (the runner catches Polars' errors,
`TypeError` and `ValueError`).

### 3.2 Go oracles and differentials

- `TestWindowsAreKlauspostsHeaders` (zstdframe): every Frame_Header_Descriptor with
  every Window_Descriptor, its dictionary ID and content size random, 16,448 headers:
  the walk's window is klauspost's header parser's (`zstd.Header.Decode`), and the walk
  resumes past the frame at the next one.
- `TestTheWalkGoesPastSkippableFramesAndBlocks`: 30 frames of klauspost's, raw, RLE
  and compressed blocks, checksums, and 10 skippable frames holding a frame's magic;
  each window klauspost's parser's.
- `TestCheckIsTheLimit`: all 256 Window_Descriptors and single segments about the limit
  and at 2^64−1: refused exactly past 128 MiB, the window named klauspost's; klauspost's
  decoder held to the same limit refuses each refused frame for its window, and decodes
  the passed ones it can afford, up to 1 MiB, without a word about it.
- `TestNewReaderReadsAsKlauspost`: klauspost's streams, single segments and windows of
  1 KiB to 8 MiB, several frames and skippable ones, read whole, a byte at a time, in
  halves and with EOF on the last bytes: the bytes of klauspost's decoder made with its
  defaults; and 60 of each cut short or with a byte changed, on one goroutine, the same
  bytes and an error where it gives one (500 at least compared, a change declaring a
  window past 8 MiB left out).
- `TestNewReaderRefusesBeforeTheWindow`: a frame declaring 144 MiB, 256 MiB, 512 MiB,
  3.75 TB, or a single segment of 128 MiB and a byte or of 2^64−1, after a frame of
  64 KiB: the first frame's bytes, then the `*WindowError`, within 16 MiB of allocation
  (`fuzzcheck.Run`).
- `TestEveryZstdBodyIsChecked` (arrowmeta): the Zstandard files of 40 generated records,
  big- and little-endian, dictionaries among them: `File` checks every body with a
  Zstandard buffer, as many as the walk finds.
- `TestAZstdWindowPastTheLimitIsRefusedBeforeArrowGo`: the first and the last Zstandard
  buffer of each of 20 generated records, files and streams, declaring 256 MiB: KindIO,
  the `*WindowError` its cause, within 16 MiB.
- `TestZstdWindowUpToTheCapReadsAsBefore` (root): klauspost's encoder at windows of 1,
  8, 64 and 128 MiB, declared as they are, a CSV, an NDJSON and a JSON of 2,000 rows:
  read as the text klauspost's default decoder, ursus's before, makes of them.
- `TestTheWalkPassesWhatArrowGoWrites` (step 519's) reads arrow-go's Zstandard files and
  streams of 300 records through the check: none refused.

### 3.3 The fuzz targets

- `skipZstdWindow` skips a frame declaring past the bound and up to 128 MiB, among the
  frames a reader reaches, up to the first past 128 MiB, now before `inflated`, which
  decodes through `zstdframe.NewReader` so that it makes no window past the cap.
- The bound is 16 MiB, not 32. A frame of 32 MiB, which the skip let through (it skipped
  more than 32 MiB), allocated 69.0 MiB through the NDJSON reader and 67.2 MiB through
  the CSV reader against the bound of 64.0 MiB, two windows of 33 MiB; 24 MiB passed. A
  latent false finding of steps 511 and 524, fixed.
- The IPC targets skip an input whose frames, wherever a frame's magic begins one, add
  up past the bound among those up to 128 MiB (`skipZstdWindowsIn`).
- `TestZstdWindowPastTheCap` (readfuzz): each window past the cap, by descriptor or as
  a single segment, alone or after a frame of 1 MiB, and step 511's kept input
  `finding-zstd-window-512mib`, through the CSV, NDJSON and JSON targets' own checks
  (`scanCSVOne`, `scanNDJSONOne`, `scanJSONOne`) and a read held to refuse naming the
  limit, within the targets' bounds; Polars' and pyarrow's Zstandard IPC files and
  streams of `testdata/ipc`, every frame declaring 256 MiB, through `scanIPCFileOne` and
  `scanIPCStreamOne`. The kept input, skipped since step 511, is a passing seed.
- `TestZstdWindowBelowTheCap`: the kept inputs the targets still skip, step 511's
  `finding-zstd-window-64mib` (FuzzScanJSON) and step 524's `zstd-window-past-the-bound`
  (FuzzScanCSV, 104 MiB), and frames of 24, 64 and 128 MiB through each text reader:
  read within the targets' bounds widened by twice the window and 1 MiB.

## 4. The fuzz runs

20 s each, coverage-guided (`go test -c -fuzz`), 4 workers, under the heavy lock and
`prlimit --as=4294967296`, `-fuzzminimizetime=3s`: `FuzzScanCSV` 7,382 executions, 39
new interesting inputs; `FuzzScanNDJSON` 8,550 and 34; `FuzzScanJSON` 6,176 and 29. No
failure.

## 5. The teeth

Each broken from a saved copy, the tests run, the copy restored:

- `Decompressed` decoding with `zstd.NewReader` again: `TestZstdWindowPastTheCap` ends
  in fuzzcheck's "runaway allocation: 537990208 bytes (513.1 MiB) and counting, 4 times
  the bound", and five refusals of the oracle group fail (`window_256mib_csv_zst`,
  `window_512mib_csv_zst`, `single_segment_140mib_csv_zst`, `window_256mib_ndjson_zst`,
  `window_256mib_json_zst`).
- `NewReader` without `checking`, klauspost's limit alone: `TestNewReaderRefusesBeforeTheWindow`
  fails, "a frame declaring 150994944 bytes: window size exceeded", and
  `TestZstdWindowPastTheCap` fails for the refusal's words.
- `checkZstd` checking nothing: `TestAZstdWindowPastTheLimitIsRefusedBeforeArrowGo`
  fails, the oracle's `window_256mib_arrow` and `window_256mib_arrows` fail, and
  `TestZstdWindowPastTheCap/polars_types_zstd.arrow` allocates 258.4 MiB against 67.1.
- Parquet's decoder given `WithDecoderMaxWindow(128 << 20)`: the oracle's
  `window_256mib_parquet` fails, "refused where Polars answers".
- The walk skipping an RLE block's size, not its byte:
  `TestTheWalkGoesPastSkippableFramesAndBlocks` fails, 2 windows walked of 30.

## 6. The checks

After the last edit: `go test ./internal/...` ok; `go test .` in full, under the lock,
ok; `GOEXPERIMENT=simd go vet ./...` ok; `go run ./internal/gen/levels` ok, the new
package at level 0; `GOEXPERIMENT=simd go run ./internal/gen/api -check`, 0 gone;
`go run ./internal/gen/testinventory -check` ok, 1,223 lines as in the baseline;
`gofmt -l .` nothing. qcheck imports the root package, so `cd qcheck && go vet ./...
&& go test ./...`, ok.

## 7. Left for the maintainer

- **A Parquet page whose frame declares more than 512 MiB is refused**, "could not
  decompress page: window size exceeded", where Polars 2.1.0 and pyarrow read it (1 and
  2 GiB tried): klauspost's `DecodeAll` keeps its default limit, though it makes no
  window. `zstd.WithDecoderMaxWindow` at the format's most in `zstdDecoder` would read
  them at no cost; it adds to what ursus reads, which the brief did not ask.
- Below the cap a frame's window is made whole from a few bytes, and a text reader makes
  it twice a `Collect`: a CSV declaring 128 MiB costs 258 MiB. Reading the stream once
  for the schema and the rows would halve it.
