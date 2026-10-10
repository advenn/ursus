# Step 161 — as built

**Item 3 of `v0.6-scope.md`, its first half: Semi and Anti joins build in
parallel.**

## 1. Evidence first

Step 157's q4 profile put its build at 41% of the query:

- q4's semi join builds about 3.8 million lineitem keys, 1.5 million of them
  distinct, on one goroutine;
- q22 does the same with 1.5 million customer keys.

`deferBuild` refused both, because a Semi or Anti join keeps its keys and never its
rows, so there was nothing to read the keys back from at freeze.

## 2. What changed

**`deferBuild` defers Semi and Anti joins** under the same conditions as the rest:
several threads, a key, and no limit the caller set.

**A deferred Semi or Anti join keeps its keys, not its rows** (`joinBuildSink.keyParts`).

- **What it keeps:** for each build batch, `Consume` evaluates the keys as the
  streaming build would, cast to the join's key types, and keeps them in a batch of
  their own, charged.
- **Why not the batches:** a wide build side would otherwise be held whole until
  freeze.
- **What it costs:** O(build rows) of key columns until freeze, against the streaming
  build's O(distinct keys). That is about what a kept build side costs every other
  kind, bounded the same way, by `catchUp` at half the budget.

**`buildPartitioned`:**

- reads those key batches directly;
- tracks no `rowKey` or counts for a kind that never reads them;
- since step 159, keys an integer join key as integers, so q4 and q22 get both
  changes.

**`catchUp`** inserts the kept keys through `admitKeys`, which is the streaming
`admit` split in two. Then it releases the key batches' charge, since a Semi or Anti
join keeps nothing else. **`freeze`** drops them, and **`Merge`** carries them.

**Each partition visits its own rows alone.** Step 1 now lists each batch's rows by
partition. Step 2's goroutines had scanned every build row's partition byte, once
per partition, which every deferred build since step 151 paid. In q4's profile step
2's own loop fell from 0.30 s to 0.08 s of samples, of 2.10 s and 1.99 s in all.

## 3. Measured

Step 160's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included. Two sessions: before
and after the row lists.

| query | median wall | CPU per iteration |
| --- | --- | --- |
| q4 | 262 → 144 ms (−45%) | 587 → 684 ms (+17%) |
| q22 | 90 → 50 ms (−44%) | 218 → 222 ms |
| q12 | 289 → 280 ms | 996 → 952 ms (−4%) |
| q16 (first session) | 78 → 73 ms | 284 → 253 ms (−11%) |
| q21, q20 (first session) | unchanged | unchanged |

**q4 does more work in total, on more cores.** Its CPU was +17% in both sessions,
before the row lists and after: their saving is within the A/B's spread. What remains
is the inserts:

- after: `IntKeyTable.getOrInsert` is 0.57 s flat in q4's profile, eight goroutines
  each filling a table of about 512k slots, 4 MB;
- before: the serial byte table's insert, encoding aside, was 0.36 s.

Eight cores missing the cache at once each wait longer than one did. The wall clock
is what the target measures. Both figures are given.

q17 read 1,067 to 1,515 ms on both runners in the second session, a spread this
change cannot explain. Its join already deferred.

## 4. Tests

- **Step 151's `TestThePartitionedBuildAnswersAsTheStreamingOne`** now covers Semi and
  Anti joins, nulls equal and not, against the streaming build. It also checks that
  no key batch outlives freeze.
- **`TestADeferredSemiJoinKeepsItsKeysAlone`:** a Semi join fed by hand keeps one Int64
  column for each build batch, no build batch, and is charged less than the batches.
- **`TestADeferredSemiJoinCatchesUpOnItsKeys`:** past half a default budget, the keys
  it kept go into the table, which then holds every distinct key. They rise batch by
  batch, so an early batch's keys appear nowhere later. Its charge is its state's
  alone, nothing spills, and it answers as the streaming build does.
- **`TestMergedDeferredSemiSinksKeepBothKeys`:** two deferred sinks merged, and built,
  have every key. Nothing merges join sinks today; this holds `Merge` to its contract.

## 5. Teeth

| tooth | result |
| --- | --- |
| Semi and Anti never defer | **bites:** the partitioned build's Semi and Anti cases, both new tests |
| a deferred Semi join keeps its build batches | **bites:** `TestJoinNullKeysDoNotMatch`, `TestJoinRowCountsPerKind`, the new tests |
| the key batches kept past freeze | **bites:** the partitioned build's Semi and Anti cases |
| a catch-up still charged for its key batches | **bites:** `TestADeferredSemiJoinCatchesUpOnItsKeys` |
| a catch-up that drops the kept keys | **silent at first,** as every key came back in a later batch; bites since the keys rise |
| a partition's rows visited in reverse | **bites:** `TestThePartitionedBuildRefusesADuplicateAtTheSameRow` |
| `Merge` drops the key batches | **silent at first;** bites since `TestMergedDeferredSemiSinksKeepBothKeys` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
