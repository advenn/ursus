# Step 219 — as built

**O3 of `v0.6.3-scope.md`: a keyless Semi or Anti join with a residual spills.**

## 1. Why

A `WhereExists` or `WhereNotExists` of a predicate alone is a Semi or Anti join with
no key and a residual. Partitioning divides keys, and it has none, so it kept its
whole build side and refused past the budget. Step 195 had spilled a plain cross
join's build side whole and read it back in a block nested loop; this is the same
shape, with a verdict a probe row rather than its pairs.

## 2. What changed

- **`crossSpills`** admits a keyless Semi or Anti join with a residual. Its build
  side goes to the cross file, as a cross join's does.
- **`crossProbe`** gives such a join an `existsLoopOp`, a probe batch at a time:
  - it reads the build file a block at a time;
  - each block is paired with the batch's rows not yet decided, a batch of pairs at a
    time;
  - the residual's conjuncts fold over the pairs as the in-memory probe's do
    (`residualFold`), where a null is not a match;
  - once every row is decided it leaves the file;
  - the rows whose verdict the join wants come out in probe order.

  It holds a probe batch, a block and a batch of pairs, and reads the file once a
  probe batch at most.
- **The refusal stays as a fallback** for any keyless shape still not covered.

## 3. Tests and teeth

- **`TestAKeylessExistsSpillsItsBuildSide`:**
  - an exists of two conjuncts, and a not-exists over a build column a fifth null;
  - under 4 KiB, on one thread and on four;
  - each answers as it does in memory, in the probe's order, spills, and leaves no
    file.
- **`TestCrossJoinUnderALimitSpills`:** its residual case spilled where it was
  refused.
- **`TestBudgetErrorNamesTheOperator`:** the keyless residual case leaves the
  refusals.

**Found in the fixture.** A first exists compared `v % 7` with `bv % 7`. The
planner took that equality for a join key, which made a keyed join of seven keys,
too few to partition. The fixture's conjuncts now mix the two sides on one side
of each comparison.

| tooth | result |
| --- | --- |
| a keyless residual join never spills | **bites:** two tests |
| probed as a cross join | **bites** |
| the verdict wanted ignored | **bites:** the not-exists case |
| a null predicate taken for a match | **bites:** the nulls the fixture has, added for it |
| only the last conjunct counts | **bites:** the two-conjunct exists |
| decided rows kept open | silent, and equivalent: a decided row's pairs are skipped anyway, so it costs only the early stop |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
