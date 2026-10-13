# Step 246 — as built

**Tier 2 of `v0.6.4-scope.md`: a rolling window by time over one huge partition,**
with no partition_by or over a hot key, where it refused. Every window function now
streams past the limit; a window by time holds the rows of its period.

## 1. Why

A rolling window by time (`RollingSumBy` and the rest, Polars' `rolling_*_by`)
takes, for each row, the rows of its partition whose by value lies within the period
of its own. The in-memory kernel sorts the partition by its by value and walks it
once. Over one partition past the limit, the window refused: its rows are bounded by
time, not by count.

## 2. What changed

**`kernel.RollingByNext` (`rollingbynext.go`).** It walks the next batch of a
partition already in its by order, continuing a `RollingByState`. It replays
rollingBy's walk row by row:

- **the window's ends** are moved as rollingBy's `win` moves them;
- **each kernel carries its state:** a float window's compensated sum and Welford
  state, refreshed from the window every so many removals as rollingFloat does; an
  integer sum's Int128; a quantile's sorted values; min and max's deque;
- **so every add, removal and refresh is the one rollingBy makes,** in its order,
  and the answers are the same to the bit;
- **the rows held** are those in reach of a window, from the walk's `from` on: their
  ticks, their values' validity, and their values as floats or Int128. Min and max
  keep only their deque's values and the rows not yet in a window, as a column of the
  value's type, so they work for strings too;
- **a window ends at its row's own by and takes the rows that tie with it,** so a
  batch must hold every run of equal by values whole. A run split between two
  batches is refused.

**A sorted pass for each by (`winsorted.go`).**

- **`passOrder`:** the by, nulls last, then the spec's order_by. rollingBy stably
  sorts the order_by's order by the by, so the order_by orders the ties, and with
  them how the state rounds. `rollingByOf` reads the by out of the pair child
  (`expr.PairArgs`, now exported).
- **`sortedPlan`:** one forward step with the new need, `rollingBy`.
- **`tieAligned`:** cuts the sorted stream between runs of equal `__k0`, the by. A
  run is held back to the next batch, and a batch that is all one run is joined to
  the next.

**The walk is charged (`winStreamOp.rollingBy`).** A walk by time has an account,
which `walkPass` now takes from the budget. Its state's bytes are charged after each
batch. A period whose rows alone are past the limit is refused, naming the period,
its rows and its bytes. The sorts around the walk spill.

**Hints.** extwindow.go's two hints say every window function streams, a window by
time holding its period's rows. `holdsHint("over")` says the same and that implode
holds its partition. The Check hint says a rolling window by time fails when one of
its periods holds more than the limit.

## 3. Tests and teeth

**`TestARollingWindowByTimeStreamedInPiecesIsRollingBy`** (kernel) compares
`RollingByNext` with rollingBy, to the bit, over a stream cut between runs at random
into pieces of one run to three hundred rows:

- every function: sum, mean, var, std, min, max, median and quantile;
- of floats with nulls, NaN, ±Inf and ±0 whose sums round, of integers (an Int128
  sum), and of strings for min and max;
- by a Datetime with runs of ties and nulls, over periods of about two hundred and
  twelve hundred rows, and a calendar month over day-apart rows;
- closed each way, wanting one value and five.

**`TestARollingWalkByTimeRefusesASplitRun`** checks that a run split between
batches is refused.

**`TestARollingWindowByTimeStreamsPastItsLimit`:**

- **the frame:** 20,000 rows by a Datetime in runs of three, with a run of 1,200 at
  one instant and nulls;
- **the windows:** a float mean and sum, closed both; an integer sum and std
  wanting three; a string max closed left; a median; a quantile closed on neither
  side; and a std in an order_by of its own;
- **the claim:** with no partition_by and over a hot key, under a tenth of the input
  on one thread and four, the answer is the in-memory one, to the bit, it spills,
  and the directory is empty after;
- **the refusal:** a period of three hours, every row, refuses, naming it.

**Tests that flipped:**

- `spill_byhand_test.go`: a rolling sum by time over no keys and over one partition
  larger than the limit now spill. The wall that stays is a period past the limit;
- `TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse` keeps only an hour's
  period under 16 KiB;
- `TestAWindowWithNoPartitionBesideAKeyedOneStreams` streams a rolling sum by time.

| tooth | result |
| --- | --- |
| a float window never refreshed | **bites,** kernel and end to end |
| min and max forget the rows not yet in a window | **bites** |
| rows shed one too many | **bites** |
| batches not cut between runs | **bites:** the split run is refused |
| ties not in the order_by's order | **bites:** the std in an order_by |
| a period past the limit not refused | **bites** |
| a split run walked | **bites** |

The order_by case was first a sum, and that tooth was silent: a compensated sum is
too accurate for the order of a tie's adds to show. Welford's variance does.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
