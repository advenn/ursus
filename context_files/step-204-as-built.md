# Step 204 — as built

**D3 of `v0.6.2-scope.md`: a dense build skips the hash tables.**

## 1. Why

In step 201's profile, q21's join build was 45% of the query. Of that,
`IntKeyTable.getOrInsert` alone was 19%: six million lineitem order keys, each
hashed into its partition's table.

A deferred build holds all its keys before it inserts any. When they are dense,
step 203's direct index can take each insert by subtraction, and no table is needed.

## 2. What changed

- **Phase 1 of `buildPartitioned`** gathers each batch's valid integer keys and their
  range (`intKeyStat`). It no longer routes them.
- **The keys are then routed:**
  - **dense keys, by step 203's rule** (at most 4 index slots a key, or at most 2²⁰
    slots), go by their place in the range: `(k - lo) >> shift`, the least shift
    that leaves at most one run a partition;
  - **other keys hash,** as before.
- **`buildDense`:**
  - partition p owns the index's slots from `p << shift`. Its goroutine sets them to
    -1, then walks its own rows in row order;
  - an empty slot makes a new id. A full one is a duplicate, counted, and the first
    is kept for `Validate`;
  - `counts`, `rowKey` and the first duplicate of all come out as the hashed build
    leaves them;
  - then each partition's ids move past the ids of the partitions before it, in its
    run of slots and in its rows' `rowKey`, in parallel. Step 3 numbers the tables
    the same way.
- **`kernel.NewDenseIntKeyParts`** is the result: one table of keys alone, its index
  already built. `DenseIndexFits` is the rule, shared with `Index()`.
- **The rule counts rows, not distinct keys,** which a build cannot know before it
  builds. So the index is at most 16 bytes a build row, or 4 MB.

**Serial first.** The first version filled the index in one serial pass. It took
22% less CPU than step 203 on q21, and 32% less on q4. But q4's wall time rose,
161 → 169 ms, since the hashed build had run a goroutine a partition. Routed by
range, alternated against the serial version:

| query | CPU | wall |
| --- | --: | --: |
| q21 | 1,385 → 1,604 ms, +16% | 465 → 436 ms, **−6%** |
| q4 | 543 → 626 ms, +15% | 165 → 140 ms, **−15%** |

Wall time is what the benchmarks rank, so the parallel version stays.

**Found on the way: a join's key tables went off the ledger while it probed.**

- `freeze` releases the join's account, then charges the build rows and the CSR
  arrays again. The key tables the probe reads were not charged again; only step
  203's index was.
- Now `freeze` charges whatever keys the table holds: an `IntKeyParts`, with its
  index, or the encoded `KeyTable` or `KeyParts`. `joinKeys` gains `NBytes`, and the
  two encoded tables answer 0 when nil.
- **Measured:** a Semi join under a Sort, over 200,000 keys. The peak is 13.1 MB
  without the charge; with it, 23.6 MB on one thread and 19.7 MB on four.

## 3. Tests and teeth

- **`TestADenseBuildJoinsAsTheHashTablesDo`** (root):
  - every join kind, over keys with duplicates and nulls, on eight threads;
  - run once on dense keys and once on the same keys 2³³ apart, which hash;
  - the values agree, and `Validate`'s refusals are word for word the same, naming
    the first duplicate row.
- **`TestADenseBuildHoldsOneIndexAndNoTables`** (physical): what no answer shows,
  the probe's keys.
  - **Keys over 1.5 million values** fit only by their count, gathered from every
    batch: one table and an index.
  - **Moved 2⁴⁰ up,** they fit by their own range, not one stretched down to zero.
  - **Spread 2³³ apart,** they hash.
- **Step 151's tests now run the dense build,** since their keys span 6,000 and
  50,000:
  - `TestThePartitionedBuildAnswersAsTheStreamingOne`: rows in the streaming build's
    order;
  - `TestThePartitionedBuildRefusesADuplicateAtTheSameRow`: the refusal at its row.
- **Memory:**
  - `TestADenseJoinAccountsItsIndex`, over keys three apart: the peak is 9.84 MB
    charged, and 7.44 MB with the index left out. The floor is 43 bytes a key.
  - `TestAJoinAccountsItsKeysWhileItProbes`: the floor is 80 bytes a key.
  - `TestJoinAccountsItsHashTable` now spreads its keys 2³³ apart, so it still
    measures hash tables.

| tooth | result |
| --- | --- |
| the dense path never taken | **bites:** the index test, added for it |
| a key's id one past its own | **bites:** about twenty join tests |
| a duplicate's count not raised | **bites** |
| a row's key left unset | **bites** |
| a null key's row routed | **bites** |
| a duplicate never refused | **bites:** five tests |
| a partition's last duplicate named, not its first | **bites:** the two refusal tests |
| the last partition's first named, not the first of all | **bites:** the two refusal tests |
| the range's low end not lowered | **bites** |
| a batch's range merged without its count | **bites:** the index test's 1.5 million values |
| a merge with an empty side kept its zero range | **bites:** the index test's keys moved 2⁴⁰ up |
| one partition too many | **bites** |
| a run's last slot left out | **bites** |
| the index's ids not moved past the partitions before | **bites** |
| `rowKey`'s ids not moved past the partitions before | **bites** |
| the dense keys' index not charged | **bites:** the dense memory test, once its keys were three apart |
| the probe's key tables not charged | **bites:** the probe memory test, added for it |
| only the integer tables charged | **bites** |

Four of these teeth were silent at first: no answer changes when the dense path is
not taken, and a 0.8 MB index was within the first memory floor's margin.

## 4. Measured

Step 203's runner against this step's, alternated three rounds, PDS-H SF=1:

| query | CPU | wall |
| --- | --: | --: |
| q21 | 2,258 → 1,856 ms, **−18%** | 566 → 493 ms, **−13%** |
| q4 | 807 → 644 ms, **−20%** | 160 → 142 ms, **−11%** |
| q17 | 1,085 → 1,080 ms, ±0% | 203 → 205 ms, +1% |
| q7 | 1,792 → 1,802 ms, +1% | 356 → 373 ms, +5% |
| q3 | 1,079 → 1,072 ms, −1% | 190 → 188 ms, −1% |
| q9 | 2,095 → 2,099 ms, ±0% | 390 → 397 ms, +2% |

- **q21 ran in two modes on both runners:** 1,995 or 2,258 ms on step 203's, and
  1,644 or 1,856 ms on this step's. Either way it is −18%.
- **q7's +5% wall** is within its two modes (step 203). Its CPU did not move.

**q21's profile,** on this step's runner, classified as step 201's was (the classes
overlap):

| | CPU | build | keys | probe | Parquet |
| --- | --: | --: | --: | --: | --: |
| step 201 | 6.07 s | 45% | 33% | 16% | 31% |
| this step | 4.58 s | 18% | 2% | 20% | 40% |

What is left of the build is freeze's CSR scatter and the build side's concat.

**Against 0.6.1,** `runner-s200` and this step's, alternated three rounds:

| query | CPU | wall |
| --- | --: | --: |
| q21 | 2,078 → 1,597 ms, **−23%** | 481 → 430 ms, −11% |
| q7 | 2,294 → 1,771 ms, **−23%** | 410 → 351 ms, −14% |

The scope claims q7 and q21 at least 20% faster. By CPU that is met; by wall time
it is not yet.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
