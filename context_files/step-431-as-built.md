# Step 431 — as built

**`TestTemporalMediansSpillTheirValues` no longer samples the heap; the heap is read
exactly in `internal/memcheck`.** The plan's 436, M2 of `v0.7.2-plan.md` (H3e).

## 1. What was wrong

Step 244's test of a temporal median's value spill also bounded the live heap. It
sampled `/gc/heap/live:bytes` every millisecond while the query ran, and required
growth under 10 MB (`rollingmem_test.go`). Two things were wrong with the bound:

- **It had no teeth.** H3e read the spilled run at 3.8 MB and a run with the values
  held at 7.8 MB, and 10 MB passed both.
- **It flaked.** A sample counts the floating garbage a collection allocates while it
  marks, and on a loaded machine that crossed 10 MB.

## 2. The change

- **The root test keeps what is deterministic:**
  - the answer equals the run without a limit;
  - it spills;
  - the ledger peaks at most twice the limit;
  - the spill directory is empty after.

  The sampler stays for the two tests whose bounds sit far from both readings: the
  expansion's, and the refusal's.
- **`physical.TemporalSegmentFinishing`** is a nil-checked hook in `segBatch`
  (`internal/physical/tempsegment.go`). It runs as a segment of windows is about to
  finish its aggregates, when every open segment holds its windows' values, in memory
  or spilled. It follows `kernel.ConcatColumnDone`'s pattern, and nothing but
  memcheck sets it.
- **`TestTemporalMediansHoldTheirValuesOnDisk`** (`internal/memcheck`) runs a rolling
  median over 2,000 rows a minute apart, with windows reaching back a week: two million
  values, 16 MB. It runs in batches of 256 on one thread, once without a limit and once
  under 2 MB. At the hook it collects twice and reads the live heap above where it
  stood.
  - Three runs read 7.7 MB without a limit and 1.7 MB under 2 MB, every time.
  - The bound is that the spilled reading is under half the held one, so the test
    measures its own scale and needs no number that drifts with the machine.
  - A reading under 4 MB without a limit fails as a reading taken in the wrong place.
- **Not done:** the plan's bounds "measured on CI". The reading is deterministic, and
  the bound is a ratio of two readings in one process, so there is no machine
  constant to measure.

## 3. Teeth

| mutation | |
| --- | --- |
| an uncharged copy of the spilled values kept | **bites:** 16.0 MB spilling against 7.7 |
| the hook never called | **bites:** 0.0 MB without a limit, the guard |
| a spill keeping its values' room | **bites,** by the ledger: refused under 2 MB |
| a spill keeping its values | **bites,** by the ledger: refused under 2 MB |

The first is the class of bug the reading is for: memory the ledger cannot see. The
last two are caught by the ledger first, since it charges a slice's capacity.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.

The public API is unchanged.
