# Step 172 — as built

**F4 of `v0.6-scope.md`, its first half: `Interpolate`'s nearest method, and rolling
windows labelled at their middle row.**

## 1. Evidence first: what Polars does

Both were moved to 0.5 by the v0.4 scope and then dropped without a reason. Their
semantics were taken from Polars 1.44.1 itself, run in the bench's own Python
environment, offline: R4's oracle, in miniature.

- **`interpolate(method="nearest")`:**
  - `[1, ∅, ∅, 7, ∅, 10, ∅, ∅, ∅, 20, ∅]` gives `[1, 1, 7, 7, 10, 10, 10, 20, 20, 20, ∅]`;
  - a null takes the nearer value by position, and the later one on a tie;
  - a leading or trailing null stays null;
  - the type is kept: Int64 stays Int64, where linear interpolation answers a Float64.
- **`rolling_sum(w, center=True)`** over `[1, 2, 3, 4, 5, 6]`:
  - w=2: `[∅, 3, 5, 7, 9, 11]`;
  - w=3: `[∅, 6, 9, 12, 15, ∅]`;
  - w=4: `[∅, ∅, 10, 14, 18, ∅]`.

  A window of w covers w/2 rows before the row and (w−1)/2 after. A window running
  past either end is partial, null unless `min_samples` allows it.

## 2. What changed

**`InterpolateNearest()`**, a `WindowOption`. `Interpolate` now takes options
(`Interpolate(opts ...WindowOption)`), which every existing call still compiles
against.

- The kernel, `winInterpolateNearest`, builds a selection: each value is its own row,
  each null between two values the nearer one's, the later on a tie, and an edge
  null `NullIndex`. One `Take` then keeps the column's type.
- Typing keeps the input type, for a number or an instant.

**`RollingCenter()`**, a `WindowOption` for the six rolling functions.

- A centered window at row i is the trailing window ending (w−1)/2 rows later. So
  `rollingCentered` pads each partition with that many null rows after its last, runs
  the unchanged trailing kernels, and takes each row's answer from those rows on.
- A padded row is null, and counts as no value, which is Polars' partial window at
  the end.

**Both are rendered in the window's arguments** (`WinParams.args`), which is what
window resolution deduplicates on. Otherwise a centered and an uncentered window of
one size, or a linear and a nearest `Interpolate`, would share one answer.

## 3. Tests

Against Polars' answers, written into the tests:

- **`TestInterpolateNearestAnswersAsPolars`:**
  - three integer series, the type kept;
  - floats;
  - Dates, kept a Date, the later day on a tie.
- **`TestRollingCenterAnswersAsPolars`:** w = 2, 3, 4, with and without
  `MinSamples(1)`.
- **`TestRollingCenterIsPerPartition`:** two partitions, each centered in its own
  rows, beside an uncentered window of the same size.
- **`TestLinearAndNearestAreTwoAnswers`:** both methods in one `Select`.

## 4. Teeth

| tooth | result |
| --- | --- |
| a tie going to the earlier value | **bites:** `TestInterpolateNearestAnswersAsPolars` |
| a centered window one row off | **bites:** `TestRollingCenterAnswersAsPolars` |
| center not rendered, so shared | **bites:** `TestRollingCenterIsPerPartition` |
| nearest not rendered, so shared | **bites:** `TestLinearAndNearestAreTwoAnswers` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
