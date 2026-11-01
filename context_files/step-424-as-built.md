# Step 424 — as built

**The memory ledger counts a sliced buffer as part of the allocation it views.** The
plan's 432, M2 of `v0.7.2-plan.md` (H2.5), found by the swarm's ledger probe and
confirmed by running it.

## 1. What was wrong

The ledger keys an allocation by a pointer into it and counts each key once. The key
was the buffer's first byte, and the size was arrow-go's `Buffer.Cap()`, which for a
slice made by `memory.SliceBuffer` is the slice's length, not its parent's. A
fixed-width column's `Slice` is such a slice (step 37), and so is every batch an
in-memory frame is split into. Two things went wrong:

- **A slice from the start hid its parent.** A frame's head has the frame's first
  byte for its key and its own few bytes for a size. Seen first, it was the entry,
  and the frame behind it was a duplicate. A sort of `Concat(df.Head(10), df)` over
  8 MB of Int64 had a ledger peak of 0.0 MB. Under a 4 MB limit it held the 8 MB and
  spilled nothing.
- **A slice from within counted its parent again.** `df.Slice(1, n-1)` has a key of
  its own, so a sort of it beside `df` counted 16 MB for the one 8 MB allocation, and
  would spill or refuse at half the limit.

## 2. The change

- **The key is the allocation's last byte** (`internal/data/nbytes.go`). Every view of
  an allocation runs to its end in Go's capacity, so all of them have the one key. A
  Go slice's (`sliceID`, `unsafe.go`) is keyed the same way.
- **A `Span` places each view in its allocation:** `[Lo, Hi)` in bytes, counted back
  from the end. `Column.Spans` yields them. `Buffers` still yields each view's own
  size.
- **Two views of an allocation count the least span covering both** (`Span.With`).
  This applies in `BufferSet.Add`, in `Budget.retain` across accounts, and in
  `Account.retainColumn` within one, through `Budget.widen`.
  - A batch and its head count the batch.
  - A sort holding all of a frame's batches counts the frame.
  - A stream holding one batch at a time counts that batch, as before.
- **Not the alternative of counting each view's capacity to the allocation's end.**
  It was written first, and its tests passed. But a stream holding the first batch of
  a large in-memory frame would then be charged the whole frame, and spill or refuse
  where it never did.
- **Not the plan's key of first byte and capacity either.** That would have kept the
  double count of a slice from within, at most twice, recorded as D12a. The span
  removes it, so D12a has nothing to record.
- **A gap counts.** Two views at either end of an allocation count all of it between
  them, the gap included. That is never more than the allocation, which they keep
  alive.
- **Not changed:** a String's slice copies its offsets into a new buffer and shares its
  characters as the same buffer, and both were already counted right. A validity
  bitmap's slice shares the parent's bytes and was keyed alike before.

## 3. Tests

- **`TestASlicedBufferIsItsAllocation`** (root), on a sort of 8 MB of Int64 at one
  thread, in one batch:
  - the frame alone peaks at 8 MB, as it did;
  - its head then the frame peaks at 8 MB, where it was 0.0;
  - the frame then its head peaks at 8 MB;
  - the frame from its second row then the frame peaks at 8 MB, where it was 16;
  - its head then the frame, under a 4 MB limit, spills.
- **`TestASliceIsItsParentsAllocation`** (`internal/data`): for an Int64 column, with
  and without nulls, its first row, its rows from the second, and its middle row
  each count once beside the column, in either order. A slice alone of a column with
  no nulls holds its own bytes.
- **`TestBudgetHoldsASliceAsItsAllocation`** (`internal/execopt`): a batch's head, or
  a slice from its second row, then the batch, in one account and across two. Alone,
  the head is its own bytes. The allocation is held until its last holder lets go.
- **`TestAGroupByHoldsItsAnswerOnce`** (`internal/memcheck`) sized the answer with a
  map keyed by a buffer's ID. Its batches are slices of one allocation, which now
  share an ID, so the map kept the last batch's size, 78 MB for the answer. It sizes
  the answer with a `BufferSet`, as its comment says, "each allocation once".

**Teeth: 7 of 7 bite.**
- the first byte for the key;
- a view's capacity to the allocation's end for its span;
- a set keeping the first view it sees;
- `retain` keeping the first account's span;
- an account skipping a second view;
- `widen` a no-op;
- `retain` widening a new entry from an empty span.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.
