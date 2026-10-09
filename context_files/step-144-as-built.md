# Step 144 — as built

**Item 5 of `v0.5-scope.md`: the CSV reader parses a stream on several goroutines.**
Step 141's profile confirmed it: h2o `gb1` and `gb4` over CSV waited on one goroutine.

## 1. Evidence first

- **`gb1` kept 1.4 cores busy, and `gb4` 1.6.**
- **The scanner, `scanner.Next`,** was 56% and 34% of their CPU, on one goroutine
  behind the reader's lock.
- **Converting fields was split by column** (`convertAll`): two ways at most for
  `gb1`'s two columns.

## 2. What changed

### The splitter (`internal/source/csv/split.go`)

It cuts a stream into blocks of whole records, about 1 MiB each, and counts each
block's data rows and physical lines. A block's first row is then known before it is
parsed.

**It follows the scanner's own rules, not quote parity.** The scanner reads a quote
inside an unquoted field as data, so a stray one flips any parity, and a newline
inside a later quoted field would read as a record's end. So the splitter applies
the scanner's rules:

- a quote opens a quoted field only where a field starts: at the record's start, or
  just after a separator;
- a doubled quote is data;
- after a closing quote comes a separator or a line ending.

It skips comment lines whole, and counts a blank line as a row only in a one-column
file, as the scanner does.

**Anything it does not fully understand, it does not cut:**

- a character after a closing quote;
- a quote still open at the end of the stream;
- a record longer than `MaxRecordSize`.

It stops before that record, and the reader hands the rest of the stream to the
serial scanner (`rest`), which reports it exactly as it always has.

### The reader (`csv.go`, `parallel.go`)

**Blocks parse in the background:**

- each on a goroutine of its own from the moment it is cut, up to twice the thread
  count at once;
- `Next` waits only for the oldest, so cutting overlaps parsing;
- each block has its own scanner and builders;
- the batches are delivered in file order, cut to the batch size;
- `Close` waits for every block still parsing.

**The record path is shared, so the two paths cannot drift:**

- `appendRecord` became `appendTo(sc, builders, wanted, row)`;
- the error builders take their row and line;
- the non-nullable check became `checkNonNullable`.

**A block's scanner is built by `newScannerFrom`,** which skips the byte-order-mark
check: those three bytes are a mark only at the start of a file. The serial path
takes over through it too.

**When the parallel path runs:**

- not with one thread, which reproduces the serial path exactly, as
  `WithThreads(1)` promises;
- not under `MaxRows`, which reads only the rows asked for;
- with several streams, a new splitter takes over at each stream's header.

**Errors:**

- A block's error is delivered after the blocks before it. It names the same row
  and line as the serial path's.
- A read error mid-stream, and a scanner's error in a block, name the stream, as
  the serial path's do.
- **One difference:** the good rows of the failing block, before its bad record, are
  not delivered, where the serial path delivers the full batches before it. How many
  rows arrive before an error already depended on the batch size, and `Collect`
  discards them.

### First versions, measured and replaced

1. **Rounds** cut every block of a round before parsing any, and waited for the
   slowest before cutting more: `gb1` −8 to −30%, `gb4` −22%.
2. **A sliding window** fixed both, giving the result below.

A first block took everything the splitter held: the scanner had buffered a small
file whole while it read the header. `boundary` now stops at the block size.

## 3. Measured

Alternated against step 143's commit, two rounds of three iterations, ten million
rows:

| query | before | now | change | peak RSS |
| --- | --: | --: | --: | --- |
| `gb1`, CSV | 1,797–1,913 ms | 1,039–1,154 ms | −40% | 37 → 107–122 MB |
| `gb4`, CSV | 2,830 ms | 1,745 ms | −38% | 38 → 128–133 MB |

**The RSS is the window,** sixteen blocks of 1 MiB and their batches.

**What bounds it now is the splitter:** 0.6 s of `gb1`'s 1.04, serial, about 60 ns a
record for the IndexByte hops between quotes and newlines. `gb1`'s CPU went from 1.4
cores to about 4.

At step 127's Polars timings, `gb1` over CSV goes from 5.3× to about 3×, and `gb4`
from 7.0× to about 3.9×, estimated on two different days.

## 4. Tests

- **`TestParallelReadsAsSerialDoes`** reads each input serially and on four threads,
  with blocks of 1, 3, 17 and 64 bytes and 1 MiB, and requires the same rows, values
  and nulls. Every column is read as a String, so field text is compared exactly. The
  inputs:
  - **forty random files:** quoted fields with a separator, a newline, a CRLF or a
    doubled quote; empty and quoted-empty fields; quotes inside unquoted fields;
    blank lines; CRLF endings; the last record with and without its newline;
  - **twenty more with comment lines** that hold quotes;
  - **edge cases:** a one-column file whose blank lines are nulls; ragged rows,
    truncated; four streams, one reordered and one empty; a field beginning with a
    byte-order mark mid-file.
- **`TestParallelErrorsAsSerialDoes`** requires the same error, both ways, for:
  - a bad value two hundred rows in;
  - a ragged row;
  - an unterminated quote;
  - a character after a closing quote;
  - a record past `MaxRecordSize`;
  - a null in a non-nullable column;
  - a bad value after comment lines holding quotes;
  - a bad value after blank lines in one column.

  The rows delivered before an error must be the file's from its start.
- **`TestTheSplitterCutsWellFormedQuotingWithoutStopping`:** well-formed quoting is
  cut at every block size without the splitter stopping.
- **`TestThreadsReachTheBlockParser`** replaces `TestThreadsReachTheConverter`. Through
  `Open`, four blocks parse at once. The converter now runs only where the serial path
  takes over, and its unit tests stay.
- **Under `-race`:** all of them, clean.

## 5. Teeth

| tooth | result |
| --- | --- |
| a blank line never a row | **bites:** the errors test |
| a block read as the start of a stream | **bites:** the reads test, at the byte-order mark |
| a block takes all the splitter holds | **bites:** the block-parser test |
| a quote anywhere opens a quoted field | **bites:** the no-stopping test |
| comment lines followed like records | **bites:** the errors and no-stopping tests |
| a doubled quote read as a closing one | **bites:** the no-stopping test |
| a block's scanner error not named for its stream | **bites:** the errors test |

**Three of them were silent against the read and error tests at first, and that is
the design working.** A splitter that misreads a quote or a comment reaches a record
it calls odd and hands the stream to the serial path, so the answer stays right; only
the parallelism is lost. The no-stopping test is what sees that.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. PDS-H reads Parquet, which this step did not touch.
