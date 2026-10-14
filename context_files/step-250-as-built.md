# Step 250 — as built

**E1 of `v0.6.5-scope.md`: an aggregate in `Select`, `WithColumns` and `Filter` is a
scalar,** as in Polars, where it was refused pointing at `GroupBy`. A window in
`Filter`, SQL's QUALIFY, is computed too.

## 1. What changed

**`internal/expr/scalar.go`:**

- **`IsScalar(n)`** reports one value for the frame: `n` aggregates, and reads no
  column, selection or window's answer outside an aggregate.
- **`IsConstant(n)`** reports a literal, or an expression of literals.
- **`BindScalars(n)`** wraps each largest scalar subexpression in a `Window` with no
  partition, the node `.Over()` builds. A name at the root, an alias or a prefix,
  stays at the root. Naming reads through a window either way, but `Explain` then
  shows the window where a user would write it.

**The three row contexts (`lazy.go`, `agg.go`):**

- **`Select`:** when every expression is a scalar or a constant, and one at least a
  scalar, it is a keyless `plan.Aggregate`, which is `GroupBy().Agg(...)`: one row,
  as Polars makes `select(a.sum(), lit(7))`. Otherwise each expression is bound, so
  an aggregate is broadcast to every row. A `Select` of literals alone is left as it
  was, a row for each, where Polars gives one.
- **`WithColumns` and `Filter`** bind each expression.

**A window in a `Filter` (`internal/plan/resolve.go`).** `resolveFilter` lifts the
predicates' windows into temporaries beneath the filter (`extractWindows`), as
`WithColumns` does, filters on them, and projects them away above it (`dropTemps`),
keeping the input's columns. It refused every window; now any works, the aggregates
`Filter` broadcasts and the user's own, such as
`Filter(Col("x").Sum().Over(Col("g")).Gt(10))`. Predicate pushdown already stops at
a window, so nothing is pushed beneath it.

**The broadcast streams** past a memory limit, since it is the window with no
partition of steps 197 and 242: the one-partition file, the aggregate fed as group 0,
a median's values spilled.

## 2. Tests and teeth

**`TestAnAggregateInARowContextIsAScalar`:** twelve queries, each against Polars
1.44.1's answer on the same frame, written as CSV by Polars itself:

- an aggregate beside a column;
- aggregates alone, and beside a literal, one row each;
- a centred column;
- a filter above the mean;
- a QUALIFY filter;
- scalar arithmetic;
- a column times its sum;
- `All().Sum().Suffix("_s")`;
- `Col("x", "y").Mean()` beside a key;
- shares of a total;
- a range and a standard deviation.

They were also compared with Polars in one script before the test was written, and
the twelve were identical.

**`TestABroadcastAggregateStreamsPastItsLimit`:** a centred column with a broadcast
median, and a filter above the mean, under a tenth of the input. Each gives the
in-memory answer, spills, and leaves the spill directory empty.

**Tests that flipped:**

| was | now |
| --- | --- |
| `TestAggregateRejectedInSelect` | `TestAnAggregateInSelectIsAScalar`: one row, equal to the keyless group-by; broadcast beside a column, equal to `.Over()` |
| `TestCondRejectedInSelect` | `TestAnAggregateInsideAConditionalBroadcasts`: Polars' answer |
| `TestWindowAcceptedWhereAggregateIsNot`: the bare form refused | it answers |
| `TestWindowRefusals`: a window in a filter | removed; a window in a sort key is still refused |
| `TestTheWindowHintMovesTheWindow`: a window in a filter refused | its answer equals the `WithColumns` the hint gave |

| tooth | result |
| --- | --- |
| a column outside the aggregate counts as scalar | **bites** |
| a Select of aggregates is never one row | **bites** |
| a literal beside an aggregate is broadcast | **bites** |
| a filter's windows not dropped | **bites:** the temporaries show in the answer |
| a filter's aggregate not broadcast | **bites** |
| the window wraps the alias | silent, and equivalent: naming reads through a window; the case keeps `Explain` as written |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
