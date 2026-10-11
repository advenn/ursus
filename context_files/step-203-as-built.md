# Step 203 — as built

**D2 of `v0.6.2-scope.md`: a join's probe finds a dense key by index.**

## 1. Why

A partitioned join's probe looks each probe row's key up in the build's hash tables
(`IntKeyParts.GetMany`): a hash, a partition, a probe. That was 17% of q17 in step
201's profile. TPC-H's keys are dense, so a slot for each value in their range,
holding the key's id, answers by subtraction.

## 2. What changed

- **`IntKeyParts.Index()`** builds, once, under its own `sync.Once`, a direct index:
  `index[key - lo]` is the key's id, its table's base plus its id there, or -1. It
  skips a group-by's null placeholder.
- **The rule:** an index is built when the keys' range is at most 4 slots a key,
  sixteen bytes a key, about what the tables spend on each; or at most 2²⁰ slots,
  4 MB, whatever the count.
- **`GetMany`** reads the index when there is one: a key outside the range is -1.
- **A join's `freeze`** calls `Index()` and charges `IndexBytes()` with the table's
  arrays.
- **The `sync.Once` is not caution.** A join that publishes early (step 167) can be
  read by its runtime filters while it freezes.

## 3. Tests and teeth

- **`TestIntKeyIndexAnswersAsTheTables`:**
  - keys from -300 to 3,000 in steps of 2, across three tables placed as a
    partitioned build places them, by `PartitionOf`, one holding a null key;
  - every key in and around the range, and the extremes, gets from the index the id
    the tables' hash path gives;
  - the rule's edges: 4 and 5 slots a key past a mega-slot, two keys a mega-slot
    apart and one more, and none.
  - **The first fixture split keys by value,** not by `PartitionOf`, and the hash
    path could not find them. A partitioned build never does that; the test was
    wrong, not the index.
- **Every join test runs the index:** their keys are small integers. The sparse-key
  runtime-filter test runs the hash path.

| tooth | result |
| --- | --- |
| ids without their table's base | **bites:** the index test and seven join tests |
| a key one slot off | **bites:** the index test and seven join tests |
| absent slots left zero | **bites:** the index test and four join tests |
| a null key's placeholder indexed | **bites** |
| an index at any density | **bites** |
| no index, ever | **bites** |

## 4. Measured

Step 202's runner against this step's, alternated three rounds, PDS-H SF=1:

| query | CPU | wall |
| --- | --: | --: |
| q17 | 1,230 → 1,084 ms, **−12%** | 226 → 204 ms, −10% |
| q3 | 1,114 → 1,073 ms, −4% | 195 → 180 ms, −8% |
| q7 | 1,863 → 1,787 ms, −4% | 375 → 357 ms, −5% |
| q21 | 2,273 → 2,236 ms, −2% | 546 → 566 ms, +4% |

**Two modes, a process each.** Several queries run in one of two modes: q7 at about
280 ms or about 360 ms. Every iteration of a process is in the same mode:

- **not the plan:** q7's `Explain`, hashed in six separate processes, is identical
  in all six;
- **not a seed:** the engine uses no random seed, no `maphash` and no clock in its
  decisions;
- **not this release:** v0.6.0's runs showed the same two modes.

It is the machine's: likely where the process's threads land on the laptop's cores.
An alternated comparison's medians absorb it, and a claim that hangs on it needs
more rounds.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
