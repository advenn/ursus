# Step 241 — as built

**G1 of `v0.6.4-scope.md`: n_unique spills its values over a hot key and with no
key,** where it refused.

## 1. Why

n_unique keeps every distinct value of every group, one entry each in its key table.
A group-by partitions keys, not the values of one, so over a few huge groups, or
with no key at all, it refused, as a quantile did until step 220. Its answer is a
count a group. Keys sorted by group give it in one pass: a group's distinct values
are its runs of equal keys.

## 2. What changed

**`internal/kernel/keyspill.go`:**

- **`KeySpill`/`KeySpiller`,** step 220's interfaces for keys: `Put` a run's (group,
  key) pairs in order, `EndRun`, then `Ordered` gives every key back merged.
- **`nuniqueAcc.SpillKeys`:**
  - sorts its table's entries by group, then key bytes;
  - puts them as one run;
  - resets its table.
- **Its Finish, once it has spilled,** puts the rest and counts each group's
  distinct keys as they come back. Its counts from before no longer hold, since a
  value seen before a spill can come again after one.
- **Its Merge refuses once spilled,** either way.

A key is the group-key encoder's bytes, so equal bytes are equal values under
grouping equality: NaN is NaN, and −0 is +0.

**`internal/physical/valuespill.go`:**

- **`groupRuns`** is step 220's value spill with its schema made a parameter: gid,
  then a state column. It still cascades its runs 16 a level, merges them through
  the external sort's merge, and removes them.
- **`valueSpill`** (gid, Float64) and **`keySpill`** (gid, Binary) are its two kinds.
  The merge orders Binary as bytes.

**In the group-by sink:**

- **`hashAggSink.spillValues`** drives both: an accumulator that is a
  `ValueSpiller` or a `KeySpiller` gets its spill from `spillOf`, kept in `spills`.
- **What still refuses** is implode, whose state is its answer. The hints say so in
  `extagg.go` and `execopt`.

## 3. Tests and teeth

- **`TestAnNUniquesSpilledKeysGiveItsCount`** (kernel):
  - spills at random points between eighty batches, against an n_unique that held
    its keys;
  - floats with NaN, ±0 and values that recur across spills, nulls, a group with half
    the rows, and groups with none;
  - every run is in order, a spill empties the table, and the merge refuses either
    way.
- **`TestAHotGroupsNUniqueSpillsItsKeys`:**
  - n_unique of all-distinct integers, a constant string, an integer with nulls, a
    float with NaN, ±Inf and ±0, and a string of 97 values;
  - over five hot keys, maintaining order or not, and over no key;
  - on one thread and four, under 8 KiB: as in memory, spilling, and the spill
    directory empty after.
- **The refusals that stay move to implode:**
  - `TestAHotGroupsQuantileSpillsItsValues`;
  - `TestGroupBySpillFilesAreCleanedUp`'s two error cases. The second now refuses
    after a quantile and an n_unique beside implode have both spilled;
  - `TestBudgetErrorNamesTheOperator`'s holistic case.
- The hint test looks for "implode over one group".
- `valuespill_internal_test.go` builds its spill with `newValueSpill`.

| tooth | result |
| --- | --- |
| keys put unsorted | **bites** |
| a group's keys counted once | **bites** |
| the table kept past a spill | **bites:** the kernel test's empty table |
| the rest not put at Finish | **bites** |
| never spills keys | **bites:** it refuses |
| keys sorted ignoring their group | **bites** |
| the last key not copied | silent, and equivalent: the slice keeps its batch alive in Go; the copy is defensive |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
