# Step 168 — as built

**One of step 160's found items: `First` and `Last` over many groups.**

## 1. Evidence first

**Step 160's group-by test** used 300,000 rows with about 182,000 groups, on one
thread. Timed again here, the best of three runs:

| aggregate | time |
| --- | --: |
| `Sum` | 33 ms |
| `First` | 782 ms |
| `Last` | 1,219 ms |

**`positionAcc` kept a one-row `Column` per group.** Each batch it built a map of the
rows to keep and ran a `Take` per group. `NBytes` walked every group, and `Finish`
concatenated one part per group.

`implodeAcc`'s own comment records the same shape costing 93 seconds on h2o gb7 at
step 20, and implode was rewritten then. `First` and `Last` had kept it.

## 2. What changed

**`positionAcc` keeps a row index a group and gathers once a batch,** in implode's
manner:

- **each batch** decides its rows (a group's first row for `First`, a group's last
  row in the batch for `Last`, found walking backwards) and gathers them in one
  `Take`;
- **each group** keeps the index of its row in the concatenation of those gathers,
  `NullIndex` until it has one, and a batch number so a batch picks a group once;
- **`Finish`** concatenates and gathers once; a group with no row gathers `NullIndex`,
  a null;
- **`Take` keeps the dtype,** so every type works with no case of its own.

**`Last` compacts.** Every batch a group appears in supersedes its earlier row, which
is then garbage in the gathered parts. When the garbage passes twice the live rows,
and 64k, the live rows are gathered into one part again, so `Last` holds O(groups),
not every row it saw.

**`Merge`** appends the other accumulator's parts after this one's, as implode's does:
`First` keeps its own row, and `Last` takes the other's, which saw a later portion.

`assembleRows`, which stitched the one-row columns, had no other caller and is gone.

## 3. Measured

The same test, best of three:

| aggregate | before | after |
| --- | --: | --: |
| `Sum` | 33 ms | 39 ms |
| `First` | 782 ms | 34 ms (23×) |
| `Last` | 1,219 ms | 45 ms (27×) |

No PDS-H or h2o query uses `First` or `Last`, so the bench has nothing to time.

## 4. Tests

- **`TestFirstAndLastAnswerAsTheRowsSay`:** Int64 and String values, one in five null,
  25 batches of 700 rows over 2,000 groups, three never appearing. Against the rows'
  own first and last, positionally, nulls included.
- **`TestFirstAndLastMerge`:** a later portion, its groups numbered backwards, merged
  into an earlier one. It answers as one accumulator over both.
- **`TestLastHoldsAboutItsGroups`:**
  - 10,000 groups in each of 40 batches, which would hold 3.2 MB if every row were
    kept, and holds under 2 MB;
  - 500 more groups that appear in the first batch alone, so the compactions after it
    must carry rows no later batch rewrites.
- **The existing tests,** `TestEveryAggregateMergeIsCovered` and
  `TestAggregateBatchSizeInvariance` among them, cover every type.

## 5. Teeth

| tooth | result |
| --- | --- |
| `First` taking a later row | **bites:** the new tests, `TestAggregateBatchSizeInvariance` |
| `Last` taking a batch's first row | **bites:** `TestEveryAggregateMergeIsCovered` and more |
| a group picked twice in a batch | **bites:** the same, and `TestAggregateHintsSayWhatIsTrue` |
| `Last` never compacted | **bites:** `TestLastHoldsAboutItsGroups` |
| compaction leaving the old indices | **silent at first,** when every group reappeared after each compaction; bites since 500 groups do not |
| a merged `Last` keeping the earlier row | **bites:** `TestEveryAggregateMergeIsCovered` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
