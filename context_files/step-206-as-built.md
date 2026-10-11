# Step 206 — as built

**O1 of `v0.6.2-scope.md`: reverse spills.**

## 1. Why

`reverseSink` held its whole input, then concatenated it and gathered that
backwards: two copies at once. Past the budget it refused, one of three operators
left that did: reverse, hstack and tail.

Unlike those two, reverse needs nothing at once but the part it is giving out.
Written in order, its input can be read back a stretch at a time from the end.

## 2. What changed

`reverseSink` moves to its own file, `reverse.go`.

- **Pieces, in input order:** the batches it holds, always the last piece, or a run
  file that held batches went to.
- **Past the budget** (`Account.Over`, under a limit), the held batches are written
  to a run, in order, and let go. They are everything the sink holds, so its
  account goes to nothing.
- **`Finish`** returns `reverseOp`, which gives the pieces out last to first:
  - a held piece as it is;
  - a run read back whole into the account;
  - each piece's batches last to first, and each batch's rows gathered backwards.

  It lets a piece's charge go when it moves to the next, so it holds one run at a
  time. The whole input is no longer concatenated, in memory or not.
- **`Merge`** keeps the pieces in input order. When the other sink has runs, this
  sink's held batches go to a run first, so held batches stay last. The other's
  held batches are held here, and its run directory becomes this sink's to remove.
- **The budget's hint** now lists reverse among the operators that spill; hstack and
  tail still refuse. `lazy.go`'s doc says the same.

## 3. Tests and teeth

- **`TestReverseSpillsUnderATenthOfItsInput`:** 20,000 rows under a tenth of their
  bytes, on one thread and on four:
  - the answer is the in-memory one, row for row;
  - at least five runs (it makes nine);
  - the peak at most half again the limit. Measured, it is 133,888 bytes under
    131,296: the limit and part of a batch.
- **`TestAMergedReverseKeepsItsPiecesInOrder`** (physical): two sinks, each holding
  its share or having spilled every batch, merged:
  - held batches only in the last piece;
  - the input given back backwards;
  - a run charged while it is given out;
  - no file left behind.
- **`TestSpillingByHand`:** reverse over a `List(String)` and a `Struct` through the
  spill format, exact; and a spilled reverse leaving no files, after a full read and
  after an early break.
- **`TestBudgetErrorNamesTheOperator`:** reverse leaves the refusals for
  `reverse_spills`. The hint test, which used reverse to provoke a refusal, uses
  tail.

| tooth | result |
| --- | --- |
| never spills | **bites:** three tests |
| pieces given out first to last | **bites** |
| a piece's batches given out in their order | **bites** |
| a batch not reversed | **bites:** the merge test, and step 9's exact test across batch sizes; the tenth-of-input test cannot see it, its want made by the same code |
| a merged sink's held batches dropped | **bites:** three merge tests |
| held batches left before merged runs | **bites:** the merge test |
| a merged sink's directory not taken | **bites:** the merge test |
| a spill not let go | **bites:** the peak ceiling |
| a run read back uncharged | **bites:** the merge test, added for it |
| the piece given out before not let go | **bites:** the peak ceiling |
| the directory not removed | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
