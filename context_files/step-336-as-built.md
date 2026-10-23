# Step 336 — as built

**A window inside a window, and an aggregate inside an aggregate,** as Polars computes
them: two P1 rows of `feature-gaps.md` §5, found at steps 279 and 281, and an internal
error found at step 335.

## 1. What answers now

| expression | Polars' answer, and now ursus's | before |
| --- | --- | --- |
| `x.Shift(1).CumSum(false).Over(g)` | shift within g, then a running sum within g | refused |
| `x.Diff(1).CumSum(false).Over(g)` | the same, Diff's Shift beneath | refused |
| `x.Shift(1).CumSum(false).OverWith(g by t)` | both in t's order | refused |
| `x.Sum().Over(g).CumSum(false).Over(h)` | g's sum within each h, partitioned by h then g | refused |
| `x.Shift(1).OverWith(g by t).CumSum(false).Over(h)` | the inner window's own order | refused |
| `(x - x.Mean()).CumSum(false).Over(g)` | each g's mean beneath the sum | **an internal error** |
| `(x - x.Mean()).CumSum(false).OverWith(g by t)` | the mean takes no order | refused |
| `(x - x.Mean()).Abs().Sum()` in a `Select` | the frame's sum of distances from its mean | refused |
| the same in `Agg` | each group's | refused |
| `x - x.Mean()` in `Agg` | each row's value less its group's mean, a list a group | refused |
| `x.Sum().Mean()` in `Agg` | the group's sum | refused |
| `t.SortBy(x - x.Mean()).First()` | sorted by the distances | refused |
| `x.Rank(...).Mean().Over(g)` | the mean of g's ranks | wrong, by step 335's bug |

**Still refused:** an aggregate whose answer depends on the order, `First`, under an
order, inherited or written, as an ordering over an aggregate is (Polars takes the
first in the order); a window in a window's keys; a window inside one mapped other than
to rows.

## 2. How

`internal/plan/resolve_window.go`, `extractWindows`:

- **Layers:** each window is registered one layer past the deepest temporary its body
  reads, and the layers become a stack of Window nodes, the innermost first. Identical
  windows still share one temporary.
- **`liftOperands`:** in a window's operands, the WinFn's children or the aggregate's,
  an ordered function becomes a window of the outer one's partition and order; an
  aggregate a window of its partition, and of its order only where the answer depends
  on it, which is refused; a window a sugar built takes the outer keys; one the user
  wrote is partitioned by the outer keys then its own, ordered by its own order or
  else the outer one's (`orderFor`). Each is rewritten, so nesting of any depth lifts.
- **`distribute`** hands an explicit inner window the outer partitions too, where it
  declined, and an aggregate the order only where it matters.
- **An ordering over an aggregate** is refused while the query is resolved, before an
  optimizer rule would type it.

`internal/plan/resolve.go`, `listsPerGroup`: `innerAggsWindowed` makes each aggregate
inside another's input a window over the frame, which `perGroup` partitions by the
group-by's keys; what is still not an aggregate, a mix, is imploded and its aggregates
windowed. `internal/expr/reshape.go`: `SortBy` and `Gather` take an aggregate in their
keys, an implode holding one.

**Faster windows, all of them:** `internal/physical/window.go` numbers the partitions
of one integer key with the group-by's `IntKeyTable`, a chunk of keys at a time,
nulls by `GetOrInsertNullable`, where every row's key was encoded and hashed as bytes;
a sink a level down keeps the encoded table its skew reads (`winPartition.ints`,
`groups`, `idBytes`, `intIDs`). `kernel.takeValidity` takes a validity bitmap with no
null in it for none, an aggregate's one-row answer's, and builds a word of 64 rows at
a time where it appended a bit a row.

## 3. Tests

**`TestNestedWindowsAndAggregates`,** every answer Polars 1.44.1's but one sum of only
a null (§9): fourteen windows inside windows, two aggregates inside windows, eight
aggregates inside aggregates over the frame, by g and broadcast, two mixes in `Agg`,
`x.Sum().Over(g).Sum().Over(g)`; three refusals, none from an optimizer rule; three
nestings streamed past a memory limit, equal to memory.

**`partitioned by an integer, a null beside a zero`:** a null key is a partition of
its own, not 0's, by `Sum` and `CumSum` over it, Polars' answers.

**Flipped,** each to Polars' answer: `TestNestedAggregateRejected` (now
`TestNestedAggregateIsTheGroups`), `TestNonAggregateInAggRejected` (now
`TestAMixInAggIsAListAGroup`), `TestAggregationLists`' mix,
`TestWindowsTheUserScopedStayRefused`' three (now `TestWindowsInsideWindowsAnswer`, a
`First` under an order still refused), `TestWindowRefusals`' nested windows.

**Teeth (16 bite):**

- one layer for every window;
- the layers outermost first;
- an inner function over the frame;
- an inner function in no order;
- an inner aggregate over the frame;
- a user's inner window by its own keys alone;
- a user's inner window in no order;
- an order kept over a mean;
- a distributed window by its own keys alone;
- an ordered first answered;
- an inner aggregate left in its outer one;
- a mix's aggregates left;
- an aggregate in sort_by's keys refused;
- an integer key's nulls numbered as their slots;
- an integer key's partitions counted as none;
- a gather's validity a bit off.

## 4. Performance

A million rows of 1000 Int64 groups, Float64 values, the median of nine, old and new
alternated twice, against Polars 1.44.1:

| | before | now | Polars |
| --- | --- | --- | --- |
| `x.Sum().Over(g)` | 38.4 ms | 15.5 ms | 8.1 ms |
| `x.CumSum(false).Over(g)` | 45.6 | 22.7 | 14.9 |
| `x.Shift(1).Over(g)` | 48.6 | 23.9 | 16.2 |
| `Agg(x.CumSum(false))` by g | 75.8 | 51.3 | 10.6 |
| `x.Shift(1).CumSum(false).Over(g)` | refused | 45 | 23.1 |
| `(x - x.Mean()).Abs().Sum()` by g | refused | 32 | 17.0 |
| `(x - x.Mean()).Abs().Sum()` over the frame | refused | 21 | 4.2 |

A window inside a window is a window beneath, and costs one more window: its
partitions found again, its input concatenated again. Over the frame the mean is a
column of a million rows where Polars keeps one value; a scalar computed first, which
kernels already broadcast, is the way to Polars' time (§8).

**Gate:** test-all 135 ok, race 27 ok (the root package 553 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
