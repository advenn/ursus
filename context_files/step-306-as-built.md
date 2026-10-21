# Step 306 — as built

**P4: `Upsample`** (`v0.6.7-scope.md` §3), as Polars' `DataFrame.upsample`, and a lazy
form, which Polars does not have.

## 1. What Polars 1.44.1 answers, probed

`upsample(time_column, *, every, group_by=None, maintain_order=False)`:

- **a grid** from each group's first instant to its last, every step: a row of the
  frame at a step is kept, and nulls fill a step it has none at;
- **a left join of the grid with the frame:** a row between two steps, at 12:00 of a
  daily grid or Jan 4 of one every 2d from Jan 1, is left out; two rows at one
  instant are both kept;
- **months step from the start,** clamped: Jan 31, Feb 29, Mar 31, Apr 30;
- **a zone's day is its wall clock's;** hours over the change to summer time skip
  02:00;
- **groups:** each its own grid, the keys filled on the new rows, a null key a group;
  with `maintain_order=True`, the order each first appears;
- **refused:** a frame not sorted by its time, "not sorted"; a null instant, which
  reads as unsorted; no rows, "cannot determine upsample boundaries"; a step that is
  not positive;
- **an integer index** by "1i";
- **a Date by 12h** steps each date twice, the second unmatched.

## 2. The API

```go
func (lf *LazyFrame) Upsample[T Span](time string, every T, groupBy ...string) *LazyFrame
func (df *DataFrame) Upsample[T Span](ctx context.Context, time string, every T, groupBy ...string) (*DataFrame, error)
```

`every` is an `Every` interval or a `time.Duration`, as `DatetimeRange` takes. There is
no `maintain_order`: the groups always keep the order each first appears.

## 3. As a join

The scope's §4 has it: an upsample is written in frame operators, and streams and
spills as they do.

1. **The grid:** `GroupBy(groupBy...).MaintainOrder().Agg(ranges)`, the ranges
   `DateRanges` or `DatetimeRanges` of the time's `Min` and `Max`. It is exploded, a
   null instant dropped, and numbered by `WithRowIndex`. A group of no instant has a
   null range, which explodes to a null and is dropped. So does a frame of no rows,
   whose keyless aggregate is one row of nulls.
2. **The join:** the grid left-joined with the frame, numbered too, on the keys and
   the time, `JoinNullsEqual(true)`, so that a null key's rows meet its grid.
3. **The order:** a sort by the grid's number and the frame's, then the frame's
   columns in its order.

**The sort is not for memory's sake.** A left join keeps the left frame's order in
memory, but its doc says no join's order is promised under a memory limit. An
upsample is mostly followed by a fill forward, which would then fill from the wrong
rows, silently.

- With the sort, 40 stations' 500 days under 64 KiB, spilling, filled forward,
  answer as without a limit.
- Without it, the same test fails, the tooth below.
- It costs 95 ms of the 355 that a million rows take.

**A Date stepped by less than a day** is refused before the range would refuse it
under its own name, "date_range". The hint says to cast to a Datetime.

## 4. Against Polars

**Every case of §1 answers as Polars,** each held in `TestUpsample`. The guide's
example, a weekend upsampled and filled forward, is Polars' answer exactly.

**§9, where ursus answers and Polars refuses:**

- **an unsorted frame:** the grid is made from its min and max, and the sort puts the
  rows in its order;
- **a null instant:** its row has no step, and is left out;
- **no rows:** none come out.

**§9, where ursus refuses:**

- **an integer index:** `Interval` has no unit for one, and G4's integer index for
  temporal groups is where one belongs;
- **a Date by less than a day.**

**§8: it is 15 times Polars' time,** 355 ms where Polars takes 24 ms at a million
rows, walking a frame it requires sorted. A performance release's.

## 5. Found on the way

§1 had the heap-bound flake twice: step 304 added a row for what §8's "A heap bound
that trips" had held since step 292. The readings are merged into §8's row, and the
§1 row is gone.

## 6. The site

`docs/guide/dates-and-times.md` on `dev` has "Upsampling", new in 0.6.7, with
`Example_timeUpsample`: ACME's prices over the weekend, filled forward. The Polars
page maps `upsample`.

## 7. Tests and teeth

**The tests:**

- `TestUpsample`: sixteen answers, thirteen Polars', three where Polars refuses; seven
  refusals; the eager form;
- `TestUpsampleUnderAMemoryLimit`: spilling, filled forward, equal to the run without
  a limit.

**Teeth, all biting:**

- no sort back into the grid's order, which only the memory-limit test catches;
- null keys unequal;
- groups in no order;
- two rows at one instant reversed;
- null instants kept;
- a Date by 12h let through;
- a negative step let through.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
