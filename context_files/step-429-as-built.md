# Step 429 — as built

**A streamed rolling window of rows answers as in memory, to the bit.** The first
part of the plan's 431, M2 of `v0.7.2-plan.md` (H3b): windows ending at their row.
Centred windows are the next step. `feature-gaps.md` row 58, found at step 284.

## 1. What was wrong

Over one huge partition under a memory limit, a window function streams a batch at a
time (`internal/physical/winstream.go`). A rolling function took the window's last N
rows before the batch, added the batch, and ran the in-memory kernel over both, from an
empty state each batch.

The float kernels carry state from row to row, and their answers depend on where that
state started:
- `rollingFloat`, for sum, mean, var and std: a compensated sum and Welford's state,
  refreshed from the window every N removals;
- `rollingMoment`, for skew and kurtosis: a queue of two stacks, which flips when its
  front empties;
- `rollingCovCorr`: three float states, refreshed as `rollingFloat`'s are.

Started afresh at each batch, the refreshes and the flips fell at other rows than in
memory, and the last bits differed. Run at step 429, over 20,000 rows of sin(i)·1000 in
batches of 64 under 16 KiB:
- var differed in 33% to 74% of its rows, std in 12% to 30%, and corr in 19% to 35%;
- skew differed in 57% to 81%, and kurtosis in up to 30%;
- the same held over the whole frame, over a hot key, and in an order of its own.

## 2. The change

- **`kernel.RollingNext`** (`internal/kernel/rollingnext.go`) walks a batch on from
  where the last one left off, as `RollingByNext` does for windows by time (step 246).
  - `RollingState` holds the window's last N+1 rows in a ring, with each kernel's
    state: the float state with its count and removals, the moments' queue, and the
    pair's three states.
  - Each row makes the in-memory kernel's moves, in its order:
    - the float kernels add the row, remove the one N before, and refresh from the
      window every N removals;
    - the moments pop the row N before and push the row, and a window of one row starts
      afresh at each row.
  - `RollingSteps` says which functions it answers: sum (not an integer one, which is
    an exact Int128), mean, var, std, skew, kurtosis, cov and corr, over a window of
    rows ending at the row.
- **The stream uses it** (`winNeed.rolling`): over the whole frame, over a hot key's
  partition (`winskew.go`), and in a sorted pass's order (`winsorted.go`, which reads
  the same needs). Its state is charged to the walk's account where there is one.
- **The in-memory kernels are unchanged,** apart from two answers factored out for both
  to share: `momentAnswer` and `covCorrAnswer`. The plan put the in-memory path through
  the stepper too. That would have changed the kernel Polars' bits are pinned to, for
  nothing a test of the two could not hold instead.
  - A micro-benchmark over 100,000 rows of a 64-row window, six runs each:
    - skew has a median of 11.6 ms, against 11.6 before;
    - corr has a median of 15.6 ms, against 16.2 before.
- **Not changed:**
  - Min, max, median, quantile, rank and an integer sum stream as before: their
    answers are exact.
  - A centred window still takes the rows around each batch. It is the next step, and
    `rollmore_test.go`'s tolerance stays until then, for its centred kurtosis.

## 3. Tests

- **`TestARollingWindowStreamedInPiecesIsTheWindow`** (`internal/kernel`): each of the
  eight functions walked over 3,000 rows cut at random into pieces of 1 to 150 rows
  must equal `winRolling` over the whole, bit for bit.
  - The data is sin(i)·1000 with nulls, NaN, ±Inf and ±0, in Float64 and Float32, and
    a pair with a second column of its own nulls.
  - Windows of 1, 2, 7, 64 and 100 rows, wanting the window's size, one value, or half.
- **`TestStreamedRollingIsTheInMemoryBits`** (root): the eight functions at windows of
  2, 7, 64 and 100 rows, over 20,000 rows and a Float32 copy, in batches of 64 under a
  16 KiB limit.
  - Three shapes: the whole frame, over a key holding nine rows in ten, and in an order
    of its own.
  - Each spills, and each answer column must equal the run without a limit, bit for
    bit. Unfixed, 21 of its 64 columns differed over the frame, and 21 over the key.
    Sum, mean and cov never did, their compensated sums absorbing where the refresh
    fell.

**Teeth: 7 of 7 bite.**
- the stream rebuilding from history;
- a refresh one removal late;
- the moments pushing before they pop;
- a one-row window not afresh;
- the pair's refresh skipped;
- each batch afresh;
- a refresh taking the row that left.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.

The public API is unchanged.
