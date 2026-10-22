# Step 308 — as built

**G1: a group's `Head(n)` and `Tail(n)`** (`v0.6.7-scope.md` §4), as Polars'
`group_by().head(n)` and `tail(n)`.

## 1. What Polars 1.44.1 answers, probed

- **Columns:** the keys first, then the frame's other columns. A key computed from
  `v` and named `v`, `group_by(pl.col("v") % 3)`, replaces `v`; aliased, `m`, it
  stands beside it.
- **Rows:** each group's first or last n rows, in the frame's order. A group of
  fewer rows gives them all.
- **Order:** with `maintain_order=True` the groups come in the order each first
  appears; without it, in another.
- **A null key** is a group.
- **No rows** give none.
- **`head(0)`** gives a row of nulls for each group: it is built as
  `agg(all().head(n))` then exploded, and an empty list explodes to a null.
- **A negative n** fails, "can't convert negative int to unsigned".
- **A dynamic group** has no `head` at all.

## 2. The API

```go
func (g *GroupBy) Head(n int) *LazyFrame
func (g *GroupBy) Tail(n int) *LazyFrame
```

A `DataFrame`'s `GroupBy` is a lazy one already, so `df.GroupBy(k).Head(2)` works,
collected.

## 3. Built from operators that spill

Polars' way, an aggregation of every column's head exploded, would hold every value
of every group: an aggregation's `Head` lowers to an implode and a list head, and an
implode of one hot key refuses past a limit. So it is built as:

1. the keys, as columns of their output names: `WithColumns(keys...)`, which is how a
   computed key replaces a column of its name, as Polars' does;
2. a row number, `WithRowIndex`;
3. each row's place in its group, `CumCount` over the keys, from the end for a tail,
   and its group's first row, the row number's `Min` over the keys;
4. a filter to the places of n or under;
5. a sort by the group's first row and then the row, which gives the groups in the
   order each first appears, each group's rows in theirs;
6. a select: the keys, then every other column in the frame's order.

The key names are the keys' own output names, from their schema. `keyNames()`, which
the shorthand aggregates use, gives the columns a key reads, and would drop `v` for a
key `(v % 3).Alias("m")` where Polars keeps it.

**`GroupBy()` with no keys** is one group, the frame: `Head(n)` is `lf.Head(n)`.

**A dynamic or rolling group refuses,** as Polars has no such method: its windows
overlap, so a row is in several.

## 4. Against Polars

**Every answer of §1 is Polars',** each held in `TestGroupHeadTail`. The guide's
example, each region's last order, is Polars' answer exactly.

**§9:**

- `Head(0)` is no rows, where Polars' is a row of nulls for each group;
- the groups always keep first appearance, Polars' `maintain_order=True`, which is
  not its default.

## 5. The site

`docs/guide/aggregation.md` on `dev` has "Each group's first and last rows", new in
0.6.7, with `Example_aggHeadTail`. The Polars page maps both.

## 6. Tests and teeth

**The tests:**

- `TestGroupHeadTail`:
  - head and tail of 2, a head past a group's rows, two keys, a computed key and an
    aliased one, no rows, `Head(0)`, no keys;
  - refused: a negative count for each, a dynamic group, an unknown key;
  - the eager form;
- `TestGroupHeadTailUnderAMemoryLimit`: 20,000 rows of 300 groups under 64 KiB,
  spilling, head and tail each equal to the run without a limit.

**Teeth, all biting:**

- a tail counted from the start;
- groups in the frame's order;
- keys not first;
- one row short;
- a computed key beside its column;
- a negative count let through;
- a dynamic group let through.

The row number among the sort's keys has no tooth. The sort is stable and the window
keeps the frame's order, so it only says what the order already is.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
