# Step 199 — as built

**P4 of `v0.6.1-scope.md`: the CSV splitter stops being the read's bound.**

## 1. Why

On several threads the CSV reader parses blocks in parallel (step 144), but cuts
them on one goroutine. Cutting a block exactly means scanning every byte with the
scanner's own quote rules, to find where records end and count them.

**Profiled on h2o gb1 over CSV, step 197's code:** the splitter was 0.43 s of each
0.72 s read, at about 43 ns a record over three quoted fields. The parse itself was
2 s of CPU spread over eight threads.

## 2. What changed

**Speculative cuts** (`splitter.specNext`). A stream's blocks are first cut after
the first line ending past the block's size, one `bytes.IndexByte`, with no record
scanned and no rows counted. A block knows neither its first row nor its first line.

**Why it is exact when it works.**

- A block's parse starts where a record starts, provided the block before it ended
  cleanly; the first block starts after the header.
- So a block ends cleanly exactly when its cut fell where a record ends. A cut
  through a quoted field leaves the quote open at the block's end, and its parse
  refuses that.
- So a block that ends cleanly proves the next one's start, and by induction every
  delivered block is the exact path's.

**When it does not work.** A speculative block whose parse fails, from a bad cut or
from any error in it, sends the stream back:

- `nextParallel` gathers that block and every block cut after it, all waiting in
  the window;
- `splitter.resume` puts them ahead of what is still unread, and turns speculation
  off for the rest of the stream;
- the exact splitter takes over, with the rows and lines before the failed block.
  So any error is reported by the exact path, with the row and line numbers it has
  always given.

**The counts.** In speculative mode the splitter's `row` and `line` are the counts
before the next undelivered block. The reader advances them as it delivers each
block, by the rows parsed and the line endings counted (`block.count`, a SIMD
count, in the block's own goroutine). In exact mode the splitter advances them as
it cuts, as before.

**Two things found on the way:**

- **The rest of a stream given back was never read** once the splitter had already
  reached the stream's end and set `splitEnd`. `resume`'s caller clears it now. The
  error tests found it: the error vanished.
- **Step 188's builder sizing** reads a block's row count, which a speculative block
  does not have at its cut. `block.count` sets it from the block's line endings
  before the parse. That is an estimate that only errs high, by blank lines,
  comments and quoted line endings, and `take` copies a slice an eighth unused down
  to its size.

**Reading past the block's size.** The first version read a block to its size
exactly. The line ending after the cut then usually lay past the buffer, so the
buffer was grown and copied, and about a block's worth was copied again as the
next block's start: two serial copies of nearly every byte, 0.27 s over a profile's
three reads. `specNext` now reads 64 KB past the size (`splitSlack`).

## 3. Tests and teeth

- **The existing parallel tests now run through speculation.** They compare serial
  and parallel reads at block sizes 1, 3, 17, 64 and 1 MB, over random files with
  quoted line endings and comments. With line endings inside quotes at every block
  size, they exercise the fallback hard:
  - `TestParallelReadsAsSerialDoes`;
  - `TestParallelErrorsAsSerialDoes`, for errors' rows and lines;
  - `TestParallelMeasuresARecordAsSerialDoes`;
  - `TestABlockAllocatesWhatItHolds`, which now counts as `parseLater` does.
- **`TestSpeculativeCutsHoldOnOrdinaryFiles`:** at 4 KB blocks, 20,000 rows of
  quoted fields with no line ending in them fall back no times. One quoted line
  ending 300 KB in falls back exactly once, and reads as the serial path does. This
  pins the speed, which the comparisons cannot see.

| tooth | result |
| --- | --- |
| the stream given back never cut, once the splitter has ended | **bites:** four tests |
| a failed speculative block's error reported as is | **bites:** four tests |
| rows not advanced on a speculative block | **bites:** the error test |
| lines not advanced on a speculative block | **bites:** three tests |
| a speculative cut at the size, not after a line ending | **bites:** five tests |
| a speculative block's rows not estimated | **bites:** the allocation test |
| the later blocks dropped on a fallback | **bites:** three tests |

## 4. Measured

Step 197's runner against this step's, h2o 10M over CSV, alternated three rounds, one
iteration:

| query | CPU | wall |
| --- | --: | --: |
| gb1 | 4,385 → 4,381 ms, ±0% | 889 → 815 ms, −8% |
| gb4 | 10,893 → 9,985 ms, −8% | 1,596 → 1,397 ms, −12% |
| gb7 | 9,819 → 9,011 ms, −8% | 1,347 → 1,242 ms, −8% |
| j1 | 17,341 → 16,520 ms, −5% | 2,153 → 2,037 ms, −5% |

**The laptop was noisy:** v0.6.0's own gb1 read 716 ms of wall time in the first
measurement and 889 ms in this one.

**What is still serial** is the read itself: each `Read` call's copy out of the page
cache, and Go zeroing each new block. Reading a file's blocks in parallel by offset
needs the source to be an `io.ReaderAt`, which a stream is not. That is left for
later.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
