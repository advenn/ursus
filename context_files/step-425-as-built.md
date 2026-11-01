# Step 425 — as built

**A slice of a String column is charged the text its rows cover.** The plan's 433,
M2 of `v0.7.2-plan.md` (H3d2), in the narrowed form the critic gave it, on step
424's spans.

## 1. What was wrong

`Column.Slice` of a String column copies its offsets and shares its characters, the
parent's whole buffer. The ledger charged each slice that whole buffer. Every batch of
an in-memory frame is a slice, so an operator holding one batch of 64 rows was charged
all the column's text.

`WithColumns(IntRange(0, Len(), 1))` beside a text column showed it. The stack holds a
batch of the frame in hand while `Len()` counts, and over 20,000 rows that batch was
charged 148.5 KiB. Under a 16 KiB limit the count, with nothing of its own to spill,
refused. `feature-gaps.md` row 56 recorded the refusal at step 287. It blamed the
stack holding the frame's side, which it does not: it holds a batch.

## 2. The change

- **`charsID`** (`internal/data/nbytes.go`) is `bufferID` for the characters,
  narrowed to the bytes the column's rows cover, from its offsets (`charSpan`).
  - Rows that start at the characters' first byte hold the buffer from its start.
  - Rows that run to their last byte hold it to its end, the under-filled room past
    them included, as `bufferID` has it.
  - A whole column is therefore charged as before.
- **Step 424's spans do the rest:**
  - a batch beside the column adds only its own offsets;
  - all of a frame's batches together hold all of its text;
  - a sort holding them still spills.
- **Not the plan's first form.** That had memsrc mark the caller's buffers external,
  so they would count nothing. The critic found that a sort of an in-memory frame
  would then never spill, and that most spill tests would go quiet.
- **Not changed:** a List's child column is held whole by its slices, as before. A
  slice of a list column is charged its parent's elements.

## 3. Tests

- **`TestARangeToLenBesideTextUnderALimit`** (root): over 20,000 rows of Int64 and
  text, at a batch of 64 under 16 KiB.
  - The range to `Len()` answers, equal to the run without a limit, where it refused.
  - A sort of the text still spills, and is equal too.
- **`TestAStringSliceHoldsItsText`** (`internal/data`): a String column's first,
  middle and last 64 rows.
  - Each is charged the text it covers, the last with the buffer's room.
  - Alone, each holds its offsets and that text.
  - Beside the column, each adds only its offsets.
  - The column's batches together hold all the column's text.
- **`TestV066UnderAMemoryLimit`:** its "G1, a range to Len() beside text" moves from
  the refused cases to the ones that answer equal under 16 KiB.
- **`feature-gaps.md` row 56** is marked fixed.

**Teeth: 5 of 5 bite.**
- the whole buffer charged;
- the start not narrowed;
- the end not narrowed;
- the room past the last row dropped;
- the text not charged at all. This one was silent until the unit test checked a
  slice's own count through `Spans`.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.
