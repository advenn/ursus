# Step 251 — as built

**E2 of `v0.6.5-scope.md`: `Expr.Filter` as an aggregate's input,** a conditional
aggregate, SQL's `FILTER (WHERE …)`:
`Col("a").Filter(Col("b").Gt(0)).Sum()`. It works in a group-by, a window, a rolling
or dynamic group, and as a scalar in `Select`, `WithColumns` and `Filter`.

## 1. What changed

**`internal/expr/filter.go`:**

- **`Filter{Child, Pred}`** is the node. Its type is its child's, and its predicate
  must be Boolean. `Rebuild` copies it.
- **`LooseFilter(n)`** finds a filter that is not an aggregate's input. An
  aggregate's input may be a chain of filters, whose predicates are checked too.
  `expr.Resolve`, which every context calls on each expression, refuses one: "…
  changes the column's length, and is only an aggregate's input yet". The hints name
  the aggregate form and the frame's `Filter`.
- **`FilterChain(n)`** strips the chain from an input and returns its predicates.

**`Expr.Filter(preds...)`** (`fill.go`) ANDs its predicates. The comment on the
refusal stubs, which said there is no `Expr.Filter`, now says where it stands.

**The sinks.**

- **`aggInput`** (`internal/physical/agg.go`) evaluates an aggregate's input, and
  under filters keeps the rows every predicate is true on, with their groups.
  `passingRows` intersects each predicate's `SelectionFromMask`, so a null drops its
  row as the frame's `Filter` does.
- **The group-by,** a window's in-memory feed and its one-partition stream feed
  their accumulators through it.
- **The temporal groups' feed** evaluates which gathered pairs each filtered
  aggregate reads, and `readPairs` narrows each segment's pairs to those.
- **The accumulators are the same,** so a filtered median or n_unique spills its
  values, and a filtered aggregate over one partition streams, where the plain ones
  do.

**Semantics:**

- **What the aggregate sees:** only the rows that pass. `Len` counts them; `First`
  and `Last` pick among them; a group none of whose rows pass aggregates no rows.
- **A chain** (`a.Filter(p).Filter(q)`) is p AND q over the frame's rows. Polars
  refuses a chain whose second predicate reads the whole column, a length mismatch;
  its own form for the same is one filter of both.
- **Not yet:** a filter inside an expression an aggregate reads,
  `(a.Filter(p) + 1).Sum()`, is refused. Polars takes it; it is E3's and E4's shape.

## 2. Tests and teeth

**`TestAFilterIsAnAggregatesInput`:** six queries against Polars 1.44.1's answers on
the same frame:

- a group-by of nine filtered aggregates, among them a chain, over a group none of
  whose rows pass and a null predicate;
- a window;
- a scalar `Select`, and a broadcast;
- a rolling group and a dynamic one.

It also checks the refusal of a filter in `Select`, in `WithColumns`, and inside an
expression an aggregate reads.

**Where it differs from Polars, on purpose (`feature-gaps.md` §9):**

- the sum of a group none of whose rows pass is null; Polars gives 0;
- `n_unique` does not count a null; Polars counts it.

The comparison also showed that **`GroupByDynamic` with keys puts the index column
first, and Polars the keys.** That is older than this step, and is now in §9.

**`TestAFilteredAggregateSpills`,** under a tenth of the input: a filtered median,
n_unique and len over a hot key, and a filtered sum and broadcast median over one
partition. Each gives the in-memory answer, spills, and leaves the directory empty.

| tooth | result |
| --- | --- |
| the group-by reads every row | **bites** |
| a null predicate keeps its row | **bites** |
| only the first of a chain read | **bites** |
| the window reads every row | **bites** |
| the one-partition stream reads every row | **bites** |
| a temporal group reads every pair | **bites** |
| a loose filter planned | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
