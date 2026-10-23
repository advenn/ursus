# Step 331 — as built

**A list a group, and a window a group at a time:** a non-aggregate in `Agg` is each
group's values as a list, as Polars answers it, where ursus refused it, "expression is
not an aggregate" (found at step 329, `feature-gaps.md` §5); and a window anywhere in
`Agg` is computed a group at a time, where it was refused with a hint to move it.

## 1. What it answers

Polars 1.44.1's, a group's rows in their order:

- `Agg(Col("x"))`, `Col("x").Mul(2)`, `Col("x").Add(Col("y"))`, a cast, a conditional, a
  filter;
- `Agg(Col("x").CumSum(false))`, a shift, a diff, an interpolation, and a window under
  arithmetic, `CumSum(false).Mul(10)`, each a group at a time;
- `Agg(Col("x").Sum().Over(Col("h")))`, its partition within the group;
- `Agg(Col("x").CumSum(false).Max())`, a window inside an aggregate, each group's
  greatest running total, Polars' [3, 8];
- named as the expression is: an alias, a name function, and a conditional by its
  first branch.

**Unchanged:**

- **an aggregate** is one, and **a literal** a scalar, beside a list;
- **a mix of an aggregate and a row's value,** `Col("x").Sub(Col("x").Mean())`, is
  still refused (`feature-gaps.md` §5, an aggregate inside an aggregate's input), and
  `TestNonAggregateInAggRejected` passes as it did;

**Where ursus differs,** as elsewhere: an integer sum is an Int128 (`List(Int128)` for
`CumSum` of an Int64), and a sum of nulls is null where Polars' is 0.

## 2. How

`listsPerGroup` in `internal/plan/resolve.go`, in `resolveAggregate` before the
aggregates are checked:

- each expression neither an aggregate nor holding an unbound one becomes
  `Implode` of itself, under its own name: names at its top kept above, and an alias
  where an aggregate's naming would differ;
- a window in one is partitioned by the group-by's keys first: a window function
  under `Over(keys…)`, and an `Over`'s partition with the keys before its own;
- those windows are lifted by `extractWindows` into a `Window` beneath the
  aggregation, which reads each as a column.

A window inside an aggregate is lifted the same way, and the aggregate reads its
column: `TestTheWindowHintMovesTheWindow`'s two refusals now answer as the recipes
their hints gave. Only the expressions holding a window go to `extractWindows`, each
partitioned first: given every aggregate, it would lift a window over the whole
frame.

## 3. Tests

**`TestAggregationLists`,** every answer Polars' but the Int128 sums:

- fifteen expressions in `Agg`, each group's list and its name and type;
- an aggregate, a list and a literal side by side;
- a window inside an aggregate;
- a mix refused.

**Three tests that asserted the refusals,** now asserting the answers:

- `TestTheWindowHintMovesTheWindow`'s window in `Agg` and window function in `Agg`
  answer as the recipes their hints gave;
- `TestWindowInsideAggIsRefused` is `TestWindowInsideAggRunsAGroupAtATime`: Explain
  prints a plan that runs, and the answer is the window computed first;
- `TestWindowRejectedInsideGroupByAgg` is `TestWindowInsideGroupByAggIsAListAGroup`:
  the window's values imploded a group.

## 4. Performance

Against Polars 1.44.1, a million rows of 1000 groups, the median of 7:

| operation | ursus | Polars |
| --- | --- | --- |
| `Agg(Col("x"))`, a list a group | 26.7 ms | 10.1 ms |
| `Agg(Col("x").Implode())`, for scale | 26.5 ms | 8.7 ms |
| `Agg(Col("x").Mul(2))` | 29.7 ms | 12.8 ms |
| `Agg(Col("x").CumSum(false))` | 113.0 ms | 13.5 ms |

A list a group is `Implode`'s own speed, which this step does not change. A window a
group at a time is a window's machinery beneath a group-by, which Polars computes in
the group-by's own groups. By profile:

- `Over`'s partition keys, hashed by the generic key table where the group-by
  uses its integer one, about a fifth;
- `CumSum` of an Int64 computed in Int128, ursus's integer sum, a quarter;
- the gather back to the rows.

`feature-gaps.md` §8 records it, for 0.7's performance.

## 5. Docs

- **Aggregation:** a paragraph under "Lists per group".
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md` §5 and §8, `polars-parity.md`.**

## 6. Teeth, all 6 biting

- non-aggregates refused as before;
- a mix taken for a list;
- a window inside an aggregate left refused;
- windows over the whole frame, not a group at a time;
- an `Over`'s own keys alone, without the group's;
- a conditional named by its predicate.

**Gate:** test-all 130 ok, race 26 ok (the root package 529 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
