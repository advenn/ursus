# Step 314 — as built

**J1, a join's order:** Polars' `maintain_order` (`v0.6.7-scope.md` §5), for every kind
of join, under a memory limit too. `JoinMaintainOrder` took a Boolean; it takes a
`JoinOrder` now, a break.

## 1. What Polars 1.44.1 does, probed

Two frames, `k = [3, 1, 2, 1, 9]` and `[1, 3, 1, 7, 3]`, joined every way:

- **`left` and `left_right`:**
  - the left rows in their order, each row's matches in the right's;
  - a left or full join's unmatched left rows in place;
  - a right or full join's unmatched right rows after all, in theirs.
- **`right` and `right_left`:** the mirror, the unmatched left rows after all.
- **semi and anti:** the left order, whichever is asked.
- **cross:** each left row beside every right row, or each right row beside every
  left one.
- **A merged key:** the left's, `-0.0` against `0.0`, whatever the order.

Polars' `left` promises less than `left_right`, the order of a row's matches. Here
both keep it, as do `right` and `right_left`.

## 2. How

### The API

```go
type JoinOrder = plan.JoinOrder // JoinOrderNone, Left, Right, LeftRight, RightLeft
func JoinMaintainOrder(o JoinOrder) JoinOption
```

`plan.Join.Order` replaces `MaintainOrder`. The label shows `maintain_order right`.

### The left order costs nothing

A join probes its table in the probe side's order. It gives each probe row its matches
in the table's order, which is the build side's arrival order: the parallel build keeps
a key's rows ascending. A Right or Full join then flushes the table's unmatched rows in
that order too.

So a join that fits is already in the left order, the parallel probe included, whose
lanes are drained round-robin. Nothing is sorted. As before, an ordered join is never
swapped by `build_side`.

### The right order exchanges the inputs

The right input probes a table of the left, which `planJoin` builds as follows:

- **kinds:** Left becomes Right and Right becomes Left; Inner, Full and Cross stay;
- **validation:** `1:m` and `m:1` are exchanged;
- **layout:** `swappedLayout` reads each column from the other input. A merged key is
  read from the right's key, merged with the left's;
- **the merged key's value is still the plan's:**
  - `joinSpec.swapped` makes an exchanged inner join read the table's key;
  - an exchanged full join reads it through a new mode, `keyFromEitherRight`;
  - so a `-0.0` key keeps its sign, as Polars' does;
- **errors name the plan's sides:** `duplicateKeyErr` takes the spec and maps them back.

The `runtime_filters` rule leaves an exchanged join alone. Its filter would sit on the
build side, see no keys, and pass everything: an equivalence, but wasted work.

### A spilled join sorts back

A join whose table spills answers its resident buckets' probe rows as they come, then
each spilled bucket after the probe ends. So its rows leave in bucket order.

Whether it spilled is settled when the build ends, before a probe row is read. Then,
and only then, `orderedProbe`:

1. numbers the probe rows, `ordinalOp`, a last column, which routed rows carry through
   their spill files;
2. extends the sink's probe schema, layout and output with that number, and with the
   build side's for a Right or Full join;
3. probes;
4. sorts by the numbers, the probe's with nulls last, then the build's;
5. drops them.

The sort is stable, so the probe number alone orders an inner, left, semi or anti
join: a probe row's rows come out together and in table order in every bucket.

**A Right or Full join's unmatched table rows** have no probe row, and each bucket
flushes its own. So an ordered join of those kinds numbers its build rows as they
arrive. It does so always, in `planJoin`, since a spill is only known after the build.

**A spilled cross join keeps its order anyway,** a block nested loop in probe order,
and is not sorted.

## 3. Found on the way

**`spilled()` only sees closed files.** The build's partition files are closed, and
recorded, at freeze, inside `Probe`. So the first version asked too early, never
sorted, and the spilled rows came back in bucket order: the memory test caught it.
`wroteBuild()` also counts the open writers.

**The sort starved the join.** Over 20,000 rows a side under 64 KiB, the ordered join
refused: "a single join key has more build rows than the limit". The sort held what
the query left it, and each spilled bucket's table then found the query over the limit.

That is the misattribution `feature-gaps.md` §1 recorded at step 307, met from the
other side. `sortSink.maxHeld`, new, makes this sort spill at a quarter of the limit
whatever the query holds. `TestOrderedJoinLeavesTheLimitToTheJoin` keeps that case.

## 4. Tests

**`TestJoinMaintainOrderAsPolars`:**

- the four keyed kinds under four orders, against Polars' answers;
- semi and anti under every order;
- cross both ways;
- a right join's key on every row, the unmatched one's too, and a full join's merged
  key;
- a `-0.0` key kept under the right order, inner, left and full;
- a validation naming the plan's sides;
- the plan's label, and an order out of range refused.

**`TestJoinOrderUnderAMemoryLimit`:**

- six kinds, both orders, one thread and four;
- 4,000 rows a side of 2,000 keys under 32 KiB;
- each spills, and equals the answer without a limit, row for row.

It takes 1.9 s, and 7 s under the race detector on one thread.

**`TestOrderedJoinLeavesTheLimitToTheJoin`** is the starving case.

**Updated for the break:** `buildside_test.go`'s three uses and the guide's three
examples.

## 5. Speed

A million left rows against 100,000 right, in memory, medians of seven:

| | ursus | Polars |
| --- | --- | --- |
| inner, none | 25 ms | 13 ms |
| inner, left | 17 ms | 13 ms |
| inner, right | 57–63 ms | 33 ms |
| left, none | 16 ms | 12 ms |
| left, left | 16 ms | 12 ms |
| left, right | 75–88 ms | 37 ms |

**The left order is free.**

**The right order hashes the million rows.** A plain join that hashes a million rows
takes 47 ms, which is most of the cost. A probe of the left frame, then a counting sort
by the right row, would hash the smaller side.

**The build side's row numbers** are about a tenth of an ordered left join's right
order: 67 ms without them. Only a spilled join needs them.

Both are recorded in `feature-gaps.md` §8.

## 6. Docs

- **The joins guide:** the row order paragraph, and the right join note, now pointing
  to `JoinOrderRight`.
- **Lazy and eager:** the orders.
- **Coming from Polars:** a row.
- **The CHANGELOG:** the break and the feature.
- **The godoc** of `JoinMaintainOrder` and the constants.

## 7. Teeth: 25 of 26 bite

**Biting:**

- kinds or validations not exchanged;
- a merged key read from the probe side;
- an exchanged inner or full join's key from the probe;
- errors naming the run's sides;
- a spilled join not sorted;
- `spilled()` asked before the freeze;
- unmatched table rows first;
- no build numbers;
- the sort holding what it likes;
- an ordered join swapped by the planner;
- an order out of range taken;
- the plan hiding its order.

**Silent, an equivalence:** the runtime filter on an exchanged join.

Two teeth were silent at first. Each led to a new test: the right join's key, and the
starving join.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
