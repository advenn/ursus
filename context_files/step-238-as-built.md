# Step 238 — as built

**W4 of `v0.6.4-scope.md`: interpolation, linear and nearest, streams over one huge
partition,** in the file's order or one of its own, with no partition_by or over
one hot key, where it refused.

## 1. Why

A null row's interpolated value needs the value before it and the one after it,
however far, and their positions. A walk each way finds them: forward, the last
value and where it was; backward, the next. Each walk carries only the last value
and position it saw.

## 2. What changed

**`internal/physical/winsorted.go`.** `sortedPlan` gives interpolation five steps:

- **forward:**
  - a forward fill, which carries the last value;
  - a "last held" step, the position of the last row holding a value;
  - cum_count, each row's own position;
- **backward:** the fill and the "last held" step again, which, walked backward, are
  the next value and its position counted from the end.

Its combine is `kernel.InterpolateFrom`. A step's `childTyp` gives a fill the
child's type, which the pass knows.

**`winStreamOp`'s `lastHeld`** is the new step: the position, among the rows walked,
of the last at or before each row that holds one, null before the first. It is
carried across batches; `walkPass` starts it at −1.

**`internal/kernel/interpnext.go`, `InterpolateFrom`.** From the five columns:

- a row holding a value is its value;
- a row with nothing on one side stays null;
- **linear** takes the line between the two by position, with `winInterpolate`'s
  arithmetic, `a + (b − a)·t`, and its float type;
- **nearest** takes the nearer, the later on a tie, as `winInterpolateNearest`, of
  the column's own type, by one Take.

## 3. Tests and teeth

- **`TestInterpolationFromBothWalksIsTheKernels`** (kernel):
  - integers and Float32s, 2,000 rows, runs of nulls of every length and nulls at
    both ends;
  - the walks' columns written out by plain loops;
  - linear and nearest against the kernels, bit for bit and type for type.
- **`TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit`** adds four interpolations:
  - linear and nearest of the integer with long runs of nulls, in the file's order;
  - linear of a Float32 and nearest of the integer, in orders of their own.
- **`TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse`** keeps only the
  rolling window by time.

| tooth | result |
| --- | --- |
| nearest's tie to the earlier | **bites** |
| the next position not counted from the end | **bites** |
| own position 1-based | **bites** |
| a trailing null answered | **bites** |
| the held position not carried | **bites** |
| the next value walked forward | **bites** |
| a value held walked as own | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
