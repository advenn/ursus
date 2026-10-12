# Step 218 — as built

**O2 of `v0.6.3-scope.md`: hstack streams.**

## 1. Why

`HStack` drained every frame, concatenated each, compared their heights and paired
them, so it held all its input and refused past the budget. Pairing row i of each
frame needs only row i of each in hand.

The comment it carried said re-chunking would copy, since `Column.Slice` copied
fixed-width columns. It does not, and has not for some time: a slice shares the
buffers.

## 2. What changed

- **`hstackOp` keeps one batch per frame,** and where it is in it. Each output batch
  is the fewest rows any frame has left, sliced from each, with no copy.
- **A frame that ends before the others is a height mismatch,** refused as before.
  The others are read to their ends to count them, so the message gives each height
  in full and names the first frame that differs from frame 0. Rows paired before it
  have gone out by then, as with `mergeSortedOp`'s out-of-order refusal.
- **No rows at all still gives one empty batch,** as before.
- **The batches in hand are charged.**
- **The budget's hint** lists hstack among the operators that stream, and `lazy.go`
  says so.
- **The refusal test's hint case** now uses a global `Implode`, which still refuses
  through the generic check.

## 3. Tests and teeth

- **`TestHStackPairsItsFramesABatchAtATime`:**
  - three frames built of 300-, 700- and 1,000-row pieces, whose boundaries never
    align, 21,000 rows, under three-tenths of one frame's bytes: every row pairs as
    the whole frames do;
  - a frame one row short, and one half as long, are refused with both heights in
    full.
- **`TestBudgetErrorNamesTheOperator`:** hstack moves from the refusals to
  `hstack_streams`.
- **The existing hstack tests,** `TestHStack` among them, pass as before.

| tooth | result |
| --- | --- |
| a batch the size of the last frame's, not the fewest | **bites** |
| a frame's place never advanced | **bites:** a test times out |
| the mismatch names no frame | **bites:** two tests |
| the rest not counted | **bites:** the half-height case, added for it |
| no batch for no rows | silent, and equivalent: Collect gives the same empty frame |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
