# Step 202 — as built

**D1 of `v0.6.2-scope.md`: a runtime filter tests a dense key against a bitmap.**

## 1. Why

A runtime filter (step 167) asks one thing of each probe row: is its key among the
join's? It asked through `IntKeyParts.GetMany`, the join's own hash tables, a hash
and a probe a row. In step 201's profile of q7 that was 19% of the query.

TPC-H's keys are dense integers. A bit for each value in the keys' range answers the
question with one test.

## 2. What changed

- **`kernel.IntKeyBits`** is a set of integer keys as a bitmap over `[lo, lo+span)`.
  `Has(k)` takes `uint64(k) - uint64(lo)`, which wraps a key below `lo` past `span`,
  and tests one bit.
- **`IntKeyParts.Bits()`** builds it once, on first use, under a `sync.Once`, so every
  worker of the filter shares it. It skips a group-by's null placeholder, and returns
  nil when there are no keys, or when the range is too sparse for one.
- **The rule:** a bitmap is built when the range is at most 64 bits a key, eight
  bytes a key, about what the table spends on each, or at most 2²³ bits, a megabyte,
  whatever the count.
  - The first rule allowed 16 bits a key. q7's filter holds a fiftieth of the order
    keys, 120,000 over a range of six million, and was refused, so the profile
    still showed `GetMany`.
  - That bitmap is 750 KB and stays in cache, beside a 2 MB table that does not.
- **`runtimeFilterOp.Apply`** tests the bitmap when there is one, and looks keys up in
  the tables as before when there is not.

## 3. Tests and teeth

- **`TestIntKeyBitsHoldTheKeysAndNoOthers`:**
  - keys from -500 to 10,000 in steps of 3, across three tables, one holding a null
    key: every value in and around the range answers as the set does, and the
    extremes are outside;
  - the rule's edges: two keys a megabit apart, and one bit more; 64 and 65 bits a
    key past a megabit; the whole int64 range; no keys.
- **`TestARuntimeFilterKeepsExactlyTheRowsThatCanJoin`** (physical): the filter's own
  answer, which a query's cannot show, since the join drops what the filter keeps.
  It runs on dense keys, through the bitmap, and on keys 2³³ apart, through the
  tables, with nulls: exactly the rows whose key is among the join's and not null.
- **`TestRuntimeFiltersKeepTheAnswerOnSparseKeys`:** the query-level comparison with
  the filters off, on keys spread past any bitmap. The existing test's keys span
  2,000, so it runs the bitmap path.

| tooth | result |
| --- | --- |
| a key one past its bit | **bites:** the bitmap test, the query tests |
| a null key's placeholder set in the bitmap | **bites** |
| no bitmap, ever | **bites** |
| a bitmap at any density | **bites** |
| the sparse path keeps every row | **bites:** the operator test, added for it |
| a null key kept by the bitmap | **bites:** the operator test, added for it |
| no range check, only the bitmap's length | silent: a bit past the range is never set, so the two checks agree |

## 4. Measured

0.6.1's runner (`runner-s200`) against this step's, alternated three rounds, PDS-H
SF=1:

| query | CPU | wall |
| --- | --: | --: |
| q7 | 1,705 → 1,474 ms, **−14%** | 308 → 289 ms, −6% |
| q8 | 1,372 → 1,108 ms, **−19%** | 236 → 217 ms, −8% |
| q5 | 1,396 → 1,210 ms, **−13%** | 261 → 232 ms, −11% |
| q21 | 2,475 → 2,232 ms, **−10%** | 576 → 548 ms, −5% |
| q9 | 2,282 → 2,268 ms, ±0% | 416 → 415 ms, ±0% |

In q7's profile, the filter's `Apply` fell from 1.57 to 0.50 s of CPU.

**The first measurement was unusable:** q7 ran at about 1.7 s or about 2.3 s of CPU,
on either runner, from round to round, and the medians followed the mode. A filter
keeps every row until its join publishes, so the likely cause is a race between the
join's build publishing and the probe side's first batches. It is noted for D2.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
