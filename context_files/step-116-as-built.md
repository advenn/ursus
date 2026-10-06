# Step 116 — as built

**The 0.4 release candidate.** No Go changed. This step records where 0.4 stands,
so the tag is the maintainer's single remaining act.

## 1. What changed

- **`v0.4-scope.md` §7:** every tier 1 and tier 2 item, done, partly done, or moved
  to 0.5, each with its steps or its reason. It also holds the measurements the
  steps took, and the release claim with the step behind each clause.
- **`CHANGELOG.md`:** the Unreleased list became **v0.4.0 — candidate**, in v0.3.0's
  shape:
  - highlights;
  - the behaviour changes a v0.3 user can trip on;
  - the API added;
  - everything, by step;
  - the known limitations.

  The list itself is kept as it was, under "everything, by step".
- **`README.md`:**
  - v0.4, and the scope's §7 linked;
  - the expressions and execution rows now include what 0.4 added;
  - the not-done list drops the trigonometric block and names what moved to 0.5;
  - the benchmark section no longer says v0.3.1's fixes are unreleased;
  - it says plainly that the report is v0.3.0's, and that 0.4's speed-ups were
    measured on single queries;
  - the test-case count is updated: 3240 pass, counted from one scalar-only run with
    `go test -json`, against the 2969 it last stated.

## 2. Checked before writing

- **The public API since v0.3.1 only grew.** I diffed the exported declarations of
  every non-internal package: additions in the root package and `i128`, nothing
  removed or changed in its parameters. `dtype` is unchanged.
- **Each limitation still holds:**
  - Enum and Duration are still refused by the Parquet writer's `toNode`;
  - the operators that cannot spill are as listed;
  - the temporal statistics are refused, which step 115 documented.
- **Each number** in the changelog and the scope is from an as-built's measurement
  section.

## 3. Left to the maintainer

- **Tag v0.4.0.** Also tag v0.3.2 at step 92 if a 0.3 patch is still wanted;
  0.4.0 contains it.
- **Run the full benchmark report** for 0.4. It is too heavy for this machine to run
  step by step. §5's target, PDS-H SF=1 within 4× of Polars by geomean, is decided
  by it.
- **Then date the changelog's heading,** and replace the README's table with the new
  report's.

**Gate:** step 115's run covers this step, which changed no Go: test-all 110 ok,
race, levels, vet ×3, the bench engine tests, and PDS-H SF=0.1 22/22 against
DuckDB.
