# Step 185 — as built

**R5 of `v0.6-scope.md`: the memory test gaps steps 131, 135 and 138 name.** Each is
built where it can be made to bite within a step, and otherwise closed for good, with
its reason.

## 1. Measured first

A probe in `internal/memcheck`, through an overlay, read the live heap as its tests
do: sampled, and exactly at every concatenation.

| query, 2,097,152 rows | sampled | exact | charged |
| --- | --: | --: | --: |
| group-by of 2M keys, one thread | 94.5 MB | 34.8 MB | 82.0 MB |
| the same, eight threads, under a limit | 94.5 | 34.8 | 82.0 |
| the same, eight threads, **default budget** | 185.0 to 188.3 | 185.0 to 188.3 | 168.8 |
| join, whole: at its freeze | 194.9 | 179.5 | 178.2 |
| join under 16 MB: at its replay | 19.5 | 15.2 | 16.3 |
| join under 64 MB: at its replay | 68.7 | 15.2 | 64.0 |

**Every memcheck case set a limit,** and under a limit the caller set a group-by runs
serially (step 92). So **the parallel fold step 131 left unmeasured was never
reached** by a test. Under the default budget it is: it charges 168.8 MB and holds
185 MB at most, about 1.1 times the charge.

## 2. Built

- **"A parallel group-by holds what it charges":** eight threads, batches of 64K rows,
  under the default budget. Both readings must stay within 1.3 times the charge plus
  16 MB, the bound the other cases use.
  - **bites:** the workers' accounts released before the fold reads them;
  - **silent, and why:** the fold keeping its workers after merging, or leaving its
    partitions' answers uncharged. The fold's own peak, every worker and every
    partition at once, is the largest moment either way, and both faults add less
    than the 1.3 allows.
- **The join's freeze, bounded by its build side as well as by its charge:** at most
  2.6 times the build side's 80 MB.
  - Step 135 found that a freeze that held the build side twice and counted it twice
    passes a bound relative to the charge, and left the tooth silent.
  - Held once, the freeze reads 2.24 times the build side; held and counted twice,
    3.24. The new bound bites it: "freezing the build side held 259 MB, 3.24 times
    its 80 MB". The ratio bound beside it stays silent, as step 135 found.

## 3. Closed, with their reasons

- **Step 131: the serial fold's triple.** `hashAggSink.Merge` concatenates a worker's
  keys, holding its parts, the concatenation and the keys it takes. Since step 160 it
  runs only:
  - for fewer than 65,536 groups, where the triple is a few megabytes;
  - or when the partitioned fold is refused because the default budget cannot hold
    both, which needs half the machine's memory.

  No affordable test reaches the second, and the first is too small to matter.
- **Step 135: the resident table kept through the replay.** The replay reads 15.2 MB,
  exactly, under a 16 MB budget and under 64 MB alike. Only one bucket's sub-join is
  live at a time, and the resident part is bucket 0, one of sixteen, under 5 MB of an
  80 MB build side. Kept, it adds less than the sub-joins' own copy and table vary,
  so no bound can see it without failing a healthy run. A budget large enough to
  keep much resident spills nothing at this size.
- **Step 138:** its one test gap, the List reader's batch reuse, it closed itself
  with `TestAListColumnsFirstBatchSurvivesTheRest`. What it left unmeasured, `j5`
  at ten million rows, is the final report's: `gb10` and `j5` under 3 GB.

## 4. Found by the gate: a flaky allocation test

The first gate failed in `internal/source/csv`, on
`TestReadingAllocatesAboutWhatItProduces`, which this step had not touched: "4
threads: allocated 14.3 MB to produce 9.8 MB: 1.47", against a bound of 1.25. Rerun,
it failed once in three.

- **The cause:** it began its reading after the first batch, since a reader grows
  its staging and builders once. On four threads each worker grows its own, and the
  reader parses ahead, so how much of the other workers' growth fell inside the
  reading was the scheduler's.
- **Read from each worker's second batch:** 0.06 to 1.11 over sixteen runs. That is
  still the scheduler's, since read-ahead can land before the reading begins.
- **So four threads take a looser bound, 1.5,** and the serial case, steady at 1.13,
  keeps 1.25 and is the one that catches the fault the test exists for, each value
  allocated about six times.
- **The whole read is no measure either:** each worker's staging, about 8 MB, is a
  fixed cost that 200,000 rows do not amortize, 43 MB to produce 10.7.

**Gate,** run twice: the first failed on the CSV allocation test of §4; with it fixed,
test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests clean. **PDS-H at
SF=0.1:** all 22 answers match DuckDB's.
