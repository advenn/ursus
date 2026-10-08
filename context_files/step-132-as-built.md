# Step 132 — as built

**Where `gb10` allocates.** Step 131's run under 3 GB showed `gb10` over Parquet
allocating 25.6 GB in 4.7 s, with its heap at the cap and twice what the budget
counted. This step is the allocation profile the maintainer asked for. No code
changed.

## 1. How

**The runner was run directly,** as the driver runs it, with `-allocprofile`:

- under a 3 GB scope with swap off, on 8 threads;
- at `79636f6`;
- one warm-up and one timed iteration;
- over Parquet and over CSV.

**The figures cover both iterations:** the `allocs` profile counts every allocation
since the process started, sampled.

| run | time | allocated in all | sampled in the profile | heap in use, peak | counted | collections |
| --- | --: | --: | --: | --: | --: | --: |
| Parquet | 4.3 s | 32.4 GB | 22.9 GB | 3.19 GB | 1.54 GB | 57 |
| CSV | 7.2 s | 35.1 GB | 25.1 GB | 3.02 GB | 1.72 GB | 75 |

The profile's total trails the runtime's, because a profile is published at the
collection before it is read. The shares below are of the profile's.

## 2. What it says, over Parquet

| where | allocated | share |
| --- | --: | --: |
| **the key table's arrays, growing** | 11.5 GB | 50% |
| — its key arena, `t.arena = append(t.arena, key...)` | 8.1 GB | 35% |
| — `hashes` and `offs`, by `append` | 2.4 GB | 11% |
| — the slot ring, doubling | 0.7 GB | 3% |
| Arrow buffers: decoded strings, taken and concatenated keys, values | 5.5 GB | 24% |
| the sum and count accumulators, growing in `Reserve` | 2.7 GB | 12% |
| the Parquet reader's own buffers, pages and dictionaries | about 1.8 GB | 8% |

**By caller:**

- **`hashAggSink.Consume`:** 70% of the key table's bytes. These are the workers,
  and then the serial sink.
- **The parallel fold, `hashAggSink.Merge`:** 5.4 GB in all, 24%:
  - inserting every worker's keys again into the first worker's table, 3.5 GB;
  - merging the accumulators, 0.9 GB;
  - concatenating and taking the workers' keys, 0.8 GB.

  With one group per row, no two workers share a key, so the fold re-encodes every
  key it folds.

**Over CSV, the same shape:**

- the key table 52%;
- Arrow buffers 25%;
- the accumulators 10%;
- the CSV reader's column builders, `fixedBuilder.appendField`, 2.4 GB, 10%. These
  also grow by `append`.

## 3. Why so much: `append`'s growth

**Go grows a large slice by about a quarter at a time,** not by doubling: past 256
elements, `growslice` adds about a quarter of the capacity.

To reach a final size F that way, the allocations sum to about F × (1 + 0.8 + 0.64
+ …), about 5F. Doubling would sum to 2F. Arrays that never moved would allocate F.

**The arena holds every key, encoded.** For `gb10` that is ten million keys of
about 60 bytes, about 0.6 GB per complete table, and so about 3 GB per run for one
table.

Every worker's table grows the same way, and the fold grows the first table again
to hold every key. That is the order of the 4 GB a run the profile shows. The
mechanism is read from the code and the Go runtime, not measured on its own.

**It is also the likely answer to step 131's open question.** There, a six-key
group-by's live heap ran 1.31 of what it counted while it aggregated. When `append`
grows the arena, the old array and the new one are both alive while it copies, and
the old one stays as garbage until the next collection. The ledger counts only the
new capacity, so that is up to four fifths of an arena, uncounted. A six-key arena
of two million keys is on the order of 100 MB, and the gap was 116 MB. Not
verified.

## 4. What would move it, not built

In the order of what each would save, estimated from the shares above:

1. **The key table's arrays grow without `append`'s quarter steps.**
   - **Chunked arena:** fixed-size chunks that never move. Keys are addressed by
     offset already, so a key must not straddle two chunks. This allocates about
     F, and nothing is held twice while it grows.
   - **Simpler:** grow `arena`, `hashes` and `offs` by doubling. That allocates
     about 2F, and still holds the old array while it copies.

   Either takes the table from about half of all allocation to about a fifth or
   less. The chunked arena would also remove the uncounted copy of §3.
2. **The accumulators' `Reserve` grows by doubling** instead of to the exact count
   each batch. That is most of 2.7 GB.
3. **The fold.** Sizing the first table for every worker's keys before inserting
   them avoids growing it, but the re-encoding stays.

   The fold's whole cost, a quarter of all allocation here, is what a partitioned
   (radix) parallel group-by removes: each worker owns a range of hashes, and
   nothing is merged. That was v0.4-scope item 18, moved to 0.5.
4. **The CSV reader's builders** grow the same way: 10% of the CSV run.

Items 1 and 2 are small and local, and the memory tests from steps 129 and 131 can
check them. Whether they also make `gb10` faster takes a run, and how much of its
time goes to copying and collecting takes a CPU profile; neither was taken.

## 5. Files

Not committed, being binary and machine-specific:

- the profiles, `gb10-parquet.allocs` and `gb10-csv.allocs`;
- the runner's JSON results.

Reproduce with:

```
systemd-run --user --scope -p MemoryMax=3G -p MemorySwapMax=0 -- taskset -c 0-7 \
  env GOEXPERIMENT=simd GOMAXPROCS=8 bench/bin/runner --engine ursus --suite h2o \
  --query gb10 --data bench/data/h2o/n10000000 --io parquet --iterations 1 \
  --threads 8 --out gb10.json --result gb10.parquet --allocprofile gb10.allocs
go tool pprof -sample_index=alloc_space -top bench/bin/runner gb10.allocs
```
