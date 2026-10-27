# Step 386 — as built

**A window in an aggregation evaluated over each group's list.** W1 of
`v0.7-scope.md`, its aggregation part, and two costs found on the way.

## 1. How

Step 331 answered `GroupBy(g).Agg(Col("x").CumSum())` as Polars does, each group's
running sum as a list, by lifting the window beneath the aggregation, partitioned by
the group-by's keys: the rows hashed and partitioned a second time, each partition
walked, the answer taken back to the rows and imploded a group.

- **`listEvalPerGroup`** (`plan/resolve.go`): an aggregation's expression of one
  column, of window functions with no partition or order of their own and of
  row-wise functions, is the column's list a group with the expression evaluated over
  each list, `Agg(Col("x")).List().Eval(Element().CumSum())` as a user would write
  it. Both read a group's rows in their order. An `Over` within the group, a second
  column, an aggregate inside, a UDF, keep the window beneath, as before.
- **A list's window without an order** (`physical/listeval.go`, `bodyWindows`): a
  window function in a list's eval sorted the elements by their list, to find each
  list's elements, though they lie a list at a time already; with no order and no
  rank the sort is the identity, and is skipped: half an eval's time.
- **An Int64's running sum** (`kernel/window.go`, `winCumInt128`): the values are
  read where they lie, widened a row at a time, where all of them were first copied
  at 128 bits; and a column of no nulls is not asked for each row's. This is every
  integer `CumSum`'s kernel, in a window, an `Over` and a list.

## 2. Measured

§4's W1 probe (step 382), a million Int64s of 1,000 groups, four threads, step 385's
build and this step's alternating, two rounds of seven at a load of 1.7 to 1.9:

| | step 385 | here | Polars, step 382 |
| --- | --- | --- | --- |
| `Agg(Col("x").CumSum())` | 61.9–69.7 ms | 34.3–36.2 | 10.6–12.0 |
| `CumSum().Over(g)` | 31.8–36.6 | 28.2–30.3 | 15.1–17.2 |
| the list eval alone, 1,000 lists of 1,000 | 30.2–32.1 | 15.1–17.0 | |

What is left of the aggregation is the implode, a fifth, and the running sum at 128
bits, a third, which ursus answers as an Int128 where Polars' is an Int64. Of the
`Over`, half is the window kernel, which walks the partitions one after another on
one thread, each partition's rows scattered through the frame; running partitions
in parallel changes every window kernel and is left.

No suite query changes: the 37 plans of PDS-H and h2o are step 382's, none
aggregating a window or evaluating a list, and none sums an integer cumulatively.

## 3. Tests

**`TestAWindowInAnAggIsEvaluatedOverEachGroupsList`** (root): 5,000 rows of 40
groups, an Int64 with nulls and a Float64 with nulls and NaN; each of `CumSum`,
reversed, `CumMax`, `CumMin` reversed, `CumProd`, `CumCount`, `Shift` forward and
back, `Diff`, `Rank` average and dense descending, arithmetic around a `CumSum`, a
`CumSum` of a `Shift`, a conditional's `CumMax`, in an `Agg`, against the same
expression `Over` the keys, imploded a group; on one thread and four in batches of
301; and an `Over` within the group, one by the column itself, a second column and an
aggregate inside, which keep the window beneath. Each plan evaluates over the lists
or not as it should.

**Teeth: 3 of 4 bite:** a body of two columns taken as one list; a list's elements
left unsorted for an order; an Int64's null rows summed. Silent: an `Over` within the
group taken as the group's, since the list eval's lowering refuses a window with a
partition of its own, and the rewrite falls back on it; the check is a second guard.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
