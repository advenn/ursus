# Step 537 — as built

**Random queries run every night, unattended.** A CI workflow only. The maintainer
approved it on 2026-11-20: move bug-finding onto CI and off the laptop.

## Why

The bug hunt on master (steps 507–534) ran its finders on the laptop through
parallel agents: costly in the maintainer's usage, and twice it nearly filled the
disk. ci.yml's qcheck jobs run five minutes on each pull request. The finders that
found the subtlest bugs need hours, not minutes: a result that changed with the
thread count (step 523), memory that grew with the square of the rows (step 512).
Fuzzing already runs nightly (`fuzz.yml`, step 508). This adds the same for qcheck.

## What it is

`.github/workflows/qcheck-nightly.yml`, at 03:41 UTC each night, by hand
(`workflow_dispatch`, `duration` input), and for two minutes on a pull request that
edits it:

| job | finder | length | built |
| --- | --- | --- | --- |
| self | `qcheck self`: the queries against themselves across threads, batch sizes, the optimizer and records (step 512) | 45 min | pure Go |
| self-race | the same, with the race detector, which ursus's parallel operators answer to (plan step 474) | 30 min | `-race`, cgo |
| duck | `qcheck duck`: the queries against DuckDB 1.5.5 (step 513) | 45 min | cgo, the duckdb tags, ci.yml's cache key |

- Each job starts from a seed set by the run's id, printed first, so a finding runs
  again with `qcheck <self|duck> -seed <seed> -cases 1`.
- `-shrink` shrinks at most three findings a job (step 514). The cases, their Go
  tests and DuckDB's SQL are uploaded as `qcheck-nightly-*-findings`.
- GitHub mails the repository's owner when a scheduled run fails. A finding then
  needs a session only to be fixed: shrink, add it to `qcheck/testdata/corpus`
  marked open with its step, fix it, and unmark it.
- Not a required check.

## Checks

The workflow's YAML parses: three jobs, the schedule, the dispatch input and the
pull-request trigger. Its own pull-request run, two minutes a job, is the check that
each job builds and runs.
