# Step 534 — as built

**A read error ending in `io.EOF` is an error, not the end of the input.** Step 529's
fuzzing found it, an IPC twin of step 521's F12: a compressed Arrow IPC buffer cut
short, and an IPC stream reader's error, wrap `io.EOF`. Every operator in the engine
takes `errors.Is(err, io.EOF)` for the end of its input, so `Collect` and `Count`
answered 0 rows, or fewer than there were, with no error, where arrow-go refuses the
same bytes. The same class was found in a stream cut where a message part begins, in
a Parquet `ReaderAt` short of a column chunk, and in NDJSON, lines and CSV streams
whose read fails with an error wrapping `io.EOF`. Each is refused now, an `ErrIO`
wrapping `io.ErrUnexpectedEOF`. A source's end is `io.EOF` itself, and the scan, where
every source's batches enter the engine, reads any other error wrapping it as the
source's failure.

## 1. The findings, traced

The inputs are `~/ursus-scratch-archive/step529-findings/`, kept now as the seeds
`FuzzScanIPCFile/finding-buffer-cut-short-read-as-the-end` (2,160 bytes) and
`FuzzScanIPCStream/finding-buffer-cut-short-read-as-the-end` (1,608 bytes). Both are
Polars' `polars_null_lz4` files with one buffer's metadata mutated. Replayed under
`prlimit --as=4294967296` on this branch's base, both fail as step 529 recorded:

```
FuzzScanIPCFile:   ursus reads 0 rows of 2160 bytes arrow-go reads as: arrow-go panicked: EOF
FuzzScanIPCStream: ursus reads 0 rows of 1608 bytes arrow-go reads as: arrow/ipc: unknown error while reading: EOF
```

A compressed IPC buffer is eight bytes of decompressed length and the codec's frame.
arrow-go v18.7.0's `ipcSource.buffer` (`arrow/ipc/file_reader.go:584`) decompresses it
with `io.ReadFull` into a buffer of the declared length and panics with the error. A
frame that yields nothing gives `io.ReadFull`'s `io.EOF`. Where it leaves arrow-go:

- **A file.** `FileReader.RecordBatchAt` does not recover, so the panic reaches
  `arrowsrc`'s `callerCode`, which recovers it as `uerr.Attributed`, KindIO.
  `uerr.PanicError.Unwrap` exposes a panic value that is an error, so the error the
  scan returned wrapped `io.EOF`. `files` passed it on (its own test is `err ==
  io.EOF`), and the first operator above the scan took it for the end.
- **A stream.** arrow-go's `Reader.next` recovers the panic itself, as
  `utils.FormatRecoveredError`, `%w`, and `Next` answers false. `arrowsrc` tested
  `readErr != nil && !errors.Is(readErr, io.EOF)` and took the error for the stream's
  end.
- **A stream cut where a message part begins.** `arrowmeta`'s message reader (step
  519) answered as arrow-go's `messageReader` does: `io.ReadFull`'s `io.EOF`, wrapped,
  for a part none of whose bytes is there, the length after the continuation marker,
  the metadata or the body. arrow-go's reader takes `errors.Is(err, io.EOF)` for the
  stream's end (`reader.go:268`), so a stream cut there read as the messages before
  it. arrow-go's own message reader does the same (§3.2).
- **A reader's error wrapping `io.EOF`.** arrow-go's stream reader in
  `getInitialDicts` keeps a wrapped one as its error (it tests `r.err == io.EOF`), and
  its file reader answers `could not read message body: EOF` where a `ReaderAt` gives
  none of a block's bytes (`metadata.go:131`, `io.ReadFull` over a section). `arrowsrc`
  took the first for the end, and passed the second on, which the operators above
  took for it.

`internal/source.Decompressed` was not on the path: the inputs are IPC, and IPC's
codecs are arrow-go's (§4).

## 2. The design

### 2.1 The rule, and where it is kept

The engine's end of an input is `errors.Is(err, io.EOF)`, at some sixty sites in
`internal/physical` and `internal/exec`, and the engine's own errors keep `io.EOF` out
of what they wrap (spill's `wrap` says so, step 64). The brief asked whether the test
should accept only a sentinel the sources return. Not at those sites: `extjoin`'s
`nullPadOp` returns `errors.Join(o.Close(), io.EOF)` as its end on purpose, the other
operators hand their child's end on through wrappers, and sixty sites is not a small
change. But every source's batches enter the engine at one place, `physical.scanOp`'s
`Next`, and its schema and opening at two more, `plan.resolveScan` and
`physical.planScan`. So the rule is kept there:

- **A source's end is `io.EOF` itself** (`source.BatchSource`'s doc says so now), as
  `io.Reader`'s is: "Read must return EOF itself, not an error wrapping EOF, because
  callers will test for EOF using ==".
- **Any other error wrapping it is the source's failure.** `scanOp.cut` makes it
  `uerr.Truncated`'s, its `io.EOF` made `io.ErrUnexpectedEOF`, and an `ErrIO` where it
  was none of ursus's errors. `resolveScan` and `planScan` pass a schema's and an
  open's error through `uerr.Truncated` too, so no source error, planned or run, wraps
  `io.EOF`. A plan-time error was never read as an end, but a scan of several files
  opens each after the one before it ends, inside `Next`.

That closes the class for every source, those added later among them. The scan cannot
see a source that swallows such an error itself, taking it for its own end and
answering `io.EOF`, so each source that did is fixed too (§2.3), where the error can
also say what was cut short.

### 2.2 `uerr.Truncated`

`Truncated(err)` is `err` with the `io.EOF` in its chain made `io.ErrUnexpectedEOF`.
`io.EOF` itself becomes `io.ErrUnexpectedEOF`. An `*uerr.Error` is copied with its
cause truncated, keeping its kind, op, message, hints and node, so `errors.As` to
`*ursus.Error` still works. A `*uerr.PanicError` is copied with its value truncated,
keeping where it panicked. Any other error is wrapped in a `truncation`, which keeps
its text, unwraps to `io.ErrUnexpectedEOF` and answers `errors.Is` for every target
but `io.EOF` as the error did. It is step 521's `truncated` (`parquet/dictindex.go`)
for any error, keeping arrow-go's words. Step 521's own is left as it is.

### 2.3 At each source

- **arrowsrc.** A reader's `Err()` ends the stream where it is nil or `io.EOF`
  itself, which a caller's reader may answer; any other error, wrapped or not, is the
  read's failure. `finish`, which every error of a scan's `Next` goes through,
  truncates it, the recovered panic among them. The schema's error is truncated as it
  is cached.
- **arrowmeta's message reader.** A clean end where a message would begin is
  unchanged: an error wrapping `io.EOF` itself, which arrow-go takes for the end of a
  stream whose end marker the format makes optional, and refuses where it wants a
  dictionary, as arrow-go's own. Once a byte of a message is read, an end of the input
  is the stream's refusal, KindIO, wrapping `io.ErrUnexpectedEOF`: "cut.arrows ends
  inside its message 3, in its body". Any other error of the reader is wrapped with
  its `io.EOF` truncated, as arrow-go's reader would take it for the end.
- **NDJSON, JSON and lines** (`internal/source/ndjson`). `objectLines.Read`,
  `blockReader.next` and `lineReader.next` tested `errors.Is(err, io.EOF)` on the
  stream's read; they test `err == io.EOF` now, `io.Reader`'s contract.
- **Parquet.** arrow-go reads a column chunk's first bytes as it opens it, and wraps a
  `ReaderAt`'s `io.EOF` ("parquet: tried reading from file, but got error: EOF"); the
  two "opening column" sites in `reader.go` truncate it. Step 521's page reader sites
  were already right.
- **CSV.** Its scanner's tests are `err == io.EOF` already. It wraps a stream's error
  as "reading near line N", which wrapped a stream's `io.EOF`-wrapping error and was
  read as the end by the operators above; the scan's rule is its fix.

The text of a refusal, from the tests:

```
ursus: scan_arrow: reading the Arrow stream panicked
  at github.com/apache/arrow-go/v18/arrow/ipc.(*ipcSource).buffer (ipc/file_reader.go:584)
  at fuzz.arrow
  caused by: unexpected EOF

ursus: scan_arrow: reading the Arrow stream
  at cut.arrows
  caused by: ursus: scan_ipc_stream: cut.arrows ends inside its message 3, in its body
  caused by: unexpected EOF
```

## 3. The tests and their oracles

All in `internal/readfuzz`, through the public API. The byte-by-byte loops step by
`flipStride`, every byte, and every eighth under the race detector, as step 521's.

### 3.1 The seeds

The two findings in the IPC targets' corpora. Each also runs in
`TestAnIPCInputCutShortIsRefusedOrReadAsArrowGoReadsIt`, which needs both refused by
`Collect` and by `Count`.

### 3.2 `TestAnIPCInputCutShortIsRefusedOrReadAsArrowGoReadsIt`

Every file and stream in `testdata/ipc`, Polars 1.44.1's, Polars 2.1's and pyarrow
25.0.1's, three of each in each codec, and `cutRecord` (eight of the kitchen sink's
columns, a bitmap, fixed widths, offsets, views, a list, a struct and a dictionary)
as arrow-go writes a file and a stream of it in each codec, each cut after each of
its bytes: 81,428 cuts. The oracle is arrow-go reading the same bytes under the same
bounds (`arrowRead`, the IPC targets' oracle). Each cut must be:

- refused by `Collect` (`refusal`): no error wrapping `io.EOF`, none `ErrInternal`, and
  an `ErrIO` where it wraps `io.ErrUnexpectedEOF`; a refusal of malformed Arrow data is
  arrowin's `ErrValue`, as for `ScanArrow`'s records; and `Count` refused too, or
  answering the rows arrow-go's reader reads the records to hold (`arrowRows`, no array
  validated), as `Count` reads no column (step 370);
- or read, by `Collect` and `Count` alike, as arrow-go reads it: no error from arrow-go,
  the same rows, and each cell what arrow-go's arrays hold (`checkIPC`).

A file cut anywhere loses its footer and is refused. A stream cut between messages
ends there for arrow-go and for ursus. Where a message part begins, arrow-go's reader
reads the stream as ending there, and ursus refuses it now. 31 cuts read; on master
121 did, 26 of them answering 0 rows where arrow-go refuses the bytes.

### 3.3 `TestAnIPCBufferCutShortIsRefusedOrReadAsArrowGoReadsIt`

Inside a record, which a cut of a whole file does not reach: each buffer of each
compressed record batch and dictionary batch of each compressed input above, its
length in the batch's metadata declared shorter by each of its bytes, 10,797 cuts,
held to arrow-go as above. A cut inside the eight bytes of decompressed length is
refused by arrow-go slicing past it. One after them decompresses short: arrow-go
panics with `io.EOF` where it decompressed nothing, `io.ErrUnexpectedEOF` otherwise.
One whose frame is whole enough for every declared byte reads. 824 read; on master
1,688 did, 864 of them with no error where arrow-go refuses the bytes: 624 as 0 rows,
240 as 4.

### 3.4 `TestACompressedTextCutShortIsRefusedOrReadAsItsDecoderReadsIt`

A CSV, an NDJSON and a JSON text of 20 rows from a seeded generator (`cutText`: an
integer, a float, text with quotes and commas, a boolean, a date), compressed by
gzip, gzip in two members, Zstandard, Zstandard in two frames, and zlib, each cut after
each byte of its compressed form: 11,258 cuts, 4 of them read. The oracle is a decoder ursus does not
read through: the standard library's gzip, every member, and zlib, and klauspost's
Zstandard `DecodeAll`, every frame. Where it refuses the cut, `Collect` and `Count`
refuse it (`refusal`). Where it reads it, the cut reads as that text reads, a
differential. Cuts start at the bytes `Decompressed` knows a stream by, gzip's two and
Zstandard's four. A zlib stream is trusted only where its first 64 KiB decompress to a
quarter more than they are, or the whole of it to its checksum, which no cut does
(step 346), so the zlib stream is one just past 64 KiB of a text that compresses
little (`zlibPast`), cut after each byte past 64 KiB. A two-member or two-frame stream
cut between them reads as its first. The test passes on master too: the decoders
answer `io.ErrUnexpectedEOF` for a cut, so the text readers had no bug of this class
through `Decompressed`.

### 3.5 `TestAReadFailingWithAnErrorWrappingEOFIsRefused`

A read failing with `fmt.Errorf("the connection was reset: %w", io.EOF)`, which
`io.Reader`'s contract makes an error: a CSV, an NDJSON and a text of lines, each
plain and gzip, through `ScanCSVFrom`, `ScanNDJSONFrom` and `ScanLinesFrom`; an IPC
stream, plain and LZ4, through `ScanIPCStreamFrom`; each failing a quarter, a half
and three quarters of the way. Then a `ReaderAt` answering `io.EOF` for bytes from
the file's middle to its footer, as one over a ranged GET whose body ended early may:
an IPC file through `ScanIPCFrom`, and a Parquet file of five row groups through
`ScanParquetFrom`. Each reads whole, the differential's other side. Each must be
refused by `Collect` and `Count` with an `ErrIO` that does not wrap `io.EOF`. A
Parquet file's `Count` is its footer's, which reads no chunk, as pyarrow's and
DuckDB's (step 521): the whole file's rows, or refused.

### 3.6 Failing on master, passing here

Run on `origin/master` 3627942, after #87, in a detached worktree with these tests
copied in (`~/ursus-scratch-archive/s534/before-master2.log`):

| test | master | here |
| --- | --- | --- |
| `FuzzScanIPCFile` and `FuzzScanIPCStream`, the seeds | FAIL, 0 rows | PASS |
| whole-input cuts | FAIL: 121 cuts read, 82 of them streams cut inside a message, read as the messages before it; 26 read as 0 rows where arrow-go refuses ("could not read continuation indicator / message length / message metadata / message body: EOF"); 38 refused by `Collect` and `Count` with a schema error wrapping `io.EOF` | PASS, 31 read |
| buffer cuts | FAIL: 864 cuts read with no error where arrow-go refuses the bytes, 624 as 0 rows, 240 as 4 | PASS |
| compressed text cuts | PASS | PASS |
| failing reads | FAIL, 45 of 52 outcomes, `Collect`'s and `Count`'s: lines read as the rows before the failure (99, 199 and 300 of 400; gzip 94, 194, 297), IPC streams as 0 or 1,000 of 2,000, plain CSV as 0 of 400 at a half and three quarters, and their `Count` alike; a Parquet file of 1,000 rows read as 400; CSV failing in its schema's sample refused with an error wrapping `io.EOF`; NDJSON refused as "malformed JSON … the string is not closed", the failure read as the end and the line cut short parsed. The IPC file passed: its hole begins inside a block, which `io.ReadFull` answers with `io.ErrUnexpectedEOF` | PASS |

## 4. The audit of the other sources

- **CSV, NDJSON and JSON through `Decompressed`.** Its decoders, klauspost's gzip,
  zlib and Zstandard, answer `io.ErrUnexpectedEOF` for a stream cut short (§3.4).
  `Decompressed`'s own `errors.Is(err, io.EOF)` tests (lines 35 and 60) are short
  reads of the stream's first four bytes and first 64 KiB, where `io.ReadFull`'s
  `io.EOF` and `io.ErrUnexpectedEOF` mean a short stream, and `zlibStream`'s looks for
  bytes after a zlib stream's end; none ends an input, and a stream's error read again
  surfaces at the next read. Left as they are, out of step 528's way.
- **NDJSON, JSON and lines.** Fixed (§2.3).
- **CSV.** Fixed by the scan's rule (§2.3); its own tests were right.
- **Parquet.** The page reader's sites are step 521's. The column-open sites are
  fixed (§2.3). Its other `io.EOF` tests are a `ReaderAt`'s end of the footer read,
  where `io.EOF` with every byte is allowed, and its codecs' own reads, `err ==
  io.EOF`.
- **`ScanArrowRecords`.** Records in memory, read by `memsrc`, which answers `io.EOF`
  itself and reads nothing; nothing to cut.
- **`ScanArrow`.** A caller's reader, through `arrowsrc` (§2.3).

## 5. The fuzz runs

Each target ran for 20 s, `-fuzzminimizetime=3s`, from one coverage-instrumented
binary built at this branch's head (after the merge of #87), under the heavy lock and
`prlimit --as=4294967296`, `-test.parallel 4`, its cache on disk
(`~/ursus-scratch-archive/s534/fuzzcache`, `fuzz-*.log`):

| target | execs | new interesting | outcome |
| --- | --- | --- | --- |
| `FuzzScanIPCFile` | 23,630 | 24 | PASS |
| `FuzzScanIPCStream` | 7,270 | 22 | PASS |
| `FuzzScanCSVChunked` | 74,555 | 111 | PASS |
| `FuzzScanNDJSON` | 81,332 | 81 | PASS |

No new finding.

## 6. The teeth

`~/ursus-scratch-archive/s534/teeth/teeth534.py` breaks part of the fix in the
worktree from a saved copy of each file, builds `internal/readfuzz`'s tests, runs the
four tests and the two IPC targets' seeds, and puts the file back from the copy, its
SHA-256 checked. Never git. Seven of eight bite; the eighth is covered by the scan's
rule by design, and bites with it gone.

| tooth | breaks | fails |
| --- | --- | --- |
| T1 | the scan's rule gone, `scanOp.Next` passing a source's error wrapping `io.EOF` on | failing reads: a CSV stream failing halfway read as 0 of 400 rows, and `Count` 0 |
| T2 | arrowsrc's end test back to `!errors.Is(readErr, io.EOF)` | whole-input cuts, buffer cuts, the stream seed: read as 0 rows, "where arrow-go refuses the same bytes: arrow/ipc: unknown error while reading: EOF" |
| T3 | arrowsrc's `finish` returning its error as it is | none: the scan's rule truncates it, as designed for a source that misses one |
| T4 | T3, and the scan's rule gone | all four tests, both seeds: the file seed read as 0 rows |
| T5 | arrowmeta's `cut` answering `io.ReadFull`'s error wrapped, as before | whole-input cuts, 65 failures, "a stream cut inside a message" read; failing reads: an IPC stream failing a quarter in read as 0 of 2,000 rows |
| T6 | the NDJSON and lines readers' end tests back to `errors.Is` | failing reads: NDJSON refused as "malformed JSON at byte 62: the string is not closed"; lines read as 99 of 400 rows, and `Count` 99 |
| T7 | `uerr.Truncated` returning its error as it is | all four tests, both seeds |
| T8 | Parquet's column-open sites keeping `io.EOF`, and the scan's rule gone | failing reads: the Parquet file of 1,000 rows read as 400, with no error |

The logs are `teeth534.log` and `T1.log` to `T8.log` beside the script.

## 7. Observations and what is left

- **arrow-go reads a stream cut where a message part begins as ending there**, with
  no error (its `messageReader.Message` answers `io.ReadFull`'s `io.EOF` wrapped, and
  its reader takes any error wrapping `io.EOF` for the end). That is arrow-go's to
  fix; ursus's own message reader refuses such a cut now, so ursus refuses some cuts
  arrow-go reads as fewer rows, which the brief allows.
- **A recovered panic's value that is an error still unwraps** (`PanicError.Unwrap`).
  An operator's panic with `io.EOF` in a user's function, `MapElements` say, would be
  an error wrapping `io.EOF` above the scan, where the scan's rule does not reach. No
  case was found where one is read as an end; it is not this step's class, a source's.
- **The engine's sixty `errors.Is(err, io.EOF)` sites are unchanged** (§2.1).

## 8. Checks

After the last code edit, on this branch's head (the merge of `origin/master` 3627942),
`GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`
(`~/ursus-scratch-archive/s534/run534.sh` and its logs):

| check | outcome |
| --- | --- |
| `go test ./internal/...` | ok, 30 packages |
| `go test -timeout 40m .`, under the lock | ok, 203.7 s |
| `cd qcheck && go vet ./... && go test ./...`, under the lock | ok |
| `GOEXPERIMENT=simd go vet ./...` | clean |
| `go run ./internal/gen/levels` | ok, no new package |
| `GOEXPERIMENT=simd go run ./internal/gen/api -check` | 2,481 features, 92 added and 0 gone since v0.7.2 |
| `go run ./internal/gen/testinventory -check` | 1,223 hand-typed lines, the baseline's 1,223 |
| `$(go env GOROOT)/bin/gofmt -l .` | nothing |
