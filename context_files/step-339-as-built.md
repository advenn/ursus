# Step 339 — as built

**`CorrSpearman` spills its pairs,** the first half of `polars-parity.md` §2's last
row: a Spearman's correlation over a group larger than the memory limit, in a
group-by, a window or a temporal group, answers where it refused. `Mode` is the other
half.

## 1. What changed

A Spearman's correlation keeps every pair of a group, as a quantile keeps every value,
and radix partitioning divides a group-by across keys, never within one: over a few
huge groups, or none, it refused past the limit. Its pairs now go to disk as a
quantile's values do (step 220) and `n_unique`'s keys (step 241), through the same
`spillState`, so a group-by, a window over one partition and a temporal group take it
alike.

**The answer is exact first.** Spearman's is Pearson's of the ranks, a tie the mean of
its ranks. Doubled, a rank is an integer, `2·lo + t + 1` for a tie of `t` from `lo`,
and so is each rank less the mean rank, so `Σ dx·dy`, `Σ dx²` and `Σ dy²` are sums of
integer products, summed exactly at 128 bits (`wide.addMul`), each rounded once at the
end. The answer no longer depends on the order the pairs came in: a spilled group's is
a held one's to the bit, and so is a group merged from several workers. It was a float
sum in the pairs' order.

**Spilled,** the pairs go to a `PairSpill` as `(y, x)`, each group sorted by y in place
(`byFirst`), a run each spill. At `Finish` the rest go too, and:

1. **by y,** each tie of y is a run of the merge, ranked at its end; `Σ dy²` gets its
   share, and its pairs go on to a second spill, `Sorting()`'s, as `(x, y's doubled
   rank)`. That spill takes its pairs in any order, holds them to a quarter of the
   limit (4096 at least), sorts them by group and x, and writes the run.
2. **by x,** each tie of x is a run: its `d` times its pairs' `Σ dy` is its share of
   `Σ dx·dy`, and its length times `d²` its share of `Σ dx²`.

A tie's pairs wait for its end in a buffer of 8192; a longer tie is numbered instead,
its pairs sent on with the number, negated, and its rank kept in a table of one entry
for every 8192 pairs at most.

**Ranked by radix, a large group's two variables side by side.** The ranks were a
stable comparison sort of an index; `ranker.doubled` sorts each variable's
`OrderKeyF64` keys with the kernel's radix sorter from 256 values (`radixSorted`, which
is `radixByKey` giving back its sorted keys too, so the walk over ties reads them in
turn), keeps its buffers from one group to the next, and over a group of 65,536 pairs
or more ranks y on a goroutine beside x.

## 2. Tests

**`TestASpearmansSpilledPairsGiveItsCorrelation`** (kernel): a Spearman whose pairs
went to a spill at random points between batches answers as one that held them, to the
bit, over values with NaN of two payloads, ±0 and many ties, nulls on either side, a
group of most rows whose y has two values, each tie past the 8192 buffer and so
numbered, a group of one pair and groups of none; each run in order, a spill letting
every pair go, and a spilled Spearman refusing a merge either way. The fake spill gives
each tie back in an order of its own.

**`TestSpearmanIsPearsonsOfTheRanks`:** the exact sums against a float Pearson's of the
average ranks, by comparison sort and by radix; a group of 66,536 pairs, ranked side by
side, to the bit of the ranks summed one after the other; the doubled ranks of a tie,
of NaN and of ±0. **`TestWideAddMulIsExact`:** 4000 products at int64's ends against
`big.Int`.

**`TestAHotGroupsSpearmanSpillsItsPairs`:** four Spearman correlations over five hot
keys (all-distinct integers, a column with nulls, a float of NaN, ±Inf and ±0, long
ties), keyed, maintaining order or not, and keyless, on one thread and four, under
8 KiB; in a window over one partition and over the hot keys; in a dynamic group under
32 KiB, its batch of 512 rows alone 16 KiB (a median and `n_unique` refuse it under 8
too). Each spills, equals the answer without a limit, and leaves the spill directory
empty. `TestV066UnderAMemoryLimit`'s Spearman moved from refused to answered, and
`Example_memoryRefusal`'s hint names it.

**Polars 1.44.1:** over a million rows, 0.5328322575980518 over the frame against
Polars' 0.532832257598053; the exact value is 0.53283225759805183…, so ursus is within
an ulp and Polars about eleven off. By group the two agree to an ulp.

**Teeth (13 of 13 bite):**

- pairs not spillable;
- a tie of y ranked a half low;
- a long tie's rank not kept;
- each x its own tie;
- the mean rank a half low;
- a negative product's low half not carried;
- the low half's carry dropped;
- the radix sort's keys not the sorted ones;
- a sorting spill sorted by group alone;
- a large group's x ranked twice;
- a spill sorted by x;
- a spilled Spearman merged;
- the hint naming no Spearman.

Two first drafts left a variable unused and only failed to build; each was redrawn to
compile.

## 3. Performance

A million rows, the median of seven, against Polars 1.44.1:

| | before | now | Polars |
| --- | --- | --- | --- |
| `CorrSpearman(x, y)` by 1000 groups | 332 ms | 89 ms | 123 ms |
| `CorrSpearman(x, y)` over the frame | 909 | 98 | 82 |

Over the frame what is left is two radix sorts of a million keys, 33 ms each, and the
pairs gathered from eight workers' partial states.

**Gate:** test-all 135 ok, run again alone: its first run stopped at the 256-bit width on
`TestTemporalMediansSpillTheirValues`' heap growth, 12 MB against its 10, while step
340's tests ran beside it, and alone that test passed five runs of five. Race 27 ok
(the root package 655 s here), levels, vet ×3 and the bench engine tests clean; the 22
PDS-H answers match DuckDB.
