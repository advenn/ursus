# Step 430 — as built

**A streamed centred rolling window answers as in memory, to the bit.** The second
part of the plan's 431, M2 of `v0.7.2-plan.md` (H3b), on step 429's
`kernel.RollingNext`. `feature-gaps.md` row 58 is closed.

## 1. What was wrong

Step 429 left centred windows on the old path. A centred window's stream took the N
rows around each batch, ran the in-memory kernel over them from an empty state, and
kept the batch's rows. Its refreshes and flips fell elsewhere, as the trailing
window's had.

Run at step 430, over 20,000 rows in batches of 64 under 16 KiB, 75 answer columns
differed across the three shapes: the whole frame, over a hot key, and in an order of
its own.
- var over 3 rows differed in 50% of its rows;
- skew over 8 rows in 61%.

## 2. The change

In memory, `rollingCentered` runs a centred window as the trailing window ending
k = (N−1)/2 rows later, over the partition with k null rows after its last. The stream
does the same with the stepper: `winStreamOp.centred` (`internal/physical/winstream.go`).

- **Each row is walked once, in order.** Answering a batch walks:
  - the batch's rows not walked yet;
  - the lookahead's rows, up to k past the batch, from the queue the op keeps `l = k`
    rows deep;
  - past the last row, nulls, to k in all.
- **The answers line up with their rows.** The first k answers belong to no row and are
  dropped. Answers walked ahead of the batch wait for the next one.
- **Short lookahead is an internal error.** If the queue is short of the lead before
  the input ends, or the answers fall short of the batch, the op raises an internal
  error rather than answering with too few rows.
- **A window of one or two rows leads by none** and is the trailing walk, as in memory.
- **`RollingSteps` now admits a centred window,** and `RollingNext` itself refuses one:
  the caller leads it.

## 3. Tests

- **`TestStreamedCentredRollingIsTheInMemoryBits`** (root): the eight functions over
  centred windows of 2, 3, 8, 64, 101 and 200 rows.
  - The window of 200 leads 99 rows, more than a batch of 64 holds.
  - The data is 20,000 rows of sin(i)·1000 with nulls, NaN and +Inf, in batches of 64
    under 16 KiB.
  - It runs over the whole frame, over a key holding nine rows in ten, and in an order
    of its own. Each spills, and each column must equal the run without a limit, bit
    for bit.
- **`TestRollingMoreStreamPastTheirLimit`** (step 284) compared its moments,
  covariance and correlation within 1e-9, because of this difference. It now compares
  their bits, its centred kurtosis among them.
- **`feature-gaps.md`:** row 58 is marked fixed, and the line that excepted the
  streamed rolling moments from bit-exact spilling is updated.

**Teeth: 6 of 6 bite.**
- no lead;
- one pad short;
- the first k answers kept;
- the batch walked whole again;
- an even window's lead taken as N/2;
- no nulls past the last row.

The short pad and the missing nulls are caught as the op's internal error, not as a
wrong answer.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.

The public API is unchanged.
