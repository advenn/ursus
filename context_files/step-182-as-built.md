# Step 182 — as built

**R1 of `v0.6-scope.md`, its first half: `MergeSorted` streams.**

## 1. Evidence

`mergeSortedOp` read both sides whole:

1. it retained every batch of each, charged to the budget;
2. it concatenated each side;
3. it checked sortedness over each side's keys;
4. it merged the row indices, gathered once, and sliced the result into batches.

Its comment called this "the honest v1", and the streaming version a follow-up. Its
memory was O(both inputs), and `Check` refused it under a limit smaller than they
are.

## 2. What changed

`mergeSortedOp` holds a `mergeSide` for each input: its current batch, the batch's
keys, a cursor, and the last key of the batch before it.

- **`Next`** first has `fill` give each side a batch with rows left, pulling past
  empty ones, or mark it done. Then it fills one output batch from whichever side is
  behind, ties taking the left row as before.
- **The output stops** when it is full or when either side's current batch runs out.
  So one gather reads at most one batch a side, through the same `gatherRows` and
  `rowRef` as before.
- **Sortedness** is checked by `keysOf` as each batch arrives, within it and against
  the side's previous batch.
- **Nothing is charged to the budget,** as a filter's batch is not: it holds two
  input batches and one output batch however long the inputs.

Both sides are ordinary pull operators, so one goroutine pulls them in turn. The
comment's worry, that the breaker protocol has no two-input streaming shape, does
not arise: no breaker is needed.

**One behaviour moves:** an unsorted side is refused at its first backward row,
after the rows before it were emitted. `Collect` fails either way. A streaming
consumer sees those rows first. The CHANGELOG says so.

## 3. Tests

- **`TestMergeSortedStreamingAgreesWithAStableSort`:**
  - random sorted runs with ties and leading nulls, sides of 0 to 200 rows;
  - batch sizes 1, 3, 7, 64 and 4096;
  - each row's tag against the stable sort of the left rows then the right.
- **`TestMergeSortedRefusesARowBackwardsAcrossBatches`:** with batches of three, a
  left side 1 2 3 | 2 4 goes back only across its batch boundary.
- **`TestMergeSortedHoldsNoInput`:** two runs of 200,000 Int64 keys merge with a peak
  under 256 KiB, where both inputs are 3.2 MB.
- **Every existing `MergeSorted` test passes:** order and ties, unsorted sides,
  batch-size invariance, pushdown, the key types.

| tooth | result |
| --- | --- |
| a tie taking the right row first | **bites:** the stable-sort brute force, order, and every key type |
| a side's previous batch forgotten | **bites:** the backward row across batches |
| the previous batch's last key never kept | **bites:** the same |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
