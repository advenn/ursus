# Step 410 — as built

**The 0.7.2 scope: finding the bugs, and fixing them.** No code changed.

[`v0.7.2-scope.md`](./v0.7.2-scope.md) takes the theme the maintainer set on
2026-10-31, a patch of 0.7 given to bugs alone, once they had stopped the move to 0.8
the day before. It is the first step made by a branch and a pull request, into
`release-0.7`, with nothing pushed to a release or to master.

## 1. The survey

On master's tree at step 409, which holds 0.7's code and 0.8's:

- **Read,** by three agents, each reporting back:
  - every as-built record, the CHANGELOG's fixes and limitations, `feature-gaps.md`,
    `polars-parity.md` and `audit.md`, for the bugs found, fixed and left;
  - the tests, for what they check against;
  - the code, for where bugs most likely are.
- **Measured:** the suite with `-coverpkg=./...`, one package at a time on four
  procs at v3. 89.9% of statements run, 4,767 never: `internal/kernel` 1,086 of
  12,736, `internal/physical` 996 of 8,827, the root 768 of 7,145, `internal/plan` 435
  of 3,549, Parquet 346 of 2,881, `internal/expr` 318 of 3,261. A package's own tests
  alone run 20.9% of `internal/expr` and 33.2% of `internal/plan`, the root's tests
  the rest.
- **Run:** a probe of NDJSON nested 2,000,000 levels deep, with
  `debug.SetMaxStack(64 << 20)`, on master's code and then on `release-0.7`'s: on
  each, `fatal error: stack overflow`, the process ended, `Collect` never returned.
- **Not checked by running:** the scope's suspected list, which H2 probes.

## 2. The silent teeth that are real gaps

Of about 2,650 teeth recorded, about 135 were silent. Most are equivalences,
defensive second checks or speed-only, each so labelled in its record. These are
not, and H3 takes them:

- **394:** a closed lane read as the end under a cancel, caught only by the drivers'
  check;
- **395:** a group-by frozen at its last depth under pressure, and a keyless join
  with a residual, which no test reaches (0.8's code: for 0.8's hunt);
- **334:** an Eisel-Lemire table entry one bit wrong, which none of 1.2 million
  inputs finds;
- **284:** an insert's fused multiply-add;
- **282:** the Reshape type's nullability, which no test can see;
- **131:** accumulators kept until the answer is built; a median's and an implode's
  memory unmeasured;
- **72:** fifteen recovery and guard paths recorded rather than tested, the Parquet
  loop's context check, the sticky reader error and the rank and quantile kernels'
  defaults among them;
- **50:** a UDF's non-null output check;
- **68:** the Time plus calendar refusal, untested;
- **62:** the WinCumSum gate;
- **147, 148, 193, 337, 341, 351, 386:** guards behind the planner, and type flags
  nothing reads.

## 3. The workflow it starts

- `release-0.7` takes 0.7.2's work by pull requests, CI on each; its first
  `ci.yml` change, at 0.7.1, runs CI on `release-*` pushes too.
- Master takes each fix by a pull request of its own.
- Nothing is pushed to `release-0.7` or to master directly.

**Open for the maintainer:** protecting the two branches, the nightly runs on GitHub
Actions, and a person's review (the scope's §7).
