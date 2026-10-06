# Step 110 — as built

**A full join merges its keys on request.** This is `v0.4-scope.md` item 23,
`audit.md` J9, reproduced in step 108's probe.

## 1. What was wrong

`JoinCoalesce(true)` on a full join was refused while planning:

> *a full join cannot coalesce its keys yet* — *merging them needs a coalesce
> expression ursus does not have*

`ursus.Coalesce` existed. A full join can lack either side of a row, so its merged
key is neither side's alone: it is the left key where the row has a left side, and
the right key where it does not.

## 2. The fix

- **`gatherOut` takes a `keyFrom`** instead of a boolean. A merged key is read from:
  - **the left,** for inner, left, semi and anti joins, where every row has a left
    side;
  - **the right,** for a right join;
  - **either,** for a full join: `eitherKey` takes both, casts each to the promoted
    type the layout gives the merged column, and selects the left where it is
    present, through `kernel.Select`, which is what `ursus.Coalesce` runs on.
- **The spill path's null bucket** streams build rows with every left column
  absent. It used a Right-only flag, so it now takes the same mode. Its comment
  already named the case that matters: a row keyed (1, null) is null-keyed, and its
  1 must come from the right.
- **The resolver's refusal is gone,** and so is the error-table case that pinned it.
  The default is unchanged: an unset `JoinCoalesce` keeps a full join's keys apart,
  as Polars' does.

## 3. Tests and teeth

**`TestFullJoinCoalescesItsKeys`:**

- **Shapes:** one key, two keys, `NullsEqual`, each at 1 and 4 threads.
- **Oracle:** the default full join, which keeps both key columns, with
  `ursus.Coalesce` applied to them by hand. The schema must be equal too.
- **An Int32 key against an Int64 one** merges in Int64, with the expected eight
  keys: matched, unmatched on each side, and null.

**`TestFullJoinCoalescesItsKeysWhenItSpills`:** one key, under a 32 KB limit (two or
more spills required), against the unspilled oracle.

**`TestFullJoinCoalescesTwoKeysWhenItSpills`:** two keys, so a build row keyed
(null, m) is null-keyed and goes to the null bucket while its m must come back from
the right.

| tooth | result |
| --- | --- |
| a full join takes its key from the left | **bites:** every in-memory case |
| the spill bucket takes its key from the left | **bites:** the two-key spill test only. It was silent at first for two reasons in turn. With one key, a null-keyed row's key is null from either side, which is why the two-key test was written. Then the harness's pattern did not match the new test's name. A run by hand, with the mutation applied to a copy, showed the 38 right-side null-key rows losing their m, which is what made me look at the pattern. |
| `eitherKey` prefers the right | **bites** |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
