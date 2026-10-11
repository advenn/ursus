# Step 197 — as built

**O2 of `v0.6.1-scope.md`: a window with no partition_by over its budget.**

## 1. Why

A window spills by routing its rows to files by their partition key (step 82), and
a window with no `partition_by` is one partition, so past its limit it refused.
Much of what is asked of such a window needs only a bounded part of the frame for
each batch:

- **an aggregate broadcast back to the rows,** `x - x.Mean().Over()`, is one value,
  and its accumulator is already fed as the rows arrive;
- **a function that reads rows in order** needs a bounded context: `Shift(n)`, n rows
  before or after; a rolling window of N rows, N before and, centred, after; a fill
  with a limit, that many.

## 2. What changed

**`winstream.go`.** Past the limit, a window whose partitionings all have no keys,
and whose every function has a bounded need, writes its rows to one spill file
(`startOneSpill`), the rows held so far first. Later batches go after them, and its
aggregates are fed as before, every row in group 0 (`consumeOneSpill`). What stays
in memory is the aggregates' state.

**`Finish` returns `winStreamOp`.** It reads the file back a batch at a time, the
input's columns and then each window column (`answer`):

- **an aggregate:** its one value, taken to every row;
- **`cum_count`:** numbered on from the rows before;
- **a cumulative function, and a forward fill without a limit:** they carry their
  last answer that held a value. It leads the next batch, cast to its type: an Int32
  sum's batch is taken to Int128, the type the kernel sums in. Then the kernel
  continues from it, and the leading row is sliced off (`carried`);
- **the rest:** the ordinary `kernel.WinCall` over the batch with its context, and
  the batch's answer sliced out:
  - the last h rows before it, kept from batch to batch;
  - the first l rows after it, from the batches read ahead, which is why the
    operator reads `maxL` rows past the batch it answers.

**`streamNeed` gives each function's need:**

| function | need |
| --- | --- |
| an aggregate | none |
| `cum_count` | the count so far; refused when reversed |
| `cum_sum`, `cum_prod`, `cum_min`, `cum_max` | the last answer; refused when reversed |
| `Shift(n)`, `Diff` | n rows before, or after for n < 0 |
| `ForwardFill(k)` | k rows before; with no limit, the last answer |
| `BackwardFill(k)` | k rows after; refused with no limit |
| a rolling window of N rows | N before, and N after when centred; refused by time |
| rank, ewm, interpolation, an ordered window | refused |

**A larger context than a function needs does not change its answer.** A rolling
window's row reads only its own rows, and the frame's own start truncates it
exactly as before.

**What still refuses, and says so:**

- a window with a `partition_by` whose one partition is over the limit;
- a mix of keyed and unkeyed partitionings;
- the refused functions above.

The refusal now lists what streams. The budget's hints and `LazyFrame`'s memory doc
say the same, and also note that `Rolling` and `GroupByDynamic` stream their input
since step 196.

## 3. Tests and teeth

- **`TestUnpartitionedWindowsStreamPastTheirLimit`:** 20,000 rows in batches of 64,
  under 16 KiB, frame-equal to the unlimited answer, with:
  - aggregates of an integer, a float and a string;
  - cumulative sums (one an Int32 widened), a max of floats, a min of strings, and a
    count;
  - a lag of 2, a lead of 3 with a fill, and a `Diff`;
  - forward fills with and without a limit, and a backward fill with one, over runs
    of nulls of 130 rows, longer than two batches;
  - rolling sums, a centred mean, and a minimum with `MinSamples`.
- **`TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse`:** rank, a reversed
  cumulative sum, an ewm mean, interpolation, a backward fill with no limit, and a
  rolling window by time. Each refuses, naming the window and its one partition.
- **The refusal cases moved to a rank.** `TestBudgetErrorNamesTheOperator`'s `over`
  case and `TestSpillingByHand`'s "Over() with no keys" case pinned the refusal with
  `Sum().Over()`, which now streams.

| tooth | result |
| --- | --- |
| a cumulative function's last answer not carried | **bites** |
| the rows before a batch not kept | **bites** |
| no rows read ahead | **bites** |
| the aggregates not fed past the limit | **bites** |
| the rows held before the limit not written | **bites** |
| `cum_count` starting over each batch | **bites** |
| a reversed cumulative function streamed | **bites:** the refusal test |
| an unlimited backward fill streamed | **bites:** the refusal test |
| a rolling window by time streamed | **bites:** the refusal test |
| a centred rolling window given no lookahead | **bites** |

**`TestSpillingByHand`'s hint case** failed the gate's first run: it checked the
hint's old phrase, "unique and a partitioned over spill". The hint now says "unique
and over spill" and names the unpartitioned windows that still fail, and the case
checks that.

**Gate,** the second run: test-all 115 ok, race 23 ok, levels, vet ×3 and the bench
engine tests clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
