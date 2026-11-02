# Step 411 — as built

**The race job's headroom.** H0 of `v0.7.2-scope.md`, first, so that 0.7.2's tests
do not time it out.

- **What was wrong:** CI's race job runs the whole suite under `-race -timeout 20m`.
  At v0.7.1 its root package took 1,049 to 1,072 s of the 1,200 (runs 41034082929,
  41048854859 and PR #1's 41213621323), and 0.7.2 adds its tests mostly to the root
  package. The job timed out unnoticed once before, at steps 237 to 243.
- **The change:** `-timeout 40m`, in `ci.yml` and the Makefile's `race` target. The
  job's own limit is GitHub's six hours. The job keeps its name, so a required check
  named `race` holds.
- **Not done:** splitting the root package from the rest, which renames the check.

**Gate:** CI on the pull request; no Go code changed.
