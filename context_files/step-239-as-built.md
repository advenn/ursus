# Step 239 — as built

**W5 of `v0.6.4-scope.md`: the sorted pass's memory, and the messages.**

## 1. Evidence

A window with no partition_by that ranks by average, interpolates and sums in an
order of its own, 20,000 rows under a tenth of the input (81 KB), peaked at **6.7×
its limit** with 157 spill files.

The pass's sorts spill when the whole query is over its limit. While the forward
merge holds a batch of every run, the back sort sees the query over at nearly every
batch, and spills each as a run of its own. Its final merge then holds a batch of
each of those runs.

## 2. What changed

**`internal/physical/sort.go`:**

- **A cascade.** Every `sortFanIn` (16) runs of one level at the end of the run list
  are merged into one run of the next (`cascade`, `mergeToRun`). They are contiguous
  in input order, and the merge breaks ties by run, so the sort stays stable. The
  final merge holds a batch of fewer than 16 runs a level.
- **A minimum run** (`minRun`): a sink given one spills only once it holds that
  much, whatever others hold. A plain sort has none, and spills whenever the query
  is over, as before.
- Run files are named by a counter, since merged runs replace the runs they came
  from. `openRuns` and `merger` are factored out of Finish, which uses them too.

**`internal/physical/winsorted.go`:** the pass's two sorts take a minimum run of a
sixteenth of the limit.

**Measured,** the same query at 64-row batches:

| | peak | spill files |
| --- | --: | --: |
| neither | 4.83× the limit | 389 |
| the cascade only | 1.23× | 389 |
| the minimum run only | 1.75× | 135 |
| both | **1.23×** | **135** |

**The bound depends on batch size.** A merge holds one batch of each of up to 16
runs a level, so the bound holds for a limit well above 16 batches. At 500-row
batches and an 81 KB limit the peak is 4×. At 8,192-row batches of a few narrow
columns, 16 batches are a few megabytes.

**Not done:** the stream walker has no account of its own. The peak above does not
need one, and its lookahead and history are bounded by the functions' needs.

**The messages:**

- **`execopt`'s Check hint** names what still refuses: a rolling window by time over
  one partition larger than the limit, and n_unique and implode over one group
  larger than it.
- **Its join hint** no longer names a keyless join with a residual, which spills
  since step 219.
- **Its over hint** says functions in an order of their own are sorted on disk.
- **`fill.go`** no longer says `FillMin`/`FillMax`/`FillMean` refuse.

**Order keys:** struct and list order keys, and an unorderable rank child, are
already refused when planned (`IsOrdered`). Every orderable type has a merge key
that mirrors the in-memory comparator (`newCrossKey`), so nothing was needed.

## 3. Tests and teeth

- **`TestASortedPassStaysNearItsLimit`:**
  - the query above peaks within twice its limit;
  - it writes more files than the fan-in, but no more than 250;
  - it leaves the spill directory empty after an early break.
- **`TestASortMergesItsRunsAsTheyGather`** (internal):
  - 276 one-batch runs;
  - fewer than 16 runs a level, reaching level 2;
  - as many files as runs;
  - the stable in-memory order, ties in input order.
- **`TestASortWithAMinimumRunWaitsForIt`** (internal): with the query over its limit
  because of another account, the sort spills only when a batch brings it to its
  minimum.
- **`TestSpillingByHand`**'s hint case looks for the new words.

| tooth | result |
| --- | --- |
| no cascade | **bites:** the cascade test (the pass test alone stayed under 2×, at 1.75×) |
| a cascade that loses the runs' order | **bites:** `TestExternalSortMatchesInMemory` |
| the minimum run ignored | **bites:** the minimum-run test |
| a merged level not put up a level | **bites:** the cascade test's level 2 |
| merged runs left on disk | **bites:** its file count, added for it |
| the pass sets no minimum run | **bites:** the pass test's file count, added for it |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
