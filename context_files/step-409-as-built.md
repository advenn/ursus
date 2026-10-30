# Step 409 — as built

**0.7.1: the fixes of steps 393 and 394 on the 0.7 line.** A patch, on the branch
`release-0.7`, after v0.7.0 (step 408).

## 1. What

**Master's three fixes since 0.7's code,** each a wrong answer or a process ended:

- a cancelled query answered part of its input as the whole, and no error (394);
- a cancelled sort or join stopped 100 to 400 ms after its cancel (394);
- a panic on one of a query's own goroutines ended the process (393).

**Backported as they are.** Each commit's code, tests and as-built record, its diff
applied to `release-0.7`. Master made both on `44c7818`, whose code is `16e617a`'s,
0.7's, so every file applied exactly. Left out: master's bookkeeping (the CHANGELOG,
the index, `v0.8-scope.md`) and step 394's change to the 0.8 service probe,
`bench/micro/svcprobe`, which the 0.7 line does not have.

**No public API changes:** `uerr.Relay` is internal, and `ToDummies`, `Corr` and
`PartitionBy` only name their results.

**Left on master:** 0.8's two breaks, a shared budget and shared threads; its
features; step 400's performance work.

**CI:** `ci.yml` ran on master and main alone. The release branch adds `release-*`,
so the backport has CI's every width, the scalar build and the race detector.

## 2. Checked

- **The light gate in the release tree:** the tests at v3, 28 packages ok; levels,
  vet ×3 and the bench engine tests clean.
- **The answers:** PDS-H's 22 at SF=0.1, from the release tree's runner, match
  DuckDB's.
- **No slower:** against v0.7.0's runner, both built alike, alternated on four
  threads at a load of 5 to 8:
  - SF=0.1, four rounds of ten: 450.6 ms summed against 451.2, 1.00;
  - SF=1, two rounds of five: 1.02. Its queries above 1.05 run again over four
    rounds: q10 0.97, q14 1.00, q17 0.98 and q1 1.05, the controls q6 1.01 and q12
    0.98; q1 over six rounds 0.87, its control q6 1.10;
  - level, within noise, as on master, where step 394 measured its change at 0.99.
- **The tests are master's,** whose teeth bit at steps 393 and 394, over the same
  code byte for byte.

## 3. The tag

v0.7.1 is this branch's head once CI passes, and its push publishes 0.7.1's
documentation as `latest`.
