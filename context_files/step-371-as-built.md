# Step 371 — as built

**Slice pushdown:** a `Head` or a `Slice` tells the scan or the sort beneath it how few
rows it needs through the nodes that keep rows, where it reached only the one directly
beneath it. P3 of `v0.6.8-scope.md`.

## 1. How

`limitPushdown` (`internal/plan/rule_limit.go`) carries the row count down as a hint,
`limitRows`, to a scan's `MaxRows` or a sort's top-k, the Limit or Slice staying above
to enforce it. It reaches through what keeps each row, in order, one for one:

- a **`Project`** and a **`WithColumns`**: the planner makes a window, a sort, a
  reverse, a head or a unique of an expression nodes of their own beneath them, so
  their expressions are all row by row, probed at this step;
- a **row index**, whose first rows are numbered as they are beneath it;
- another **`Limit`**, the tighter of the two;
- a **union**, into each input;
- a **left join that keeps its left order**, `JoinOrderLeft` or `JoinOrderLeftRight`,
  into its left input: each left row is a row or more of its answer.

A **`Slice`** of `Len` rows from `Offset` is a hint of `Offset+Len`; one to the end,
a negative `Len`, none. A filter, a window, a group-by, a distinct, every other join,
and a left join of no promised order stop it, as does every other node.

## 2. Measured

The PDS-H lineitem file at SF=1, on four threads, v0.6.8's step 370 and this step
alternating, two rounds of two runs of ten:

| | step 370 | here | Polars 1.44.1 |
| --- | --- | --- | --- |
| `Select(l_orderkey, l_quantity*2).Head(10)` | 7.7–8.4 ms | 3.1–3.4 ms | 2.0–2.7 ms |
| `Select(...).Sort(desc l_extendedprice).Select(...).Head(10)` | 379–380 ms | 143–151 ms | 198–228 ms |

The second is a top-k where it was a sort of six million rows.

**The plans:** the 37 queries the suites run, printed by the bench's own query code at
step 370 and here: **byte-identical**; their heads sit directly over their sorts
already. Of the 34 golden plans one changed, `slice.txt`: a slice of two rows from
the first now tells its scan `max rows: 3`, checked by hand and updated.

## 3. Tests

- **`TestALimitReachesThroughWhatKeepsRows`:** a head over a projection, over a
  `WithColumns` and a row index, over a projected sort, a slice, a slice of a sort, a
  head over a union, over an ordered left join and over two heads, each with its hint
  in the plan, the union's in both inputs; a filter, a window, a group-by, a left join
  of no order, an inner join and a slice to the end with none. Each answers as with
  limit pushdown off, on 1 and 4 threads, in batches of 4, 64 and the default.

**Teeth: 10 of 10 bite:** not through a projection, or a `WithColumns` and a row
index; through a filter, or a window; into a left join of no order, or an inner join;
a slice's hint its offset alone; a union's first input alone; a limit's hint not the
tighter; a slice to the end hinted.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 27
packages ok and the root's golden `slice` plan failed, as above; updated, the plan
coverage test passes. Levels, vet ×3 and the bench engine tests clean; the 22 PDS-H
answers match DuckDB.
