# Step 236 — as built

**W2 of `v0.6.4-scope.md`: reversed cumulative functions and a backward fill with no
limit stream over one huge partition,** in the file's order or one of their own,
with no partition_by or over one hot key, where they refused.

## 1. Why

A reversed cum_sum's answer for a row depends on every row after it, and so does an
unlimited backward fill's. Walked backward, each is its forward twin: cum_sum, and a
forward fill, each carrying its last answer. Step 235's sorted pass already puts the
rows in an order; walking it backward is a reverse sink away.

## 2. What changed

**`internal/physical/winsorted.go`:**

- **`sortedPlan`** gives each spec the spec its pass walks, its need, and its
  direction:
  - a reversed `cum_*` walks backward with `Reverse` cleared;
  - a backward fill with no limit walks backward as a forward fill;
  - either with an order of its own or none;
  - a forward spec needs an order of its own (step 235).
- **`sortedPass`:**
  - **the order's stream** is the projection sorted by its keys and the position, or
    with no keys the file itself, through `projOp`, which stamps the positions as it
    reads;
  - **the forward specs walk it** (`walkPass`);
  - **a reverse sink** holds what they give, and the backward specs walk its
    stream. It is the same rows, ties and nulls, exactly backward, so one sort
    serves both directions;
  - **every answer** is sorted back by position into the pass's answer file, each
    spec's column where the walks put it.
- `feedEach` drains an operator into a consumer.

**The refusal hints** (`extwindow.go`) and the comments now list only rank,
interpolation and rolling by time as refusing.

## 3. Tests and teeth

- **`TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit`** adds, walked backward:
  - **in the file's order:** a reversed cumulative sum, a reversed cumulative
    minimum of strings, and a backward fill with no limit over runs of nulls longer
    than two batches;
  - **in orders of their own:** a reversed cumulative product, a reversed cumulative
    maximum, and a backward fill.

  With no partition_by and over one hot key, under a tenth of the input, on one
  thread and four.
- **`TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse`** drops the
  reversed cumulative sum and the backward fill.

| tooth | result |
| --- | --- |
| a reversed function never walked backward | **bites:** it refuses |
| Reverse left set on the backward walk | **bites:** reversed twice |
| the backward fill left a backward fill | **bites** |
| a backward step walked forward | **bites** |
| the reverse sink skipped | **bites** |
| a limited backward fill walked backward | silent, and equivalent: a limited backward fill walked backward is a limited forward fill, the same answer |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. One test file's map keys
were left unaligned when the gate began; gofmt aligned them after, and vet and its
tests were run again.
