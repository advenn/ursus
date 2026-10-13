# Step 237 — as built

**W3 of `v0.6.4-scope.md`: rank, every method, streams over one huge partition,**
with no partition_by or over one hot key, where it refused.

## 1. Why

Rank needs the rows in its child's order, which step 235's sorted pass gives. Its
ordinal, dense and min ranks need only a count, a group count and the row before.
Max and average need a peer group's last position, which is not known until the
group ends, and one group can be the whole partition, all nulls say. Walked
backward, the group's last row is its first, so min over the order reversed, counted
from the end, is the group's max.

## 2. What changed

**`internal/kernel/ranknext.go`:**

- `RankState` and `RankNext` rank the next rows of a partition in rank's order, on
  `EwmNext`'s template;
- ordinal is the position, dense the groups begun, and min the group's first
  position;
- **peers are winRank's:** two nulls tie with each other and with nothing else, and
  values tie when their order does. They are tested against the last row of the
  piece before.

**`internal/physical/winsorted.go`:**

- **`passOrder`:** rank's pass walks its own order, its child in its direction with
  nulls first, as `segments` sorts it, whatever the window's order_by. A rank and
  a function ordered by the same keys share a pass.
- **`sortedPlan` returns a `passPlan`:** a spec's steps, and a `combine` that makes
  its answer from their columns.
  - ordinal, dense and min: one forward step;
  - max: one backward step of min, combined as `rows + 1 − minRev` (`rankMax`);
  - average: min forward and min backward, combined as `float64(first + last) / 2`,
    the sum in Uint32 then halved, as winRank computes it (`rankAverage`).
- **`sortedPass`** names each step's column, walks the forward and backward steps,
  and combines before sorting back by position.

**`winStreamOp`** walks a rank step with its `RankState`.

## 3. Tests and teeth

- **`TestARankStreamedInPiecesIsWinRank`** (kernel):
  - 1,500 floats with nulls, NaN, ±0 and long ties, and strings;
  - ascending and descending, cut into pieces of 1 to 50 rows;
  - every method against winRank over the whole column.
- **`TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit`** adds six ranks: every
  method, both directions, over the float, the integer with long runs of nulls and
  strings with many ties; one with the window's order_by, which rank ignores.
- **`TestSpillingByHand`:**
  - its two rank refusals now answer as spill cases, three methods over no keys and
    dense over a hot key;
  - a rolling sum by time takes their place as the wall that stays.
- **`TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse`** drops rank, and
  **`TestBudgetErrorNamesTheOperator`**'s `over` case is a rolling sum by time.

| tooth | result |
| --- | --- |
| max off by one | **bites** |
| average from min and min | **bites** |
| a tie across pieces forgotten | **bites:** all three |
| nulls tie with values | **bites:** all three |
| dense not carried | **bites**, once its first form, which added zero, was made to reset the count |
| rank's order ignores its direction | **bites** |
| max walked forward | **bites** |
| a rank sharing an order_by's pass | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
