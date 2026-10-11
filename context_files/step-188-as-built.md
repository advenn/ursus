# Step 188 — as built

**The CSV reader's copies, the midpoint audit's §4.6.** The audit said its builders
finish through `NewFixed` and `NewStringParts`, which copy, and that step 163's
owned constructors apply wherever a builder does not reuse its slices.

## 1. What was found

There are two paths, and only one reuses its builders:

- **Serial,** one thread or `MaxRows`: one set of builders for the reader. Each
  batch's column is copied out of them and the slices are reused for the next batch
  (step 136). The copy is what makes the reuse safe, so this path keeps it.
- **Parallel,** the default on several threads: each block of about 1 MB gets
  builders of its own, parses into them, finishes them once, and drops them. Here
  the copy bought nothing.

The copy was the smaller cost. The builders started empty and grew by `append`,
about a quarter at a time once large, so each value was allocated about five times
before the copy made it six. Reading h2o's 2M-row group-by CSV on four threads:

| | allocated | the columns |
| --- | --: | --: |
| before | 1,041 MB | 251 MB |
| after | 315 MB | 255 MB |

Of the 1,041 MB:

- 408 MB was the fixed-width builders growing;
- 313 MB was the String builders growing;
- 164 MB was the copies;
- 108 MB was the file's bytes, which the splitter reads once.

A block's scanner also read the block through a 64 KB buffer of its own, a second
copy of the block's bytes.

## 2. What changed

- **A block's builders are sized once.** The splitter counts a block's rows before
  the block is parsed, so the new `size(rows, chars)` makes each builder's values,
  offsets and validity exactly that long.
- **Text is sized by estimate.** A String column's characters cannot be counted
  ahead, so they are sized from the bytes a row of that column held in the last
  block parsed (`reader.textRate`, one atomic per column), plus a sixteenth. Before
  any block has finished, the estimate is an average field of the block itself.
- **A block's columns take their builders' slices.** The new `take` is `finish` for
  a builder used once: `NewFixedOwned` and `NewStringOwned`, with no copy.
  - A column holds all of what it takes, for as long as the query holds it. So a
    slice with more than an eighth unused is copied to its size first
    (`wasteful`). That happens where the estimate was well over, or where `append`
    grew a slice past it.
  - A Bool column's bitmaps were never copied.
- **A block is scanned in place,** by `newScannerOver`, with no buffer and no copy.
  - The scanner's buffer is moved only by `fill`, which is not called at the end of
    input, so the block is never written.
  - `MaxRecordSize` was enforced only by `fill` refusing to grow the buffer past it,
    and a block's scanner no longer has a buffer. The splitter refuses an oversized
    record only when it has not found the record's end, and here it had found it.
    `TestParallelErrorsAsSerialDoes` failed on the first run.
  - So `readRecord` now measures each record. A record with its line end must fit
    the limit, and a last one without a line end must leave a byte, which is where
    `fill` learns the input ended. A serial scanner's buffer never holds more than
    the limit, so the check fires only for a block's. The error moved into
    `tooLong`, which both use.
- **The serial path is unchanged.**

## 3. Tests and teeth

- **`TestABlockAllocatesWhatItHolds`:**
  - Blocks of 256 KB are taken from the splitter and parsed one after another,
    through `parseBlock`.
  - The frame has Int64, Float64 and Bool columns and three Strings. Two of the
    Strings change width halfway, one from 20 bytes to 1 and one the other way, so
    the blocks there are sized from a block of the other width.
  - Each column of each block may hold what it needs, an eighth more, and 64 bytes a
    buffer.
  - All the blocks together may allocate what their columns hold, a fifth more. It
    reads 1.16×.
  - `TestReadingAllocatesAboutWhatItProduces` begins measuring after the first
    window of blocks, and its file is parsed by then, so it never saw this path.
- **`TestParallelMeasuresARecordAsSerialDoes`:**
  - records of 62 to 65 bytes against a limit of 64;
  - in the middle of the input and last, with a line end and without, and with
    CRLF;
  - serial against parallel at every block size;
  - and the limit itself: 64 bytes with the line end are read, and 65 refused. A
    comparison cannot see a limit that both paths move.

| tooth | result |
| --- | --- |
| a fixed column copied out, as before | **bites:** the allocation |
| a fixed builder not sized | **bites:** the allocation |
| a String column copied out, as before | **bites:** the allocation |
| characters over their estimate kept | **bites:** the column `w` holds too much |
| text sized from the block's average field alone | **bites:** the allocation |
| a block copied into a scanner buffer of its own | **bites:** the allocation |
| a block's record never measured | **bites:** both record tests |
| a last record at the limit, unterminated, taken | **bites:** the record test |
| a record at the limit refused | **bites:** the limit itself; the comparison alone was silent |

**Time.** The two readers were run alternately on h2o's 2M rows, five reads each,
three rounds. The laptop warmed through the runs:

- the first round reads 315 ms before and 396 ms after, with the new reader's own
  reads rising from 274 to 419 ms within the round;
- the next two read 460 against 405 ms.

So the allocation is the result, and the time is no worse.

**Left:** `Column.Slice` copies a String column's offsets, since it does not rebase
them, and the parallel path slices each block into batches. That is 24 MB of the
315 MB.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
