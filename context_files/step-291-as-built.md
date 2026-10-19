# Step 291 — as built

**R3 of `v0.6.7-scope.md`: `Slice` with a negative offset,** and with it one rule for
every slice's window.

## 1. What changed

**Polars' rule,** read off its answers, which every one of its slices shares (frame,
expression, `list.slice`, `str.slice` and `bin.slice` alike):

- **The rule:** the window `[start, start+length)`, a negative offset counting from
  the end, is clipped to the elements.
- **So a start before the first element cuts the length by as much.** Of six rows,
  `(-3, 2)` is rows 3 and 4, `(-7, 2)` row 0 alone, `(-10, 2)` none and `(-10, 5)` row
  0.

**A frame's `Slice` from the end:**

- **The node:** a negative offset was refused; it is now `plan.Tail` with `Limited`
  and `Len`, as `TAIL 3 len 2` in a plan.
- **The operator:** it holds the last −offset rows as `Tail` does, spilling them past
  a limit. It then emits the first `Len`, fewer by as many as the input fell short of
  −offset, through the limit operator.
- **Why not `Slice`:** the optimizer's rules read `plan.Slice` as counted from the
  start. A negative length is ursus's "to the end", Polars' None, and is `Tail`
  itself.

**A bug the rule found in the expression `Slice` in a `Select`:** its frame form was
`Tail(−offset)` then `Head(length)`, which kept a row too many where the window began
before the first row: `(-7, 2)` was rows 0 and 1. It is the frame's `Slice` now.

**`str.slice` and `list.slice` clamped,** a decision of their own, so the two could not
disagree. They moved a start before the first element up to it and kept the whole
length: `(-7, 2)` of `"012345"` was `"01"`, `(-10, 2)` `"01"`.

- They now share `sliceWindow`, Polars' rule, with `bin.slice`, which step 274 built
  to it.
- That is a break, listed: `listns_test.go`'s `(-99, 2)` of three elements was two and
  is none, Polars' answer, and `(-4, 2)` is one.
- **In an aggregation,** the expression `Slice` lowers to `list.slice`, which refuses
  a negative length. Its "to the end" is lowered as the longest length, where it
  failed.

**The refusal flipped:** `TestFrameOpRefusals`'s negative offset.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestNegativeSlice`:**
  - twelve windows in a frame, an expression in a `Select`, an aggregation and a
    list;
  - six over a string and a binary alike;
  - after a filter; no rows; the eager form; the plan.
- **`TestNegativeSliceSpills`:** 15,000 rows from the end of 20,000, 10,000 long,
  under 16 KiB: it spills and equals the answer without a limit.
- **`TestListReshapeEdges`:** `(-99, 2)` none, `(-4, 2)` one element.

| tooth | result |
| --- | --- |
| the window clamped, as it was | **bites** |
| a frame's shortfall ignored | **bites** |
| a frame's length ignored | **bites** |
| the expression by Tail and Head | **bites** |
| a negative length to list.slice | **bites** |
| the string's own window | **bites** |
| the end before the start kept | silent |

**The silent tooth is a guard.** `sliceWindow` returns `max(lo, hi)` as its end. By
its arithmetic the end is never before the start, so dropping the guard changes no
answer. It stays because the string kernel slices a rune array with the two, and a
reversed pair would panic.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
