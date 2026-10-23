# Step 329 — as built

**`InterpolateBy`,** Polars' `interpolate_by` (`polars-parity.md` §2): a gap filled
on the line at each row's `by`, a time or a number, where `Interpolate` steps by rows.

## 1. The API

`Col("x").InterpolateBy(by)`: a window function, so it runs over the frame, a
partition at a time under `Over`, and in a sorted pass over one huge partition past
a memory limit.

**What Polars answers, and ursus to the bit:**

- **The line:** a null at x between (x0, y0) and (x1, y1) is
  y0 + (y1 − y0)/(x1 − x0)·(x − x0), the slope first. That order matched Polars on
  1968 random points, by integers, by floats and of integers. The same arithmetic in
  four other orders missed between a tenth and nearly half.
- **The distances** are taken in by's own type: an integer's or a Datetime's ticks
  subtracted as integers, then made a float, which a Datetime of 1.7e18 ticks needs
  (400 of 400; subtracted as floats, 0 of 400). A Date counts days.
- **A Float32** is computed in float32, the by distances made float32 (800 of 800).
- **In by's order:** by need not be sorted, and the answers go back to the rows. A
  NaN by sorts last, as ursus's order and Polars' put it. Equal by on both sides of
  a gap is a zero span, and NaN.
- **Runs at either end** stay null; a NaN value is a value.
- **A null in by** is refused, as Polars refuses it.
- **Types:** a Float32 answers a Float32, any other number a Float64.

**Where ursus differs:**

- **More value types:** a narrow integer or a Decimal is taken, as `Interpolate`
  takes them, where Polars refuses them.
- **Ties in by** are taken in the window's order, stably, where Polars' sort is its
  own.
- **In an aggregation** it is refused, as every window function is (`feature-gaps.md`
  §5, recorded at this step): Polars answers a list a group.

## 2. How

- **The kernel** (`internal/kernel/interpby.go`): `winInterpolateBy` takes each
  partition in by's order, as is when it already is, as a time series mostly is, else
  by the radix argsort of its by (`ArgSortColumns`), stable. It then fills each gap
  as `winInterpolate` does, at the by distances.
- **The expression** is `WinInterpolate` with `Params.By` over `PairOf(value, by)`, as
  `EwmMeanBy` is built; `interpolateByOut` types it.
- **The sorted pass** (`winsorted.go`) walks one huge partition in by's order
  (`byOrderOf`), in eight steps:
  - interpolation's five: the last value and its place, each row's place, the next
    value and its place;
  - three fills: of by where the value is, forward and back, and of by itself.

  `kernel.InterpolateByFrom` answers from them. A step reads a field of the pair: the
  pass puts the projected column in place of the spec's child wherever a step's child
  reads it (`onto`), where it replaced the child whole.

**The engine, faster for every window without an order** (found measuring this one):

- **`windowSink.segments`** sorted the frame by (partition, order) by radix sort when
  there was no order: a third of an interpolation's time over a million rows in one
  partition. With no order it is now a counting sort by partition id
  (`segmentsByID`), which is a stable sort by the id alone, with the same rows in the
  same order; one partition is the identity.
- **`seenBitmap`** builds a validity a word at a time, where it took a bit at a time.
- **`floatsAs`** wraps its caller's slice, which every caller makes for the one answer,
  where it copied it.
- **`toFloat64`** allocated a buffer before its switch, for a Float64 column too, whose
  own values it returns.

## 3. Tests

**`TestInterpolateBy`,** every answer Polars', compared to the bit:

- the line;
- by unsorted;
- runs at the ends;
- a NaN value, a zero span, a repeated by;
- forty random rows with an unsorted by;
- integers, a Float32, a Date by, a Datetime of big ticks;
- fourteen random Float32s, two of whose answers float64 arithmetic rounded to
  float32 misses;
- a NaN by in the middle and first;
- a null by, text and a Boolean by refused;
- `Over`, by sorted and out of order within each group.

**`TestInterpolateByStreamsPastItsLimit`:**

- 6000 rows, in batches of 64, under 16 KiB;
- an unsorted integer by, each value about three times, and a float by;
- a Float32 value of a sine's, gaps of 130 rows;
- frame-equal to the answer in memory, on one thread and on four;
- the spill directory empty after.

## 4. Performance

Against Polars 1.44.1, a million rows, a third of the values null, the median of 7:

| operation | first draft | as built | Polars |
| --- | --- | --- | --- |
| `InterpolateBy`, by sorted | 37.8 ms | 15.5–15.9 ms | 7.1–7.3 ms |
| `InterpolateBy`, by shuffled | 90.1 ms | 62.8–64.3 ms | 48.0–49.9 ms |
| `InterpolateBy` over 100 groups | 118.2 ms | 89–101 ms | 55.6–65.1 ms |
| `Interpolate`, for scale | 31.9 ms | 12.1–12.5 ms | 5.8–7.0 ms |

The ranges are two runs each, on a laptop whose load moved between them.

**What changed:**

- **The engine:** the four changes in §2, which took `Interpolate` from 5.5× Polars'
  time to under 2×;
- **the kernel:** no closure a row, a partition already in by's order taken as it
  is, and a whole-frame partition sorted from by itself, not a copy.

**Where Polars is still ahead,** and why, by profile:

- **with by sorted, by 2.2×:** a window over the frame keeps every batch and joins
  them, every column, before it computes, and the answer is allocated and zeroed;
- **with by shuffled, by 1.3×:** the radix sort, and the fills in by's order reading
  rows out of theirs;
- **over 100 groups, by 1.5×:** `Over`'s partition keys, hashed a row at a time,
  about a quarter of the time.

Each is the window machinery's, not this function's; `feature-gaps.md` §8 records it.

## 5. Docs

- **Missing data:** a paragraph under "Filling from the neighbours".
- **Coming from Polars:** a row.
- **The CHANGELOG, `polars-parity.md`, `feature-gaps.md`:** its §5 for a non-aggregate
  in `Agg` and §8 for the window's floor, and §8's `cache()`, which step 328 built.

## 6. Teeth, all 17 biting

The kernel:

- the product first;
- a by distance in floats;
- a Float32 in float64;
- a null by taken;
- the leading run filled;
- every partition taken as in by's order;
- NaN below every number;
- sorted in the rows' order;
- a partition's bys taken whole.

The sorted pass:

- a streamed row's own value interpolated;
- streamed from the next by;
- a streamed Float32 in float64;
- streamed in the rows' order;
- no streaming plan.

The engine:

- segments backwards, against the window tests;
- one partition's segments backwards;
- validity bits reversed within a word.

Four were silent at first:

- **the two Float32 teeth,** whose values rounded alike either way: fourteen random
  Float32s were added, and a sine's in the streaming test;
- **a partition's bys taken whole,** each group's by already sorted: a group out of
  order was added;
- **segments backwards,** which `InterpolateBy` sorts away: it bites the window tests.

**Gate:** test-all 130 ok, race 26 ok (the root package 526 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
