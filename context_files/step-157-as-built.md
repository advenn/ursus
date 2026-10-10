# Step 157 — as built

**Item 0 of `v0.6-scope.md`: the profiles and the baseline.** No code changed.

## 1. How

**The profiles:**

- a runner built from the `v0.5.0` tag, run directly with `-cpuprofile`;
- PDS-H at SF=1, one query at a time, three profiled iterations after a warm-up, on
  8 threads;
- q4 and q16, never profiled, and q2, q7, q12, q17 and q19 again, after 0.5's changes
  to the filters, the fold and the build.

**Each sample is classified by its whole stack** (`go tool pprof -traces`), so a
category is every sample with one of its functions anywhere below, and categories
overlap. The profiles are in the scratchpad, not committed.

**The baseline:** ursus and Polars in one session, PDS-H at SF=1, the report's
settings: an 8 GB scope, three iterations. Results went to the scratchpad, not to the
report.

## 2. The baseline

| | ursus | Polars | ratio |
| --- | --: | --: | --: |
| PDS-H SF=1, geomean | 302 ms | 97 ms | 3.12× |

**Polars' own geomean was 70 ms on 2026-10-08, 78 ms on 2026-10-09 and 97 ms
today,** on the same laptop and the same data. A ratio between sessions moves by
more than any one 0.6 item will, which is why the target also measures ursus against
v0.5.0, alternated in one session.

**The widest gaps today:** q7 8.9×, q17 6.8×, q19 6.2×, q8 4.6×, q4 4.3×, q15 4.3×,
q12 4.1×, q16 3.9×.

## 3. What the profiles found

| query | timed | CPU | Parquet | ZSTD | filter | probe | build | group-by | `Take` | keys |
| --- | --- | --: | --: | --: | --: | --: | --: | --: | --: | --: |
| q4 | 346, 302, 297 ms | 1.98 s | 34% | 12% | 10% | 1% | **41%** | 1% | 3% | **38%** |
| q16 | 77, 88, 88 ms | 0.91 s | 14% | 2% | 6% | **46%** | | 21% | 6% | **46%** |
| q2 | 72, 78, 66 ms | 0.85 s | 25% | 14% | 2% | **58%** | 1% | | **21%** | 24% |
| q7 | 524, 479, 499 ms | 7.88 s | **45%** | 25% | 14% | **33%** | 1% | | 6% | 21% |
| q12 | 461, 358, 341 ms | 3.80 s | **40%** | 10% | 12% | 2% | **36%** | 1% | 2% | 23% |
| q17 | 443, 436, 420 ms | 8.09 s | 34% | 22% | | **59%** | | | | **40%** |
| q19 | 441, 418, 400 ms | 4.94 s | **57%** | 20% | **22%** | 10% | 3% | | 7% | 5% |

"Keys" is encoding a key to bytes, and the key table, wherever they run: in the
build, the probe and the group-by.

**Five findings:**

1. **Encoding keys and looking them up is the widest cost:** a fifth to almost half
   of six of the seven queries. Item 4, an integer-key path, reaches all six.
2. **A probe row that misses pays twice for nothing** (`internal/physical/join.go`
   `enter`). It encodes its key again and hashes it, byte by byte, with `HashKey`, to
   ask whether its spill bucket has a build file, even when the build never
   spilled. In q17, where nearly every lineitem row misses the 200 parts, that is
   `Encode` 0.68 s and `HashKey` 0.64 s of 8.09 s: about 16%. Not in the scope; S.
3. **q4's build is 41% of it,** the semi join's serial build that `deferBuild`
   refuses. **q12's is 36%:** orders' 1.5M keys, built because the estimate ignores
   the filter that leaves 31k lineitem rows. Item 3.
4. **q2's `Take` is 21%,** the probe's gather of wide supplier rows. Items 6 and 7.
5. **q19 is the reader's (57%) and the filter's (22%).** Items 1, 2 and 5.

## 4. The order, re-ranked

1. **The probe's miss path** (new, S): skip the re-encoding and the routing when
   nothing spilled and nothing validates. q17, q16, q7.
2. **Item 4, the integer-key path** (M).
3. **Item 3:** join sides by what survives, and the parallel semi/anti build.
4. **Item 1:** the reader's double copies.
5. **Item 2:** the predicate kernels.
6. **Item 5:** q19's implied predicates.
7. **Item 6:** runtime filters, one hop.
8. **Item 7's rest:** probe columns reused when every row matched once.

**The features and the robustness items** go between them, as the scope says.

**Gate:** none; no code changed.
