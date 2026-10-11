# Step 177 — as built

**F3 of `v0.6-scope.md`: rolling windows by time,** `Rolling*By(by, period)`, the
expression form of the frame-level `Rolling`.

## 1. Evidence first: what Polars does

Asked of Polars 1.44.1 in the bench's environment, offline. The frame is `t` at hours
0, 1, 2, 4, 5 and 9 of a day, with `v` = 1, 2, ∅, 4, 5, 6.

| question | Polars' answer |
| --- | --- |
| the functions | `rolling_{sum,mean,min,max,var,std,median,quantile,rank}_by` |
| `rolling_sum_by("t", "2h")` | 1, 3, 2, 4, 9, 6: the window is (t − 2h, t] |
| `min_samples` | 1 by default; 2 makes the one-value windows null |
| `closed` | `right` by default |
| `closed="left"` and `"none"`, an empty window | its sum is 0.0, although `min_samples` is 1 |
| `by` unsorted | windows by value: t = 2, 0, 1, 5h answers 4, 2, 5, 4 |
| ties | rows of one t share a window, the later ones included |
| a null `by` | that row is null, and in no other row's window |
| `.over(g)` | per partition |
| a Date `by`, `"2d"` and `"1mo"` | calendar windows |
| a zoned `by` across London's change, `"1d"` against `"24h"` | a calendar day against twenty-four hours: 1, 2 against 1, 3 |
| integers | the sum keeps Int64; the mean is Float64 |
| a string `by` | refused: an integer, Date, Datetime, Duration or Time |
| a period of 0 or less | refused |

## 2. What changed

**The plan** (`internal/expr/window.go`):

- `WinParams` gains `By`, `Period` and `Closed`.
- A rolling function by time keeps its op. Its child is `PairOf(value, by)`, the
  struct step 175 built for the aggregates of two inputs.
- **Typing,** `rollingByOut`, checks `by` and the period as the frame-level `Rolling`
  checks its index:
  - `by` is a Date, Datetime or Time;
  - the period is positive and parses;
  - the function is not centered;
  - a Time `by` takes no calendar period, and no period is finer than `by`'s tick;
  - `min_samples` is at least 1.

  The value is then typed as any rolling function's, by `rollingValueOut`, which
  `rollingOut` now shares.
- **Rendering:** `window()` puts the period and the closing in the arguments, which
  window deduplication keys on. `WinFn.String` reads as written:
  `col("v").rolling_sum_by(col("t"), 2h, closed right, min_samples 1)`.
- **Naming:** `OutputName` names it after its value, not the struct, whose first
  field is called "0".

**The kernel** (`internal/kernel/rolling.go`, `rollingby.go`):

- **The rolling kernels walk ranges.** Each of the three gives row i the rows
  [lo_i, hi_i) of its partition. A row enters as hi passes it and leaves as lo does,
  and both ends only rise.
  - A window of N rows is `lo_i = i−N+1`, `hi_i = i+1`. Its arithmetic is unchanged:
    the float state's refresh, after as many removals as the window holds, is the
    old one's after N.
  - `rollingExtremum` now marks every row null at the start, since a row whose `by`
    is null is reached by no walk.
- **`rollingBy`:**
  - keeps each partition's rows that have a `by` and sorts them stably by it;
  - gives each row its window start, `windowStarts`: its value less the period,
    subtracted as ticks, or for a calendar period moved on the wall clock of `by`'s
    zone through `Interval.AddTo`, as the frame-level `Rolling` moves it;
  - finds both ends of every window by two pointers, which only rise as the sorted
    values do.

**The API** (`window.go`):

- `RollingSumBy`, `RollingMeanBy`, `RollingMinBy`, `RollingMaxBy`, `RollingVarBy` and
  `RollingStdBy(by Expr, period Interval, opts ...WindowOption)`;
- `WindowClosed(c Closed)`;
- `MinSamples` and `Ddof` apply as before. `min_samples` defaults to 1, as Polars'.

**Where Polars differs, each in the API's doc:**

- an integer `by` with a period such as "2i" is not built;
- neither are `rolling_median_by`, `rolling_quantile_by` and `rolling_rank_by`. The
  rows-based rolling functions have no median either;
- an empty window is null here, where Polars answers 0.0 under `closed="left"` or
  `"none"`;
- min and max take any ordered value, as the rows-based ones do. Polars refuses a
  string's.

## 3. Measured

One targeted run over 1M rows of instants a random 0–2 minutes apart, best of three:

| expression | time |
| --- | --: |
| `RollingSum(60)` | 57 ms |
| `RollingSumBy(t, 1h)` | 81 ms |
| `RollingMaxBy(t, 1h)` | 85 ms |
| `RollingSumBy(t, 1d)`, by the calendar | 131 ms |

## 4. Tests

- **`TestRollingByAnswersAsPolars`:**
  - the six functions, `MinSamples(2)` and the four closings;
  - integers;
  - `by` unsorted, ties, and a null `by`;
  - `.Over(g)`;
  - Dates by "2d" and "1mo";
  - London's change of offset by "1d" and by 24 hours.
- **`TestRollingByRefuses`:**
  - a string or integer `by`;
  - a zero, negative or unparseable period;
  - a centered window;
  - an hour of Dates and a month of Times;
  - a sum of strings;
  - `MinSamples(0)`.
- **`TestRollingByNamesAndRenders`:**
  - named after the value;
  - two periods and two closings in one `Select` are three answers;
  - the plan's rendering.
- **`TestRollingByAgreesWithABruteForce`:**
  - 400 rows in three partitions, instants unsorted with ties and nulls, values with
    nulls;
  - every closing at periods of 1, 17 and 60 minutes, in batches of 64;
  - sum, min, max and var against every window computed by scanning its partition.
- **The rows-based rolling tests** pass unchanged: `TestRollingByHand`, the oracle,
  and the centered windows.

## 5. Teeth

| tooth | result |
| --- | --- |
| a window stopping at its own row, not its ties | **bites:** ties, and the brute force |
| the lower end's openness inverted | **bites:** every closing, and the brute force |
| `by` left unsorted | **bites:** `by` unsorted, and the brute force |
| a null `by` kept in the windows | **bites:** the null `by`, and the brute force |
| a calendar period taken as its nanoseconds | **bites:** "1mo" and "1d" across the change |
| min and max not null where no walk reaches | **bites:** the brute force, and the rows-based min and max |
| a fresh float state taking the leaving value back | **bites:** the closings, the brute force, and the rows-based oracle |
| by time rendered without its period | **bites:** two periods in one `Select` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
