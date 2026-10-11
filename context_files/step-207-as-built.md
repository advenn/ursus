# Step 207 — as built

**O2 of `v0.6.2-scope.md`: an unkeyed `Rolling` or `GroupByDynamic` hands its windows
out as they close.**

## 1. Why

Step 196 streamed a temporal group's input, and the group still held its answer to
the end. A rolling group's answer is a row for every input row, many times its
input with a few aggregates. In step 196's test, the 300,000-row rolling case
peaked at about twice its answer, 20 MB.

With no key there is one bucket, and its windows close in the order they are made.
Both ends of a window rise from one window to the next, so a row past one window's
upper end is past every earlier one's, and `place` closes them from the front.
Every window made before the oldest one still open is final once its rows are fed.

## 2. What changed

- **Segments** (`tempsegment.go`):
  - the windows go to segments of `chunk()`, a batch's worth, each with aggregates
    of its own, since an aggregate cannot hand out some of its groups and keep the
    rest (`newAccs` makes them as the planner did);
  - `feed` gathers the queued pairs once, then gives each segment its own: almost
    always one, and then the pairs go as they are; otherwise each segment's are
    picked out;
  - window ids stay global; `wBase` is the first window the per-window arrays
    still hold.
- **`handOut`,** after every `feed`: a segment is finished and queued once it is
  complete (its count of windows, or a later segment exists) and its last window is
  older than the oldest still open. The per-window arrays then drop it, into arrays
  of their own size. At the end of the input, every segment goes.
- **`tempStreamOp`** replaces the breaker for an unkeyed group. It feeds the sink a
  batch at a time and gives out what `handOut` queued. An index that goes backwards
  is refused at that batch, not at the end.
- **A keyed group keeps one segment of every window,** and its `Finish` is as it was:
  its answer is per bucket, each bucket's windows in order, and the last row may
  still add to the first bucket.
- **A rolling group's kept rows** point into the batches they came in. They were
  gathered into one batch once eight were held. They are now also gathered whenever
  they are under an eighth of a batch, which is cheap. A group that hands its answer
  out holds little else, and eight batches for thirty rows in reach were most of it.

**Found on the way.** The first version handed out a segment only after the batch
that closed it. By then the next segment's windows were held as well, and `feed`'s
charges in mid-batch saw both: 745 KB held against a 626 KB limit. Handing out from
`feed`, as soon as the last window's rows are in, brought that to 670 KB.

## 3. Tests and teeth

- **`TestUnkeyedTemporalGroupsHandOutTheirWindows`:** each case against the same
  group keyed by a constant column, which still holds its answer:
  - a 30-minute rolling group; a 2-hour one reaching 15 minutes forward, closed both
    ends; a dynamic group every hour over 3 hours, with boundaries, labelled by a
    row; one every 10 minutes, offset 20 minutes back, closed neither end;
  - over 200,000 rows, under a tenth of their bytes, 427 KB, at batches of 2,048:
    the rolling groups peak at 190 and 206 KB, for an answer of 6.5 MB;
  - over 3,000 rows at batches of 1, 7 and 64, with `Implode` beside the bounded
    aggregates, so segments end everywhere.
- **`TestAnUnkeyedRollingGroupHoldsABatchsWorth`** (physical): forty batches of 256
  rows, a 30-minute window. After each batch the sink holds at most two batches and
  two segments.
- **Step 196's tests** still pass. The golden test's 120 configurations, run at
  batches of 64, match what the buffered sink answered. In
  `TestTemporalGroupsStreamTheirInput`, the unkeyed 30-minute rolling group now peaks
  at 0.73 MB unlimited; its keyed twin peaks at 21.4 MB.
- **`TestDynamicWindowStateIsCharged`** takes a key, the same in both rows: it
  measures what a group holding its windows charges, and an unkeyed one no longer
  holds them.
- **`TestFinishedGroupingsDropTheirState`** looks under a `tempStreamOp` too, and
  runs the rolling case with and without a key.

**Why a tenth needs batches of 2,048 here.** What an unkeyed group holds is a
batch's worth, whatever its input: the batch, a segment of as many windows, and
their pairs. At 8,192 rows a batch that is about 700 KB, more than a tenth of this
6.4 MB input, though not of a larger one.

| tooth | result |
| --- | --- |
| never streams | **bites:** the peak, and the holding test |
| a segment handed out with a window open | **bites:** four test families |
| the newest open window taken for the oldest | **bites** |
| a later segment's windows numbered from the first | **bites** |
| a segment's pairs not picked out | **bites** |
| `wFed` not moved with the windows | **bites:** the golden test |
| `wBase` not advanced | **bites** |
| a pair's window read without `wBase` | **bites** |
| a few kept rows not gathered | **bites:** the holding test, added for it |
| segments handed out only at the end | **bites:** the peak |
| a complete last segment kept to the next batch | silent: the answers are the same, and the segment is held until the next window is made, which opens the next segment and lets it go; only a gap in the rows makes that wait long |
| an empty answer handed out as nothing | silent, and equivalent: Collect builds the same empty frame from the schema |
| the state kept after the end | silent, and equivalent: `handOut` drops the last segment itself; the line in `finishStream` only matters for an input whose last window closed before it ended, which the last row's own window prevents |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. `lazy.go`'s doc says
what an unkeyed group now holds.
