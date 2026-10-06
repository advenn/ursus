# Step 105 — as built

**Median and quantile by selection, not a sort.** This is `v0.4-scope.md` item 15,
for h2o gb6 (median and sd of v3 by id4 and id5).

## 1. What was wrong

`quantileAcc.Finish` sorted every group in full under ursus's total order, then read
two ranks. That is O(n log n) per group, run serially in the group-by's Finish.

**Measured** (h2o gb6 at 2M rows, Parquet): 18% of the query's CPU, all in
`slices.pdqsortCmpFunc`, which is about 195 ms of each run.

## 2. The fix

A quantile needs two order statistics, and they are adjacent. `quantileOf` now
reorders the group itself:

- **`selectNth` puts the lower rank in place,** in O(n) on average.
  - It is a quickselect with median-of-three pivots, under `compareTotalF64`: NaN
    above everything, −0 equal to +0. Sort and GroupBy use the same order.
  - Its partition is three-way, so a run of equal values is settled in one pass,
    not recursed into.
  - After 2·log₂n rounds it sorts what is left, so an input built to defeat the
    pivot costs O(n log n), not O(n²).
- **The upper rank, when it differs, is the least value to the right,** which the
  partition has put there.
- **The interpolation switch is unchanged.** It reads the two values, not two
  indices into a sorted slice.

The answer is the sort's, because the order statistics of a total order do not
depend on how they are found. Signed zeros compare equal, so which of the two comes
back was already arbitrary under the unstable sort.

## 3. Measured

h2o gb6 at 2M rows, best of three, step 104's runner and this one alternately:

| before | after |
| --- | --- |
| 316–322 ms | 215–232 ms |

That is −30%.

## 4. Tests and teeth

**`TestQuantileBySelectionIsTheSortedAnswer`** compares against the old method,
sorting under the total order then reading the ranks:

- **3,000 groups:**
  - one to sixty values, every hundredth group 1,000–6,000;
  - NaNs, infinities, signed zeros, runs of four distinct values, and normal
    noise;
  - some already sorted and some reversed, the classic worst cases for
    quickselect.
- **Every interpolation, at nine values of q,** at and between ranks.

**`TestSelectNthPlacesTheRank`** checks every k of 300 small arrays with many
repeats: the rank's value, nothing larger before it, nothing smaller after.

| tooth | result |
| --- | --- |
| the search goes the wrong way after partitioning | **bites:** both new tests, and `TestQuantileParametersAreNotCollapsed` |
| the upper neighbour taken as the next slot, not the least above | **bites** |
| equal values sent right instead of kept in the middle | silent, and rightly: the answer is still correct. An all-equal range then makes no progress until the round budget runs out and it is sorted. It costs speed, not answers. |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
