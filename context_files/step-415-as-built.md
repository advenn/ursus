# Step 415 — as built

**A stream's empty read is not its end.** H3 of `v0.7.2-scope.md` (found by H4's
reading), the plan's 425 with the critic's correction.

## 1. What was wrong

`io.Reader`'s contract has a read of no bytes and no error mean that nothing happened,
not the end. A stream a caller hands over, the body of a GET through `ScanCSVFrom` or
`ScanNDJSONFrom`, may do it while it waits.

- **CSV cut the stream short, silently:** its scanner (`scanner.go`) and its splitter
  (`split.go`) took the first empty read for the end of the input. A CSV of 1,000 rows
  from a stream that paused between its reads read as 0 rows, and no error.
- **A stream that never moved hung the read:** `source.Decompressed`'s `io.ReadFull`,
  which every CSV, NDJSON and JSON stream goes through first, and NDJSON's block
  reader, which looks at no context, spun for good.

## 2. The change

- **`progressing`** (`internal/source`), around every stream `Decompressed` opens:
  an empty read is passed on, and the hundredth in a row fails with
  `io.ErrNoProgress`, as bufio's readers do, so no loop around a stream spins.
- **The CSV scanner and splitter read again** on an empty read, where they stopped.

## 3. Tests

- **`TestAnEmptyReadIsNotTheEnd`** (root): CSV and NDJSON from a stream that hands back
  three bytes a read with an empty read between read 1,000 rows, on all threads and on
  one; a stream that never moves is an error wrapping `io.ErrNoProgress`, within 30 s.
  Before the change CSV read 0 rows and then hung on the stuck stream.

**Teeth: 4 of 4 bite:** the wrapper gone hangs the stuck stream; the scanner's empty
read as the end reads 0 rows on one thread, which the test reads once the one-thread
case was added; the splitter's reads 0 rows on all threads; a count that is never
reset fails the pausing stream after its hundredth pause.

**Gate:** the source packages' tests; the root package's CSV, NDJSON, JSON, stream and
compression tests; vet and levels. CI's every width, the scalar build and the race
detector on the pull request.
