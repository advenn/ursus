# Step 184 — as built

**R4 of `v0.6-scope.md`: 0.5's expressions against an outside oracle, Polars.**

## 1. The suite

- **`bench/scripts/oracle_expressions.py`** runs in the bench's own environment,
  offline: `cd bench && uv run --offline python scripts/oracle_expressions.py`.
  - Each case is a small frame and one Polars expression.
  - It writes the frame and Polars' answer to **`testdata/oracle/expressions.json`**.
  - Instants are written as integer ticks, and an integer's kind is its Polars type,
    so a bit count's width is kept.
- **`oracle_test.go`** reads the file and asks ursus each case by name, through
  `oracleExprs`, ursus's spelling of each.
  - Floats compare to 1e-9 relative, NaN equal to NaN, and everything else exactly.
  - A case spelled in ursus but asked of Polars nowhere fails, and so does the
    reverse.
- **What it covers:**
  - 46 expressions: rolling by rows (sum, mean, min, max, var, std, `min_samples`,
    ddof, center) and by time, the ewm functions (alpha, span, com, half-life,
    adjust, `ignore_nulls`, `min_samples`, bias), `Cut` and `QCut` (labels,
    `left_closed`, quantiles, n), `ConcatStr`, the six bit counts at Int64 and Int8,
    and the calendar (`offset_by`, `truncate`, `round`, `month_start`, `month_end`,
    `is_leap_year`, both time-zone functions, `strftime`);
  - `Describe`, statistic by statistic, and `ValueCounts`, as pairs, since Polars
    leaves the order of ties to its threads.

## 2. What it found

Of 46 expressions and two frames, 37 expressions and both frames answered as Polars
did from the start. The rest came to three differences.

**Weeks started on Thursday: a silent wrong answer, fixed.**

- `Every("1w")` was read as seven days, and a grid of days counts from the epoch,
  a Thursday. So `Truncate("1w")` of 1970-01-01 answered 1970-01-01 where Polars
  answers 1969-12-29.
- **The same was true of `GroupByDynamic` every "1w":** weekly buckets ran Thursday
  to Wednesday.
- Polars aligns a week to Monday: "1w" to 1969-12-29, "2w" to 1969-12-22, a Date and
  a zoned instant alike, and in `round` and `group_by_dynamic`. "7d" and "14d" stay
  on the epoch's grid.
- **The fix:**
  - `dtype.Interval` gains `weekly`, set by `Every` when weeks are its only unit;
  - `floorWall` counts a weekly grid from Monday 1970-01-05;
  - `Truncate` and `Round` pass it into the kernel as a third value of their `wall`
    argument, and `AsWeekly` rebuilds it there;
  - `GroupByDynamic` holds the `Interval` itself, so it keeps the marker;
  - `String` writes weeks only for a weekly interval, so "7d" and "1w", now two
    grids, render apart for deduplication.
- "1w1d" is eight days from the epoch; Polars refuses mixing those units.

**ewm answered at a null row: fixed, to Polars'.** Polars' `ewm_mean`, `ewm_std` and
`ewm_var` are null where the input is null. ursus answered the running value, as
pandas does, which step 153 chose without an oracle. The weights age as before, and
every other row agreed. A null row now answers null, and `TestEwmByHand`'s two null
cases and the closed form are updated to it.

**A window holding only nulls: kept, and listed.** `rolling_sum_by` over a window
whose rows are all null answers 0.0 in Polars, though its `min_samples` of 1 asks for
a value. It is the same rule as its empty window under `closed="left"`, which step
177 already recorded. ursus answers null, and `RollingSumBy`'s doc now names both.
`oracleDifferences` holds that one row, with ursus's answer, so the test checks it
rather than skipping it.

## 3. Tests and teeth

- **`TestExpressionsAnswerAsPolarsOracle`** and
  **`TestDescribeAndValueCountsAnswerAsPolarsOracle`:** §1.
- **`TestWeeksStartOnMonday`:**
  - `Truncate` by "1w", "7d" and "2w";
  - `Round` by "1w";
  - a Date, and London in March;
  - `GroupByDynamic` every "1w" and "7d": Mondays and Thursdays, as Polars'.
- **`TestWeeksAndDaysRenderApart`:** "1w", "2w", "7d", "14d", "1w1d" as "8d", and
  "-1w".

| tooth | result |
| --- | --- |
| weeks from the epoch again | **bites:** the oracle's `truncate_1w` and six week cases |
| seven days rendered as a week again | **bites:** the rendering test |
| the kernel never told it was weeks | **bites:** the oracle and the `Truncate`/`Round` cases |
| a null ewm row answering the running value | **bites:** the oracle's ewm cases, by hand, and the closed form |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
