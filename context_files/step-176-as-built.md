# Step 176 — as built

**What step 175 found:** `Var` eight digits short over large near-equal values, and
`ArgMin` 70 times slower than `MinBy` over many groups.

## 1. Evidence

**Precision**, against Polars 1.44.1 in the bench's environment:

| values | Polars' `var` | ursus's, before |
| --- | --- | --- |
| 1e9+1, 1e9+2, 1e9+4 | 2.3333333333333357 | 2.333333283662796 |

Welford's update keeps the deviations from a running mean. That mean is near 1e9, so
it rounds in the eighth significant digit of the spread. Values like these are common:
epoch seconds, or prices in small units. Step 175's `Corr` had the same flaw and was
built with the fix.

**Speed**, over 1M rows in 200k groups, one targeted run:

| aggregate | time |
| --- | --: |
| `First` | 42 ms |
| `MinBy` (step 175) | 111 ms |
| `ArgMin` | 7,762 ms |

`argExtremumAcc` kept each group's winning value as a one-row column. It compared a
batch's candidate with it by concatenating the two and building a comparator. That
is the shape step 168 removed from `First` and `Last`.

## 2. What changed

**`varAcc` shifts each group's values by its first** (`kernel/aggstat.go`):

- The mean is of the shifted values; M2 is the same either way.
- A merge moves the other group's mean to this group's shift.
- **Where the shift itself overflows,** two finite values more than float64's range
  apart, the group moves back to no shift. Its mean takes k back, and the existing
  overflow-safe update then answers [1e308, −1e308] with +Inf, as before.
- **A group's state is one struct,** `varState`.

**`coMomentAcc` keeps its state the same way,** in one struct per group, `coState`.

**`argExtremumAcc` is rebuilt on step 175's typed keys (`byKeys`):**

- each group's best row of a batch is found first, then compared with the group's
  kept key;
- positions count every row, nulls included, as before;
- a merge shifts the other's positions by this one's row count, as before.

## 3. Measured

Each figure is the best of alternated runs, against step 175's tree in a worktree.

| measure | before | after |
| --- | --: | --: |
| `ArgMin`, 1M rows, 200k groups, query | 7,762 ms | 103 ms |
| `Corr`, the same, query | 102–109 ms | 74–89 ms |
| `varAcc` alone, 1M rows, 200k groups | 16.3–17.9 ms | 18.2–19.5 ms |
| `Var`, the query | 52–60 ms | 56–76 ms (noise) |

**The shift costs `Var` about a tenth of its accumulator's time,** some 2 ns a row.
One struct a group did not win that back: `Var`'s three slices fit in L3. It did pay
off for `Corr`, which had eight. Eight digits are worth the 2 ns.

## 4. Tests

- **`TestVarOfLargeNearEqualValuesAnswersAsPolars`** checks Polars' `var` and `std`,
  ddof 1 and 0, and quarter seconds near an epoch.
- **`TestVarAndArgExtremaAgreeWithABruteForce`:**
  - the fixture: 30,000 rows of values near 1e9 in 1,000 groups, batches of 700;
  - the reference: `Var` by two passes, and `ArgMin` and `ArgMax` of a float with
    ties, nulls and NaN, and of a string, by a scan;
  - it runs on one worker, on four, and under a 128 KiB budget that it asserts spilled.
- **Every existing test of `Var`, `Std`, `ArgMin` and `ArgMax` passes unchanged,**
  including `TestArgMinMaxPositions`, the merge sweep, and the by-hand overflow case.

## 5. Teeth

| tooth | result |
| --- | --- |
| var's values not shifted | **bites:** the precision test, and the merge sweep |
| var's merge ignoring the shifts | **bites:** the merge sweep's var and std, every family |
| var's overflowing shift kept | **bites:** the by-hand Var(0) and Std(0) of [1e308, −1e308] |
| arg_min's tie taking the later row | **bites:** `TestArgMinMaxPositions` and the brute force |
| arg_min's merge not shifting positions | **bites:** the merge sweep, every family |
| arg_min's positions not counting nulls | **bites:** the naive-implementation test, positions, and the brute force |
| arg_min's null a candidate | **bites:** the all-null group, positions, and the brute force |

## 6. On the way

The laptop's disk filled during this step: `~/.cache/go-build` had grown to 16 GB over
the session's gates. With the maintainer's approval, `go clean -cache` cleared it.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
