# Step 383 — as built

**Upsample's grid joined in its order, not sorted back.** U1 of `v0.7-scope.md`, its
first part.

## 1. How

`Upsample` (step 306) is a composition: each group's grid of instants, a group-by of
its first and last explode into steps, joined left to the frame by the group's keys
and the instant. The grid and the frame were each numbered, and the join's rows
sorted by the two numbers, the grid's order and each step's rows in the frame's.

A join that keeps its left frame's order, `JoinMaintainOrder(JoinOrderLeft)` (step
314), promises that order itself: the left rows in theirs, each one's matches in the
right frame's, under a memory limit too, where a join that spills sorts its rows back
by their places in the inputs. So the grid and the frame are no longer numbered, and
the sort is gone: a quarter of `Upsample`'s CPU.

## 2. Measured

§4's U1 probe (step 382): a million rows of 100 groups, one or two seconds apart,
upsampled by the second, on four threads, step 382's build and this step's
alternating, two rounds of seven: **283.9–309.1 ms to 223.7–229.2**, 0.74, where
Polars takes 21.0–24.5.

What is left is the join's keys, about two thirds of the time: a group's Int64 and
the Datetime, joined with nulls equal, are 130 bits with their null bits, which step
381's two words do not hold, so each row's keys are encoded, hashed and compared as
bytes, built and probed. A join's keys of two words, and an upsample that joins on a
group's number, are left for a later step.

No suite query upsamples.

## 3. Tests

**`TestUpsampleKeepsTheGridsOrder`** (root): 20,000 rows of 60 groups, a null group
among them, in no order, with instants repeated, between two steps and null,
upsampled by the hour on one thread and eight in batches of 97: row for row the
answer of step 306's composition, numbered, joined and sorted, written out in the
test. `TestUpsample`'s answers, Polars' and in its order, and
`TestUpsampleUnderAMemoryLimit`, spilled, stand.

**Teeth: 2 of 2 bite:** the join's order not asked for, which only the spilled run
catches, a join that fits keeping its left frame's order anyway; the frame's order
asked for.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
