# Step 434 — as built

**Teeth for the closed lane under a cancel, in the parallel operator and the parallel
probe.** Part of the plan's 439, M2 of `v0.7.2-plan.md` (H3f2). Tests only. Of the
plan's four items, this is the one with a wrong answer behind it; §2 says why the
other three are left.

## 1. The tests

`internal/physical/closedlane_internal_test.go`:

- **`TestAClosedLaneUnderACancelIsTheCancel`** (step 394). The parallel operator
  reads its lanes round-robin, and a closed lane means the end of the input, since
  dispatch is monotone. A cancel closes the lanes too, and step 394 found `Next`
  reading that as the end, so a cancelled query answered part of its input as the
  whole. The fix checks the context when a lane closes. The drivers' own check behind
  it kept the wrong answer from a caller, so this tooth was silent.
  - The test calls `parallelOp.Next` directly, with a source that waits for the query's
    context and then ends rather than failing.
  - It cancels while `Next` waits, and requires the cancel. It runs 200 times.
- **`TestAClosedProbeLaneUnderACancelIsTheCancel`:** the parallel probe's lanes
  (`parjoin.go`), the same way. It checks that the fixture's probe is `parProbeOp`.

**Teeth: 2 of 2 bite.** Each check removed answers `io.EOF` on the first run.

## 2. Left: three items of the plan's 439

- **The Parquet loop's context check between row groups** (step 72). The loop's other
  calls, opening the next row group among them, take the context too. A check removed
  is then answered by the next one, an equivalence. A test that tells them apart needs
  a run of row groups whose batches a dictionary filter empties, cancelled between
  two of them.
- **The sticky reader error in CSV, NDJSON and Parquet** (step 72):
  - Parquet's stored error after a panic, removed, is likely answered by the next
    decode panicking again: an equivalence.
  - CSV's `failed` exists only on its parallel split path, whose test needs a parallel
    read with an I/O failure mid-stream.
  - Every driver stops pulling after an error, which is why each was silent.
- **A pushed predicate's evaluation error, and filtering under `MaxRows`, at the
  source** (step 193). These are guards behind the planner, which never hands a scan
  a fallible conjunct, or a limit below a filter. They need a Parquet scan built by
  hand with a pushed predicate.

Each is a defence behind another check, and none has a wrong answer known behind it.

**Gate:**
- `./internal/...` in full, and vet;
- no code the root package runs changed;
- on the pull request, CI's every width, the scalar build and the race detector.
