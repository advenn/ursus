# Step 149 — as built

**The second half of item 6 of `v0.5-scope.md`: the fixed-size rolling functions.**
`RollingSum`, `RollingMean`, `RollingMin`, `RollingMax`, `RollingVar` and
`RollingStd`, with `MinSamples` and `Ddof`.

## 1. Evidence first

**v0.4-scope.md had deferred them.** `Expr.Rolling*` needed a frame concept in the
window parameters first.

**Reading the window path showed most of it was already there.** An ordered window
function gets `Segments`: one argsort of the partition ids and the window's order
keys, so each partition is a contiguous run of rows in its order. A fixed-size
rolling window is a run of N rows in that run. So the rolling functions are new
`WinFnOp`s with new kernels, and nothing else in the window machinery changes.

**The scope's one warning applied again.** Every new `WinParams` field must be
rendered in `args()`, or two windows that differ only in it share one temporary in
the dedup maps.

## 2. What changed

### The ops (`internal/expr/window.go`)

**Six `WinFnOp`s,** classified by `IsRolling`:

- `rolling_sum`, `rolling_mean`, `rolling_min`, `rolling_max`, `rolling_var` and
  `rolling_std`.

**`WinParams` gains two fields,** and every rolling op renders all it depends on:

- `MinSamples`, where zero means N, as Polars' `min_samples=None` does;
- `Ddof`, for var and std.

Example renderings: `rolling_sum(3, min_samples 0)` and
`rolling_var(3, min_samples 0, ddof 1)`.

**`rollingOut` types each as the aggregate of the same name,** so a full window's
answer is that aggregate's over its rows:

- **Sum:** an integer or Bool is an Int128, which no window of 64-bit values can
  wrap; a float keeps its width.
- **Mean:** a float, Float32 for a Float32.
- **Var and std:** Float64.
- **Min and max:** the column's own type, of any type with an order: numbers,
  temporals, strings, Bools.
- **Refused:** a sum of a Decimal, a Duration or an Int128, which could leave its
  type over a long window, and a mean of a Duration. Cast to Float64 first.
- **The window itself is checked:** at least one row, `MinSamples` between 1 and
  N, `Ddof` between 0 and 255.

**A rolling result is declared nullable:** its first rows hold too few values.

### The kernels (`internal/kernel/rolling.go`)

Each is O(1) per row: a value enters as its row is reached and leaves N rows later.

- **Min and max:** a monotonic deque of the window's positions. A row that can never
  be the answer again leaves as a better one arrives. The answer is a `Take` of the
  deque's front, so every ordered type works, by `valueComparator`'s total order:
  NaN above every number, as `Max` of a column holding one is NaN.
- **An integer sum:** an `i128.Int128`, added to and subtracted from exactly.
- **A float sum, mean, var and std** share one state:
  - the window's finite values' Neumaier-compensated sum and Welford state, with a
    removal update that is Welford's run backwards;
  - counts of its NaNs and infinities of each sign, kept apart, so an Inf that has
    left the window leaves no NaN behind (`[Inf, 1, 2]` sums to 3 over the last two);
  - **recomputed from the window every N removals,** so the error is that of at most
    2N steps, however long the partition.
- **Null where fewer than `MinSamples` values are in the window.** With the default,
  any null in a full window nulls the row, as in Polars.
- **A variance of no more values than `ddof` is null,** as `Var`'s is, counting NaNs
  and infinities among the values. Past that, a NaN or an infinity makes it NaN.

### The public API (`window.go`)

- **The methods:** `RollingSum(size, opts...)` and its five siblings.
- **The options:** `MinSamples(k)` and `Ddof(d)`, whose default for var and std
  is 1.
- **The window's rows:** `.Over(g)` partitions them, `.OverWith` orders them, and
  without either they are the frame's, in scan order.
- **`center=True` and windows by time are not here.** A window by a duration, such
  as `"2d"` over a time column, is Tier 2.

## 3. Tests

**`rolling_test.go`:**

- **`TestRollingByHand`:**
  - all six over 1 to 5;
  - nulls with and without `MinSamples(1)`;
  - a variance of one value;
  - NaN and an infinity entering and leaving;
  - `MaxInt64 + MaxInt64` exact;
  - Float32 kept;
  - text and dates;
  - six windows in one `Select` differing only in size, `MinSamples` or `Ddof`, each
    its own answer;
  - six refusals.
- **`TestRollingAgainstTheOracle`:** every function, at sizes 1, 2, 3 and 7 and three
  `MinSamples` each, over 600 rows in five interleaved partitions ordered by a
  shuffled key. One value in eight is null and one in twenty a NaN or an infinity.
  The oracle recomputes each window from its rows, and 43,200 answers are compared
  at full precision, to 1e-9.
- **`TestRollingDoesNotDrift`:** a mean and a std over 64 rows, across one partition
  of 50,000 values near 1e9, checked at rows 63, 1,000, 25,000 and the last.
- **`TestCumulativesAreExactOnAOneRowPartition`** now covers rolling sum, min and max
  over a window of one row, which must be the row: an Int64 of 2^53+1 among others.

**A first draft of the oracle test compared four significant digits,** through the
`numbers` helper, with a tolerance of 1e-3 to match. That would have hidden a real
error, so it now reads the columns at full precision.

## 4. Teeth

| tooth | result |
| --- | --- |
| a row stays in the deque one row too long | **bites:** by hand, the oracle |
| min and max never count a value out | **bites:** the oracle |
| an integer sum never subtracts | **bites:** by hand |
| never computed afresh | **bites:** the drift test |
| a NaN or an infinity summed with the rest | **bites:** by hand, the oracle |
| Welford's removal keeps the old mean | **bites:** by hand, the oracle, the drift test |
| a NaN answers before the count | **bites:** by hand, the oracle |
| `min_samples` not rendered | **bites:** the parameters case |
| `ddof` not rendered | **bites:** the parameters case |
| `min_samples` defaults to one | **bites:** by hand, the oracle |
| a rolling window declared never null | **bites:** by hand |
| a Float32 answered as a Float64 | **bites:** the Float32 case |

**The oracle found one defect while it was being written.** A variance checked for a
NaN before its count, so a one-row window holding a NaN answered NaN where `Var`
answers null. The order is now the aggregate's.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean.
