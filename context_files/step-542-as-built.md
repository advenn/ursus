# Step 542 — a Datetime casts to its Time at any instant; the optimizer's rule for errors documented

**A fix and a documented rule, in v0.8.1, before its tag.** The maintainer decided
both on 2026-11-23, after qcheck's wide generator (step 539) found the case in
#96's five-minute `qcheck self` run (seed 874020098).

## The case

The finding was shrunk to one row: a Datetime(ms) of 18014398509481983, about the
year 570,000, cast to Time(us), with every row then filtered away
(`Filter(Null(Bool))`).

- With the optimizer on, the filter moved below the cast and dropped the row first,
  so the query answered with no rows.
- With the optimizer off, the plan as written cast the row and refused it: "instant
  18014398509481983 at row 0 overflows Time(us)".

## The cast, fixed

`kernel.Cast` retimed an instant to the Time's unit (`rescaleTemporal`) and then
folded it into its day (`normaliseTime`). Widening the whole instant overflowed
int64 past a limit: a Datetime(ms) past about the year 294,000 as microseconds, and
a Datetime(s) past 2262 as nanoseconds. The cast refused such an instant under
strict and nulled it under lossy, though its time of day exists.

It now folds the instant into its day in its own unit first, then retimes the
folded tick:
- A day is a whole number of ticks of every unit, so the answer is the same wherever
  retiming first gave one.
- A folded tick widens to under a day of nanoseconds, so the cast never overflows.
- The day is the UTC wall clock's, a zoned instant's too, as it was.

**Test.** `internal/kernel/casttimeofday_test.go` checks every pair of units, naive
and zoned, strict and lossy: 14,016 instants in all, at int64's ends, each
widening's limit and one past it, and random ones. Its oracle is math/big. Without
the fix it fails 2,504 of them.

## The rule, documented

The maintainer's choice: a value the query discards may not be computed, so its
error may not be raised. A filter moved below a `Select` drops rows before a cast or
an operation reads them, and Polars' optimizer behaves alike. The other way round,
an optimized query failing where the plan as written answers, is a bug.

It is written in:
- `docs/concepts/lazy-and-eager.md`, "Rewritten before it runs";
- `NoOptFlags`' godoc.

qcheck's self check allows such a refusal (step 539's #96). The reference must
answer, the variant must refuse with a value error, and the difference must bisect
to the optimizer alone. The other direction stays a finding.

## Release

The fix joins v0.8.1: its CHANGELOG section's paragraph and a Fixed entry. Since step
543, v0.8.1 is tagged on 543's merge commit, not on this one's or #95's (step 541's
commands, with that branch's name), and only on the maintainer's word.

## Checks

Local: build, vet, levels, `api -check` (0 added, 0 gone since v0.8.1), testinventory,
gofmt, the new test, and `go test ./internal/...` (30 packages ok). The root package's
local run was stopped at the maintainer's word, to free the laptop: CI runs it, at
three widths, scalar-only and under the race detector.
