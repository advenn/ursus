# Step 104 — as built

**An inner join hashes its smaller input.** This is `v0.4-scope.md` item 3, the
second lever of step 101's ranking. The scope named one decision to take first:
ursus kept probe-side row order, and a swap changes it.

## 1. What was wrong

The physical join always builds its hash table from the right input
(`join.go:1501`: "The RIGHT side always builds. Not cost-based"). In the PDS-H ports
the right input is often the fact table:

- q7's chain joins a few thousand customers to orders, then to lineitem;
- q8 and q9 build part, lineitem and orders in the same way.

Step 101 measured q7's build at 26% of its CPU, serial. In q8 the build also held
900 MB.

## 2. The decision: order

**Polars promises no inner-join order unless asked** (`maintain_order`). ursus now
says the same, and it is the only kind that can swap: an inner join's answer is
symmetric.

- **`JoinMaintainOrder(true)`** keeps the left frame's order. It is new, and the
  scope's "promised, not built" list had it as MaintainJoinOrder.
- **Left, right, semi, anti and full joins** keep their order regardless.
- **The `Join` doc says so.**

Under a memory limit, no join's order was promised already
(`TestJoinOrderIsUnspecifiedUnderALimit`).

## 3. The rewrite

**`build_side`, a plan rule** (`internal/plan/rule_buildside.go`):

```
Join(L, R)  →  Project(Join(R, L))   when est(R) > 2 × est(L)
```

The physical join is unchanged: its spill partitioning, residuals and parallel probe
all assume the right input builds, and all of them still see that.

- **The Project restores every output column's name, position and type.**
  - Each original column is mapped by its provenance in the two layouts.
  - A merged key, which the swapped join merges the other way round, maps to the
    swapped join's own left key.
  - A column that would collide in the swapped join takes an internal suffix,
    `__build_side_swap`, and is renamed back.
- **Whenever the mapping fails, or the projected schema is not exactly the
  original** (nullability included), the join stays as written. An optimization
  never fails a query.
- **It is not applied to:**
  - a join with a residual;
  - a join with a Validate, which names one side's keys as the unique ones;
  - a join with `MaintainOrder`;
  - a join with an input of unknown size.
- **It runs after every pushdown,** so the inputs it exchanges are the ones that
  will run, and before simplification.
- **`Flags.BuildSide`** switches it off, as the other rules have their flags.

**Estimates.** `plan.RowEstimator` is a new optional interface on sources:

- **The memory source** is exact.
- **Parquet** sums its footer row counts, which `Schema` already reads at plan time.
- **CSV knows none,** so a join over a CSV input is never swapped.

`plan.EstimateRows` walks the plan:

- **An upper bound through everything that only removes rows:** filters, group-bys,
  distinct, windows. Nothing here can estimate a selectivity, and a bound is honest
  where a guess would not be.
- **A limit or tail:** its N.
- **A join:** the larger input, which is right for key-to-foreign-key joins.
- **A union:** the sum.
- **Anything else is unknown.**

The swap needs the right input estimated at more than twice the left, so a loose
estimate does not flip joins of similar size.

## 4. Measured

The machine was in use (Chrome, load average 4–10), so wall times are the best of
several runs, alternating step 103's runner with this one, at SF=1:

| query | before | after | |
| --- | --- | --- | --- |
| q8 | 2,341–2,504 ms | 673–675 ms | **−72%** |
| q9 | 2,313–2,358 ms | 1,060–1,074 ms | **−54%** |
| q5 | 915–926 ms | 552–617 ms | −37% |
| q7 | 1,176–1,577 ms | 1,174–1,347 ms | about the same |

Peak RSS is not affected by load:

| query | before | after |
| --- | --- | --- |
| q8 | 900 MB | 237 MB |
| q7 | 600 MB | 175 MB |
| q9, SF=0.1 | 157–166 MB | 133 MB |

q7's CPU time rose about 8%: it now probes 3.3M rows in parallel where it built them
serially. Its wall time did not move, because its remaining cost is the scan and the
probe.

## 5. Tests and teeth

**`TestBuildSideKeepsTheAnswer`** joins a 6-row frame to a 60-row one in eight key
shapes:

- a same-named merged key;
- different names;
- different names merged on request;
- Int32 against Int64;
- `NullsEqual`;
- two keys;
- a computed key;
- `JoinCoalesce(false)` with a custom suffix.

In each, the schema is exactly the unswapped one, and the rows, sorted, are the
same. It runs `WithVerify`. Each case checks the swap happened, and that the join
matched rows, so none is vacuous.

A ninth case is a merged key nullable on one side only. It must not swap, because
the schema would differ.

**`TestBuildSideFiresOnlyWhereItShould`:**

| case | swaps? |
| --- | --- |
| the right ten times the left | yes |
| the right only twice the left | no |
| the right smaller | no |
| a left join | no |
| a semi join | no |
| an anti join | no |
| `JoinMaintainOrder` | no |
| a validated join | no |
| the right input under a filter | yes, the estimate being a bound |

**`TestJoinMaintainOrderKeepsTheLeftOrder`:** exact row order, against the rule
switched off.

**One existing test changed.** `TestSpillingJoinIsBounded` measures the 60,000-row
right input's build spilling, and the rule now hashes the 500-row left instead. It
runs with `BuildSide` off, saying why: it pins the operator, not the planner's
choice. No other test in the suite depended on which side builds.

| tooth | result |
| --- | --- |
| any larger right swaps, not only twice | **bites** |
| a merged key mapped as a plain left column | **bites:** four answer cases, two firing cases |
| the schema not compared | **bites:** the one-side-nullable case. A first mutation failed to build and does not count. |
| no estimate through a filter | **bites** |
| `MaintainOrder` ignored | **bites:** both order tests |
| a semi join allowed to swap | **bites**, once the semi case's keys were named differently. With a merged key the mapping failed first and hid it. |
| a left join allowed to swap | silent, and rightly: its right columns are nullable, so the swapped inner schema differs and the guard refuses it |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB, with the rule swapping the joins it does in q5, q7, q8 and q9.
