# Step 217 — as built

**O1 of `v0.6.3-scope.md`: tail spills.**

## 1. Why

`Tail(n)` holds the last n rows, a ring of batches, and refused past the budget. The
last n rows are its answer, and n can be more than the budget.

## 2. What changed

`tailOp` moves to `tail.go`, beside reverse's.

- **The ring is held as pieces, in input order,** as `reverseSink`'s are: the batches
  it holds, always the last piece, or a run file batches it held were written to when
  the query was over its budget.
- **Pieces drop from the front** while the rest still covers n:
  - a whole run, its file deleted with it, so the files hold about n rows;
  - then whole batches of the held piece, as the ring always did.
- **At the end, `tailOut` gives the pieces out forward,** reading a run a batch at a
  time and skipping the rows past n at the start, so it holds a batch.
- **The budget's hints and `lazy.go`'s doc** list tail among the operators that spill.

## 3. Tests and teeth

- **`TestTailSpillsUnderATenthOfItsInput`:** 20,000 rows under a tenth of their bytes.
  The last 15,000, all 20,000, 25,000, 1 and 0 each answer as in memory, the first
  three spilling, with nothing left on disk.
- **`TestATailKeepsOnlyWhatItsLastRowsNeed`** (physical): sixty batches of 100 rows,
  the last 1,500, under 2,000 bytes. After the input it holds n rows and less than a
  piece more, and its directory holds a file per run piece.
- **`TestATailWithNoBudgetHoldsNAndABatch`** (physical): with no budget, under n rows
  and a batch.
- **`TestSpillingByHand`:** a tail over a `List(String)` and a `Struct` through the
  spill format, exact, and a spilled tail leaving no files. The hint test provoked its
  refusal with a tail; it uses hstack now.
- **`TestBudgetErrorNamesTheOperator`:** tail moves from the refusals to
  `tail_spills`.

| tooth | result |
| --- | --- |
| never spills | **bites:** four tests |
| runs never dropped | **bites:** the pieces test |
| held batches never dropped | **bites:** the no-budget test, added for it |
| no skip at the start | **bites:** three tests |
| the directory not removed | **bites** |
| a dropped run's file kept | **bites:** the pieces test's file count, added for it |
| a skip ending mid-batch not sliced | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
