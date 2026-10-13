# Step 244 — as built

**T1's fourth piece, of `v0.6.4-scope.md`: a temporal group's median, quantile and
n_unique spill their values,** where they refused.

## 1. Why

A window's median keeps every value it covers, one for each (window, row) pair. So
windows that overlap hold many times their input: a week's rolling median over a
minute series is a value for every pair. Step 243 hands windows out as they close,
but a window still open holds its values. Under a limit, it refused.

## 2. What changed

**`temporalSink.chargeState(ctx)`.** Past the limit, it puts the values to disk
before it refuses:

- every segment's spillable aggregates hand their values to disk, through step 242's
  `spillState`, as a group-by's do;
- a segment's spills are kept on the segment (`tgSeg.spills`), named by the
  segment's first window and the aggregate (`w{base}-{i}-`);
- the spills go in the sink's own spill directory (`ensureDir`), from the budget the
  sink now has;
- it is charged again, and refuses only if still past the limit. What is left to
  refuse is implode, whose answer is every value, and the windows themselves.

The charging is split out as `charge`.

**`segBatch(ctx, g)`.** A segment's aggregates read their spilled values back in
their Finish, with the context the hand-out has, then let go of the spills.
`handOut` takes the context for it.

**`temporalSink.Close`** closes any spills still open and removes the directory,
after an error or an early break too.

**The hint** for rolling and group_by_dynamic says implode keeps every value of its
windows, and that a median, a quantile and n_unique spill theirs.

## 3. Tests and teeth

**`TestTemporalMediansSpillTheirValues`:**

- 2,000 rows a minute apart, windows reaching back a week: two million values, 16 MB;
- a rolling median, a dynamic median every minute over a week, and a rolling
  quantile and n_unique by key;
- under 2 MB, at 256 rows a batch, so that eight segments of windows, none closed
  before the end, spill side by side:
  - the answer is the one without a limit;
  - it spills;
  - it peaks at most twice the limit, at 1.01× to 1.79×;
  - the live heap grows by under 10 MB, at 4.3 to 4.9 MB;
  - the spill directory is empty after;
- a median beside an implode spills, then refuses, and the directory is empty after
  the error.

**`TestRollingStateIsCharged`** keeps its refusals for implode, rolling and dynamic.
Its two median cases became the test above.

| tooth | result |
| --- | --- |
| a segment never spills | **bites:** it refuses |
| not charged again after the spill | **bites:** it refuses |
| two segments' spills share a name | **bites:** the dynamic median |
| the spill directory kept | **bites** |
| a segment's spill read with a stale context | silent, and equivalent: a spill keeps the context it was made with, the same one the hand-out has |

## 4. The race run

**The failure.**

- This step's first gate failed its race run: the root package passed the default
  ten minutes, at 680 seconds;
- **CI's race job had failed the same way since step 237,** at steps 237, 238, 239,
  241 and 243, unnoticed: the gate here passed, near the limit;
- the time is spread over many tests, not one. Under race the slowest was
  `TestTemporalGroupsStreamTheirInput`, at 92 seconds, and 105 at step 242.

**The fix.**

- **`TestTemporalGroupsStreamTheirInput`** measures a budget, not a race. Under the
  race detector it runs a tenth of the rows at a tenth of the batch, with the full
  run's limits: 8 seconds.
- **`TestTemporalMediansSpillTheirValues`** runs its keyed case alone under race.
- **The race run has twenty minutes,** in the Makefile's `race` target and in CI's
  race job (`-timeout 20m`). The root package holds most of the end-to-end tests,
  and the race detector multiplies them about tenfold.

**Gate:** test-all 115 ok, race 23 ok (the root package in 574 seconds, of twenty
minutes), levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
