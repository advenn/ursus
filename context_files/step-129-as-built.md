# Step 129 — as built

**A test that fails when a query holds memory its budget does not count.** No
benchmark was run, by request: this is a test of a few million rows that runs in
seconds, in every gate.

## 1. Why

Step 128 found that a join killed under a 4 GB container had held state the budget
never counted:

- its build side, alive while `Collect` assembled the result;
- the result held twice while it was concatenated;
- the build side held twice when the join froze it.

Nothing in the suite measured the live heap against the budget, so every step since
the budget existed could add such state unnoticed. Step 126's per-row ids were 5% of
peak memory, and only a benchmark showed it.

## 2. The test: `internal/memcheck`

It is its own package, with a one-line `doc.go`, so it runs alone in its own
process: the heap is the process's, and the root package runs its tests in
parallel. It is registered at level 80 in the levels check, beside `ursustest`.

**Two readings of the live heap,** above where it stood before the query:

- **Sampled every 200µs** with GOGC at 10. That follows an operator's state, which
  lasts. A sample can also count the floating garbage a collection allocates while
  it marks: the same `Collect` read 145 MB in one run and 111 in the next. The
  bounds on this reading are wide.
- **Exact, at every column a concatenation copies.** `kernel.ConcatColumnDone`, a
  hook that is nil outside this test, runs a full collection there and reads the
  result. A copy takes milliseconds, too fast for any sample to land in, and it is
  where a doubled result or a doubled build side is largest. These readings repeat to
  a tenth of a megabyte.

**The cases,** at two million rows:

| case | reading | bound | measured |
| --- | --- | --- | --- |
| a join | sampled | 1.3 × what it charges + 16 MB | 192–202 MB against 185 counted |
| the same join, as it freezes its build side | exact | what it charges + 24 MB | 186.7 MB |
| a group-by | sampled | 1.3 × what it charges + 16 MB | 145 MB against 122 counted |
| a join under a 16 MB budget | sampled | must spill; 5 × the budget + 16 MB | 13–42 MB, 30 spill files |
| `Collect` of a 1.5-million-row result | exact | 1.3 × the result + 8 MB | 96.6 MB for 84 MB |
| `Collect` of a join with a 128 MB result | exact | 1.2 × the result + 12 MB | 142.7 MB |

## 3. What the test got wrong first, and how each was found

**Each version was checked against its teeth; three versions passed every tooth
silently.**

1. **Samples alone.** All three concatenation teeth were silent: a copy happens
   between collections. Hence the exact readings.
2. **Inputs freed mid-query.** With the hook, the teeth were still silent. The exact
   reading at the start of the old concatenation was 16 MB *below* the baseline,
   though it held 84 MB of batches.

   Go frees a variable once nothing will read it again, so the test's input frame,
   part of the baseline, was collected once its scan closed: every reading after
   that was low by about 114 MB. `liveDuring` now keeps the inputs reachable until it
   returns.
3. **A build side of views.** The freeze tooth stayed silent. An in-memory frame's
   batches are views of its own buffers, so a join built from one retains nothing
   new, and a doubled concatenation of views costs nothing. The joins now build from
   a filter that keeps 99.9% of rows, which copies them, as a file's scan does. The
   case that was killed read CSV.

## 4. Teeth

| tooth | result |
| --- | --- |
| `Collect` concatenates with `Concat`, holding every batch | **bites:** both `Collect` cases |
| `Collect` keeps the tree open while it concatenates | **bites:** the join's |
| `ConcatOwned` lets go of nothing | **bites:** both `Collect` cases |
| a join's freeze concatenates with `Concat` | **bites** |

Not caught, by design: a few percent of uncounted state. Leaving a join's CSR
arrays uncharged, 16 MB of 185, moves no bound. The test is for a second copy of
something, which moves a ratio by most of a whole.

**It passes under `-race`,** in 30 s, and at the 512, 128 and 0 vector widths, in
about 2.6 s each.

**Gate:** test-all 115 ok (the new package adds one per leg, five; it passed in every leg in about 4 s, beside the other packages), race 23 ok (memcheck 38 s), levels, vet ×3 and the bench engine tests clean. No PDS-H run, by request.
