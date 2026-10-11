# Step 195 — as built

**O4 of `v0.6.1-scope.md`: a cross join over its budget.**

## 1. Why

A spilling join divides its build side by radix-partitioning the key space, and a
cross join has one key, so it never split. Past the limit it refused: "a cross join
buffers its whole build side and cannot spill". `JoinWhere` over a non-equi
predicate is a cross join with a Filter above it, as `Explain` shows, so that
refused too.

## 2. What changed

A cross join needs no dividing. Every probe row meets every build row, so the
build side can sit on disk and be read back: a block nested loop (`crossspill.go`).

- **`crossSpills`:** a pure cross join, of kind `JoinCross` with no residual.
- **`spillCross`:** past the limit, `overBudget` writes the build side's resident
  rows to one spill file instead of refusing, and lets them go, per-row state
  included. Every later build batch is written after them (`Consume`'s new first
  case).
- **`crossLoopOp`:** `Probe` returns it once the file is closed. It joins the probe
  side a row at a time, each probe row against the file read from its start, a block
  at a time, through the join's own `gatherOut`. That keeps the in-memory order, each
  probe row's pairs in build order, and holds one probe batch and one block.
- **The cost:** reading the file once a probe row is I/O in proportion to the output,
  which is a build side's worth of rows for each probe row anyway.
- **A join with no keys and a residual still refuses.** That is a `WhereExists` of a
  predicate alone, a semi or anti join. The message now names it, and says a plain
  cross join spills. The budget's join hint and `LazyFrame`'s memory doc say the same.

A cross join's build is never deferred, since it has no keys, and so is never
exchanged at runtime (step 162), whose condition is a deferred build. The breaker
drains it into one sink, so no merge meets a spilled one.

## 3. Tests and teeth

**`TestCrossJoinUnderALimitSpills`** replaces `TestCrossJoinUnderALimitRefuses`.
Under 4 KiB:

- a 300-by-500 cross join reaches disk and answers frame-equal to the in-memory
  join, all 150,000 rows in order;
- a keyed join of the same data spills by partitioning, as before;
- a `WhereExists` with no keys refuses with a resource error naming the residual.

| tooth | result |
| --- | --- |
| a cross join over its limit refuses again | **bites** |
| later build batches not written | **bites** |
| the rows held before spilling not written | **bites** |
| a probe row meets only the first block | **bites** |
| any keyless join spilled as a cross join | **bites:** the `WhereExists` case |

**`TestBudgetErrorNamesTheOperator`'s cross join case** now answered, after five
seconds of spilling a 4,000-by-4,000 product, and the gate's first run failed on it.
Its operator is now a keyless `WhereExists` with a residual, the join that still
refuses. `TestEveryCitedTestExists` caught the comment that cited the renamed test.

**Gate,** the second run: test-all 115 ok, race 23 ok, levels, vet ×3 and the bench
engine tests clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
