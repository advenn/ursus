# Step 281 — as built

**M4 of `v0.6.6-scope.md`, first part: run lengths.** These are Polars' `rle_id` and
`rle`. The rest of M4, `append`, `extend_constant`, `sample` and `shuffle`, is step
282. `RepeatBy` was built at step 263.

## 1. What changed

**`RleID()`, a window function of its own** (`WinRleID`):

- **The answer:** each row's run's number from 0, a run being rows a group-by keys
  alike, so NaN runs with NaN, −0 with 0 and a null with a null, as Polars runs them.
  A UInt32, never null.
- **The kernel,** `winRleID`: each partition's rows in the window's order, each key
  compared with the one before it, the count restarting at each partition. So it is
  over the frame, or `.Over(key)` a partition's own. Polars' `group_by` gives each
  group's as a list; ursus's aggregation takes aggregates.
- **Streaming:** it has no bounded need, as rank has none, so it runs over a whole
  partition in memory.
- **First draft:** `RleID` was written in window functions there were: a row starts a
  run where step 279's `order_cmp` with its `Shift` is not 0, and `CumSum` counts the
  starts. Every context refused it, "a window may not contain another window". ursus
  takes no window inside another, not even `Col("a").Shift(1).CumSum()`, which Polars
  takes. That gap is wider than this step, and `feature-gaps.md` §5 records it now.
- **A Struct** cannot be a key here and is refused, where Polars runs it.

**`Rle()`, a reshape** (`ReshapeRle`):

- **The answer:** each run's length and value, a Struct of `len`, a UInt32, and
  `value`, as Polars'. A column of its own length in a `Select` and a list a group in
  an aggregation.
- **In a `Select`,** the zipper's frame form: the run's number by `RleID`, a
  group-by of it counting its rows and keeping its first value, sorted by the number.
  So the −0 and 0 run's value is −0, its first, as Polars'.
- **In an aggregation,** `list.rle` (`internal/kernel/listmode.go`): each list's
  runs, by adjacent keys, through `listGather`, so a sliced list runs only its own
  elements. `FnListRle` joins the list block at 700.

**The sweeps:**

- the names test counts 204;
- the sliced-list floor is 36 of 37, all but `item`.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestRle`:**
  - rle of integers, of NaN and the zeros, of text;
  - rle_id of integers, of NaN and the zeros, over g;
  - rle by group;
  - no rows;
  - the types;
  - a Struct refused.

| tooth | result |
| --- | --- |
| every row a run | **bites** |
| a partition continues the last | **bites** |
| a list's run counted twice | **bites** |
| a run's last value | **bites** |
| runs last first | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
