# Step 335 — as built

**A null's rank is null,** as Polars ranks it: the open wrong answer of
`feature-gaps.md` §1, found at step 268. `Rank` ranked a null as the least value and
gave it a position, so `[5, null, 4]` ranked `[3, 1, 2]` where Polars ranks
`[2, None, 1]`.

## 1. What changed

| | before | now, as Polars |
| --- | --- | --- |
| a null's rank | the least value's, a position taken | null |
| the values' ranks | counted after the nulls | among themselves |
| the rank's field | never nullable | nullable as its input |

By every method, ordinal, dense, min, max and average, either way; over a partition
and over the frame; in memory and streamed past a memory limit; in `List().Eval` and
`CumulativeEval` bodies, which rank with the same kernel.

## 2. How

- **`kernel.winRank`:** a partition's rows sorted by value, the nulls one peer group,
  passed over: no rank, no position, no dense group. A value's position counts the
  values before it. The answer's validity is the input's.
- **`kernel.RankNext`,** the streamed rank of one partition past the limit (step 237):
  a null is passed over and its rank null; the peer test is against the last value
  ranked, which is what `RankState.prev` keeps now, never a null.
- **The sorted pass's max and average** are made from min over the order reversed,
  `values + 1 − min reversed`: `values` was the partition's rows, and is now its
  values, the rows less the nulls `projOp` counts in each spec's child as it reads
  them (`passPlan.values`). A null's rank stays null through the combine.
- **`expr.WinFn.Field`:** a rank is nullable as its input; cum_count and rle_id are
  still never null.
- **Docs:** `Rank`'s doc comment; the windows guide, *changed in 0.6.7*.

## 3. Tests

**`TestRankLeavesNullsNull`,** every small answer Polars 1.44.1's:

- the five methods, ascending and descending, over a partition and over the frame,
  of `[5, null, 4, 5, null, null, 1, 4]` in two groups, and the field nullable;
- only nulls; text; an ordinal of integers; a list's body, Polars' `l`;
- streamed: 4,000 values, every fifth null, ranked over the frame under 16 KiB in a
  sorted pass that spills, each method each way equal to ranks worked out in the test.

**Flipped:** `TestRankMethods`, which pinned a null ranked first: `[10, 20, 20, null]`
ranks `[1, 2, 2, null]` by min now, and its average tie 2.5, where it was 3.5. And
`TestARankStreamedInPiecesIsWinRank`, the streamed rank against the kernel's: a group's
last position counted among the values, each row's validity checked, and a null's
slot holding the least value, so a peer test against a null where the nulls meet the
values would tie.

**Teeth (9 bite):**

- a null ranked;
- a null's position kept;
- a null's rank valid;
- a streamed null ranked, in the frame's test and in the kernel's;
- a streamed peer test against a null;
- a streamed max over the rows;
- a streamed average over the rows;
- a streamed average's null valid;
- a rank never null.

**Gate:** test-all 135 ok, race 27 ok (the root package 530 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
