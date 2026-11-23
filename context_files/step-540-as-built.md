# Step 540 — as built

**No full-size benchmark on GitHub's runners, and a superseded pull-request run
cancelled.** CI workflows only. The maintainer asked for both on 2026-11-23, after
v0.8.0 was tagged: "we should not run full benchmark in github".

## Why

Nothing ran a benchmark on its own: `bench.yml` is dispatched by hand only. It had
been dispatched 22 times, 17 of them on 2026-11-20 for v0.8.0's report (step 536):
h2o at 1e7 rows over Parquet and CSV, PDS-H at SF=1 and 0.1, and the service probe.
Its h2o default was 1e7, so any dispatch of that suite ran it at full size.

`ci.yml` had no `concurrency`: two pushes to a pull request ran two full runs, the
race job's twenty minutes each, the first for a commit nobody would merge.
`oracle.yml` had none either. `fuzz.yml` already cancelled its superseded runs.

## What changed

| file | change |
| --- | --- |
| `bench.yml` | The `correctness` job refuses, right after the checkout, a scale above 0.1 or rows above 1e6, or either one not a number. `rows` defaults to 1e6, not 1e7. Its timeout is 30 minutes, not 150. The header says so and that v0.8.0's report was the last one to run full sizes here. The `probe` job is unchanged: its sizes are svcprobe's own, small queries. |
| `ci.yml` | `concurrency`: a pull request's runs are one group, by its number, and a newer push cancels the older run. A push to master is grouped by its commit, so it is never cancelled, and every merge is still checked in full. |
| `oracle.yml` | The same for its pull-request runs. A run by hand is grouped by its run id, so it is never cancelled. |

Where a later report's full-size suites run is the maintainer's to say: not on
GitHub, and not on the laptop as routine.

## Checks

- The three workflows parse as YAML (PyYAML): `ci.yml`'s and `oracle.yml`'s
  `concurrency` blocks, and `bench.yml`'s check as the correctness job's second step.
- The size check, run under mawk and the system awk with the workflow's expression:
  - accepted: 0.1 and 1e6; 0.01 and 1000; 0.1 and 1000000; 0.05 and 999999.
  - refused: 0.1 and 1e7; 1 and 1e6; abc and 1e6; 0.1 and 2e6.
- This pull request's own `ci` run is the check that `ci.yml` still runs.
