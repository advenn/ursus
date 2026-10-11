# Step 191 — as built

**The 0.6.1 scope, `v0.6.1-scope.md`, and the profiles it stands on.** No code
changed.

## 1. The maintainer's choice

After v0.6.0 was tagged (step 190), the maintainer chose two themes for the next
release, **speed and out-of-core**, and named it **0.6.1, not 0.7**.

## 2. The profiles

- **The runner:** v0.6.0's code, built as the bench builds it, with
  `GOEXPERIMENT=simd` and `GOAMD64=v3`. It is kept in the scratchpad as `runner-v060`,
  every 0.6.1 speed step's baseline.
- **The queries,** one at a time with `-cpuprofile`:
  - PDS-H at SF=1: q19, q7, q15, q12, q16, q17 and q14, the widest gaps against
    Polars, three iterations after a warm-up;
  - h2o at 10M over Parquet: j3, j2, gb1 and gb4, one iteration.
- **The classes:** each sample is classified by its whole stack, as step 157's were,
  so the classes overlap. The script is `classify.py` in the scratchpad.

The table is §1 of the scope. **Its six findings:**

1. Parquet is 48 to 77% of the widest PDS-H gaps.
2. ZSTD is 17 to 48% of them. It runs klauspost's decoder, whose fast loop is
   disabled in the library (`const useSafe = true` in `seqdec_asm.go`, after crashes
   found by fuzzing), so there is nothing to switch on.
3. A dictionary string is copied twice, once by arrow-go's `dictConverter` and once
   by `byteArrayCol`: 25 to 31% of q19, q12, j3 and j2.
4. String filters compare every row: 21 to 22% of q19 and q12.
5. Definition levels are 3 to 10%.
6. Probe and `Take` are 27 to 37% of h2o j2 and j3.

## 3. What the scope takes

- **Speed:**
  - P1, the reader decoding its own pages through arrow-go's public page reader;
  - P2, string predicates evaluated on the dictionary, in the reader;
  - P3, a join's output gathered once;
  - P4, the parallel CSV splitter.
- **Out of core:**
  - O1, `Rolling` and `GroupByDynamic` streaming over their sorted index;
  - O2, a window over one partition larger than the budget;
  - O3, forward and nearest `JoinAsOf` streaming;
  - O4, a cross join as a block nested loop.

**The claim:**

- q19 and q12 at least 30% faster than v0.6.0, and PDS-H SF=1 at least 10% faster,
  each alternated in one session;
- the five out-of-core operators pass under a budget a tenth of their input.
