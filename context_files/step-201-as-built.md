# Step 201 — as built

**The 0.6.2 scope, `v0.6.2-scope.md`, and the profiles it stands on.** No code
changed.

## 1. The maintainer's request

With 0.6.1 finished, the maintainer asked for 0.6.2 to be planned and built. No new
theme was named, so 0.6.1's two stand: **speed and out-of-core**.

## 2. The profiles

- **The runner:** 0.6.1's code, `806070b`, built as the bench builds it,
  `runner-s200` in the scratchpad. It is every 0.6.2 speed step's baseline.
- **The queries,** the widest gaps of step 200's report and two more, one at a time:
  - PDS-H SF=1: q7, q16, q17, q15 and q21, three iterations;
  - h2o gb1 over Parquet, one.

**Three findings:**

1. **ZSTD is 11 to 42% and stays.**
2. **An integer key's hash table is the next largest cost:**
   - q7's runtime filter, `IntKeyParts.GetMany`, 19%, a lookup a row to ask whether
     a key exists;
   - q21's build, 45%, of which `IntKeyTable.getOrInsert` is 19%;
   - q17's probe, 17%.

   TPC-H's keys are dense integers, so a bitmap or a direct index answers them
   without hashing.
3. **A string key's group-by** is two-thirds of h2o gb1.

## 3. What the scope takes

- **Dense integer keys:**
  - D1, a runtime filter's bitmap;
  - D2, a join probe's direct index;
  - D3, a dense build without hashing.
- **G1,** a string key's group-by, profiled first.
- **Out of core:** O1, reverse spills; O2, an unkeyed `Rolling` and `GroupByDynamic`
  emit windows as they close.

**The claim:** q7 and q21 at least 20% faster than v0.6.1, PDS-H SF=1 at least 5%,
and reverse and an unkeyed `Rolling` under a tenth of their input.
