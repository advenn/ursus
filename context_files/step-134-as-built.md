# Step 134 — as built

**Every accumulator's per-group arrays grow by doubling, and `NBytes` counts their
capacity.** Step 132's profile put the sum and count accumulators of h2o `gb10` at
12% of everything it allocated. No benchmark was run, by request.

## 1. Evidence first

**`TestAccumulatorsGrowByDoubling`** (`internal/kernel`) covers seventeen
accumulators, one per kind:

- sum, mean, min and max over each storage they use;
- count, len, n_unique;
- first, var, product, arg_max, median, implode, top_k.

Each is grown to a million groups, ten thousand at a time, as a group-by's batches
grow it. The test reads the bytes allocated and, after a collection, the live heap
the accumulator holds, against its `NBytes`.

**On the code before this step:**

- **every one allocated about five times what it held,** for example sum(Float64)
  44.8 MB to hold 9.1 MB, and var(Float64) 119.2 MB to hold 24.2 MB;
- **each `NBytes` counted length, not capacity,** 5 to 13% under what was held at
  this size.

`Reserve` appended one group at a time, so past 256 groups each array grew by
`append`'s quarter steps.

## 2. What changed

**`extend(s, n, fill)`** in `internal/kernel/agg.go` returns `s` at length `n`:

- the new elements are set to `fill`;
- the capacity at least doubles when it must grow, or becomes `n` if that is larger.

**Every `Reserve` calls it, in `agg.go`, `aggstat.go` and `topk.go`:**

- count, n_unique's counts, sum, mean;
- the four extremum kinds, first and last;
- implode, var, product, arg_min and arg_max;
- quantile and median, top_k and bottom_k.

Product's fill is 1, its identity; every other fill is the zero value.

**Every `NBytes` of an array `Reserve` grows counts capacity.** Doubling can leave
half an array unused, and a budget counting only the used half would miss it. Also:

- sum's and mean's `carry` were not counted at all, and are now;
- `colBytes` counted its pointer slice by length, and now by capacity.

## 3. Measured

| accumulator, to 1,000,000 groups | allocated / held, before | after |
| --- | --: | --: |
| sum(Float64) | 44.8 / 9.1 MB | 22.0 / 11.0 MB |
| sum(Int64) | 89.0 / 18.0 MB | 41.4 / 20.8 MB |
| count | 39.7 / 8.1 MB | 19.5 / 9.8 MB |
| mean(Int64) | 123.7 / 25.0 MB | 58.4 / 29.3 MB |
| var(Float64) | 119.2 / 24.2 MB | 58.5 / 29.3 MB |
| max(String) | 88.9 / 18.0 MB | 41.4 / 20.8 MB |

Every case is now 2.0 allocated per byte held. Before, n_unique was 2.6, its key
table already doubling, and the rest were 4.8 to 5.0. `NBytes` now equals what is
held, within a rounding.

**The trade: what is held at the end rose,** 9.1 to 11.0 MB for a sum. A million
groups reached in steps of ten thousand ends at a capacity of 1.28 million, where
`append` left about 1.06 million. At its worst, just after a doubling, an array
holds twice its length.

The budget counts it now, so a query under a limit spills a little sooner, rather
than holding memory the budget does not see. In `gb10`'s sum and count that is up to
17 bytes a group, against the key table's 80 or so.

**The memory tests** (internal/memcheck), against step 133's readings:

| reading | step 133 | now |
| --- | --: | --: |
| six-key group-by, sampled, counted | 367–408 MB, 362.1 MB | 361.2 MB, 362.1 MB |
| the same, assembling its 159.4 MB answer | 172.4 MB | 164.9 MB |
| one-key group-by, sampled, counted | 137.9 MB, 117.2 MB | 125.9 MB, 117.3 MB |

The counted figures barely moved. Two million groups reached in steps of 8,192 end
at exactly 2,097,152, a power of two times 8,192, so capacity equals length.

## 4. Not measured

- **Speed.** A `Reserve` now copies once per doubling instead of appending per
  group. No benchmark was run.
- **`gb10` at ten million rows.** The accumulators were 2.7 GB of the profile's
  22.9. Ten million groups reached in steps of 8,192 end at 1.68 times their
  length, so their allocations would fall by about a third, not to two fifths.
  That is an estimate.

**Left as they were:** the parallel fold, a quarter of `gb10`'s allocations, and the
CSV reader's builders.

## 5. Teeth

| tooth | result |
| --- | --- |
| `extend` grows by `append` | **bites:** `TestAccumulatorsGrowByDoubling` |
| sum counts length, not capacity | **bites:** the same test, where `NBytes` falls short of what is held |
| `extend` leaves new elements unset | **bites:** `TestAggregatesMatchNaiveImplementation`, `TestProductReturnsFloat64`, the Decimal reference test (root) |
| product starts its groups at 0 | **bites:** the same three |

The last two are silent in `internal/kernel`'s own tests: a product's value is
checked only from the root package.

**Gate:**

- race 23 ok, levels, vet ×3 and the bench engine tests clean;
- test-all 114 ok of 115. In the scalar-only leg, `TestQueriesHoldWhatTheBudgetCounts`'
  spilling join, under a 16 MB budget, read **128 MB** of live heap against its bound
  of 96.

**That reading is a flake, not this step:**

- A join holds no accumulator, and nothing it calls changed here.
- Run alone in the same build, twelve times, it read 19.6 to 26.7 MB.
- The scalar leg, rerun over every package three times, passed each time.
- It has passed in every leg of every gate since step 129, about thirty-six runs.

The sampled reading counts what a collection allocates while it marks. In the gate,
every package's tests compete for the CPU, the collector falls behind the filter
feeding the join, and the reading grows with it. Step 129 recorded the same noise in a
`Collect`, 145 MB in one run and 111 in the next.

**The open fix:** read the spilling case exactly, at its concatenations, as the other
cases are, instead of sampled. Not done here.

## 6. Measured afterwards, at the maintainer's request

`gb10` and `j5` at ten million rows, ursus alone, one timed run each, under a 3 GB
scope, at `c83a400`. Beside each, step 133's run, at `c5c9e24`. CPU is the scope's,
from the journal.

| query | time | CPU | VmHWM | heap in use | allocated | counted | collections |
| --- | --: | --: | --: | --: | --: | --: | --: |
| `gb10`, CSV | 6.8 s (6.4) | 21.7 s (20.0) | 2.77 GB (2.85) | 2.83 GB (2.85) | 17.5 GB (18.5) | 1.65 GB (1.61) | 39 (40) |
| `j5`, CSV | 10.0 s (9.6) | 53.9 s (49.6) | 2.88 GB (2.87) | 2.83 GB (2.80) | 17.1 GB (17.1) | 1.21 GB (1.21) | 40 (39) |
| `gb10`, Parquet | 4.0 s (3.8) | 16.1 s (16.0) | 2.67 GB (2.88) | 2.82 GB (2.80) | 16.4 GB (17.4) | 1.61 GB (1.55) | 30 (35) |
| `j5`, Parquet | 4.4 s (4.9) | 41.6 s (42.4) | 2.88 GB (2.90) | 2.78 GB (2.81) | 17.4 GB (17.4) | 1.04 GB (1.04) | 32 (32) |

**`gb10` allocates 1.0 GB less, 5 to 6%.** That is about what §4 estimated: the
accumulators were 12% of its allocations, and a third of that is 4%.

**Its peak is unchanged or lower:**

- VmHWM fell over Parquet, 2.88 to 2.67 GB;
- its sampled heap in use did not move;
- what the budget counts rose 0.04 to 0.06 GB, the accumulators' unused capacity
  now counted.

**Its CPU did not move beyond the noise.** `j5`, which holds no accumulator and so
is unchanged by this step, moved by +9% over CSV and −2% over Parquet between the two
runs. That is how far single runs on this machine wander. `gb10` moved by +8% and
+1%, inside it.
