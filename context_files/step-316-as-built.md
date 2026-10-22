# Step 316 — as built

**J2, `Update`:** Polars' `DataFrame.update` (`v0.6.7-scope.md` §5). It finishes the
joins.

## 1. What Polars 1.44.1 does, probed

Frame `A = [1, 2, 3, 4]`, `B = [400, 500, 600, 700]`:

- **Without `on`**, rows are matched by place. `other`'s columns the frame lacks are
  ignored, and a null in `other` leaves the value: `B = [-66, 500, -99, 700]`. With
  `include_nulls`, the null is written: `[-66, null, -99, 700]`.
- **`on="A"`, left, the default:** only matched rows change, in the frame's order.
- **inner:** only the matched rows are kept.
- **full:** `other`'s unmatched rows follow as new rows. With `include_nulls`, a null
  overwrites.
- **`maintain_order="right"`:** `other`'s order, then the frame's unmatched rows.
- **A key `other` holds twice** gives the row twice, as a join does. A key the frame
  holds twice is updated twice.
- **Types:** an Int64 updated with a Float64 is a Float64, and with text, text:
  Polars' supertype.
- **Keys:**
  - `left_on`/`right_on` work;
  - an Int32 key keeps its type on a left update and is widened on a full one;
  - a key all null, the Null type, is refused.

## 2. How

```go
type UpdateHow uint8 // UpdateLeft (the zero value), UpdateInner, UpdateFull
type UpdateOptions struct {
	On, LeftOn, RightOn []string
	How                 UpdateHow
	IncludeNulls        bool
	MaintainOrder       JoinOrder // JoinOrderNone is JoinOrderLeft here, Polars' default
}
func (lf *LazyFrame) Update(other *LazyFrame, o UpdateOptions) *LazyFrame
func (df *DataFrame) Update(ctx context.Context, other *DataFrame, o UpdateOptions) (*DataFrame, error)
```

`UpdateHow` is its own type because `JoinKind`'s zero value is inner, where Polars'
update defaults to left.

**An ordered join and a column each:**

1. Without keys, both frames are numbered by `WithRowIndex` and joined on the number.
2. `other` is narrowed to its keys and the columns the frame has. Each of those is
   renamed apart, and a `matched` marker, a literal true, is added.
3. A join of the kind, its keys coalesced, in the asked order, the left by default.
4. Each updated column is `Coalesce(theirs, mine)`, or with `IncludeNulls`
   `When(matched is not null).Then(theirs).Otherwise(mine)`. Both sides are cast to
   their `dtype.Supertype`, only where it differs.
5. A key keeps the frame's type on a left or inner update, cast back from the join's
   widening. Its values there are the frame's own.
6. The frame's columns, in their order.

**Found while testing, and fixed:** an Int32 key against an Int64 one came back
Int64 on a left update. Polars keeps Int32 there.

## 3. Speed and memory

- **Speed:** a million rows updated from 100,000, in memory: 16 ms, Polars' 12 ms.
- **Memory:** it is an ordered join, so it spills and keeps its order (step 314).
  `TestUpdateUnderAMemoryLimit`: 20,000 rows from 5,000, under 64 KiB, spilling,
  equal to the answer without a limit.

## 4. Tests

**`TestUpdateAsPolars`,** each against Polars' answer:

- by place, with and without nulls;
- on a key: left, inner, full, full with nulls, the right order;
- a key twice in either frame;
- a float, and text, over an integer;
- keys named apart;
- no column in common;
- an Int32 key, left and full;
- `other` shorter.

**Refused:** an unknown key, `On` beside `LeftOn`, keys unpaired, a kind out of range,
and two types no type holds.

**The eager form** answers as the lazy one.

## 5. Docs

- **The joins guide:** a section, *Updating from another frame*, with an example equal
  to Polars'.
- **Coming from Polars:** a row.
- **The CHANGELOG:** the feature.
- **`feature-gaps.md` §6:** the row updated.

## 6. Teeth, all 10 biting

- nulls overwriting;
- `IncludeNulls` ignored;
- the matched marker unread;
- left run as inner;
- full run as left;
- the default order none;
- no supertype;
- the key left widened;
- the places misnumbered;
- a key updated.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB. The root package took 606 s under the race
detector, against 459 s at step 273; the new tests of steps 312 to 316 are 17 s of it.
CI's race job, timed out at 20 minutes a package, last took 11 m 43 s in all.
