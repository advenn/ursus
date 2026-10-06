# Step 91 — as built

**A group-by stays parallel under a budget, and goes serial when the budget bites.**
Step 90's default budget made every group-by serial: `aggWorkers` declined to
parallelise under any limit, because a sink that has frozen cannot be merged.

Three fixes and tests, and this document.

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

**The suite is 2967 passing tests, subtests and examples** (2944 at step 88).

---

## 1. The design

**Parallel until half the budget, then serial.**

1. **The workers never freeze.** A worker whose query holds half the budget returns
   `errWantSerial` after aggregating its batch, so nothing is lost.
2. **The driver stops dispatching.** It lets each worker drain what is already
   queued, then folds the workers together. That is legal, because none has
   frozen.
3. **The merged sink finishes the input alone.** It freezes, spills and refuses
   exactly as the serial path always did.

A group-by that fits in half the budget never leaves the parallel path.

## 2. Found on the way

**A time-of-check race.** A worker read "not over", and before it reached its own
`Check`, another worker pushed the shared total over, so the first raised a hard
budget error. The race detector caught it. A parallel worker now never raises the
budget's error; it asks for serial instead.

**The fold doubled memory.** The first version switched at the full budget. The fold
builds the merged table while every worker's table still exists, and gb10 over CSV,
with ten million groups, was OOM-killed again. Two changes fixed it:

- switching at half the budget;
- dropping each worker's table as soon as it has been folded in.

**`Merge` kept the merged-in sink's account** until the query ended, so the budget
counted the folded state twice. It now releases it.

## 3. Tests and teeth

**Tests:**

- `aggWorkers` gives every worker under a budget;
- a worker switches at half the budget;
- a folded worker holds no table;
- the merged-in account is released;
- the parallel test now forces the switch, and requires that it spilled.

**Test changes:**

- **Pinned to one thread**, because that path is their subject and with more
  workers the parallel phase can consume all of a small input before the switch:
  - four spill tests;
  - the per-value refusal case.
- **Compared with a float tolerance:** the parallel test, since workers add in a
  different order. The other parallel tests already do this.

**Teeth: 9 of 9 bite.** The merged-account tooth was silent until the contract test
was added. The other eight:

- serial under any limit;
- workers not marked parallel;
- the dispatcher ignoring the switch;
- the merged sink never told it is alone;
- the rest of the input dropped after the switch;
- switching at the full budget;
- folded workers keeping their tables.

## 4. Measured

ursus only, under the benchmark's 8 GB cgroup:

| | v0.3.0 report | after step 90 | after step 91 |
| --- | --: | --: | --: |
| h2o, Parquet geomean | 2,426 ms | 2,496 ms | 1,950 ms |
| h2o, CSV | 14/15, gb10 killed | 15/15 | 15/15, gb10 9.8 s / 7.6 GB |
| gb3 / gb7 over CSV | 2.9 / 2.6 s | 4.2 / 3.9 s | 2.9 / 2.6 s |
| PDS-H SF=1 geomean | 918 ms | 716 ms | 750 ms |

Every answer validates.

**gb10 peaked at 7.6 GB under an 8 GB cap.** That is about 1.9× its 4 GB budget,
which is the Go heap's slack. `GOMEMLIMIT` tightens it, and the docs say so.

## 5. Still open

- **A parallel group-by can exceed its budget by its queued batches** before it
  switches: a few megabytes at the default batch size, documented on
  `WithMemoryLimit`.
- **v0.3.0's committed report** still shows the two findings. The fixes are on
  master, for a v0.3.1.
