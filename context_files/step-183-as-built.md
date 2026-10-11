# Step 183 — as built

**R1 of `v0.6-scope.md`, its second half: `JoinAsOf` streams, for the backward
strategy.**

## 1. Evidence

`asOfBuildSink` read the right side whole:

- every batch was retained and charged to the budget;
- the right side was concatenated and bucketed by its by-key;
- the left side then streamed, a binary search per row into its bucket.

Its memory was O(right rows).

**Which strategies can stream.** A backward match is the latest right row at or
before the left key, in the left row's by-bucket. Both sides are sorted on the key,
so walking the right side up to each left key in turn leaves each by-key's latest row,
which is the answer. A forward or nearest match is the first right row at or after
the left key, and in a by-bucket that row may lie anywhere ahead, or nowhere: finding
it can mean reading the whole right side. Backward is the default, and the one that
streams.

## 2. What changed

**`asOfStreamOp`** (`asofstream.go`) is planned for a backward join. It pulls both
sides itself, as step 182's merge does.

- **`advance(k)`** reads right rows up to the left key, at or before it, or strictly
  before it without exact matches. Each row becomes its by-key's `latest`: a `rowRef`
  into `parts`, the right batches still pointed at, and its key in `lkey`.
- **`byIDs`** numbers each right batch's by-keys as they appear. A null by-key gets no
  id and so is in no bucket, as the buffered join's bucket map left it.
- **`match`** gives each left row its by-key's latest row, if the tolerance admits it
  (`withinTolerance`, the buffered join's own rule). It gathers the matched rows from
  `parts` with `gatherRows` and builds the output with `gatherOut`, as before.
- **`compact`:** as the right side moves on and `parts` reaches 8 batches, it gathers
  every row still pointed at into one batch, and moves both sets of `rowRef`s to it.
  Those are the by-keys' latest rows and the rows matched for the output being built,
  since one left batch can span many right batches.
- **Order is checked as rows arrive.** When the left side ends, the rest of the right
  side is read for its order alone, so an unsorted right side is refused as the
  buffered join refused it, whatever the left side holds.
- **The budget** is charged for the batches held, and checked.

Forward and nearest keep `asOfBuildSink` and `joinBreaker`, and `JoinAsOf`'s doc says
which strategy holds what.

**A first version compacted only between left batches.** Over one left batch of
1,000 rows spanning 74 right batches it held them all: a peak of 6.3 MB against the
right side's 7.2 MB. Compacting as the right side moves, with the pending output
remapped, brought that under 1 MiB.

## 3. Tests

- **`TestBackwardAsOfStreamingAgreesWithAScan`:**
  - 300 left rows and 900 right rows, sorted with ties, by-keys with nulls on both
    sides;
  - exact matches on and off, by and no by, and a tolerance in ticks;
  - batch sizes 1, 4, 64 and 4096, the smaller ones compacting mid-batch many times;
  - every left row's match against a scan of the right side.
- **`TestBackwardAsOfHoldsARowAByKey`:** 300,000 right rows over ten by-keys, 7.2 MB,
  with a peak under 1 MiB.
- **`TestBackwardAsOfStillRefusesAnUnsortedRightSide`:** a backward row in a right
  batch that only the final read reaches.
- **Two memory cases in `TestSpillingByHand` pinned the buffered join:** "charged for
  the copy Probe makes", and "refuses between one and two right sides". The first
  gate failed on both, since a backward join no longer holds what they measured. Both
  now use the forward strategy, which still buffers. The second also checks that a
  backward join answers, in full, under the same limit.
- **The existing as-of tests**, now through the streaming join for backward, pass:
  strategies, exact matches, by-keys, tolerances calendar and fixed, temporal units,
  unsorted inputs.

| tooth | result |
| --- | --- |
| the pending output not moved by compact | **bites:** the scan comparison and the memory test |
| exact matches always taken | **bites:** the scan comparison and `TestAsOfAllowExactMatches` |
| the tolerance not asked | **bites:** the scan comparison and four tolerance tests |
| the right side's rest not read | **bites:** the unsorted right side, once its backward row was out of the matching's reach; it was silent before |
| never compacting | **bites:** the memory test |
| a null right by-key in a bucket | **silent by construction:** a left row with a null by-key is skipped before any lookup, so that bucket is unreachable; the −1 only saves an id |

**Gate,** run twice: the first failed on the two memory cases above; with them moved, test-all
115 ok, race 23 ok, levels, vet ×3 and the bench engine tests clean. **PDS-H at SF=0.1:**
all 22 answers match DuckDB's.
