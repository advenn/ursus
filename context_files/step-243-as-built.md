# Step 243 — as built

**T1 of `v0.6.4-scope.md`: keyed `Rolling` and `GroupByDynamic` hand their answer
out,** where they held every window to the end.

## 1. Why

Since step 207 a temporal group with no key hands its windows out a segment at a
time as they close. A keyed one kept every window in one segment. Its answer is per
bucket, in the order the buckets appeared, each bucket's windows in order, and the
last row of the input may still add a window to the first bucket. So a keyed
rolling group held an answer as large as its input, and refused past the limit.

## 2. What changed

**Every temporal group streams (`tempsegment.go`, `tempstream.go`).** The `stream`
flag, the one-segment path and its Finish with a permutation are gone.

**A segment closes by the index.**

- The index is sorted across the whole input, whatever the keys. So a window whose
  upper end the last row read has passed takes no more rows, in whichever bucket.
- A segment keeps the largest upper end of its windows (`tgSeg.hi`, set in
  `openSeg`). It is handed out once it is full and the last row has passed that end.
- This replaces "the oldest open window of bucket 0", which only an unkeyed group
  could use. It needs no scan of the buckets.

**A keyed segment carries its keys and two hidden columns.**

- Each window's bucket is kept beside its label (`wBkt`); a bucket has an `id`.
- The buckets' keys are kept a row a bucket: in batches of `chunk()` rows
  (`keyRows`), and the newest a batch each (`keyNew`), concatenated once until a
  bucket comes (`keyTail`). `windowKeys` gathers a segment's keys from them.
- `segBatch` adds the key columns, and `__bkt` and `__win`, each window's bucket and
  number. Their names are lengthened until no column has them (`unusedName`, which
  `ordinalName` now uses).

**The bucket order (`bucketOrderSink`, after a keyed group).**

- A sort on (`__bkt`, `__win`) holds the windows and spills them in sorted runs
  past the limit. Its runs are at least a sixteenth of the limit, as a window's
  sorted pass's are. The sink drops the hidden columns (`renameOp.drop`).
- Under the limit the sort is not needed: the windows come in their numbers'
  order, so a stable counting sort on the bucket is the order, and one Take of what
  was held gives the answer.

**A temporal group refuses only once its own state is past the limit.** Its open
windows cannot spill, and what it has handed out a keyed group's sort holds, and
spills. When it checked the query, it refused while that sort held the query over,
a batch before the sort spilled. It now has the budget's limit (`limit`).

**The charge.**

- It walks the buckets, and a keyed group that hands its windows out holds few of
  them. So it is made once per as many pairs as there are buckets, windows or a
  batch, whichever is most.
- The keys are charged as a running total (`keyBytes`).

**The hint** for rolling and group_by_dynamic says they hold their open windows and
hand each out as the index passes it. It also says that a gap makes one window per
`every=` across it at once, and that a median or an implode keeps every value.

## 3. In memory

A keyed rolling sum and a keyed dynamic mean over 2,000,000 rows a second apart
under 1,000 keys:

- the best of five, the runners alternated three times;
- built with the bench flags against step 242's code, from `git archive`.

| query | step 242 | step 243 |
| --- | --- | --- |
| rolling, 1h | 601 ms | 624 ms, +4% |
| dynamic, every 10m (3.3M windows) | 564 ms | 638 ms, +13% |

How it got there:

- **through the sort,** the answer cost +56% and +135%. The sort gathers each output
  batch out of every segment it holds, about four hundred;
- **with the counting sort** under the limit, +10% and +22%;
- **with the keys' tail made once and the hidden columns not taken,** +4% and +13%.

What is left: each segment's aggregates finish apart, and the segments are
concatenated once before the Take.

## 4. Tests and teeth

**`TestKeyedTemporalGroupsHandOutTheirWindows`.**

- **The groups:**
  - rolling 30m;
  - rolling 2h with a 15m forward offset, closed both;
  - dynamic every 1h, period 3h, with boundaries and labelled by a row;
  - dynamic every 30m, offset −20m, closed none.
- **The keys:** 75 keys of a string and a nullable integer, interleaved at random.
  One first appears halfway, and one has a single row.
- **The oracle:** each key's answer is the same group with no key over that key's
  rows alone, in the order the keys first appear.
- **Over 30,000 rows** (`keyedSeries`, made in frames of 1,000 rows, since a batch
  sliced from one frame is charged its string column's every byte):
  - in memory, the answer is the oracle's;
  - under a tenth of the input at 64 rows a batch, on one thread and four, the
    answer is the same;
  - it spills;
  - it peaks at most twice the limit, at 1.05× to 1.56×;
  - the spill directory is empty after.
- **Why 64 rows a batch:** at 512, the sort's cascade merged sixteen runs holding a
  batch of each, three times this limit.
- **Over 3,000 rows,** at batches of 1, 7 and 64, with implode too.
- **Under the race detector,** only the small half runs, at 7 rows a batch: 6 s.

**`statedrop_internal_test.go`** finds a keyed group's sink under its bucket order.

| tooth | result |
| --- | --- |
| a segment out before its windows close | **bites,** keyed and unkeyed |
| a segment's end is its first window's | **bites** |
| a bucket's windows reversed in memory | **bites** |
| refuses while its sort holds the query | **bites:** the tenth refuses |
| every key from the first block | **bites** |
| the newest keys never made again | **bites** |
| buckets kept past their windows | **bites** |

**Not flipped here.**

- **`TestGridAccountingIsQueryWide`** still refuses three buckets. Each bucket's
  grid is made at once when its second row comes, within one batch: that is Tier
  2's long index gap.
- **`TestRollingStateIsCharged`'s median** needs the segments' aggregates to spill
  their values, which is next.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
