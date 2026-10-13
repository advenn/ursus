# Step 245 — as built

**Tier 2 of `v0.6.4-scope.md`: a top-k with a large k spills,** where it held its
best k past the limit without a word.

## 1. Why

`Sort(...).Head(k)` is a top-k (`limit_pushdown`). Past the limit it compacts its
buffer to its best k rows, which is exact and keeps the order stable. A small k
always fits. A large k does not: the compacted rows can themselves be past the
limit, and the sink neither spilled them nor checked.

## 2. What changed

**`sortSink.Consume`.** A bounded sort over the limit compacts as before. Then, if
the query is still over and the k rows held are at least a sixteenth of the limit,
or the sink's `minRun`, it spills them as a run, as a sort's rows go. A small k's
never reach a sixteenth, so a small top-k still never spills.

**`sortSink.Finish`** of a spilled top-k merges the runs and the rest, and takes the
first k (a `limitOp`).

**Why it is exact.** Each run is the best k of its stretch of the input, in order.
So the whole input's best k are among the runs' together. The merge breaks ties by
run, and runs are made in input order, so it is stable: `ArgTopK == ArgSort[:k]`
holds across runs as it did across compactions. `compact`'s comment says so.

## 3. Tests and teeth

**`TestALargeTopKSpills`:**

- the best 12,000 of 20,000 rows, by a key of six values whose ties span every
  batch, and by a nullable key descending, nulls last;
- under 64 KB, at 64 rows a batch:
  - the rows and their order, ties included, are the in-memory answer's;
  - it spills, 21 runs;
  - it peaks at 1.3× the limit, at most 2×;
  - the spill directory is empty after;
- why 64 rows a batch: the merge of sixteen runs holds a batch of each, at 512 rows
  eight times this limit (523 KB).

**`TestASpilledTopKGivesItsBestK`** (internal): a top-k of 40, over its limit at every
batch, spills each time. Finish gives exactly the stable sort's first 40. The plan
keeps a Limit above every top-k, so only this test holds the sink to its own word.

**`TestTopKSurvivesSpilling`** still holds: a top-k of 25 compacts and never spills.

| tooth | result |
| --- | --- |
| a large k never spills | **bites:** nothing spilled, and the peak |
| the merge not cut at k | **bites:** the internal test; end to end the plan's Limit cuts it too |
| every row spilled, not the best k | **bites:** `TestTopKSurvivesSpilling` spills |
| a small k spills whenever the query is over | silent: no test holds the query over by another operator beside a small top-k. There, its k rows would spill at every batch: still right, but in more runs |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
