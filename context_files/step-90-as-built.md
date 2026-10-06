# Step 90 — as built

**A query is budgeted by default, so it spills under a container's limit.** This
was found by benchmarking v0.3.0. ursus's h2o gb10 over CSV was OOM-killed under
the benchmark's 8 GB cgroup. gb10 is a group-by that spills fine when given a
budget.

One fix, one style commit, and this document.

## 1. What was found

- **Spilling is opt-in.** It is decided by the budget, and without
  `WithMemoryLimit` there was no budget.
- **A cgroup's limit is invisible to ursus.** The kernel enforces it from outside.
- **So ursus ran until the kernel killed it,** in exactly the "falling over" the
  README says ursus avoids. Every container with a memory limit had this exposure.
- **Not a memory regression.** gb10's peak over Parquet fell from 8.75 GB at step
  40 to 7.61 GB. Step 40's h2o runs simply had no cap.
- DuckDB defaults its limit to 80% of RAM, for this reason.

## 2. The fix

With no `WithMemoryLimit`, the budget is **half** the smaller of two ceilings, on
Linux:

- **the cgroup's memory limit:** the least over the process's cgroup and every
  ancestor, since systemd or a container runtime may set it on any of them. This
  reads cgroup v2's `memory.max` and v1's `memory.limit_in_bytes`, whose
  "unlimited" is a number near 2^63.
- **the machine's RAM:** `MemTotal` from `/proc/meminfo`.

**Why half, not 80%:** ursus's buffers are on the Go heap, which with `GOGC=100`
grows to about twice what is live before collecting. The doc names `GOMEMLIMIT`
for anyone who wants it tighter.

`WithMemoryLimit(0)` is unlimited, the old default. Off Linux, nothing is
readable, so nothing changes. Detection runs once per process.

**Tests:**

- **Detection, over fixture files:** cgroup v2 and v1; `max`; the smallest limit
  up the ancestors; a cgroup limit above RAM; a container's root cgroup; and
  nothing readable.
- **The public contract:** no option gives the default, an explicit 0 gives
  unlimited, and n gives n.

**Teeth: 4 of 4 bite.** Ancestors not walked; the whole ceiling instead of half; no
default applied; an explicit 0 replaced by the default.

## 3. What it cost, and the next step

Measured right after this step, gb10 over CSV completed under the 8 GB cap: 8.7 s,
6.19 GB. But h2o's small group-bys got about 1.5× slower: gb3 from 1.9 s to 2.8 s,
gb7 from 1.3 s to 2.4 s.

The cause: a group-by ran serial under any memory limit, and now every query has
one. Step 91 fixes that.
