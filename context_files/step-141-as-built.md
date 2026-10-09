# Step 141 — as built

**Item 0 of `v0.5-scope.md`: the profile.** CPU profiles of the widest gaps, taken
before anything is built against them. No code changed.

## 1. How

- **The runner was run directly** with `-cpuprofile`:
  - at `88facfd`;
  - under an 8 GB scope with swap off, on 8 threads;
  - one query at a time;
  - a warm-up, then three profiled iterations.
- **The queries:** PDS-H q15, q2, q7, q12, q17 and q19 at SF=1 over Parquet, and h2o
  `gb1` and `gb4` over CSV at ten million rows.
- **The profiles** are in the scratchpad, not committed.

| query | step 127's report | now, three iterations |
| --- | --: | --- |
| q15 | 324 ms | 253, 266, 253 ms |
| q2 | 117 ms | 97, 92, 101 ms |
| q7 | 564 ms | 450, 471, 444 ms |
| q12 | 374 ms | 373, 356, 343 ms |
| q17 | 404 ms | 318, 301, 296 ms |
| q19 | 418 ms | 362, 379, 360 ms |
| `gb1`, CSV | 1,961 ms | 1,804, 1,819, 1,883 ms |
| `gb4`, CSV | 3,140 ms | 2,657, 3,019, 3,247 ms |

The machine is not the report's session's, so the two columns are not a measured
change.

## 2. What it found

Shares are of each profile's CPU samples. Cumulative shares overlap.

| query | reading Parquet | of it, ZSTD | filters | join probe | join build | other |
| --- | --: | --: | --: | --: | --: | --- |
| q15 | 57% | 29% | 21% | | | group-by 9% |
| q2 | 29% | 15% | | 61% | | probe's gather 20%, its lookups 15% |
| q7 | 43% | 24% | 17% | 14% | | `Take` 8% |
| q12 | 45% | 13% | 24% | | 17% | |
| q17 | 35% | 22% | | 26% | | its lookups 13% |
| q19 | 56% | 21% | 23% | | | `memmove` 14%, `Take` 10% |

**Reading Parquet is the largest cost in every one,** as step 101 found before the
reader was made parallel. Inside q19's 56%:

- **ZSTD, 20.5%.** arrow-go calls klauspost's `DecodeAll`, already in assembly.
  Step 124 found no gain in more decoders. Nothing local moves it.
- **String columns, 20%.** That is copying each value out of its dictionary
  (`byteArrayCol.take`, `appendVal`, 11%) and decoding the dictionary's indices (8%).
- **Int64 and Float64 columns,** 14% and 13%, each with its own decompression.

**q15 reads and aggregates `lineitem` twice.** The port uses `bySupplier` for the
join and again for the maximum (`pdsh.go` `q15`). Everything under it is the
reading, the filter and the group-by: about 85% of q15's CPU. Sharing it, item 1,
removes about half.

**Filters are 17 to 24%** in the four filter-heavy queries. In q19, `memmove` (14%)
and `Take` (10%) are mostly the filter copying every column. In q15, the
three-valued AND and OR are 12%. Item 4 removes the copies.

**The join probe is the most of q2, 61%, and a quarter of q17.**

- Inner joins already probe in parallel.
- The cost is the gather of the output, 20% of q2, and key lookups that miss the
  cache, 13 to 15%.
- No item in the scope addresses the probe directly.

**`gb1` and `gb4` over CSV are bound by the scan, which is serial.**

- `gb1` kept 1.4 cores busy on average, and `gb4` 1.6.
- The scanner, `scanner.Next`, is 56% and 34% of their CPU on one goroutine, behind
  the reader's lock.
- Conversion is split by column: two columns for `gb1`, four for `gb4`.
- Item 5 is confirmed.

## 3. The order, re-ranked

1. **Item 4, filters copy once:** 17 to 24% of four queries, small, and independent.
2. **Item 1, CSE:** about half of q15, the widest gap. Then q2.
3. **Item 5, the parallel CSV tokenizer:** confirmed. `gb1` and `gb4` wait on one
   goroutine.
4. **Item 2, the group-by's parallel fold:** for `gb10`, whose profile is
   allocation (step 132). None of these PDS-H queries spends much in it.
5. **Item 3, the parallel join build:** q12's build is 17%.

**Not in the scope, and the largest cost left:** the Parquet reader itself.
- ZSTD has no local fix.
- Strings copied out of their dictionary per value are about a fifth of a
  string-heavy query. Reading without that copy needs a column that refers to its
  dictionary, which ursus does not have.
- Recorded here for after 0.5's items.

**The target stays** PDS-H SF=1 within 3× of Polars. Item 1 alone takes q15 from
about 10× to about 5×. Whether the geomean reaches 3× depends on items 4 and 5 and
the probe; the full report at the end will say.
