# Step 439 — as built

**`TestColumnsBesideATextConjunctAreReadAtItsKeptRows`'s allocation bound clear of
CI's noise.** A flake on master's CI, at #37's merge commit `65a6fed`, on 2026-11-03.
The test is the same on both lines.

## 1. What was wrong

Step 391's test reads a Parquet file whose text conjunct keeps a quarter of its rows,
once with the conjunct's column alone and once with two Int64 columns beside it. The
two columns should be read at the kept rows only.

It counts the process's allocations, the least of three reads, and bounded what the
two columns add at a quarter of them read whole: 1.2 MB of 4.8.
- The laptop reads 0.77 MB, the same before v0.7.2's forward-ports as after.
- CI's runners read 1.22 MB at step 391.
- CI's scalar-only job read 1.23 MB at `65a6fed`, and failed.
- The job's re-run passed, as had the same tree on #37's own CI.

## 2. The change

The bound is half the columns read whole, 2.4 MB, where a quarter was within CI's
noise.

## 3. Teeth

| mutation | |
| --- | --- |
| the kept-rows read skipped, the columns read whole | **bites:** 5.6 MB added against 2.4 |

The same change goes to master by a forward-port.
