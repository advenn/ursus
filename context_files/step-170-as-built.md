# Step 170 — as built

**Item 7 of `v0.6-scope.md`, its second half: a probe that copies only what it
must.** The first half, a probe row that misses doing no work, was step 158.

## 1. Evidence first

A join's output gathered every left column through `kernel.Take(left.Column(i),
lsel)`, even when `lsel` was a run of the probe batch. That happens when every probe
row of a stretch matched once, in order, as a fact table probing a dimension on its
key does.

- The semi path already handed a probe batch through whole when it kept every row;
  a run of rows was copied.
- Step 157's profiles put `Take` at 21% of q2, mostly the right side's wide supplier
  rows, which this does not reach.

## 2. What changed

- **`runOf`** reports whether a selection is `start, start+1, …` within the batch. A
  `NullIndex` is never part of a run.
- **`gatherOut`** shares a left column over a run by `Column.Slice`, with no copy. The
  right side, merged keys taken from the right or from either side, and any other
  selection gather as before.
- **The semi and anti path** shares a run by `Batch.Slice`, beside the whole-batch case
  it already had.

## 3. Measured

Step 169's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q10 | 1,514 → 1,461 ms (−4%) | 349 → 343 ms |
| q9 | 2,561 → 2,491 ms (−3%) | 525 → 526 ms |
| q5 | 1,465 → 1,434 ms (−2%) | 267 → 269 ms |
| q3 | 1,204 → 1,214 ms | 236 → 242 ms |
| q14 | 815 → 820 ms | 177 → 185 ms |
| q7 | 2,480 → 2,472 ms | 526 → 511 ms |

**A small step, as measured.** PDS-H's probes rarely produce a run: one miss breaks
one, and most of these joins' probe sides have rows that miss or match more than
once.

h2o's joins, the fact-to-dimension shape this suits, could not be timed: the 1M-row
data set holds the fact table alone, without its small, medium and big tables, and
the 10M one is too heavy for this laptop. The final report runs them at 10M.

## 4. Tests

- **`TestRunOf`:** runs at the start, of the whole batch, of one row; not a gap,
  not out of order, not past the batch, not a `NullIndex`, not over no batch.
- **`TestAJoinSharesARunOfItsProbeRows`:** `gatherOut` over a run of the left batch
  aliases its column's memory, and over rows with a gap copies it. The values are the
  rows' either way.
- **`TestJoinBatchSizeInvariance` and the join suites** cover the answers, the semi
  path's included.

## 5. Teeth

| tooth | result |
| --- | --- |
| any increasing selection taken for a run | **bites:** `TestAJoinSharesARunOfItsProbeRows`, the spilled outer joins, `TestImpliedPredicatesKeepTheAnswer` |
| a run shared from the batch's start | **bites:** `TestJoinBatchSizeInvariance`'s inner, left and full cases, the new test |
| a semi join's run shared from the batch's start | **bites:** `TestJoinBatchSizeInvariance`'s semi and anti cases |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
