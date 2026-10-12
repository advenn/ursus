# Step 220 — as built

**O4 of `v0.6.3-scope.md`: a median or quantile over a huge group spills its values.**

## 1. Why

A quantile keeps every value of a group. A spilling group-by partitions keys, never
the values of one key. So a quantile over a few hot keys, or over no key at all,
held them all and refused past the budget: `TestSpilledQuantileStillRefuses` stated
it. Its answer needs only two order statistics a group, and values sorted by
(group, value) give those in one pass.

## 2. What changed

**The kernel** (`internal/kernel/quantspill.go`) touches no file:

- **`ValueSpill`:** `Put` values with their groups into the open run, `EndRun`, and
  `Ordered` gives every value back merged, by group and then by the quantile's total
  order.
- **`ValueSpiller`:** an accumulator whose values can go to one.
- **`quantileAcc.SpillValues`:**
  - it sorts each group's values in place, which costs no memory;
  - it puts the groups in turn, a chunk at a time, so each spill is one run already
    in order;
  - it keeps a count a group and lets the values go.
- **At Finish,** a quantile that spilled puts the rest and reads every value back.
  At each group's ranks it takes the two values and interpolates them. `ranks` and
  `interpolate` are `quantileOf`'s own, factored out, so a spilled quantile and a
  held one read the same ranks.
- **A quantile that spilled refuses a merge,** and a spill to a second place. Only
  a serial sink spills: parallel workers go serial first.

**The physical side** (`internal/physical/valuespill.go`):

- **`valueSpill`** writes runs into the group-by's spill directory, and reads them
  back through the external sort's merge. That merge orders a float by
  `OrderKeyF64`: NaN last, −0 and +0 alike, which is `compareTotalF64`'s order.
- **Runs are merged as they gather,** sixteen of a level into one of the next.
  - A run is about a budget of values when the keyed sink spills it: the freeze
    allows a limit's growth.
  - But a keyless aggregate spills whenever the query is over, which another
    operator can keep it, so a run can be one batch.
  - The merge at the end holds a batch of every run. So it reads at most fifteen a
    level, and each value is rewritten once a level.
- **Runs are removed** once merged or read back, and a sink closed early removes
  them too.

**In `hashAggSink`:**

- **`overBudget`,** once the sink is over, frozen or not, first spills the values
  of every accumulator that keeps them. It charges the sink again (`recharge`, now
  shared with Consume and Merge), and refuses only if the sink is still over.
- **What still refuses** is n_unique and implode: what they hold for each value is
  their answer, or decides it. The refusal's hints say so, here and in `execopt`.
- **`residentResult` takes the context.** The spill reads under Finish's context,
  not the one Consume ran under. `foldPartitioned` takes it too.

## 3. Tests and teeth

- **`TestAQuantilesSpilledValuesGiveItsAnswer`** (kernel):
  - every interpolation, at six quantiles;
  - spills at random points between sixty batches;
  - NaN, ±0, ±Inf and repeats among the values, a tenth null, one group with half
    the rows, five groups with none;
  - against a quantile that held its values;
  - every run is in order, and a spill lets every value go.
- **`TestASpilledQuantileIsNotMerged`** (kernel).
- **`TestAValueSpillMergesItsRunsAsTheyGather`** (physical), over 261 runs:
  - each level holds fewer than sixteen runs, and the files match the runs;
  - every value comes back in order;
  - no file is left.
- **`TestAClosedValueSpillLeavesNoFile`** (physical), with a run still open.
- **`TestAHotGroupsQuantileSpillsItsValues`** replaces
  `TestSpilledQuantileStillRefuses`:
  - a quantile, a median, two quantiles of floats with NaN, ±Inf and −0, a temporal
    median, and a sum;
  - over five hot keys, maintaining order or not, and over no key;
  - on one thread and four, under 8 KiB;
  - each answers as it does in memory, and spills;
  - n_unique over the same keys still refuses, naming itself.
- **`TestBudgetErrorNamesTheOperator`:** its holistic case is n_unique now.
- **`TestGroupBySpillFilesAreCleanedUp`:** its error case refused by accident of a
  quantile, and checked nothing when it did not. Both error cases now assert the
  error and that something was written. The new one refuses after a quantile beside
  n_unique has spilled its values.

**Why the 64 KiB floor stays** in `TestSpillingHashAggMatchesInMemory`. At 16 KiB
it still refuses, but not because of the quantiles, which fall to their per-group
size when they spill. `last` keeps its gathered rows a part at a time until enough
are dead. With the quantiles' new count a group, made at their first spill, that
alone passes the limit. Measured by printing each accumulator's size at the
refusal.

| tooth | result |
| --- | --- |
| a group's values put unsorted | **bites:** the kernel test's runs, and the root test's answers |
| a group's count overwritten by each spill | **bites:** both |
| values kept past a spill | **bites:** both |
| a run never ended | **bites:** both, and the cleanup test |
| the rest not put at Finish | **bites:** both |
| the last group's answer dropped | **bites:** both |
| the upper rank read as the lower | **bites:** both |
| never spills values | **bites:** the root test and the cleanup test |
| not charged again after a spill | **bites:** the root test |
| runs never merged | **bites:** the levels test |
| a merged level not put up a level | **bites:** the levels test |
| merged runs left on disk | **bites:** both physical tests |
| runs left after reading back | **bites:** the levels test |
| a closed spill forgets its open run | **bites:** the close test |
| values merged in descending order | **bites:** the levels test and the root test |
| the sink's Close forgets its value spills | silent, and equivalent: the runs are in the spill directory, which the sink that made it removes. It closes a writer a failed write left open |
| Finish reads back under the Consume context | silent, and equivalent today: the serial breaker and the parallel drain each consume and finish under one context. It guards a sink finished under another |
| a sink with spilled values merged | silent, and equivalent: no parallel worker spills, and the kernel's merge refuses a spilled quantile too (`TestASpilledQuantileIsNotMerged`) |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
