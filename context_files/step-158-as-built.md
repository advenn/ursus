# Step 158 — as built

**The first of step 157's re-ranked items: a probe row that misses does no work.**

## 1. Evidence first

Step 157's profile of q17 on v0.5.0 found `enter` (`internal/physical/join.go`)
treating every miss as a key that might have spilled:

- it encoded the row's key again, `Encode`, 0.68 s of 8.09 s;
- it hashed it byte by byte, `HashKey`, 0.64 s, to find its spill bucket;
- it asked whether that bucket had a build file.

**The join had not spilled,** so no bucket had one. The bytes were also needed for
`Validate`'s left-uniqueness check, which q17 does not ask for. Nearly all of q17's
lineitem rows miss its 200 parts, so this was about 16% of its CPU.

## 2. What changed

- **The probe operator records `routes`** when it is built: whether any bucket has a
  build file (`joinBuildSink.spilled`). A frozen sink's files are closed before the
  probe starts, so this cannot change during the probe.
- **`enter` returns at once for a miss** when nothing routes and nothing validates:
  the row matches nothing. A Left, Full or Anti join emits it unmatched, as before,
  since `routed` stays false.
- **A spilled join and a validated one** encode the miss as before.

## 3. Measured

The v0.5.0 runner against this one, alternated, three rounds of five iterations at
SF=1. Each run's CPU time per iteration comes from its process's rusage, with the
warm-up counted:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q17 | 2,735 → 2,220 ms (−19%) | 447 → 371 ms |
| q16 | 476 → 418 ms (−12%) | 135 → 127 ms |

**Wall clocks were too noisy this session to read alone:** q17's base read 320, 462
and 499 ms in three rounds. CPU time measures the work that went, and is how 0.6's
steps are timed from here when the wall clock wanders.

## 4. Teeth

| tooth | result |
| --- | --- |
| a spilled build's misses not routed | **bites:** the spilled outer, full and anti joins, and `TestMemoryLimitIsNotASemanticKnob` |
| a validated join's misses not seen | **bites:** `TestJoinKeysByHand`'s two 1:m cases with unmatched duplicate left keys |

The speed itself has no tooth: removing the shortcut changes no answer.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
