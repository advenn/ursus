# Step 198 — as built

**P3 of `v0.6.1-scope.md`, closed on its profile: a join's output is already gathered
once.** No code changed.

## 1. What the scope assumed

P3 read v0.6.0's profiles of h2o's j2 and j3, where probe and `Take` were 27 to 37%.
It assumed the probe appended output row by row, and planned to gather each output
column once, from the batch's match pairs. The scope said to profile first.

## 2. What the profile says

j2 and j3 at 10M rows over Parquet, on step 197's code, classified by stack:

| query | CPU | probe | `Take` | of it, `TakeStrings` | `ConcatOwned` |
| --- | --: | --: | --: | --: | --: |
| j2 | 3.21 s | 37% | 29% | 13% | 8% |
| j3 | 2.87 s | 31% | 20% | | |

- **There is no row-by-row append.** `joinProbeOp.emit` hands `gatherOut` the match
  pairs of a chunk, and `gatherOut` takes each output column once with
  `kernel.Take`. That is the design P3 proposed, built already: step 170 added the
  shared run, and the rest is older.
- **The 8% `ConcatOwned`** is `exec.Collect` concatenating 9 million output rows into
  one frame, not the join.
- **Finding the matches** is a few percent; the probe's share is the output's copy.

## 3. What was tried

The `Take` of a String column copies each value with `copy`, a `memmove` call. In the
Parquet gather (step 192), copying values of up to 16 bytes as two words cut that
cost. Tried in `data.TakeStrings`:

- **h2o j2, j3 and j4, alternated:** within noise, j2's CPU spreading 15% within one
  version's own rounds;
- **a micro benchmark,** a million 9-byte ids taken in random order: 49 ms a run
  either way, 49 ns a row.

A random take across a large column waits on memory: each row is a cache miss in the
offsets and another in the characters. The Parquet gather's dictionary sits in cache,
which is why the same change paid there. It was reverted, and `TakeStrings` is
unchanged.

## 4. What would pay

- **Late materialisation,** not gathering a column a later operator drops. h2o's
  joins collect every column, so it would save nothing there. PDS-H's joins mostly
  carry keys and a few values onward, and that is Tier 2's selection-vector work.
- **Gathering in key order,** so the build side is read in runs: a partitioned
  probe. That is a different join.

**P4 is next.**
