# Step 209 — as built

**Tier 2 of `v0.6.2-scope.md`: a keyed window's one huge partition streams.**

Step 208, the 0.6.2 report, waits for h2o to be measured, which the machine's
memory stopped. This step is Tier 2's first out-of-core item, done meanwhile.

## 1. Why

A window with a `partition_by` spills by routing whole partitions to disk, a level
at a time. A sink a level down that still went over while holding one key refused:
routing divides partitions, not the rows of one. A skewed key, most of a frame's
rows, made the whole window refuse.

Step 197 had already streamed a window with no `partition_by`, one partition, when
every function needs only a bounded part of it: an aggregate, a cumulative function,
a shift, a limited fill, a rolling window of rows. One key's rows, in their order,
are the same problem.

## 2. What changed

**`winskew.go`.** A sink a level down splits when four things hold (`skewable`): it
has one keyed partitioning, it holds a single key so far, every function's need is
bounded, and there is a limit.

- **`startSkew`** sends the key's rows held, and its later rows, to the
  one-partition file (`startOneSpill`, `consumeOneSpill`). Its aggregates are fed as
  group 0, which the key already was.
- **Every other key's rows** go to `rest`, a fresh sink a level deeper, which may
  spill as any does.
- **`finishSkew`** merges the key's answer, read back a batch at a time with its
  bounded context, and the rest's, both in ordinal order, on the ordinal.
  `mergeByOrdinal` is split so that `mergeRuns` takes runs, and `opRun` makes an
  operator one, so neither answer is written to disk again.
- **A function that needs the whole partition,** a rank, an ewm, an interpolation or
  an ordered window, still refuses. The refusal now says which functions stream.
- **The `over` hint and `lazy.go`'s doc** say that one partition larger than the
  limit goes to one file too.

## 3. Tests and teeth

- **`TestAKeyedWindowStreamsItsOneHugePartition`:** 20,000 rows, the first 12,000 of
  one key, then every other row one of 500 other keys. Six windows over the key: a
  sum, a cumulative sum, a shift, a rolling mean, a limited forward fill and a
  cumulative count. Under a tenth of the input, on one thread and on four:
  - the answer is the in-memory one, row for row;
  - something spilled;
  - the spill directory is empty after.
- **`TestASplitWindowsOtherKeysSpillTheirOwnWay`** (physical): a query cannot choose
  which keys share a file, so a sink a level down is driven directly. It gets 15,360
  rows of one key alone, then 40,960 that mix in 300 others, under 256 KiB. It
  splits, its other keys' sink spills too, and every row comes back in ordinal order
  with its key's sum, nothing left on disk.
- **`TestSpillingByHand`:** one partition larger than the limit now streams its sum
  and cumulative sum, and refuses a rank, saying "one partition".

| tooth | result |
| --- | --- |
| never splits | **bites** |
| a function needing the whole partition split off | **bites:** the rank refusal |
| every row sent to the key's file | **bites:** after the fixture held the key alone first |
| the key's later rows sent to the rest | **bites** |
| the two answers concatenated, not merged | **bites** |
| the rest not closed | **bites:** the driven test, whose other keys spill |
| the rest a level no deeper | silent, and equivalent: each level mixes its own number into the hash, so the rest re-routes at any level; only the depth cap differs |

**Three teeth were silent at first.** The first fixture interleaved other keys from
its first rows. The levels below then separated them before a sink ever held the
key alone, and the split's other-keys path never ran.

## 4. Found, and not fixed here

- **A batch of a frame built with `ursus.Frame` is charged for the whole frame.** The
  batches are slices of its columns, and a buffer is charged whole. A memory test
  whose input is one frame sees a peak about the size of its input, whatever the
  operator does. Forty frames of 500 rows avoid it.
- **A spilled keyed window peaks well past its limit.** Over 20,000 rows spread
  across 1,000 keys, it peaked at 971 KB under a 121 KB limit, before this step and
  without any skew. The sixteen partition writers hold only 128 KB. The likeliest
  cause, not measured, is the merge on the ordinal holding a batch of each partition
  file's result. This step's skewed fixture peaks at 519 KB under its 121 KB limit,
  for the same reason.
- **A query that spills a keyed window needs about 128 KiB for the writers alone.**
  The driven test refused at 32 KiB for that reason, and uses 256 KiB.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
