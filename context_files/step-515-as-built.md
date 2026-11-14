# Step 515 — as built

**The CSV scanner stays linear when a record arrives in small reads.** Step 509
observed it and left it (its §3, "Observed, not a finding"): `scanner.go`'s
`readRecord` parsed a record from its first byte after every refill, and `fill` adds
what one `Read` returns, so a long record sent a little at a time was parsed
quadratically. ursus 0.8's theme is a Go service, and a service reading CSV from a
network peer must not let a slow sender cost it quadratic CPU. The fix asks `parse`
again only when its answer can have changed, linear in every case, and never holds
back a record whose end has arrived. Nothing documented changes: the same records,
the same errors, the same limits.

## 1. The finding

`readRecord`'s comment said the restart from a record's first byte was "O(record)
extra work per refill, and a refill happens once per buffer-full rather than once per
record, so the total stays linear". A refill from a file brings a buffer's worth; one
from a pipe, a socket or a decompressor brings what that read gives. A record of `R`
bytes arriving `k` bytes a read was parsed `R/k` times, each over everything held so
far: `R²/2k` bytes. At 1 KiB a read, parse looked at 129.5 times a 256 KiB record's
bytes and 513.5 times a 1 MiB record's (§3). A record at the default 16 MiB
`MaxRecordSize` would be parsed 16,384 times, about 128 GiB of scanning.

The splitter of the parallel path does not have it: `splitter.read` reads until it
holds what it asked for, and an exact cut that finds no record end asks for twice as
much, so its rescans are geometric. It hands what it will not cut to the serial
scanner (`newScannerFrom(r.split.rest(), …)`), which now has the fix; a block it cuts
is scanned whole (`newScannerOver`, at the end of input), as before.

## 2. The design, and why it is linear

`parse` stays the only judge of where a record ends, read from the record's first byte
each time it is asked, so the class of bug that comes from suspending a quote-state
machine across a buffer boundary stays out of it. What changes is when it is asked.
`readRecord` asks it:

1. **first**, over what is held when a record starts: with a file's buffer that is the
   whole record nearly always, so a file read through the buffer costs what it cost;
2. **when `seekEnd` says the record has ended**, or met an error parse reports;
3. **when the bytes held have doubled** since parse last found no record;
4. **at the end of the input**, and **before `fill` refuses the record as too long**
   (the held bytes fill a buffer at `MaxRecordSize`).

`seekEnd` follows the record's new bytes only, from where it stopped, by parse's own
rules with no fields kept. Its state is the place in parse's reading the next byte
falls at: a record's first byte, after a `\r` that is one (a blank line, or a field's
first byte), a field's first byte, in an unquoted field, in a quoted field, after a
quote in a quoted field (its close or the first of two), after a closing quote and a
`\r`, in a comment line, and done. Three rules make it parse's and not a count of
quotes:

- a quote opens a quoted field only at a field's first byte, as `split.go`'s
  `recordEnd` follows it; a quote in an unquoted field is data, so a count of quotes
  would say a quoted field is open where none is (the test's "quotes inside unquoted
  fields" shape);
- each place checks its bytes in parse's order, so any separator and quote byte reads
  as parse reads it: a quote before a line end before a separator at a field's start,
  a doubled quote before a separator before a line end after a quote, a line end
  before a separator in an unquoted field, and a blank line before anything at a
  record's start;
- a comment is told by its whole prefix, as `bytes.HasPrefix` tells it in parse, so
  while a record holds fewer bytes than the prefix, parse is asked at each read, as
  before: a prefix is a few bytes, so this costs at most the prefix's length squared
  a record.

An unquoted field is followed eight bytes at a time (`wordEnd`), a quoted field and a
comment by `bytes.IndexByte`, so following costs about what parse's own scan does.

**Linear.** `seekEnd` looks at each byte of a record once: its state carries over
between reads and is reset when a record is returned. The parses that find no record
are the first, which looks at no more than the record, and those the doubling asks,
each over at least twice the bytes of the one before, so together fewer than twice the
record's bytes; then the one that finds it. With an exact `seekEnd` that is under
three times the record, and the test measures 3.00 (§3). Were `seekEnd` wrong where
parse finds no record, it stops following (`seekOff`) and the doubling alone asks
parse for the rest of the record: still linear.

**Never holding back a record.** `seekEnd` says done exactly where parse finds a
record or an error (§3's fuzz target holds it to that), so a record whose end has
arrived is returned without another read. Were it wrong the other way, the doubling,
the end of input and the limit rule would still ask parse, and no input parse alone
accepts is refused: the limit rule asks parse before `fill` refuses a record as too
long, so a record within the limit is found first, and `fill`'s error is the same.

**The same answers.** A parse not asked is one that, by `seekEnd`'s exactness, would
have found no record and no error, and parse has no effect but its fields and a cache
of the vector field ends. So every record, line number and error, and the read each
is returned after, is master's.

The doubling is a safety net here: with `seekEnd` exact it costs up to twice a long
record's bytes of parsing, and nothing when a record fits what one read brings.

## 3. The tests

In `internal/source/csv/refill_test.go`, against oracles that are the reads
themselves:

- **`TestALongRecordInSmallReadsIsParsedInLinearTime`**: six shapes a record grows
  long in (a quoted field holding doubled quotes, separators, LFs and CRs; an unquoted
  field; many short fields, a third quoted; quotes inside unquoted fields; a comment
  line; a comment of two bytes), each at 256 KiB and 1 MiB, followed by a short record,
  served 1 KiB a read by `steady` (readfuzz's `scheduled` with one size). The records
  and error are those of the bytes read whole (`newScannerOver`), and two counters on
  the scanner bound the work, never a clock (step 495's pattern): `parsed`, the bytes
  parse looked at, at most four times the input, and `followed`, the bytes `seekEnd`
  looked at, at most the input.

  On master's `readRecord`, with only the `parsed` counter added, it fails at every
  shape and both sizes: **129.5 times** the input at 256 KiB, **513.5 times** at
  1 MiB, 3.9 s. With the fix: **3.00 times** and seekEnd **1.00 times** at every
  shape and both sizes, 0.2 s.
- **`TestARecordWhoseEndHasArrivedIsReturnedWithoutAnotherRead`**: 120 records of
  every shape parse reads (blank lines, CRLF blank lines, comments, unquoted fields
  with a quote or a `\r`, quoted fields with separators, line ends and doubled quotes,
  fields to 3 KiB, LF and CRLF line ends), under four option sets (the defaults; `;`,
  `'` and `#`; a tab and `//`; quoting off and `#!`). For each record, the bytes to
  its end are served 1 to 64 a read by `stalling`, whose next read fails: the records
  returned before that failure are the first k of the whole read's. It passes on
  master, which parses at every read, and holds the fix to never holding back.
- **`FuzzRecordEndMatchesParse`**, a new fuzz target: any bytes, under any separator
  and quote byte, quoting on or off and a comment of up to three bytes, held 1 to 32
  bytes more at a time, record after record: `seekEnd` says done exactly where
  `parse(b, false)` finds a record (at the same end) or an error, but while the bytes
  held are fewer than the comment's. 52 seeds: the encoding/csv corpus with quoting on
  and steps of 1 to 32 among them, and the odd options: a newline separator, a `\r` quote, the
  separator as the quote, a comment holding a quote. `fuzzlist` puts it in fuzz.yml's
  matrix by itself.
- **`BenchmarkALongQuotedFieldInSmallReads`**: §4's measurement, one case.

**Teeth**, each mutation of the fix against the tests above and the existing scanner
targets' seeds:

| mutation | |
| --- | --- |
| master's `readRecord`, parse at every read | **bites:** the linear test, 129.5 and 513.5 times |
| a quote anywhere in an unquoted field opens a quoted field (quote parity) | **bites:** `FuzzRecordEndMatchesParse` ("seekEnd asked parse … which found no record") and the stalling test |
| a `\r` after a closing quote read as inside the quotes | **bites:** `FuzzRecordEndMatchesParse` (`"a"\rb`: parse's error, seekEnd did not ask) and the stalling test |
| `seekEnd` never asked, the doubling alone | **bites:** the stalling test ("sent to the end of record 2: 1 records") |

The existing oracles pass unchanged: `TestScannerMatchesEncodingCSV`,
`TestScannerBufferBoundaries` (buffers of 1 to 31 bytes),
`TestFieldEndsFoundSixtyFourBytesAtATime` (reads of 1 to 100 bytes), the parallel
path's tests, and the CSV tests of the root package.

## 4. Before and after

`BenchmarkALongQuotedFieldInSmallReads`, a quoted field of 2 MiB arriving 1 KiB a
read, five runs each, `GOEXPERIMENT=simd GOAMD64=v3`, the scanner alone:

| field | master | step 515 |
| --- | --- | --- |
| one byte repeated, as step 509 timed it | 61.9 ms | 2.17 ms |
| longShapes' bytes, a doubled quote or a line end every few | 1,518 ms | 12.3 ms |

Step 509 recorded 0.27 s for its 2 MiB field; its harness is not in the tree, and
here the repeated byte takes 62 ms on master, the field whose quotes parse stops at
every few bytes 1.5 s. Either way the ratio is the read count's, 2,048 reads.

## 5. The fuzz runs

`-fuzztime=20s -fuzzminimizetime=3s` each, one at a time under the shared lock, 4
workers, `GOEXPERIMENT=simd GOAMD64=v3`, after the last code edit: every target that
reads through the scanner, and the new one.

| target | inputs | new interesting |
| --- | --- | --- |
| csv `FuzzRecordEndMatchesParse` (new) | 386,888 | 141 |
| csv `FuzzScannerMatchesEncodingCSV` | 130,533 | 27 |
| csv `FuzzSplitMatchesSerial` | 96,247 | 160 |
| csv `FuzzVectorFields` | 181,334 | 61 |
| readfuzz `FuzzScanCSVChunked` | 66,026 | 103 |
| readfuzz `FuzzScanCSV` | 40,223 | 95 |

None failed: 0.9 million inputs. `FuzzScanCSVChunked` reads through schedules of
empty reads, reads of 1 to 127 bytes and a last read with `io.EOF`, against the bytes
whole, and `FuzzScannerMatchesEncodingCSV` and `FuzzVectorFields` scan through
buffers of 1 to 512 bytes and a few bytes a read: each is now `seekEnd`'s path at
nearly every read. Step 511's Zstandard window allocation (`source.Decompressed`) was
not met.

## 6. h2o over CSV

One query, as the brief asks: h2o `gb1` over CSV (`-io csv`, 10 million rows, 4
threads, 3 iterations a run), master's runner (16a5b29) against this step's, both
built with `GOEXPERIMENT=simd GOAMD64=v3` from a worktree each, alternating, under the
shared lock. Other agents' builds and tests kept the laptop's load at 4 to 6, so three
runs were made:

| run | master median | step 515 median | ratio |
| --- | --- | --- | --- |
| 3 rounds, load 6.5 to 6.8 | 1,243 ms (1,204–1,644) | 1,581 ms (1,148–2,100) | 1.27 |
| 5 rounds, load 4 to 6.5 | 912 ms (522–1,007) | 941 ms (881–1,022) | 1.03 |
| 7 rounds, started at load 1.95, wall per iteration | 919 ms (508–1,051) | 892 ms (509–1,110) | 0.97 |
| the same, CPU (user+sys) per run of 3 | 23,969 ms | 23,551 ms | 0.98 |

The first run's spread, 1,148 to 2,100 ms on one side, is the load's; the quietest
run and its CPU time show no slowdown, and the fastest iterations, 508 and 509 ms,
agree. None was expected: h2o's file is cut into blocks scanned whole at the end of
input, where `readRecord` asks parse at once (`s.eof`), and the serial scanner that
reads the header and the sample reads through a 64 KiB buffer, where the first parse
of a record finds it nearly always. What a record costs now beside parse is a
comparison and an addition more, and three words cleared.

## 7. Checks

The gate, after the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4
GOEXPERIMENT=simd GOAMD64=v3`:

- `go test ./internal/source/csv ./internal/readfuzz`: ok, also with `GOEXPERIMENT=`
  (scalar, no vector field ends); `-race` and the widths left to CI;
- `go test ./internal/...`: ok;
- `go test .` in full, under the lock: ok, 250 s;
- `GOEXPERIMENT=simd go vet ./...`: clean;
- `go run ./internal/gen/levels`: ok;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2, as on master;
- `go run ./internal/gen/testinventory -check`: 1,225 lines, as in the baseline (the
  new tests' expected values are the whole read's, encoding/csv's corpus's and
  parse's own, and the bounds are counts);
- `gofmt -l .`: empty.
