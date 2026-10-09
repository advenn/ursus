# Step 150 — as built

**Item 2 of `v0.5-scope.md`: the group-by's parallel fold, partitioned.** This is
stage (a) of the radix group-by.

## 1. Evidence first

**Step 141's profile did not reach item 2.** It found `gb10`'s cost in allocation
(step 132), and no PDS-H query it profiled spent much in a group-by's fold. So the
fold was measured on its own: a group-by of four million in-memory rows over two
million keys, `Sum` and `Mean`, on eight threads.

| | per run | CPU in `hashAggSink.Merge` |
| --- | --: | --: |
| before | 660 to 725 ms | 1.29 s over three runs, about 0.43 s a run |

**The serial fold was about 60% of the wall clock.** `Merge` re-inserts every
worker's every key into worker 0's table, on one goroutine. Consuming, by
comparison, is about 160 ms of wall clock on eight cores.

## 2. What changed

### `foldPartitioned` (`internal/physical/agg.go`)

**When it runs.** `parallelSink.drain` asks the sink, after the workers finish and
before the serial fold. It applies when all of these hold:

- the workers hold `partitionedFoldMin` (65,536) groups between them;
- none is ordered, frozen, spilled, or a global aggregate;
- no worker has reached the budget, after which the rest of the input is the
  merged sink's;
- the budget can hold the partitions beside the workers: `Used() + held ≤ Limit()`.

Otherwise the serial fold runs as it always has.

**How it folds:**

1. **Route.** On a goroutine per worker, each worker's groups are routed, in id
   order, by the hash its key table stored, so no key is hashed again. The **top**
   half of the hash names the partition, by multiply-shift. The tables index their
   slots by the low bits, so a partition taken from those would crowd its keys into
   a fraction of its own table's slots.
2. **Merge.** One partition per worker, each merged on its own goroutine, from a
   fresh sink built by the same `SinkFactory` (`mergeShare`). It takes its share of
   every worker, in worker order:
   - **the keys** go in through the new `KeyTable.GetOrInsertHashed`, 64 at a time
     after the slots are touched, as `GetOrInsertMany` does;
   - **the new keys' values** are taken straight from the worker's key parts
     (`takeKeys`). Each part holds the groups one batch introduced, in id order, so a
     group's part and row follow from the parts' sizes. `Merge` concatenates every
     part first; here that would hold every worker's keys twice;
   - **the accumulators** go through `Merge` with a **sparse** remap of the
     partition's own groups.
3. **Answer.** The workers are released, and each partition's resident answer is
   served in partition order (`partsRun`).

**The sparse remap (`internal/kernel/agg.go`).** A first version used a dense remap,
-1 for other partitions' groups, which `mergeEach` learned to skip. It was correct,
and spent about three times the serial fold's CPU: every partition filled, scanned
(`mergeCap`) and walked (`mergeEach`) every worker's every group.
`kernel.SparseRemap(src, dst)` encodes the pairs after a marker no group id can be:

- `mergeCap` and `mergeEach` read the pairs;
- `nuniqueAcc`, whose lookup is by source group, builds a dense one for itself;
- the interface's signature does not change, since its only two callers are
  `hashAggSink`'s folds.

**`Merge` only reads `other`,** which every implementation already did. That is now
the contract, since every partition reads a worker at once.

**`parallelSink`** carries the factory, and closes the partition sinks.

### What it gives up

- **Order.** The answer is partition-major. It is deterministic for an input and a
  thread count, and not first appearance. An unordered group-by's order was already
  unspecified, and already depended on the thread count past one batch's keys: the
  serial fold put worker 0's groups first. `MaintainOrder`'s doc says so now.
  Below the threshold the fold is unchanged, which keeps
  `TestParallelIsOrderPreserving`'s promise for the group-bys it checks.
- **Memory.** Every worker's state stays until the last partition has merged; the
  serial fold lets each go as it is folded. That is what the budget check is for.

## 3. Measured

The same probe, the two builds alternated, three runs each:

| | per run | peak RSS |
| --- | --: | --: |
| before | 660 to 725 ms | about 794 MB |
| partitioned, dense remap | 487 to 488 ms | |
| partitioned, sparse remap | 447 to 499 ms | about 1,000 to 1,050 MB |

**−33% per run,** and about a quarter more memory at the fold.

`gb10` under the 3 GB container is checked once, at item 9's report: it is the
case the budget check exists for.

## 4. Tests

**`internal/physical/partfold_test.go`:**

- **`TestAPartitionedFoldAnswersAsTheSerialOne`:**
  - 300,000 rows over about 110,000 groups, keyed by a string and a nullable
    integer, on four threads in batches of 4,096;
  - ten aggregates: sum, mean, min, max, len, count, var, n_unique, a median, and a
    string max;
  - against `WithThreads(1)`, row for row once sorted. Numbers are compared to a
    relative 1e-12: Chan's merge of two variances and Welford's running update round
    their last digit differently, as every parallel fold's var always has;
  - it requires the fold to have run partitioned (`physical.PartitionedFolds`,
    through `export_test.go`);
  - two runs must give one order.
- **`TestAFewGroupsFoldAsBefore`:** a thousand groups do not fold partitioned.
- **Under `-race`:** both, clean.

## 5. Teeth

| tooth | result |
| --- | --- |
| a sparse remap read as a dense one | **bites:** the differential |
| a sparse remap's pairs swapped | **bites:** the differential |
| `n_unique` ignores a sparse remap | **bites:** the differential |
| a key taken from its part without rebasing | **bites:** the differential |
| never partitioned | **bites:** the counter |
| always partitioned | **bites:** the few-groups test; over the root package, `TestParallelIsOrderPreserving` too |

**Not toothed:**

- **A partition taken from the low bits** only slows the fold, which no test sees.
- **The budget check** applies only under the default budget, because an explicit
  limit runs the group-by serially already. It is defensive, and the `gb10` run
  checks it.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. q18's group-by over
`orderkey`, about 150,000 groups, is past the threshold.
